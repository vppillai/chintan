import { useEffect, useId, useRef } from 'react';

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
  /** False for pre-v2 captures, which have no segments.json and no backfill. */
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
  const activeIndex = view === 'raw' ? activeSegmentIndex(segments, currentTime) : -1;
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

        {hasSegments && (
          <div className="transcript__toggle" role="group" aria-label="Transcript view">
            <button
              type="button"
              className="transcript__toggle-option"
              aria-pressed={view === 'raw'}
              onClick={() => {
                onViewChange('raw');
              }}
            >
              Timestamped
            </button>
            <button
              type="button"
              className="transcript__toggle-option"
              aria-pressed={view === 'cleaned'}
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
      */}
      <p className="transcript__note">
        {view === 'raw'
          ? 'What was said, as recorded. Tap any line to jump there.'
          : 'Rewritten for the note. Cleanup changes the wording, so these lines have no reliable timestamps — there is nothing to jump to.'}
      </p>

      {view === 'raw' ? (
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
          <p className="transcript__empty">
            This recording was made before timestamps were captured, so there is nothing
            to jump to. The cleaned text is in the note above.
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
