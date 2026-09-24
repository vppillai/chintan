import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { useNavigate } from 'react-router';

import { useApi } from '@/api/ApiProvider.tsx';
import { ApiError } from '@/api/problem.ts';
import {
  queryKeys,
  useDeleteCaptures,
  useDevices,
  useMoveCaptures,
  useRetryCapture,
  useSettings,
} from '@/api/queries.ts';
import {
  isTerminalStatus,
  type CaptureStatus,
  type CaptureWire,
  type NoteDetailWire,
  type NoteWire,
} from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { copyText } from '@/components/CopyButton.tsx';
import { saveBlob } from '@/components/DownloadButton.tsx';
import { Icon } from '@/components/Icon.tsx';
import { OverflowMenu, type OverflowMenuItem } from '@/components/OverflowMenu.tsx';
import { SelectionBar } from '@/components/SelectionBar.tsx';
import { SwipeRow } from '@/components/SwipeRow.tsx';
import { FilingStages, LocalUploadItem, TargetPrompt } from '@/features/capture/FilingRow.tsx';
import type { CaptureModel } from '@/features/capture/machine.ts';
import { AUTO_LANGUAGE, languageName } from '@/features/settings/languages.ts';
import { useLongPress, type LongPress } from '@/hooks/useLongPress.ts';

import { MoveSheet } from './MoveSheet.tsx';
import { TranscriptPanel, type TranscriptView } from './TranscriptPanel.tsx';
import { WaveformScrubber } from './WaveformScrubber.tsx';
import { formatTime, loadCaptureArtifacts } from './artifacts.ts';
import { describeMoment, formatDurationShort } from './groups.ts';
import { usePlayer } from './usePlayer.ts';
import { archiveName, zipRecordings } from './zipRecordings.ts';

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

  const runMove = (ids: string[], target: NoteWire): void => {
    setNotice(null);
    moveCaptures.mutate(
      { noteId: note.id, targetId: target.id, captureIds: ids },
      {
        onSuccess: (result) => {
          setPending(null);
          const outcome = describeOutcome(result.done.length, result.failed, 'moved');
          setNotice(
            result.done.length > 0
              ? { ...outcome, text: `${outcome.text} to “${target.title}”`, target }
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
        requireText="delete"
        requireLabel='Type "delete" to confirm'
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
        onChoose={(target) => {
          runMove(pending?.ids ?? [], target);
        }}
      />
    </>
  );
}

/** The capture that was moving on the last render and is `appended` on this one. */
export function justLanded(
  before: readonly CaptureWire[],
  after: readonly CaptureWire[],
): CaptureWire | undefined {
  const was = new Map<string, CaptureStatus>(before.map((capture) => [capture.id, capture.status]));
  return after.find(
    (capture) =>
      capture.status === 'appended' && was.has(capture.id) && was.get(capture.id) !== 'appended',
  );
}

/** A capture that arrived through the inbox under a device key rather than from this app. */
export function isFromDevice(capture: Pick<CaptureWire, 'source'>): boolean {
  return (capture.source ?? 'app').startsWith('device:');
}

/**
 * "From Watch" for a row a device sent, named from the devices list; "From a
 * device" when the list no longer has it (removed since) or has not answered.
 * Nothing for a recording made here — every other row on the tab is one, and
 * saying so would be the "Filed" repeated down the list all over again.
 */
export function sourceLabel(
  capture: Pick<CaptureWire, 'source'>,
  names: ReadonlyMap<string, string>,
): string | null {
  if (!isFromDevice(capture)) return null;
  const id = (capture.source ?? '').slice('device:'.length);
  return `From ${names.get(id) ?? 'a device'}`;
}

interface Notice {
  text: string;
  tone: 'ok' | 'error';
  /** The note the recordings went to, offered as "Open <title>". */
  target?: NoteWire;
}

/**
 * "Tamil" — the language Whisper heard, when it is worth a chip: under
 * Auto-detect always, because what it picked is the one thing the reader
 * cannot otherwise know; under a chosen language only when the two differ,
 * which is the transcript that came back in the wrong script. The worker
 * stores Whisper's English name, so the comparison is by name; a code the
 * curated list lacks still compares by `Intl` name through `languageName`.
 */
export function heardAs(detected: string | null, effective: string): string | null {
  if (!detected) return null;
  const name = detected.charAt(0).toUpperCase() + detected.slice(1);
  if (effective === AUTO_LANGUAGE) return name;
  const same = [languageName(effective), effective].some(
    (candidate) => candidate.toLowerCase() === detected.toLowerCase(),
  );
  return same ? null : name;
}

/** "Transcribe again in Malayalam", or the honest form for Auto-detect. */
export function retranscribeLabel(language: string): string {
  return language === AUTO_LANGUAGE
    ? 'Transcribe again (auto-detect)'
    : `Transcribe again in ${languageName(language)}`;
}

/**
 * Asks the server to transcribe one recording again. The 202 carries the
 * capture back at `transcribing`, which is written straight onto the cached
 * note so the row wears the stage strip before the refetch; the note query
 * then follows the pipeline as it does for a new recording. A 404 is a
 * backend older than the route, and is said as "not yet" rather than as a
 * missing recording.
 */
function useRetranscribeCapture(noteId: string, onNotice: (notice: Notice) => void) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (captureId: string) => api.retranscribeCapture(captureId),
    onSuccess: (capture) => {
      queryClient.setQueryData<NoteDetailWire>(queryKeys.note(noteId), (current) =>
        current?.captures
          ? {
              ...current,
              captures: current.captures.map((row) => (row.id === capture.id ? capture : row)),
            }
          : current,
      );
      void queryClient.invalidateQueries({ queryKey: queryKeys.note(noteId) });
      void queryClient.invalidateQueries({ queryKey: ['captures'] });
    },
    onError: (error) => {
      onNotice({
        text:
          error instanceof ApiError && error.isNotFound
            ? 'Transcribing again is not available on this server yet.'
            : failureText(error),
        tone: 'error',
      });
    },
  });
}

