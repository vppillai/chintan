import { useEffect, useId, useRef } from 'react';

import { CopyButton } from '@/components/CopyButton.tsx';

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
 */

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
}

export function TranscriptPanel({
  segments,
  cleanedText,
  view,
  onViewChange,
  currentTime,
  onSeek,
  hasSegments,
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

  // Follow playback, but only within the panel — `block: 'nearest'` so the page
  // itself does not jump under someone reading the note body.
  useEffect(() => {
    activeRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }, [activeIndex]);

  return (
    <section className="transcript" aria-labelledby={headingId}>
      <div className="transcript__header">
        <h2 id={headingId} className="transcript__heading">
          Transcript
        </h2>

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

        {/*
          Named for the view it copies, because "copy" on this screen could mean
          three different things — the note, what was said, or the rewrite — and
          a button that might mean any of them means none of them.
        */}
        <CopyButton
          className="transcript__copy"
          label={effectiveView === 'raw' ? 'Copy transcript' : 'Copy cleaned text'}
          text={() =>
            effectiveView === 'raw'
              ? segments.map((segment) => segment.text).join('\n')
              : cleanedText
          }
        />
      </div>

      {/*
        Stated in the interface, not buried in documentation. A user who taps
        "Cleaned" and finds the timestamps gone deserves to know why.
        Skipped for raw view with no segments: "tap any line to jump there"
        directly above the empty state's "there is nothing to jump to" told
        the reader to do the one thing the same screen said was impossible.
        That empty state already explains itself; nothing to add here.
      */}
      {(effectiveView === 'cleaned' || (hasSegments && segments.length > 0)) && (
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
