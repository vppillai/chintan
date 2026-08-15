/**
 * Autosave state, as a pure machine.
 *
 * Two things v1 got wrong and this exists to fix:
 *
 * 1. **Failures were swallowed.** The save was fire-and-forget; if it failed,
 *    nothing said so and there was no `beforeunload`, so the user closed the
 *    tab believing their edit was stored. Every state here is one the UI
 *    renders verbatim.
 * 2. **There was no version.** A voice append landing while the editor was open
 *    silently discarded one of the two writes. `PATCH` now carries `version`,
 *    and a 409 is a state the user resolves — never an automatic clobber in
 *    either direction.
 */

export type SaveState =
  | 'clean'
  /** Edited, not yet sent. */
  | 'dirty'
  | 'saving'
  | 'saved'
  /**
   * Written to this device and waiting for a connection.
   *
   * Distinct from `saved` because the server has not seen it, and distinct from
   * `error` because nothing has gone wrong and nothing needs the user's
   * attention. This is the state that makes "your work is saved on this device
   * and will sync" a true sentence rather than a reassuring one.
   */
  | 'queued'
  /** The save failed for a reason a retry might fix. */
  | 'error'
  /** Someone else wrote first. Requires a decision. */
  | 'conflict';

export interface NoteDraft {
  title: string;
  body: string;
  aliases: string[];
  tags: string[];
}

export interface EditorModel {
  draft: NoteDraft;
  /** The last draft the server acknowledged, for dirty comparison. */
  saved: NoteDraft;
  version: number;
  state: SaveState;
  error: string | null;
  /** The server's copy, held while a conflict is unresolved. */
  theirs: { draft: NoteDraft; version: number } | null;
}

export type EditorEvent =
  | { type: 'edit'; patch: Partial<NoteDraft> }
  | { type: 'saveStart' }
  | { type: 'saveSuccess'; version: number; draft?: NoteDraft }
  /** Written to the device's queue instead of to the server. */
  | { type: 'saveQueued'; draft: NoteDraft }
  | { type: 'saveError'; message: string }
  | { type: 'conflict'; theirs: NoteDraft; version: number; message: string }
  /** Discard my edit and take the server's copy. */
  | { type: 'takeTheirs' }
  /** Re-apply my edit on top of the server's version and save again. */
  | { type: 'keepMine' }
  | { type: 'reset'; draft: NoteDraft; version: number };

export function emptyDraft(): NoteDraft {
  return { title: '', body: '', aliases: [], tags: [] };
}

export function initialEditor(draft: NoteDraft, version: number): EditorModel {
  return {
    draft,
    saved: draft,
    version,
    state: 'clean',
    error: null,
    theirs: null,
  };
}

function sameList(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((value, index) => value === b[index]);
}

export function draftsEqual(a: NoteDraft, b: NoteDraft): boolean {
  return (
    a.title === b.title &&
    a.body === b.body &&
    sameList(a.aliases, b.aliases) &&
    sameList(a.tags, b.tags)
  );
}

/**
 * The 'edit' transition, pulled out of the reducer so it can also run
 * synchronously outside React's render cycle.
 *
 * `useNoteEditor.edit()` calls this directly against its `latest` ref, in
 * addition to dispatching the event. A caller that fires `onChange` then
 * `onCommit` back to back in the same handler — `TagEditor`'s add/remove,
 * unlike every debounced text field — used to have `saveNow()` read the ref
 * before the effect that mirrors `model` into it had run, so the save saw the
 * pre-edit draft, decided there was nothing dirty to send, and returned with
 * the debounce it would have relied on already cancelled by that same
 * `saveNow()`. The tag or alias just added or removed was never sent, with no
 * error and no further trigger — only a reload made the loss visible.
 */
export function applyEdit(model: EditorModel, patch: Partial<NoteDraft>): EditorModel {
  const draft = { ...model.draft, ...patch };
  // An unresolved conflict outranks everything. Letting a keystroke drop
  // back to `dirty` would dismiss the conflict UI and let the next autosave
  // clobber the other write — the exact failure this state exists to stop.
  if (model.state === 'conflict') return { ...model, draft };
  // Editing back to the saved text is not a change worth a request.
  return {
    ...model,
    draft,
    state: draftsEqual(draft, model.saved) ? 'clean' : 'dirty',
  };
}

