import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useEffect, useId, useState } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import { ApiError } from '@/api/problem.ts';
import { queryKeys } from '@/api/queries.ts';
import type { CleanedMode, CleanedWire, NoteDetailWire } from '@/api/schema.ts';
import { Icon } from '@/components/Icon.tsx';

import {
  CLEAN_POLL_MS,
  CLEAN_POLL_TIMEOUT_MS,
  CLEANED_MODE_HINTS,
  CLEANED_MODE_LABELS,
  cleanSettled,
} from './cleaned.ts';
import { describeAgo } from './groups.ts';
import { renderMarkdown } from './markdown.ts';
import type { NoteEditor } from './useNoteEditor.ts';

/**
 * The Cleaned tab: the whole note, rewritten by the worker, read-only.
 *
 * The note's text is what the recordings dictated, cleaned one recording at
 * a time and appended; after a dozen recordings it is a dozen paragraphs in
 * the order they were said. This view is the worker's rewrite of the whole
 * — `structured` into headings and lists, `polished` as tidied prose — as
 * Markdown, rendered here and never editable: the text is the document, and
 * this is a reading of it that can be generated again.
 *
 * Regeneration is asynchronous: the request is answered 202 and the result
 * arrives on the note a few seconds later, so the panel polls the note while
 * one is queued (`useRegenerateCleaned`) and says so. A view older than the
 * note it was made from is marked stale by the backend and says so here,
 * with the way to fix it beside the words. The mode switch regenerates in
 * the chosen mode and records the choice on the note; the toggle asks the
 * worker to regenerate after each recording, so the view keeps up on its own.
 */
export function CleanedPanel({ note, editor }: { note: NoteDetailWire; editor: NoteEditor }) {
  const headingId = useId();
  const autoId = useId();
  const cleaned = note.cleaned ?? null;
  const { regenerate, pending, notice } = useRegenerateCleaned(note);
  const { draft } = editor.model;

  // The mode the switch shows: the user's choice this session, else the mode
  // of the view on screen, else the rewrite that is the point of the feature.
  const mode: CleanedMode = draft.cleaned_mode ?? cleaned?.mode ?? 'structured';
  const autoClean = draft.auto_clean ?? false;

  const chooseMode = (next: CleanedMode): void => {
    // Recorded on the note, so an automatic regeneration uses it too, and
    // regenerated now in that mode.
    if (next !== draft.cleaned_mode) {
      editor.edit({ cleaned_mode: next });
      void editor.saveNow();
    }
    regenerate(next);
  };

  return (
    <section className="cleaned" aria-labelledby={headingId}>
      <h2 id={headingId} className="visually-hidden">
        Cleaned view
      </h2>

      <div className="cleaned__controls">
        <div className="cleaned__modes" role="group" aria-label="Cleaned view mode">
          {(Object.keys(CLEANED_MODE_LABELS) as CleanedMode[]).map((option) => (
            <button
              key={option}
              type="button"
              className="cleaned__mode"
              aria-pressed={option === mode}
              disabled={pending}
              onClick={() => {
                chooseMode(option);
              }}
            >
              {CLEANED_MODE_LABELS[option]}
            </button>
          ))}
        </div>

        {/*
          A real checkbox, drawn: the native control is stretched invisibly
          over the whole label, so the label is the 44 px target and the tap
          lands on the control itself; the mark beside the words is the box a
          finger sees, in the set's own stroke like every drawn glyph here.
        */}
        <label className="cleaned__auto" htmlFor={autoId}>
          <input
            id={autoId}
            type="checkbox"
            className="cleaned__auto-box"
            checked={autoClean}
            onChange={(event) => {
              editor.edit({ auto_clean: event.target.checked });
              void editor.saveNow();
            }}
          />
          <span className="cleaned__auto-mark" aria-hidden="true">
            <Icon name="check" size={16} />
          </span>
          <span>Keep it updated after each recording</span>
        </label>
      </div>

      {cleaned ? (
        <>
          <div className="cleaned__header">
            <p className="cleaned__meta">
              Generated {describeAgo(cleaned.generated_at)} · {CLEANED_MODE_LABELS[cleaned.mode]}
            </p>
            <button
              type="button"
              className="cleaned__action"
              disabled={pending}
              onClick={() => {
                regenerate(mode);
              }}
            >
              {pending ? 'Regenerating…' : 'Regenerate'}
            </button>
          </div>

          {cleaned.stale && !pending && (
            <div className="cleaned__stale" role="status">
              <p>The note changed since this was generated.</p>
              <button
                type="button"
                className="cleaned__action cleaned__action--primary"
                onClick={() => {
                  regenerate(mode);
                }}
              >
                Regenerate now
              </button>
            </div>
          )}

          <Progress pending={pending} notice={notice} />

          <div className="cleaned__body prose" data-stale={cleaned.stale || undefined}>
            {renderMarkdown(cleaned.body)}
          </div>
        </>
      ) : (
        <div className="cleaned__empty">
          <p className="cleaned__empty-title">No cleaned view yet</p>
          <p className="cleaned__hint">{CLEANED_MODE_HINTS[mode]}</p>
          <Progress pending={pending} notice={notice} />
          <button
            type="button"
            className="cleaned__action cleaned__action--primary"
            disabled={pending}
            onClick={() => {
              regenerate(mode);
            }}
          >
            {pending ? 'Generating…' : 'Generate'}
          </button>
        </div>
      )}
    </section>
  );
}

