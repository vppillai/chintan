import { describe, expect, it } from 'vitest';

import {
  editorReducer,
  hasUnsavedWork,
  initialEditor,
  reconcileQueued,
  type EditorModel,
  type NoteDraft,
} from './autosave.ts';

const BASE: NoteDraft = { title: 'Roof repair', body: 'Ridge tiles.', aliases: [], tags: [] };

function start(version = 3): EditorModel {
  return initialEditor(BASE, version);
}

describe('editing', () => {
  it('starts clean', () => {
    expect(start().state).toBe('clean');
    expect(hasUnsavedWork(start())).toBe(false);
  });

  it('becomes dirty on a change', () => {
    const model = editorReducer(start(), { type: 'edit', patch: { body: 'Ridge tiles slipped.' } });
    expect(model.state).toBe('dirty');
    expect(hasUnsavedWork(model)).toBe(true);
  });

  it('returns to clean when edited back to the saved text', () => {
    const dirty = editorReducer(start(), { type: 'edit', patch: { title: 'Roof' } });
    const back = editorReducer(dirty, { type: 'edit', patch: { title: 'Roof repair' } });
    expect(back.state).toBe('clean');
  });

  it('treats the transcription language as part of the note', () => {
    // Absent and empty both mean "inherit the default", so neither is a change
    // from the other; a real code is.
    const inherit = editorReducer(start(), { type: 'edit', patch: { language: '' } });
    expect(inherit.state).toBe('clean');

    const malayalam = editorReducer(start(), { type: 'edit', patch: { language: 'ml' } });
    expect(malayalam.state).toBe('dirty');
    expect(hasUnsavedWork(malayalam)).toBe(true);
  });

  it('treats list fields by value, not by identity', () => {
    const withTags = editorReducer(start(), { type: 'edit', patch: { tags: ['house'] } });
    const sameTags = editorReducer(withTags, { type: 'edit', patch: { tags: ['house'] } });
    expect(sameTags.state).toBe('dirty');

    const cleared = editorReducer(sameTags, { type: 'edit', patch: { tags: [] } });
    expect(cleared.state).toBe('clean');
  });
});

describe('saving', () => {
  it('reports saving then saved, and bumps the version', () => {
    const dirty = editorReducer(start(3), { type: 'edit', patch: { body: 'new' } });
    const saving = editorReducer(dirty, { type: 'saveStart' });
    expect(saving.state).toBe('saving');

    const saved = editorReducer(saving, { type: 'saveSuccess', version: 4 });
    expect(saved.state).toBe('saved');
    expect(saved.version).toBe(4);
    expect(hasUnsavedWork(saved)).toBe(false);
  });

  it('stays dirty when the user typed while the request was in flight', () => {
    // Claiming "Saved" here would be a lie about the latest keystrokes.
    const dirty = editorReducer(start(3), { type: 'edit', patch: { body: 'first' } });
    const saving = editorReducer(dirty, { type: 'saveStart' });
    const typedMore = editorReducer(saving, { type: 'edit', patch: { body: 'first and second' } });

    const acked = editorReducer(typedMore, {
      type: 'saveSuccess',
      version: 4,
      draft: { ...BASE, body: 'first' },
    });

    expect(acked.state).toBe('dirty');
    expect(acked.draft.body).toBe('first and second');
    expect(acked.version).toBe(4);
  });

  it('surfaces a failure rather than swallowing it', () => {
    // v1 was fire-and-forget: a failed autosave produced no UI at all and the
    // user closed the tab believing the edit was stored.
    const dirty = editorReducer(start(), { type: 'edit', patch: { body: 'new' } });
    const failed = editorReducer(dirty, { type: 'saveError', message: 'No connection' });

    expect(failed.state).toBe('error');
    expect(failed.error).toBe('No connection');
    expect(hasUnsavedWork(failed)).toBe(true);
    // The edit is still in the draft, so a retry sends it.
    expect(failed.draft.body).toBe('new');
  });
});

describe('conflict', () => {
  const theirs: NoteDraft = { ...BASE, body: 'Ridge tiles. Quote from Ellis: 900.' };

  function conflicted(): EditorModel {
    const dirty = editorReducer(start(3), { type: 'edit', patch: { body: 'mine' } });
    const saving = editorReducer(dirty, { type: 'saveStart' });
    return editorReducer(saving, {
      type: 'conflict',
      theirs,
      version: 4,
      message: 'This note changed somewhere else.',
    });
  }

  it('holds both copies and writes neither', () => {
    // A voice append landing while the editor is open silently discarded one
    // of the two writes in v1. Now it is a decision the user makes.
    const model = conflicted();

    expect(model.state).toBe('conflict');
    expect(model.draft.body).toBe('mine');
    expect(model.theirs?.draft.body).toBe(theirs.body);
    expect(model.version).toBe(3);
  });

  it('refuses to autosave over an unresolved conflict', () => {
    const model = editorReducer(conflicted(), { type: 'saveStart' });
    expect(model.state).toBe('conflict');
  });

  it('stays in conflict while the user keeps typing', () => {
    const model = editorReducer(conflicted(), { type: 'edit', patch: { body: 'mine, edited' } });
    expect(model.state).toBe('conflict');
    expect(model.draft.body).toBe('mine, edited');
  });

  it('takeTheirs adopts the server copy and discards the local edit', () => {
    const model = editorReducer(conflicted(), { type: 'takeTheirs' });

    expect(model.state).toBe('saved');
    expect(model.draft.body).toBe(theirs.body);
    expect(model.version).toBe(4);
    expect(model.theirs).toBeNull();
    expect(hasUnsavedWork(model)).toBe(false);
  });

  it('keepMine adopts their version number so the next save is accepted', () => {
    const model = editorReducer(conflicted(), { type: 'keepMine' });

    expect(model.state).toBe('dirty');
    expect(model.draft.body).toBe('mine');
    // Their version, my text: the next PATCH is accepted and overwrites, which
    // is exactly what the user chose — not something the app decided.
    expect(model.version).toBe(4);
    expect(model.theirs).toBeNull();
  });

  it('ignores a resolution when there is no conflict', () => {
    const clean = start();
    expect(editorReducer(clean, { type: 'takeTheirs' })).toBe(clean);
    expect(editorReducer(clean, { type: 'keepMine' })).toBe(clean);
  });
});

