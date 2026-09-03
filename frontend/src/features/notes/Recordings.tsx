import { useQuery } from '@tanstack/react-query';
import { useCallback, useEffect, useId, useRef, useState } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import { ApiError } from '@/api/problem.ts';
import { useRetryCapture } from '@/api/queries.ts';
import { isTerminalStatus, type CaptureWire } from '@/api/schema.ts';
import { DownloadButton } from '@/components/DownloadButton.tsx';
import { Icon } from '@/components/Icon.tsx';
import { TargetPrompt } from '@/features/capture/FilingRow.tsx';

import { TranscriptPanel, type TranscriptView } from './TranscriptPanel.tsx';
import { WaveformScrubber } from './WaveformScrubber.tsx';
import { formatTime, loadCaptureArtifacts } from './artifacts.ts';
import { describeMoment, formatDurationShort } from './groups.ts';
import { usePlayer } from './usePlayer.ts';

/**
 * A note's recordings: the sources it was written from, as dated rows.
 *
 * The note body is the document. Each recording beneath it is one row —
 * play, when it was made, how it was filed, how long it is — and tapping the
 * row opens that recording's player and transcript. Before this the screen
 * showed one player above the body with a strip of pills reading
 * "Recording 1", "Recording 2"…, which told the user nothing about which
 * recording was which and put the sources above the thing they were sources
 * for.
 *
 * Newest first, and the newest is open when the screen arrives: the recording
 * someone has just made is the one they came to check.
 *
 * One row open at a time, which is also what makes "only one recording plays
 * at a time" hold without a registry of audio elements: a collapsed row has no
 * `<audio>` in the document, so it cannot be playing.
 */
export function Recordings({ captures }: { captures: readonly CaptureWire[] }) {
  const headingId = useId();
  const ordered = [...captures].sort((a, b) => b.created_at.localeCompare(a.created_at));
  const newest = ordered[0];

  const [openId, setOpenId] = useState<string | null>(newest?.id ?? null);
  /** A row whose play control was tapped while it was closed. */
  const [autoplayId, setAutoplayId] = useState<string | null>(null);
  /*
   * Raw or cleaned is a reading preference, not a property of one recording,
   * so it lives here and follows the user from row to row. The panel itself
   * still falls back to raw for a capture with no cleaned text — see
   * `TranscriptPanel` for the constraint that keeps timestamps out of the
   * cleaned view.
   */
  const [view, setView] = useState<TranscriptView>('raw');

  const onAutoplayed = useCallback(() => {
    setAutoplayId(null);
  }, []);

  return (
    <section className="recordings" aria-labelledby={headingId}>
      <div className="recordings__header">
        <h2 id={headingId} className="recordings__heading">
          Recordings
        </h2>
      </div>

      {ordered.length === 0 ? (
        <p className="recordings__empty">
          Nothing recorded into this note yet. “Record into this” below adds one.
        </p>
      ) : (
        <ul className="recordings__list" role="list">
          {ordered.map((capture) => (
            <RecordingRow
              key={capture.id}
              capture={capture}
              expanded={openId === capture.id}
              onToggle={() => {
                setOpenId((current) => (current === capture.id ? null : capture.id));
              }}
              onRequestPlay={() => {
                setOpenId(capture.id);
                setAutoplayId(capture.id);
              }}
              autoplay={autoplayId === capture.id}
              onAutoplayed={onAutoplayed}
              view={view}
              onViewChange={setView}
            />
          ))}
        </ul>
      )}
    </section>
  );
}

/**
 * How a recording was filed, in the words of the row.
 *
 * The wire says *where* a capture went and *whether* it got there; it does not
 * say who decided. `CaptureWire` carries `note_id`, the router's
 * `suggested_*` fields (cleared the moment a target is set) and `appended_at`
 * — nothing that separates "the router chose this note" from "the user chose
 * it" or "it was recorded into this note from the start". So an appended
 * recording says "Filed" and no more, rather than guessing at a provenance the
 * backend does not record. The moment the contract grows a field for it, this
 * is the one function to change.
 */
export function filedLabel(capture: CaptureWire): string {
  switch (capture.status) {
    case 'appended':
      return 'Filed';
    case 'needs_target':
      return 'Needs a target';
    case 'failed':
      return 'Failed';
    case 'spend_capped':
      return 'Spending cap reached';
    case 'no_content':
      return 'Nothing to save';
    default:
      return 'Filing…';
  }
}

