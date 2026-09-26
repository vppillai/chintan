import { useQuery } from '@tanstack/react-query';
import { useEffect, useId, useRef } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import { ApiError } from '@/api/problem.ts';
import { useRetryCapture } from '@/api/queries.ts';
import { isTerminalStatus, type CaptureWire } from '@/api/schema.ts';
import { Icon } from '@/components/Icon.tsx';
import { OverflowMenu, type OverflowMenuItem } from '@/components/OverflowMenu.tsx';
import { SwipeRow } from '@/components/SwipeRow.tsx';
import { FilingStages, TargetPrompt } from '@/features/capture/FilingRow.tsx';
import { AUTO_LANGUAGE } from '@/features/settings/languages.ts';
import { useLongPress, type LongPress } from '@/hooks/useLongPress.ts';

import { TranscriptPanel, type TranscriptView } from '../TranscriptPanel.tsx';
import { WaveformScrubber } from '../WaveformScrubber.tsx';
import { formatTime, loadCaptureArtifacts } from '../artifacts.ts';
import { describeMoment, formatDurationShort } from '../groups.ts';
import { usePlayer } from '../usePlayer.ts';
import { filedLabel, heardAs } from './labels.ts';

/**
 * One recording under a note: the row's line, and — open — its player and
 * transcript. What the row *is* and why it looks as it does is said on
 * `Recordings`, which owns the list, the selection and the notice line; this
 * file holds the row's own state, which is the artifacts it has fetched and
 * the audio element it is playing.
 */
export interface RecordingRowProps {
  capture: CaptureWire;
  expanded: boolean;
  onToggle: () => void;
  /** Play was tapped on a closed row: open it and start once the audio is ready. */
  onRequestPlay: () => void;
  autoplay: boolean;
  onAutoplayed: () => void;
  view: TranscriptView;
  onViewChange: (view: TranscriptView) => void;
  selecting: boolean;
  selected: boolean;
  onToggleSelected: () => void;
  onStartSelecting: () => void;
  onMove: () => void;
  onDelete: () => void;
  onDownload: () => void;
  /** Text from this row's menu for the clipboard; the outcome is the panel's to say. */
  onCopy: (text: string) => void;
  lang: string | undefined;
  /** "From Watch" when a device sent this row; null for a recording made here. */
  from: string | null;
  /** The code a recording into this note is transcribed in, for the "Heard as" chip. */
  effectiveLanguage: string;
  /** "Transcribe again in Malayalam": the menu item, worded for the note's language. */
  retranscribeLabel: string;
  onRetranscribe: () => void;
}