/** The line that says a regeneration is under way, or why the last one did not happen. */
function Progress({ pending, notice }: { pending: boolean; notice: string | null }) {
  if (pending) {
    return (
      <p className="cleaned__progress" role="status" aria-live="polite">
        Rewriting the note — this takes a few seconds.
      </p>
    );
  }
  if (notice) {
    return (
      <p className="cleaned__notice" role="alert">
        {notice}
      </p>
    );
  }
  return null;
}

interface Queued {
  /** The view as it was when the request went out; settled when it differs. */
  before: CleanedWire | null;
  since: number;
}

/**
 * Asks the worker for a new cleaned view and waits for it to appear.
 *
 * The request is answered 202 and carries nothing; the answer is the note's
 * own `cleaned` changing. So after a 202 the note is asked for again every
 * `CLEAN_POLL_MS` until `cleanSettled` says the view moved, the backend
 * reports an error on it, or `CLEAN_POLL_TIMEOUT_MS` has passed — a worker
 * that never answers must not leave the screen saying "Rewriting…" for good.
 */
export function useRegenerateCleaned(note: Pick<NoteDetailWire, 'id' | 'cleaned'>): {
  regenerate: (mode?: CleanedMode) => void;
  pending: boolean;
  notice: string | null;
} {
  const api = useApi();
  const queryClient = useQueryClient();
  const [queued, setQueued] = useState<Queued | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const mutation = useMutation({
    mutationFn: ({ mode }: { mode?: CleanedMode; before: CleanedWire | null }) =>
      api.cleanNote(note.id, mode ? { mode } : undefined),
    onMutate: () => {
      setNotice(null);
    },
    onSuccess: (_queued, { before }) => {
      setQueued({ before, since: Date.now() });
    },
    onError: (error) => {
      setNotice(
        error instanceof ApiError ? error.userMessage : 'Could not start the rewrite. Try again.',
      );
    },
  });

  /*
   * Settled? Decided while rendering, from the note as it is now, so the
   * first paint after the answer arrives is the settled one. The backend's
   * own error on the view is the one thing to show from it.
   */
  const current = note.cleaned ?? null;
  if (queued && cleanSettled(queued.before, current)) {
    setQueued(null);
    if (current?.error) setNotice(current.error);
  }

  useEffect(() => {
    if (!queued) return;
    const tick = setInterval(() => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.note(note.id) });
    }, CLEAN_POLL_MS);
    const remaining = Math.max(0, CLEAN_POLL_TIMEOUT_MS - (Date.now() - queued.since));
    const giveUp = setTimeout(() => {
      setQueued(null);
      setNotice('The rewrite is taking longer than usual. Pull down to refresh in a moment.');
    }, remaining);
    return () => {
      clearInterval(tick);
      clearTimeout(giveUp);
    };
  }, [queued, note.id, queryClient]);

  return {
    regenerate: (mode) => {
      mutation.mutate({ ...(mode ? { mode } : {}), before: note.cleaned ?? null });
    },
    pending: mutation.isPending || queued !== null,
    notice,
  };
}