interface RecordingRowProps {
  capture: CaptureWire;
  expanded: boolean;
  onToggle: () => void;
  /** Play was tapped on a closed row: open it and start once the audio is ready. */
  onRequestPlay: () => void;
  autoplay: boolean;
  onAutoplayed: () => void;
  view: TranscriptView;
  onViewChange: (view: TranscriptView) => void;
}

function RecordingRow({
  capture,
  expanded,
  onToggle,
  onRequestPlay,
  autoplay,
  onAutoplayed,
  view,
  onViewChange,
}: RecordingRowProps) {
  const api = useApi();
  const bodyId = useId();
  const retry = useRetryCapture();

  /*
   * Artifacts are fetched only for the open row. Every one is a presigned URL
   * round trip, and a note with six recordings should cost the request for the
   * one being read, not six times four.
   */
  const artifacts = useQuery({
    queryKey: ['capture-artifacts', capture.id],
    queryFn: () =>
      loadCaptureArtifacts(api, capture.id, {
        hasPeaks: capture.has_peaks ?? false,
        hasSegments: capture.has_segments ?? false,
      }),
    enabled: expanded,
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
  // A pre-v2 capture has neither artifact and gets a plain player. There is no
  // backfill, so this is a permanent branch, not a migration window.
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
  // Nothing to play: the artifacts answered and there is no audio behind them.
  const noAudio = expanded && artifacts.isSuccess && !audioUrl;
  const failed = capture.status === 'failed' || capture.status === 'spend_capped';
  const running = !isTerminalStatus(capture.status);

  return (
    <li className="recording" data-expanded={expanded || undefined} data-status={capture.status}>
      <div className="recording__head">
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

        {/*
          The row itself is the disclosure. `aria-expanded` on a real button
          rather than a click handler on the <li>: the transcript beneath is
          reachable by keyboard, and a screen reader is told there is one.
        */}
        <button
          type="button"
          className="recording__summary"
          aria-expanded={expanded}
          aria-controls={bodyId}
          onClick={onToggle}
        >
          <span className="recording__when">{when}</span>{' '}
          <span className="recording__filed" data-running={running || undefined}>
            {filedLabel(capture)}
          </span>{' '}
          <span className="recording__duration numeric">{durationLabel}</span>
        </button>
      </div>

      {expanded && (
        <div id={bodyId} className="recording__body">
          {capture.error && (
            <p className="recording__error" role="alert">
              {capture.error}
            </p>
          )}

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

          {unreachable ? (
            <p className="screen__count" role="status">
              The recording and its transcript need a connection. The note&rsquo;s text is on
              this device.
            </p>
          ) : artifacts.isLoading ? (
            <p className="screen__count" role="status">
              Loading the recording…
            </p>
          ) : noAudio ? (
            <p className="screen__count">
              The audio for this recording is no longer stored. The note&rsquo;s text is
              unaffected.
            </p>
          ) : audioUrl ? (
            /*
             * Inline playback. Never `window.open`, never a new tab — v1 handed
             * the presigned S3 URL to the browser, which downloaded the file on
             * desktop and navigated out of the app on mobile.
             */
            <section className="player" aria-label="Recording">
              <audio ref={audioRef} src={audioUrl} preload="metadata" />

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
                cleanedText={artifacts.data?.cleanedText ?? ''}
                view={view}
                onViewChange={onViewChange}
                currentTime={player.currentTime}
                onSeek={player.seekAndPlay}
                hasSegments={hasSegments}
              />

              <div className="player__actions">
                <DownloadButton
                  label="Download audio"
                  filename={() => `chintan-${capture.id}${audioExtension(audioUrl)}`}
                  blob={async () => {
                    const response = await fetch(audioUrl);
                    if (!response.ok) throw new Error(`audio fetch failed: ${response.status}`);
                    return response.blob();
                  }}
                />
              </div>
            </section>
          ) : null}
        </div>
      )}
    </li>
  );
}

/**
 * The real file extension off a presigned S3 URL's path, not its query
 * string — `audio.webm?X-Amz-...` should download as `.webm`, not as
 * whatever came after the `?`. Falls back to `.webm`, the format every
 * capture in this app is actually recorded in; a bare fallback with no
 * extension at all is the one outcome a save dialog can't recover from.
 */
function audioExtension(url: string): string {
  const path = url.split('?')[0] ?? '';
  const match = /\.[a-z0-9]+$/i.exec(path);
  return match ? match[0] : '.webm';
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