export function editorReducer(model: EditorModel, event: EditorEvent): EditorModel {
  switch (event.type) {
    case 'edit':
      return applyEdit(model, event.patch);

    case 'saveStart':
      if (model.state === 'conflict') return model;
      return { ...model, state: 'saving', error: null };

    case 'saveSuccess': {
      const draft = event.draft ?? model.draft;
      // If the user typed while the request was in flight, the draft is
      // already ahead of what was acknowledged — stay dirty rather than
      // claiming a save that does not include the latest keystrokes.
      const stillDirty = !draftsEqual(model.draft, draft);
      return {
        ...model,
        saved: draft,
        version: event.version,
        state: stillDirty ? 'dirty' : 'saved',
        error: null,
        theirs: null,
      };
    }

    case 'saveQueued': {
      /*
       * `version` is deliberately NOT advanced. The queued PATCH still carries
       * the version the note was loaded at, and that is what the server will
       * check when the queue flushes. Incrementing it here would send an edit
       * claiming to be based on a revision that does not exist yet, which turns
       * a clean conflict into a silent overwrite.
       */
      const stillDirty = !draftsEqual(model.draft, event.draft);
      return {
        ...model,
        saved: event.draft,
        state: stillDirty ? 'dirty' : 'queued',
        error: null,
      };
    }

    case 'saveError':
      return { ...model, state: 'error', error: event.message };

    case 'conflict':
      // The edit is NOT discarded and NOT forced through. Both copies are held
      // until the user chooses.
      return {
        ...model,
        state: 'conflict',
        error: event.message,
        theirs: { draft: event.theirs, version: event.version },
      };

    case 'takeTheirs': {
      if (!model.theirs) return model;
      return {
        draft: model.theirs.draft,
        saved: model.theirs.draft,
        version: model.theirs.version,
        state: 'saved',
        error: null,
        theirs: null,
      };
    }

    case 'keepMine': {
      if (!model.theirs) return model;
      // Adopt their version number so the next PATCH is accepted, keeping the
      // local text. This overwrites their text, which is exactly what the user
      // just chose — it is not something the app decided on its own.
      return {
        ...model,
        version: model.theirs.version,
        state: 'dirty',
        error: null,
        theirs: null,
      };
    }

    case 'reset':
      return initialEditor(event.draft, event.version);

    default: {
      const exhaustive: never = event;
      return exhaustive;
    }
  }
}

/**
 * The queue's answer about this note, folded into the editor's state.
 *
 * `queued` was a dead end: the reducer entered it and no event left it, so the
 * screen kept saying "Saved on this device — will sync" after the sync had
 * already happened, or after it had permanently failed. Rather than inventing
 * an event and hoping every flush path remembers to send it, the outstanding
 * write is *derived* from the queue — which is the only thing that actually
 * knows.
 *
 *   entry absent   the flush removed it, which it does only on success
 *   entry pending  genuinely still owed to the server
 *   entry dead     it will not be sent again, and `error` says why
 *
 * `undefined` means the queue has not been read yet and nothing is claimed.
 * Only the states with nothing in flight are reconciled: a `dirty`, `saving` or
 * `conflict` model is describing something newer than the queue is.
 */
export function reconcileQueued(
  model: EditorModel,
  entry: { pending: boolean; dead: boolean; error: string | null } | null | undefined,
): EditorModel {
  if (entry === undefined) return model;

  if (model.state === 'queued') {
    if (entry === null) return { ...model, state: 'saved', error: null };
    if (entry.dead) {
      return { ...model, state: 'error', error: entry.error ?? 'That edit did not save.' };
    }
    return model;
  }

  /*
   * Arriving at a note that already had something outstanding — the edit was
   * made, the screen was left, and the user has come back. Only from a state
   * with nothing of its own to say, so this can never overwrite a live edit.
   */
  if (model.state === 'clean' || model.state === 'saved') {
    if (entry === null) return model;
    if (entry.dead) {
      return { ...model, state: 'error', error: entry.error ?? 'That edit did not save.' };
    }
    return { ...model, state: 'queued', error: null };
  }

  return model;
}

/**
 * True when there is an edit that would be lost if the document went away.
 *
 * `queued` is NOT one of them. A queued edit is in IndexedDB, survives a reload
 * and a cold start, and flushes on reconnect — warning about it would train the
 * user to dismiss the one warning that means something.
 */
export function hasUnsavedWork(model: EditorModel): boolean {
  return model.state === 'dirty' || model.state === 'saving' || model.state === 'error' || model.state === 'conflict';
}

export const SAVE_LABELS: Record<SaveState, string> = {
  clean: '',
  dirty: 'Unsaved changes',
  saving: 'Saving…',
  saved: 'Saved',
  queued: 'Saved on this device — will sync',
  error: "Couldn't save",
  conflict: 'This note changed elsewhere',
};

/** Debounce for the autosave. Long enough to not save mid-word. */
export const AUTOSAVE_DELAY_MS = 1_200;