type DownloadProgress = { phase: 'idle' } | { phase: 'working'; done: number; total: number };

/** "Wait until it has finished filing" for a 409; the server's own words otherwise. */
function failureText(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.isConflict) return 'Wait until it has finished filing.';
    return error.userMessage;
  }
  return 'That did not go through. Try again.';
}

function describeOutcome(
  done: number,
  failed: readonly { error: unknown }[],
  verb: 'deleted' | 'moved',
): Notice {
  if (failed.length === 0) {
    return {
      text: done === 1 ? `Recording ${verb}` : `${String(done)} recordings ${verb}`,
      tone: 'ok',
    };
  }
  // One refusal is quoted; several are counted, and the first reason stands
  // for them — a batch that stalled is almost always stalled for one reason.
  const reason = failureText(failed[0]?.error);
  if (done === 0) {
    return {
      text: failed.length === 1 ? reason : `${String(failed.length)} could not be ${verb}. ${reason}`,
      tone: 'error',
    };
  }
  return {
    text: `${String(done)} ${verb}; ${String(failed.length)} could not be. ${reason}`,
    tone: 'error',
  };
}

/**
 * How a recording was filed, in the words of the row — when there is
 * something to say.
 *
 * A recording that reached the note says nothing: every row on this tab is
 * in the note by definition, and "Filed" repeated down the list said so
 * six times (review 2026-09-21, T19). The wire says *where* a capture went
 * and *whether* it got there, not who decided — `CaptureWire` carries
 * `note_id`, the router's `suggested_*` fields (cleared the moment a target
 * is set) and `appended_at`, nothing that separates "the router chose this
 * note" from "the user chose it" — so there is no truer word to put there.
 * The states that are worth a word are the ones still moving or gone wrong.
 */
export function filedLabel(capture: CaptureWire): string {
  switch (capture.status) {
    case 'appended':
      return '';
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

function RecordingRow({
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
