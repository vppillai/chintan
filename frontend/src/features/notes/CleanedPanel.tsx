import { useMutation } from '@tanstack/react-query';
import { useEffect, useId, useMemo, useRef, useState } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import { ApiError } from '@/api/problem.ts';
import { usePollNote } from '@/api/queries.ts';
import type { CleanedMode, CleanedWire, NoteDetailWire } from '@/api/schema.ts';
import { CheckMark } from '@/components/CheckMark.tsx';

import {
  CLEAN_POLL_TIMEOUT_MS,
  CLEANED_MODE_HINTS,
  CLEANED_MODE_LABELS,
  PLAIN_CLEANED_MODES,
  cleanPollInterval,
  cleanSettled,
} from './cleaned.ts';
import { markTree, useReportTotal, useScrollToActiveMatch, type FindTarget } from './FindBar.tsx';
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
 *
 * The find bar searches this view too: the rendered elements are walked and
 * every match marked where it stands, so a hit inside a heading or a bold
 * run is lit in place. With no view there is nothing to find, and the bar is
 * told so.
 *
 * A checklist has no Cleaned tab (R8-F8): its one mode, `tasks`, is written
 * into the body by Tidy up list in the ⋮ menu (`useTidyList.ts`), so this
 * panel is a plain note's only.
 */

export function CleanedPanel({
  note,
  editor,
  lang,
  find = null,
}: {
  note: NoteDetailWire;
  editor: NoteEditor;
  /** The note text's language tag, for the rendered view (T60). */
  lang?: string | undefined;
  find?: FindTarget | null;
}) {
  const headingId = useId();
  const autoId = useId();
  const cleaned = note.cleaned ?? null;
  const { regenerate, pending, notice } = useRegenerateCleaned(note);
  const { draft } = editor.model;

  const bodyRef = useRef<HTMLDivElement>(null);
  const cleanedBody = cleaned?.body ?? '';
  const query = find?.query ?? '';
  const active = find?.active ?? 0;
  const rendered = useMemo(
    () => markTree(renderMarkdown(cleanedBody), query, active),
    [cleanedBody, query, active],
  );
  useReportTotal(find, rendered.total);
  useScrollToActiveMatch(bodyRef, find ? active : null, rendered.total);

  // The mode the switch shows: the user's choice this session, else the mode
  // of the view on screen, else the rewrite that is the point of the feature.
  const mode: CleanedMode = plainMode(draft.cleaned_mode ?? cleaned?.mode);
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

  const regenerateNow = (): void => {
    regenerate(mode);
  };

  return (
    <section className="cleaned" aria-labelledby={headingId}>
      <h2 id={headingId} className="visually-hidden">
        Cleaned view
      </h2>

      <div className="cleaned__controls">
        <div className="cleaned__modes" role="group" aria-label="Cleaned view mode">
          {PLAIN_CLEANED_MODES.map((option) => (
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
          The checklist's own drawn box: the native control is stretched
          invisibly over the whole label, so the label is the 44 px target and
          the tap lands on the control itself; the mark beside the words is
          the box a finger sees.
        */}
        <label className="cleaned__auto" htmlFor={autoId}>
          <input
            id={autoId}
            type="checkbox"
            className="checklist__box"
            checked={autoClean}
            onChange={(event) => {
              editor.edit({ auto_clean: event.target.checked });
              void editor.saveNow();
            }}
          />
          <CheckMark />
          <span>Keep it updated after each recording</span>
        </label>
      </div>

      {cleaned ? (
        <>
          <div className="cleaned__header">
            <p className="cleaned__meta">
              {`Generated ${describeAgo(cleaned.generated_at)} · ${CLEANED_MODE_LABELS[cleaned.mode]}`}
            </p>
            <div className="cleaned__actions">
              <button
                type="button"
                className="cleaned__action"
                disabled={pending}
                onClick={regenerateNow}
              >
                {pending ? 'Regenerating…' : 'Regenerate'}
              </button>
            </div>
          </div>

          {cleaned.stale && !pending && (
            <div className="cleaned__stale" role="status">
              <p>The note changed since this was generated.</p>
              <button
                type="button"
                className="cleaned__action cleaned__action--primary"
                onClick={regenerateNow}
              >
                Regenerate now
              </button>
            </div>
          )}

          <Progress pending={pending} notice={notice} />

          <div
            ref={bodyRef}
            className="cleaned__body prose"
            lang={lang}
            data-stale={cleaned.stale || undefined}
          >
            {rendered.nodes}
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
            onClick={regenerateNow}
          >
            {pending ? 'Generating…' : 'Generate'}
          </button>
        </div>
      )}
    </section>
  );
}

/**
 * A plain note's mode, never `tasks`: a note converted back from a checklist
 * still carries the view its list was split into, and offering that mode to
 * prose would be refused by the server.
 */
function plainMode(mode: CleanedMode | undefined): CleanedMode {
  return mode && mode !== 'tasks' ? mode : 'structured';
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
 * own `cleaned` changing. So after a 202 the note is asked for again on the
 * `cleanPollInterval` ladder until `cleanSettled` says the view moved, the backend
 * reports an error on it, or `CLEAN_POLL_TIMEOUT_MS` has passed — a worker
 * that never answers must not leave the screen saying "Rewriting…" for good.
 */
export function useRegenerateCleaned(note: Pick<NoteDetailWire, 'id' | 'cleaned'>): {
  regenerate: (mode?: CleanedMode) => void;
  pending: boolean;
  notice: string | null;
} {
  const api = useApi();
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

  usePollNote(note.id, queued?.since ?? null, cleanPollInterval);

  useEffect(() => {
    if (!queued) return;
    const remaining = Math.max(0, CLEAN_POLL_TIMEOUT_MS - (Date.now() - queued.since));
    const giveUp = setTimeout(() => {
      setQueued(null);
      setNotice('The rewrite is taking longer than usual. Pull down to refresh in a moment.');
    }, remaining);
    return () => {
      clearTimeout(giveUp);
    };
  }, [queued]);

  return {
    regenerate: (mode) => {
      mutation.mutate({ ...(mode ? { mode } : {}), before: note.cleaned ?? null });
    },
    pending: mutation.isPending || queued !== null,
    notice,
  };
}