export function RecordingRow({
  capture,
  expanded,
  onToggle,
  onRequestPlay,
  autoplay,
  onAutoplayed,
  view,
  onViewChange,
  selecting,
  selected,
  onToggleSelected,
  onStartSelecting,
  onMove,
  onDelete,
  onDownload,
  onCopy,
  lang,
  from,
  effectiveLanguage,
  retranscribeLabel: retranscribeText,
  onRetranscribe,
}: RecordingRowProps) {
  const api = useApi();
  const bodyId = useId();
  const checkId = useId();
  const retry = useRetryCapture();
  // Press and hold on a closed row to start selecting it. Off while selecting:
  // a hold on a checkbox row is a slow tap, not a second gesture.
  const longPress: LongPress = useLongPress(selecting ? null : onStartSelecting);

  /*
   * Artifacts are fetched only for the open row. Every one is a presigned URL
   * round trip, and a note with six recordings should cost the request for the
   * one being read, not six times four.
   */
  const running = !isTerminalStatus(capture.status);
  const artifacts = useQuery({
    // Keyed on the capture's write version as well as its id: transcribing
    // again replaces the segments behind the same id, and the landed row
    // must fetch afresh where a five-minute-fresh entry would be shown as is.
    queryKey: ['capture-artifacts', capture.id, capture.version],
    queryFn: () =>
      loadCaptureArtifacts(api, capture.id, {
        hasPeaks: capture.has_peaks ?? false,
        hasSegments: capture.has_segments ?? false,
      }),
    // Not while the pipeline is still writing them: the audio's presigned URL
    // would 404 and the row would say the recording "is no longer stored".
    enabled: expanded && !running,
    staleTime: 5 * 60_000,
    retry: false,
  });

  const audioRef = useRef<HTMLAudioElement>(null);
  const player = usePlayer(artifacts.data?.audioUrl ?? null, audioRef, expanded);

  // Play tapped on a closed row: the row is open now and the audio has just
  // reported its metadata, so start it. Best effort — a browser that insists
  // on a gesture in the same tick will refuse, and `toggle` says so inline.
  const { ready, toggle } = player;
  useEffect(() => {
    if (!autoplay || !expanded || !ready) return;
    toggle();
    onAutoplayed();
  }, [autoplay, expanded, ready, toggle, onAutoplayed]);

  /*
   * Audio is never cached: a presigned URL expires and the file is megabytes.
   * So offline this query is either paused or a network failure, and both used
   * to render as "The audio for this capture is no longer stored" — telling the
   * user their recording had been deleted because they walked into a tunnel.
   */
  const unreachable =
    artifacts.fetchStatus === 'paused' ||
    (artifacts.isError && artifacts.error instanceof ApiError && artifacts.error.isOffline);

  const segments = artifacts.data?.segments ?? [];
  const peaks = artifacts.data?.peaks ?? [];
  const cleanedText = artifacts.data?.cleanedText ?? '';
  // Not while the pipeline runs: what is in hand is the transcript being
  // replaced, and the chip's tap would post a second run into a 409.
  const heard = running
    ? null
    : heardAs(artifacts.data?.detectedLanguage ?? null, effectiveLanguage);
  // A capture recorded before segments and peaks were stored has neither and
  // gets a plain player. There is no backfill, so this is a permanent branch,
  // not a migration window.
  const hasSegments = (capture.has_segments ?? false) && segments.length > 0;
  const audioUrl = artifacts.data?.audioUrl ?? null;

  const duration = player.duration || (capture.duration_ms ? capture.duration_ms / 1000 : 0);
  const durationLabel = capture.duration_ms
    ? formatDurationShort(capture.duration_ms)
    : player.duration
      ? formatTime(player.duration)
      : '';

  const when = describeMoment(capture.created_at);
  const playing = expanded && player.playing;
  // Text sent through the inbox: there never was audio, so the row is its
  // transcript and nothing on it plays, downloads or transcribes again.
  const textOnly = capture.has_audio === false;
  // Nothing to play: the artifacts answered and there is no audio behind them.
  const noAudio = expanded && artifacts.isSuccess && !audioUrl;
  const failed = capture.status === 'failed' || capture.status === 'spend_capped';

  const filed = filedLabel(capture);
  const summary = (
    <>
      <span className="recording__when">{when}</span>{' '}
      {from && (
        <>
          <span className="recording__filed">{from}</span>{' '}
        </>
      )}
      {filed && (
        <>
          <span className="recording__filed" data-running={running || undefined}>
            {filed}
          </span>{' '}
        </>
      )}
      <span className="recording__duration numeric">{durationLabel}</span>
    </>
  );

  return (
    <li
      className="recording"
      data-expanded={expanded || undefined}
      data-status={capture.status}
      data-selected={selected || undefined}
    >
      {selecting ? (
        /*
         * In selection mode the row is a checkbox and its label — the one
         * control every screen reader and keyboard already knows — and the
         * player is closed, so a tap anywhere on the row toggles it.
         */
        <div className="recording__head">
          <label
            className="recording__select"
            htmlFor={checkId}
            onClick={(event) => {
              // The click that follows the long press which started this mode
              // lands on this label; letting it through would toggle the
              // checkbox straight back off. See `NoteRow`.
              if (longPress.consumeClick()) event.preventDefault();
            }}
          >
            <span className="recording__check">
              <input
                id={checkId}
                type="checkbox"
                className="recording__checkbox"
                checked={selected}
                aria-label={`Select recording from ${when}`}
                onChange={onToggleSelected}
              />
            </span>
            <span className="recording__summary recording__summary--static">{summary}</span>
          </label>
        </div>
      ) : (
        /*
         * The head alone slides, not the open player beneath it: the tray is
         * about the recording as a row, and a transcript sliding off the
         * screen would be the gesture taking more than it was asked for.
         */
        <SwipeRow
          className="recording__swipe"
          contentClassName="recording__head"
          label={`Actions for recording from ${when}`}
          actions={[
            { id: 'move', label: 'Move', icon: 'move', onSelect: onMove },
            { id: 'delete', label: 'Delete', icon: 'trash', destructive: true, onSelect: onDelete },
          ]}
        >
          {textOnly ? (
            // The disc keeps the rows aligned; the glyph says this one is words.
            <span className="recording__play" aria-hidden="true">
              <Icon name="transcribe" size={18} />
            </span>
          ) : (
            <button
              type="button"
              className="recording__play"
              aria-label={`${playing ? 'Pause' : 'Play'} recording from ${when}`}
              disabled={noAudio || unreachable}
              onClick={() => {
                if (expanded) player.toggle();
                else onRequestPlay();
              }}
            >
              <Icon name={playing ? 'stop' : 'play'} size={18} />
            </button>
          )}

          {/*
            The row itself is the disclosure. `aria-expanded` on a real button
            rather than a click handler on the <li>: the transcript beneath is
            reachable by keyboard, and a screen reader is told there is one.
            Held down, it selects instead (`useLongPress`).
          */}
          <button
            type="button"
            className="recording__summary"
            aria-expanded={expanded}
            aria-controls={bodyId}
            onClick={() => {
              if (longPress.consumeClick()) return;
              onToggle();
            }}
            {...longPress.handlers}
          >
            {summary}
          </button>

          {/*
            Named for what they copy, because "copy" on this screen could mean
            three different things — the note, what was said, or the rewrite —
            and an item that might mean any of them means none of them.
            "This", because they copy this recording only; the whole note is
            copied from Share. Offered only once the open row has the text.
          */}
          <OverflowMenu
            label={`More for recording from ${when}`}
            items={[
              { label: 'Move to…', onSelect: onMove },
              { label: 'Delete recording', onSelect: onDelete, destructive: true },
              ...(textOnly ? [] : [{ label: 'Download audio', onSelect: onDownload } satisfies OverflowMenuItem]),
              ...(expanded && segments.length > 0
                ? [
                    {
                      label: 'Copy this transcript',
                      onSelect: () => {
                        onCopy(segments.map((segment) => segment.text).join('\n'));
                      },
                    } satisfies OverflowMenuItem,
                  ]
                : []),
              ...(expanded && cleanedText
                ? [
                    {
                      label: 'Copy this cleaned text',
                      onSelect: () => {
                        onCopy(cleanedText);
                      },
                    } satisfies OverflowMenuItem,
                  ]
                : []),
              // Only a settled recording: the server refuses one in flight,
              // and the row is already following that run. Never words sent
              // as words: there is no audio to run again.
              ...(running || textOnly
                ? []
                : [{ label: retranscribeText, onSelect: onRetranscribe } satisfies OverflowMenuItem]),
              { label: 'Select', onSelect: onStartSelecting },
            ]}
          />
        </SwipeRow>
      )}

      {/*
        Still being filed: the same four segments the library's filing row
        shows, under the row's own line, whether or not the row is open. The
        row becomes an ordinary recording the moment the status settles.
      */}
      {running && !selecting && (
        <div className="recording__progress">
          <FilingStages capture={capture} />
        </div>
      )}

      {expanded && (
        <div id={bodyId} className="recording__body">
          {capture.error && (
            <p className="recording__error" role="alert">
              {capture.error}
            </p>
          )}

          {/*
            What Whisper heard, when it is not what was asked for: the one
            diagnostic the owner's wrong-script transcripts lacked (T8). Under
            a chosen language the chip is the way to put it right; under
            Auto-detect it is a fact, and the fix is a language in Details.
          */}
          {heard &&
            (effectiveLanguage === AUTO_LANGUAGE ? (
              <p className="recording__heard recording__heard--fact">Heard as {heard}</p>
            ) : (
              <button type="button" className="recording__heard" onClick={onRetranscribe}>
                Heard as {heard} — {retranscribeText.replace(/^Transcribe/, 'transcribe')}
              </button>
            ))}

          {/*
            The same controls the library's filing row offers, because a
            recording that stopped short is the same problem wherever it is
            read from. Retry resumes from whichever artifact already exists,
            so it is safe on a stalled capture as well as a failed one.
          */}
          {failed && (
            <div className="recording__actions">
              <button
                type="button"
                className="screen__action"
                disabled={retry.isPending}
                onClick={() => {
                  retry.mutate(capture.id);
                }}
              >
                {retry.isPending ? 'Retrying…' : 'Retry'}
              </button>
            </div>
          )}
          {capture.status === 'needs_target' && <TargetPrompt capture={capture} />}

          {running ? (
            <p className="screen__count" role="status">
              Being filed. The recording and its transcript will be here once it has been
              saved into the note.
            </p>
          ) : unreachable ? (
            <p className="screen__count" role="status">
              The recording and its transcript need a connection. The note&rsquo;s text is on
              this device.
            </p>
          ) : artifacts.isLoading ? (
            <p className="screen__count" role="status">
              Loading the recording…
            </p>
          ) : textOnly ? (
            /*
             * Words that arrived as words: the transcript alone, no player
             * above it. Timestamps exist only when the pipeline wrote them,
             * and without them the cleaned text is the view — the raw view
             * would say "nothing to jump to" about a recording that never was.
             * Told the words were sent as words, the panel does not explain
             * cleanup losing timestamps the text never had either.
             */
            <TranscriptPanel
              segments={segments}
              cleanedText={cleanedText}
              view={hasSegments ? view : 'cleaned'}
              onViewChange={onViewChange}
              currentTime={0}
              onSeek={() => undefined}
              hasSegments={hasSegments}
              lang={lang}
              textOnly
            />
          ) : noAudio ? (
            <p className="screen__count">
              The audio for this recording is no longer stored. The note&rsquo;s text is
              unaffected.
            </p>
          ) : audioUrl ? (
            /*
             * Inline playback. Never `window.open`, never a new tab — handing
             * the presigned S3 URL to the browser downloads the file on desktop
             * and navigates out of the app on mobile.
             */
            <section className="player" aria-label="Recording">
              {/*
                `crossOrigin="anonymous"` is load-bearing, not hygiene. Without
                it the element fetches the presigned URL in no-cors mode, S3
                answers without `Access-Control-Allow-Origin` (no `Origin` was
                sent), and Chromium keeps that response in the HTTP cache — so
                the CORS `fetch()` behind the menu's "Download audio" was
                served the cached no-cors response and failed its CORS check
                on every attempt. With the attribute both requests are CORS
                requests to a bucket whose rule already allows this origin;
                `runDownload` also asks for `no-store`.
              */}
              <audio ref={audioRef} src={audioUrl} preload="metadata" crossOrigin="anonymous" />

              <div className="player__controls">
                {peaks.length > 0 ? (
                  <WaveformScrubber
                    peaks={peaks}
                    currentTime={player.currentTime}
                    duration={duration}
                    onSeek={player.seek}
                  />
                ) : (
                  // No peaks: a plain range input, which is a real slider and
                  // works identically for keyboard users.
                  <PlainScrubber
                    currentTime={player.currentTime}
                    duration={duration}
                    onSeek={player.seek}
                  />
                )}
              </div>

              {player.error && (
                <p className="player__error" role="alert">
                  {player.error}
                </p>
              )}

              <TranscriptPanel
                segments={segments}
                cleanedText={cleanedText}
                view={view}
                onViewChange={onViewChange}
                currentTime={player.currentTime}
                onSeek={player.seekAndPlay}
                hasSegments={hasSegments}
                lang={lang}
              />
            </section>
          ) : null}
        </div>
      )}
    </li>
  );
}

function PlainScrubber({
  currentTime,
  duration,
  onSeek,
}: {
  currentTime: number;
  duration: number;
  onSeek: (seconds: number) => void;
}) {
  return (
    <input
      type="range"
      className="player__range"
      min={0}
      max={Math.max(1, Math.round(duration))}
      value={Math.round(currentTime)}
      aria-label="Playback position"
      onChange={(event) => {
        onSeek(Number(event.target.value));
      }}
    />
  );
}
