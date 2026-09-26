import { useCallback, useEffect, useId, useState } from 'react';
import { useNavigate } from 'react-router';

import { useApi } from '@/api/ApiProvider.tsx';
import { useDeleteCaptures, useDevices, useMoveCaptures, useSettings } from '@/api/queries.ts';
import { isTerminalStatus, type CaptureMoveWire, type NoteDetailWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { copyText } from '@/components/CopyButton.tsx';
import { saveBlob } from '@/components/DownloadButton.tsx';
import { SelectionBar } from '@/components/SelectionBar.tsx';
import { LocalUploadItem } from '@/features/capture/FilingRow.tsx';
import type { CaptureModel } from '@/features/capture/machine.ts';

import { MoveSheet } from './MoveSheet.tsx';
import type { TranscriptView } from './TranscriptPanel.tsx';
import { RecordingRow } from './recordings/RecordingRow.tsx';
import {
  describeOutcome,
  failureText,
  isFromDevice,
  justLanded,
  retranscribeLabel,
  sourceLabel,
  type Notice,
} from './recordings/labels.ts';
import { useRetranscribeCapture } from './recordings/useRetranscribeCapture.ts';
import { archiveName, zipRecordings } from './zipRecordings.ts';

type DownloadProgress = { phase: 'idle' } | { phase: 'working'; done: number; total: number };

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
 * Newest first, and the newest *finished* recording is open when the screen
 * arrives: the recording someone has just made is the one they came to check.
 * A recording still being made or filed is the first row, wearing the same
 * upload bar or stage strip the library's filing row does — Send returns to
 * this tab — and opens on its own when it lands.
 *
 * One row open at a time, which is also what makes "only one recording plays
 * at a time" hold without a registry of audio elements: a collapsed row has no
 * `<audio>` in the document, so it cannot be playing.
 *
 * Each row has a More control — Move to…, Delete recording, Download audio,
 * and on an open row Copy this transcript / Copy this cleaned text, then
 * Select — and a long press (or Select) enters a selection mode in which
 * move, delete and download apply to several rows at once from a bar at the
 * foot of the screen. On a phone the row also swipes aside for Delete and
 * Move (N8), which open the same dialog and sheet the menu does. Moving and
 * deleting take the paragraph the recording dictated with them (backlog D2,
 * D3); downloading several is one zip built on the device from the server's
 * manifest of presigned URLs (D4). Copy and a second Download used to be
 * buttons inside the open row as well, which stacked five control clusters
 * on one recording (review 2026-09-21, T41); the menu is the one place now,
 * and the outcome is said on the notice line under the rows.
 *
 * A settled row's menu also offers "Transcribe again in <language>" (T7):
 * once a recording has come back in the wrong script, or with sentences
 * missing, changing the note's language did nothing for it — the pipeline
 * transcribes once and Retry starts from the last good artifact. The server
 * cuts the paragraph, runs the audio again in the note's effective language
 * and answers 202 with the capture back at `transcribing`; the row follows
 * it as it does a new recording. The request names no language, so the
 * server's reading of the note's setting is the one that counts.
 *
 * A row a device sent through the inbox says so — "From Watch", named from
 * the devices list on You — and one that arrived as text has no player:
 * the row is its transcript, and Download and Transcribe again are not
 * offered for audio that never existed.
 */
export function Recordings({
  note,
  lang,
  localUpload = null,
  onSelectingChange,
}: {
  note: Pick<NoteDetailWire, 'id' | 'title' | 'captures' | 'language'>;
  /** The note text's language tag, for the transcripts (T60). */
  lang?: string | undefined;
  /** An upload this device is still making into this note — see `useLocalUpload`. */
  localUpload?: CaptureModel | null;
  /** Told when selection starts and ends, so the screen can make room for the bar. */
  onSelectingChange?: (selecting: boolean) => void;
}) {
  const api = useApi();
  const navigate = useNavigate();
  const headingId = useId();
  const captures = note.captures ?? [];
  const ordered = [...captures].sort((a, b) => b.created_at.localeCompare(a.created_at));
  const newest = ordered.find((capture) => isTerminalStatus(capture.status));

  const [openId, setOpenId] = useState<string | null>(newest?.id ?? null);

  /*
   * A recording that has just landed opens itself: the user sent it, came
   * back here, watched the strip, and the thing to look at now is the result.
   * Only a transition counts — a row that arrived already filed is the
   * ordinary case above. Derived while rendering from the previous list,
   * React's own pattern for state that follows a prop, rather than from an
   * effect that would paint the closed row first.
   */
  const [previous, setPrevious] = useState(note.captures);
  if (note.captures !== previous) {
    setPrevious(note.captures);
    const landed = justLanded(previous ?? [], captures);
    if (landed) setOpenId(landed.id);
  }
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

  /*
   * Selection. The set outlives nothing: leaving the screen is exactly when
   * "which recordings were selected" should stop mattering, and a row that
   * vanishes (deleted, moved) is dropped from the set on the next render.
   */
  const [selecting, setSelecting] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  /** What the row's More menu or the bar has asked for, awaiting confirmation. */
  const [pending, setPending] = useState<{ kind: 'delete' | 'move'; ids: string[] } | null>(null);
  /** The line under the rows: what the last action did, or why it did not. */
  const [notice, setNotice] = useState<Notice | null>(null);
  const [download, setDownload] = useState<DownloadProgress>({ phase: 'idle' });

  const deleteCaptures = useDeleteCaptures();
  const moveCaptures = useMoveCaptures();
  const retranscribe = useRetranscribeCapture(note.id, setNotice);
  // What a recording made into this note is transcribed in, for the menu's
  // wording — the note's own choice, else the You screen's default.
  const { data: settings } = useSettings();
  const effectiveLanguage = note.language || settings?.default_language || 'en';
  // The devices list names the row a watch or a shortcut sent; asked for only
  // when some row came in that way, so a note recorded here costs nothing.
  const { data: devices } = useDevices(ordered.some(isFromDevice));
  const deviceNames = new Map((devices?.items ?? []).map((device) => [device.id, device.name]));

  const present = new Set(ordered.map((capture) => capture.id));
  const selected = new Set([...selectedIds].filter((id) => present.has(id)));
  const allSelected = ordered.length > 0 && selected.size === ordered.length;

  useEffect(() => {
    onSelectingChange?.(selecting);
    // Leaving the panel mid-selection — another tab, another note — ends
    // the selection, and the screen's action bar has to be told to come back.
    return () => {
      onSelectingChange?.(false);
    };
  }, [selecting, onSelectingChange]);

  const onAutoplayed = useCallback(() => {
    setAutoplayId(null);
  }, []);

  const startSelecting = (captureId: string | null): void => {
    setSelecting(true);
    // Rows close as selection starts: an open player under a checkbox is
    // two things to tap where there should be one.
    setOpenId(null);
    setNotice(null);
    if (captureId) setSelectedIds(new Set([captureId]));
  };

  const stopSelecting = (): void => {
    setSelecting(false);
    setSelectedIds(new Set());
  };

  const toggleSelected = (captureId: string): void => {
    setSelectedIds((current) => {
      const next = new Set(current);
      if (next.has(captureId)) next.delete(captureId);
      else next.add(captureId);
      return next;
    });
  };

  /*
   * The recordings to act on, as a list even for one: the mutations take a
   * batch, and a row's own menu is a batch of one. A recording that is still
   * moving through the pipeline is refused by the server (409) — the worker
   * may be writing its paragraph — and the bar says so in words rather than
   * a status code.
   */
  const runDelete = (ids: string[]): void => {
    setNotice(null);
    deleteCaptures.mutate(
      { noteId: note.id, captureIds: ids },
      {
        onSuccess: (result) => {
          setNotice(describeOutcome(result.done.length, result.failed, 'deleted'));
          if (result.done.length === ids.length) stopSelecting();
        },
        onError: (error) => {
          setNotice({ text: failureText(error), tone: 'error' });
        },
      },
    );
  };

  /*
   * `title` is the sheet's word for where they went — the note's own, or the
   * name just typed for a note the server is about to make; the id of that
   * one is known only from the answer, which is why the notice's "Open …"
   * takes it from the result.
   */
  const runMove = (ids: string[], target: CaptureMoveWire, title: string): void => {
    setNotice(null);
    moveCaptures.mutate(
      { noteId: note.id, target, captureIds: ids },
      {
        onSuccess: (result) => {
          setPending(null);
          const outcome = describeOutcome(result.done.length, result.failed, 'moved');
          setNotice(
            result.done.length > 0
              ? {
                  ...outcome,
                  text: `${outcome.text} to “${title}”`,
                  target: { id: result.targetId, title },
                }
              : outcome,
          );
          if (result.done.length === ids.length) stopSelecting();
        },
        onError: (error) => {
          setNotice({ text: failureText(error), tone: 'error' });
        },
      },
    );
  };

  /*
   * Download: one file saved as itself, several as one archive. Either way the
   * manifest is one request and each file one CORS fetch of the presigned URL;
   * the zip is built here (`zipRecordings`). Recordings whose audio has since
   * expired are simply not in the manifest, and the notice says how many that
   * left out rather than handing over URLs that would 404.
   */
  const runDownload = async (ids: string[]): Promise<void> => {
    setNotice(null);
    setDownload({ phase: 'working', done: 0, total: ids.length });
    try {
      const wanted = new Set(ids);
      const manifest = await api.recordingUrls(note.id);
      const items = manifest.items.filter((item) => wanted.has(item.capture_id));
      if (items.length === 0) {
        setDownload({ phase: 'idle' });
        setNotice({
          text:
            ids.length === 1
              ? 'The audio for this recording is no longer stored.'
              : 'None of those recordings still has its audio.',
          tone: 'error',
        });
        return;
      }
      setDownload({ phase: 'working', done: 0, total: items.length });
      if (items.length === 1) {
        const [item] = items as [(typeof items)[number]];
        const response = await fetch(item.url, { cache: 'no-store' });
        if (!response.ok) throw new Error(`audio fetch failed: ${String(response.status)}`);
        saveBlob(await response.blob(), item.filename);
      } else {
        const blob = await zipRecordings(items, (done, total) => {
          setDownload({ phase: 'working', done, total });
        });
        saveBlob(blob, archiveName(note.title));
      }
      setDownload({ phase: 'idle' });
      const missing = ids.length - items.length;
      setNotice({
        text:
          items.length === 1
            ? 'Downloaded'
            : `Downloaded ${String(items.length)} recordings as one archive` +
              (missing > 0 ? `; ${String(missing)} no longer had audio` : ''),
        tone: 'ok',
      });
      stopSelecting();
    } catch {
      setDownload({ phase: 'idle' });
      setNotice({ text: 'Could not download — try again.', tone: 'error' });
    }
  };

  /*
   * Copy is offered only while the row is open and its artifacts are loaded,
   * so the text is in hand when the item is tapped: Safari refuses a
   * clipboard write that is not inside the gesture, and a fetch first would
   * put it outside.
   */
  const runCopy = (text: string): void => {
    setNotice(null);
    void copyText(text).then((copied) => {
      setNotice(
        copied
          ? { text: 'Copied', tone: 'ok' }
          : {
              text: 'Could not copy — this browser would not allow it. Open the recording and select the text by hand.',
              tone: 'error',
            },
      );
    });
  };

  const busy = deleteCaptures.isPending || moveCaptures.isPending || download.phase === 'working';
  const selectedList = [...selected];

  return (
    <>
      <section className="recordings" aria-labelledby={headingId} data-selecting={selecting || undefined}>
        {/* The tab above already says it; the heading names the region for a reader. */}
        <h2 id={headingId} className="visually-hidden">
          Recordings
        </h2>

        {ordered.length === 0 && !localUpload ? (
          <p className="recordings__empty">
            Nothing recorded into this note yet. The microphone below records into it.
          </p>
        ) : (
          <ul className="recordings__list" role="list">
            {localUpload && !selecting && (
              <li className="recordings__filing">
                <LocalUploadItem model={localUpload} />
              </li>
            )}
            {ordered.map((capture) => (
              <RecordingRow
                key={capture.id}
                capture={capture}
                expanded={!selecting && openId === capture.id}
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
                selecting={selecting}
                selected={selected.has(capture.id)}
                onToggleSelected={() => {
                  toggleSelected(capture.id);
                }}
                onStartSelecting={() => {
                  startSelecting(capture.id);
                }}
                onMove={() => {
                  setPending({ kind: 'move', ids: [capture.id] });
                }}
                onDelete={() => {
                  setPending({ kind: 'delete', ids: [capture.id] });
                }}
                onDownload={() => void runDownload([capture.id])}
                onCopy={runCopy}
                lang={lang}
                from={sourceLabel(capture, deviceNames)}
                effectiveLanguage={effectiveLanguage}
                retranscribeLabel={retranscribeLabel(effectiveLanguage)}
                onRetranscribe={() => {
                  setNotice(null);
                  retranscribe.mutate(capture.id);
                }}
              />
            ))}
          </ul>
        )}

        {notice && !selecting && (
          <p className="recordings__notice" data-tone={notice.tone} role="status">
            {notice.text}
            {notice.target && (
              <>
                {' · '}
                <button
                  type="button"
                  className="recordings__notice-link"
                  onClick={() => void navigate(ROUTES.note(notice.target?.id ?? ''))}
                >
                  Open {notice.target.title}
                </button>
              </>
            )}
          </p>
        )}
      </section>

      {selecting && (
        <SelectionBar
          label="Recording actions"
          count={selected.size}
          allSelected={allSelected}
          onSelectAll={() => {
            setSelectedIds(allSelected ? new Set() : new Set(ordered.map((c) => c.id)));
          }}
          onCancel={stopSelecting}
          status={
            download.phase === 'working'
              ? `${String(download.done)} of ${String(download.total)}…`
              : notice?.text
          }
        >
          <button
            type="button"
            className="selection-bar__action"
            disabled={selected.size === 0 || busy}
            onClick={() => void runDownload(selectedList)}
          >
            {download.phase === 'working' ? 'Downloading…' : 'Download'}
          </button>
          <button
            type="button"
            className="selection-bar__action"
            disabled={selected.size === 0 || busy}
            onClick={() => {
              setPending({ kind: 'move', ids: selectedList });
            }}
          >
            {moveCaptures.isPending ? 'Moving…' : 'Move'}
          </button>
          <button
            type="button"
            className="selection-bar__action selection-bar__action--destructive"
            disabled={selected.size === 0 || busy}
            onClick={() => {
              setPending({ kind: 'delete', ids: selectedList });
            }}
          >
            {deleteCaptures.isPending ? 'Deleting…' : 'Delete'}
          </button>
        </SelectionBar>
      )}

      <ConfirmDialog
        open={pending?.kind === 'delete'}
        title={
          pending && pending.ids.length > 1
            ? `Delete ${String(pending.ids.length)} recordings?`
            : 'Delete this recording?'
        }
        body={
          pending && pending.ids.length > 1
            ? 'The recordings and the paragraphs they dictated are removed from this note, and the audio and transcripts are destroyed. This cannot be undone.'
            : 'The recording and the paragraph it dictated are removed from this note, and the audio and transcript are destroyed. This cannot be undone.'
        }
        confirmLabel={pending && pending.ids.length > 1 ? 'Delete them' : 'Delete it'}
        destructive
        onCancel={() => {
          setPending(null);
        }}
        onConfirm={() => {
          const ids = pending?.ids ?? [];
          setPending(null);
          runDelete(ids);
        }}
      />

      <MoveSheet
        open={pending?.kind === 'move'}
        count={pending?.ids.length ?? 0}
        excludeNoteId={note.id}
        pending={moveCaptures.isPending}
        error={pending?.kind === 'move' && notice?.tone === 'error' ? notice.text : null}
        onCancel={() => {
          setPending(null);
        }}
        onChoose={(target, title) => {
          runMove(pending?.ids ?? [], target, title);
        }}
      />
    </>
  );
}
