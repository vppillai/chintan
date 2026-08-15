import { useQuery } from '@tanstack/react-query';
import { useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router';

import { useApi } from '@/api/ApiProvider.tsx';
import { ApiError } from '@/api/problem.ts';
import { useNote } from '@/api/queries.ts';
import type { CaptureWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { CopyButton } from '@/components/CopyButton.tsx';
import { DownloadButton } from '@/components/DownloadButton.tsx';
import { Icon } from '@/components/Icon.tsx';
import { TagEditor } from '@/components/TagEditor.tsx';
import { useOnline } from '@/hooks/useOnline.ts';
import { useCachedNote } from '@/offline/useNotesCache.ts';

import { NoteActions } from './NoteActions.tsx';
import { TranscriptPanel, type TranscriptView } from './TranscriptPanel.tsx';
import { WaveformScrubber } from './WaveformScrubber.tsx';
import { loadCaptureArtifacts } from './artifacts.ts';
import { SAVE_LABELS } from './autosave.ts';
import { usePlayer } from './usePlayer.ts';
import { useNoteEditor } from './useNoteEditor.ts';

export function NoteDetailScreen() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const online = useOnline();
  const { data: served, isLoading, fetchStatus, error } = useNote(id);
  const cached = useCachedNote(id);

  /*
   * The device's copy stands in when the server has not answered. Only a full
   * note qualifies — `useCachedNote` refuses a list row — because rendering a
   * real title over an empty body invites the user to type into a note whose
   * text is merely missing, and the next PATCH would erase it.
   */
  const note = served ?? cached.data ?? undefined;
  const offlineCopy = !served && Boolean(cached.data);
  const editor = useNoteEditor(note);

  const captures = note?.captures ?? [];
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const capture = captures.find((item) => item.id === selectedId) ?? captures[0];

  // Paused means offline, not slow: TanStack never runs the query at all, so
  // waiting for it would be waiting forever.
  const paused = fetchStatus === 'paused';

  if ((isLoading || cached.isLoading) && !paused && !note) {
    return (
      <div className="screen">
        <p className="screen__empty" role="status">
          Loading…
        </p>
      </div>
    );
  }

  if (!note) {
    /*
     * Two different sentences, because they are two different situations and
     * the screen used to say the first one for both. A note that is simply not
     * on this device was reported as one that "may have been archived or
     * purged" — describing a deletion that never happened, to a user who could
     * see the note one screen earlier.
     */
    const unreachable = paused || !online || (error instanceof ApiError && error.isOffline);

    return (
      <div className="screen">
        <header className="screen__header screen__header--detail">
          <BackButton onClick={() => void navigate(ROUTES.notes)} />
          <h1>{unreachable ? 'Not on this device' : 'Note not found'}</h1>
        </header>
        <p className="screen__empty">
          {unreachable
            ? 'This note has not been opened on this device, so there is no copy here to read. It will be here once you have a connection.'
            : 'No note with that identifier. It may have been archived or purged.'}
        </p>
      </div>
    );
  }

  return (
    <div className="screen">
      {offlineCopy && (
        <p className="screen__count" role="status">
          Saved on this device. Edits are kept here and sent when you reconnect.
        </p>
      )}
      <header className="screen__header screen__header--detail">
        <BackButton onClick={() => void navigate(ROUTES.notes)} />
        <label className="visually-hidden" htmlFor="note-title">
          Note title
        </label>
        <input
          id="note-title"
          className="note-title-input"
          value={editor.model.draft.title}
          onChange={(event) => {
            editor.edit({ title: event.target.value });
          }}
          onBlur={() => void editor.saveNow()}
        />
      </header>

      <SaveIndicator editor={editor} />

      {capture && <Player key={capture.id} capture={capture} />}

      {captures.length > 1 && (
        <nav className="capture-picker" aria-label="Recordings">
          <ul className="capture-picker__list" role="list">
            {captures.map((item, index) => (
              <li key={item.id}>
                <button
                  type="button"
                  className="capture-picker__item"
                  aria-pressed={item.id === capture?.id}
                  onClick={() => {
                    setSelectedId(item.id);
                  }}
                >
                  Recording <span className="numeric">{index + 1}</span>
                </button>
              </li>
            ))}
          </ul>
        </nav>
      )}

      <label className="visually-hidden" htmlFor="note-body">
        Note body
      </label>
      <textarea
        id="note-body"
        className="note-body-input prose"
        value={editor.model.draft.body}
        rows={12}
        onChange={(event) => {
          editor.edit({ body: event.target.value });
        }}
        onBlur={() => void editor.saveNow()}
      />

      {/*
        Directly under the text it copies, where the eye already is — and a
        clear distance above `NoteActions`, which holds Archive and Delete
        forever. A copy control adjacent to an irreversible one is how a stray
        thumb destroys a note it meant to keep.

        Title first, then the body: a body pasted somewhere else with no title
        loses what it was about, and re-typing that is exactly the friction this
        is meant to remove.
      */}
      <div className="note-copy">
        <CopyButton
          label="Copy note"
          text={() =>
            [editor.model.draft.title.trim(), editor.model.draft.body.trim()]
              .filter(Boolean)
              .join('\n\n')
          }
        />
        <DownloadButton
          label="Download note"
          filename={() => `${filenameFor(editor.model.draft.title)}.md`}
          blob={() =>
            Promise.resolve(
              new Blob(
                [
                  `# ${editor.model.draft.title.trim()}\n\n${editor.model.draft.body.trim()}\n`,
                ],
                { type: 'text/markdown' },
              ),
            )
          }
        />
      </div>

      <TagEditor
        label="Tags"
        values={editor.model.draft.tags}
        placeholder="Add a tag"
        onChange={(tags) => {
          editor.edit({ tags });
        }}
        onCommit={() => void editor.saveNow()}
      />

      <TagEditor
        label="Also called"
        values={editor.model.draft.aliases}
        placeholder="Add another name"
        onChange={(aliases) => {
          editor.edit({ aliases });
        }}
        onCommit={() => void editor.saveNow()}
      />

      <NoteActions note={note} />
    </div>
  );
}

/**
 * A dictated title, made safe as a filename.
 *
 * The title comes from speech, unbounded — no reserved characters, no length
 * limit, sometimes not even Latin script. `/` and `\` would nest or break a
 * path; a title trimmed to nothing (all punctuation, or empty) still needs a
 * name a save dialog can show.
 */
function filenameFor(title: string): string {
  const cleaned = title.trim().replace(/[/\\:*?"<>|]/g, '').trim();
  return (cleaned || 'note').slice(0, 120);
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

function BackButton({ onClick }: { onClick: () => void }) {
  return (
    <button type="button" className="icon-button" onClick={onClick}>
      <Icon name="back" size={20} />
      <span className="visually-hidden">Back to notes</span>
    </button>
  );
}

/**
 * Autosave state, rendered.
 *
 * v1 swallowed autosave failures entirely, and its "unsaved" indicator was a
 * `.btn-warning` class with no CSS behind it — invisible on every screen.
 */
function SaveIndicator({ editor }: { editor: ReturnType<typeof useNoteEditor> }) {
  const { model } = editor;
  if (model.state === 'clean') return null;

  if (model.state === 'conflict') {
    return (
      <div className="save-conflict" role="alert">
        <p className="save-conflict__title">{SAVE_LABELS.conflict}</p>
        <p className="save-conflict__body">
          A voice capture or another device saved this note while you were editing. Nothing
          has been overwritten — choose which version to keep.
        </p>
        <div className="save-conflict__actions">
          <button type="button" className="save-conflict__action" onClick={editor.takeTheirs}>
            Use the newer version
          </button>
          <button
            type="button"
            className="save-conflict__action"
            onClick={() => {
              editor.keepMine();
              void editor.saveNow();
            }}
          >
            Keep my edits
          </button>
        </div>
      </div>
    );
  }

  return (
    <p
      className="save-indicator"
      data-state={model.state}
      role="status"
      aria-live="polite"
    >
      {model.error ?? SAVE_LABELS[model.state]}
      {model.state === 'error' && (
        <button
          type="button"
          className="save-indicator__retry"
          onClick={() => void editor.saveNow()}
        >
          Try again
        </button>
      )}
    </p>
  );
}

/**
 * Inline playback. Never `window.open`, never a new tab — v1 handed the
 * presigned S3 URL to the browser, which downloaded the file on desktop and
 * navigated out of the app on mobile.
 */
function Player({ capture }: { capture: CaptureWire }) {
  const api = useApi();
  const [view, setView] = useState<TranscriptView>('raw');

  const { data, isLoading, isError, error, fetchStatus } = useQuery({
    queryKey: ['capture-artifacts', capture.id],
    queryFn: () =>
      loadCaptureArtifacts(api, capture.id, {
        hasPeaks: capture.has_peaks ?? false,
        hasSegments: capture.has_segments ?? false,
      }),
    staleTime: 5 * 60_000,
    retry: false,
  });

  /*
   * Audio is never cached: a presigned URL expires and the file is megabytes.
   * So offline this query is either paused or a network failure, and both used
   * to render as "The audio for this capture is no longer stored" — telling the
   * user their recording had been deleted because they walked into a tunnel.
   */
  const unreachable =
    fetchStatus === 'paused' || (isError && error instanceof ApiError && error.isOffline);

  const audioRef = useRef<HTMLAudioElement>(null);
  const player = usePlayer(data?.audioUrl ?? null, audioRef);
  const segments = data?.segments ?? [];
  const peaks = data?.peaks ?? [];
  // A pre-v2 capture has neither artifact and gets a plain player. There is no
  // backfill, so this is a permanent branch, not a migration window.
  const hasSegments = (capture.has_segments ?? false) && segments.length > 0;

  const duration =
    player.duration || (capture.duration_ms ? capture.duration_ms / 1000 : 0);

  if (unreachable) {
    return (
      <p className="screen__count" role="status">
        The recording and its transcript need a connection. The text below is on this
        device.
      </p>
    );
  }

  if (isLoading) {
    return (
      <p className="screen__count" role="status">
        Loading the recording…
      </p>
    );
  }

  if (!data?.audioUrl) {
    return (
      <p className="screen__count">
        The audio for this capture is no longer stored. The text below is unaffected.
      </p>
    );
  }

  return (
    <section className="player" aria-label="Recording">
      <audio ref={audioRef} src={data.audioUrl} preload="metadata" />

      <div className="player__controls">
        <button
          type="button"
          className="player__play"
          onClick={player.toggle}
          aria-label={player.playing ? 'Pause' : 'Play'}
        >
          <Icon name={player.playing ? 'stop' : 'play'} size={20} />
        </button>

        {peaks.length > 0 ? (
          <WaveformScrubber
            peaks={peaks}
            currentTime={player.currentTime}
            duration={duration}
            onSeek={player.seek}
          />
        ) : (
          // No peaks: a plain range input, which is a real slider and works
          // identically for keyboard users.
          <PlainScrubber
            currentTime={player.currentTime}
            duration={duration}
            onSeek={player.seek}
          />
        )}
      </div>

      <div className="player__actions">
        <DownloadButton
          label="Download audio"
          filename={() => `chintan-${capture.id}${audioExtension(data.audioUrl ?? '')}`}
          blob={async () => {
            const response = await fetch(data.audioUrl as string);
            if (!response.ok) throw new Error(`audio fetch failed: ${response.status}`);
            return response.blob();
          }}
        />
      </div>

      {player.error && (
        <p className="player__error" role="alert">
          {player.error}
        </p>
      )}

      <TranscriptPanel
        segments={segments}
        cleanedText={data.cleanedText}
        view={hasSegments ? view : 'raw'}
        onViewChange={setView}
        currentTime={player.currentTime}
        onSeek={player.seekAndPlay}
        hasSegments={hasSegments}
      />
    </section>
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