describe('reset', () => {
  it('reloads for a different note', () => {
    const dirty = editorReducer(start(), { type: 'edit', patch: { body: 'x' } });
    const next: NoteDraft = { title: 'Garden', body: 'Move the rosemary.', aliases: [], tags: [] };

    const model = editorReducer(dirty, { type: 'reset', draft: next, version: 1 });

    expect(model.state).toBe('clean');
    expect(model.draft).toEqual(next);
    expect(model.version).toBe(1);
  });
});

/**
 * An edit made with no connection.
 *
 * The client used to answer every offline failure with "No connection. Your
 * work is saved on this device and will sync." while writing nothing anywhere.
 * `queued` is the state that makes the sentence true, and these are the
 * properties that distinguish it from both `saved` and `error`.
 */
describe('queued', () => {
  it('is not an error, and not a claim that the server has it', () => {
    const dirty = editorReducer(start(), { type: 'edit', patch: { body: 'Ellis quoted.' } });

    const queued = editorReducer(dirty, { type: 'saveQueued', draft: dirty.draft });

    expect(queued.state).toBe('queued');
    expect(queued.error).toBeNull();
    expect(queued.saved).toEqual(dirty.draft);
  });

  it('does not advance the version, so the queued PATCH still gets a real check', () => {
    const dirty = editorReducer(start(3), { type: 'edit', patch: { body: 'Ellis quoted.' } });

    const queued = editorReducer(dirty, { type: 'saveQueued', draft: dirty.draft });

    // Incrementing here would send an edit claiming to be based on a revision
    // that does not exist yet, turning a clean 409 into a silent overwrite.
    expect(queued.version).toBe(3);
  });

  it('stays dirty when the user typed on while it was being written', () => {
    const first = editorReducer(start(), { type: 'edit', patch: { body: 'One.' } });
    const second = editorReducer(first, { type: 'edit', patch: { body: 'One. Two.' } });

    const queued = editorReducer(second, { type: 'saveQueued', draft: first.draft });

    expect(queued.state).toBe('dirty');
  });

  it('does not warn on the way out, because the edit survives the document', () => {
    const dirty = editorReducer(start(), { type: 'edit', patch: { body: 'Ellis quoted.' } });
    const queued = editorReducer(dirty, { type: 'saveQueued', draft: dirty.draft });

    // It is in IndexedDB and flushes on reconnect. Warning about it would train
    // the user to dismiss the one warning that means something.
    expect(hasUnsavedWork(queued)).toBe(false);
  });
});

/**
 * Reconciling `queued` against what the device is actually still holding.
 *
 * `saveQueued` set the state and nothing ever left it: no event meant "the
 * queued mutation reached the server" and the flush notified the editor of
 * nothing, so the screen said "Saved on this device — will sync" until a
 * reload — after the sync, and after a permanent failure alike. A user could
 * not tell "not yet" from "never".
 */
describe('reconcileQueued', () => {
  const queued = (): EditorModel => {
    const dirty = editorReducer(start(), { type: 'edit', patch: { body: 'Ellis quoted.' } });
    return editorReducer(dirty, { type: 'saveQueued', draft: dirty.draft });
  };

  it('claims nothing until the queue has been read', () => {
    const model = queued();
    expect(reconcileQueued(model, undefined).state).toBe('queued');
  });

  it('resolves to saved once the entry is gone, which only success does', () => {
    expect(reconcileQueued(queued(), null).state).toBe('saved');
  });

  it('keeps saying "will sync" while it genuinely is still owed', () => {
    expect(
      reconcileQueued(queued(), { pending: true, dead: false, error: null }).state,
    ).toBe('queued');
  });

  it('turns into an error, with the reason, when it will not be sent again', () => {
    const model = reconcileQueued(queued(), {
      pending: false,
      dead: true,
      error: 'Title too long',
    });

    expect(model.state).toBe('error');
    expect(model.error).toBe('Title too long');
  });

  it('tells someone returning to the note that an edit is still waiting', () => {
    // The edit was made, the screen was left, the user has come back. The
    // reducer is freshly `clean` and knows nothing; the queue does.
    const model = reconcileQueued(start(), { pending: true, dead: false, error: null });
    expect(model.state).toBe('queued');
  });

  it('tells them when the edit they left behind failed', () => {
    const model = reconcileQueued(start(), {
      pending: false,
      dead: true,
      error: 'Title too long',
    });
    expect(model.state).toBe('error');
  });

  it('never overrides something the screen is in the middle of', () => {
    // A live edit, an in-flight save and an unresolved conflict all describe
    // something newer than the queue does.
    const dirty = editorReducer(start(), { type: 'edit', patch: { body: 'newer' } });
    const stale = { pending: true, dead: false, error: null };

    expect(reconcileQueued(dirty, stale).state).toBe('dirty');
    expect(reconcileQueued(editorReducer(dirty, { type: 'saveStart' }), stale).state).toBe(
      'saving',
    );
    const conflicted = editorReducer(dirty, {
      type: 'conflict',
      theirs: BASE,
      version: 4,
      message: 'changed elsewhere',
    });
    expect(reconcileQueued(conflicted, stale).state).toBe('conflict');
  });
});
