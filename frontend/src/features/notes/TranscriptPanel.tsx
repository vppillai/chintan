import { useEffect, useId, useRef, useState } from 'react';

import { activeSegmentIndex, formatTime, type TranscriptSegment } from './artifacts.ts';

/**
 * The transcript panel, and the one deliberate constraint in this screen.
 *
 * Timestamps belong to the RAW transcript. Cleanup rewrites the text —
 * reorders clauses, merges sentences, drops filler — so cleaned prose carries
 * no reliable mapping back onto those times. Two views, therefore:
 *
 *   Raw       timestamped, tap a line to seek, active line highlights.
 *   Cleaned   the text that became the note, with NO timestamps and no seeking.
 *
 * The alternative — aligning cleaned text onto raw timings by proportion or
 * fuzzy match — would produce a seek that lands on a plausible-looking wrong
 * place, which is worse than not offering it, because the user has no way to
 * tell it is wrong. The UI says which view is which rather than hiding the
 * distinction behind a single "transcript" tab.
 *
 * The panel is the transcript and its two-way switch, nothing else: an open
 * recording used to stack five control clusters — play, the row's More, this
 * switch, "Copy this transcript" and "Download audio" — and a tutorial
 * sentence (review 2026-09-21, T41). Copy and download are items of the
 * row's More menu now, and "Tap any line to jump there" is shown until the
 * first line is tapped on this device, then not again.
 */

/** Written once a line has been tapped; the hint has done its job. */
export const TRANSCRIPT_HINT_KEY = 'chintan.transcript-hint-seen';

function hintSeen(): boolean {
  try {
    return localStorage.getItem(TRANSCRIPT_HINT_KEY) === '1';
  } catch {
    return false;
  }
}

function markHintSeen(): void {
  try {
    localStorage.setItem(TRANSCRIPT_HINT_KEY, '1');
  } catch {
    /* Storage denied: the hint is shown again next time, which is harmless. */
  }
}

export type TranscriptView = 'raw' | 'cleaned';

export interface TranscriptPanelProps {
  segments: readonly TranscriptSegment[];
  cleanedText: string;
  view: TranscriptView;
  onViewChange: (view: TranscriptView) => void;
  currentTime: number;
  onSeek: (seconds: number) => void;
  /** False when the capture has no usable `segments.json` — there is no backfill. */
  hasSegments: boolean;
  /** The note text's language tag, for the lines (T60). */
  lang?: string | undefined;
}

export function TranscriptPanel({
  segments,
  cleanedText,
  view,
  onViewChange,
  currentTime,
  onSeek,
  hasSegments,
  lang,
}: TranscriptPanelProps) {
  const headingId = useId();
  /*
   * The toggle is only offered when both views exist.
   *
   * `cleanedText` was hard-coded to `''` at the only call site, so the Cleaned
   * tab was offered on every capture with segments and always answered "No
   * cleaned text for this capture." — including captures the pipeline had
   * cleaned perfectly well. The user's reasonable conclusion was that cleanup
   * had failed or their text had been lost.
   */
  const hasCleaned = cleanedText.trim().length > 0;
  const effectiveView: TranscriptView = view === 'cleaned' && !hasCleaned ? 'raw' : view;
  const activeIndex = effectiveView === 'raw' ? activeSegmentIndex(segments, currentTime) : -1;
  const activeRef = useRef<HTMLLIElement>(null);
  const [showHint, setShowHint] = useState(() => !hintSeen());

  // Follow playback, but only within the panel — `block: 'nearest'` so the page
  // itself does not jump under someone reading the note body.
  useEffect(() => {
    activeRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }, [activeIndex]);

  return (
    <section className="transcript" aria-labelledby={headingId} lang={lang}>
      <div className="transcript__header">
        <h2 id={headingId} className="transcript__heading">
          Transcript
        </h2>

        {/* Inline, right of the eyebrow: one row of header, not two. */}
        {hasSegments && hasCleaned && (
          <div className="transcript__toggle" role="group" aria-label="Transcript view">
            <button
              type="button"
              className="transcript__toggle-option"
              aria-pressed={effectiveView === 'raw'}
              onClick={() => {
                onViewChange('raw');
              }}
            >
              Timestamped
            </button>
            <button
              type="button"
              className="transcript__toggle-option"
              aria-pressed={effectiveView === 'cleaned'}
              onClick={() => {
                onViewChange('cleaned');
              }}
            >
              Cleaned
            </button>
          </div>
        )}

      </div>

      {/*
        Stated in the interface, not buried in documentation. A user who taps
        "Cleaned" and finds the timestamps gone deserves to know why.
        Skipped for raw view with no segments: "tap any line to jump there"
        directly above the empty state's "there is nothing to jump to" told
        the reader to do the one thing the same screen said was impossible.
        That empty state already explains itself; nothing to add here. And
        skipped once a line has been tapped: a tutorial sentence is for the
        first time.
      */}
      {(effectiveView === 'cleaned' || (showHint && hasSegments && segments.length > 0)) && (
        <p className="transcript__note">
          {effectiveView === 'raw'
            ? 'What was said, as recorded. Tap any line to jump there.'
            : 'Rewritten for the note. Cleanup changes the wording, so these lines have no reliable timestamps — there is nothing to jump to.'}
        </p>
      )}

      {effectiveView === 'raw' ? (
        hasSegments && segments.length > 0 ? (
          <ol className="transcript__list">
            {segments.map((segment, index) => {
              const active = index === activeIndex;
              return (
                <li key={segment.id} ref={active ? activeRef : null}>
                  {/* A real button: the library and this panel are both keyboard-operable. */}
                  <button
                    type="button"
                    className="transcript__line"
                    data-active={active || undefined}
                    aria-current={active ? 'true' : undefined}
                    onClick={() => {
                      markHintSeen();
                      setShowHint(false);
                      onSeek(segment.start);
                    }}
                  >
                    <span className="transcript__time numeric" aria-hidden="true">
                      {formatTime(segment.start)}
                    </span>
                    <span className="transcript__text">{segment.text}</span>
                    <span className="visually-hidden">
                      Jump to {formatTime(segment.start)}
                    </span>
                  </button>
                </li>
              );
            })}
          </ol>
        ) : (
          // Stated as a fact about this recording, not a guess about its age.
          // This used to say the recording was "made before timestamps were
          // captured", which was wrong for every capture the app had ever made:
          // the worker had always written timestamps and the parser dropped them.
          <p className="transcript__empty">
            No timestamps are available for this recording, so there is nothing to jump
            to. The cleaned text is in the note above.
          </p>
        )
      ) : (
        <div className="transcript__cleaned prose">
          {cleanedText ? (
            cleanedText.split(/\n{2,}/).map((paragraph, index) => (
              <p key={index}>{paragraph}</p>
            ))
          ) : (
            <p className="transcript__empty">No cleaned text for this capture.</p>
          )}
        </div>
      )}
    </section>
  );
}
