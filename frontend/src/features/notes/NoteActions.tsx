import { useState, type RefObject } from 'react';

import { ApiError } from '@/api/problem.ts';
import {
  useArchiveNote,
  useDeleteNoteForever,
  usePinNote,
  useRegenerateNote,
  useRestoreNote,
  useUndoDelete,
} from '@/api/queries.ts';
import { isTerminalStatus, type NoteDetailWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { useTabNavigation } from '@/app/useTabNavigation.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { DeleteConfirm } from '@/components/DeleteConfirm.tsx';
import { OverflowMenu, type OverflowMenuItem } from '@/components/OverflowMenu.tsx';
import { showDeleted } from '@/components/Toast.tsx';
import { isStuck } from '@/features/capture/filing/model.ts';
import { useOnline } from '@/hooks/useOnline.ts';

import type { NotePanelKind } from './notePanel.ts';
import { parseChecklist } from './checklist.ts';
import { useStartTidy, useTidying } from './useTidyList.ts';

/**
 * The note's actions: Details · Share · Pin (or Unpin) · Tidy up list (a
 * checklist's) · Regenerate from recordings… · Delete (or Restore · Delete forever), behind the ⋮ in the
 * header, and the two disclosures they open in a drawer at the foot of the
 * screen. Pin is here as well as on the row (2026-09-24, B) because the note
 * is where the person decides it is worth keeping at the top; an archived
 * note offers no pin, since archiving clears it.
 *
 * Regenerate (owner, 2026-09-27) re-does the AI cleanup of every recording in
 * the note with the current prompts, from the transcripts the server already
 * has — the way to bring a note made before a prompt change up to date. It
 * asks once, plainly, because it replaces edits made inside those paragraphs;
 * ticks are kept. The count in the sentence is the recordings that have
 * landed, which is the server's own rule (`service.RegenerableCaptures`) as
 * far as this side can see it: a verbatim note has nothing from a prompt, and
 * a paragraph the person rewrote by hand is left alone by the worker and not
 * counted in its 202 — and a 202 that counted nothing says so under the
 * menu, where an error would. The item is off while a recording is still
 * moving — this regeneration or a new recording — since the server would
 * refuse, and offline, since nothing here can be queued; a recording that has
 * sat stuck (`isStuck`, the server's own rule) does not count as moving.
 * Progress is the filing strip under the meta line: the captures go back to
 * `transcribed` and the note's poll follows them until the last lands.
 *
 * They were a sticky bar at the foot with a fourth, primary "Record into
 * this". On a phone that bar wrapped to two rows (96 px) and sat 30 px above
 * the tab bar's mic, which recorded into a *new* note — two record controls
 * with different glyphs, recording to different places, from one screen. The
 * mic is now contextual (`RecordButton`), the bar is gone, and these three
 * actions taken once a month live where the row's actions already do: in an
 * overflow menu. About 130 px of the phone went back to the note's body.
 *
 * Details and Share are still disclosures, in a drawer at the foot of the
 * screen (`NoteDrawer.tsx`). Which one is open belongs to the screen, which
 * this menu tells through `onOpenPanel`; the drawer hands focus back to the
 * menu's trigger (`triggerRef`) when it closes.
 *
 * Getting rid of a note, and getting it back, are two different promises
 * (owner, 2026-09-26: no typed word anywhere):
 *
 *   Delete        the archive. Reversible for as long as the purge window
 *                 lasts. It still asks once (owner, 2026-09-27: "ask are you
 *                 sure, don't directly archive" — `DeleteConfirm`, which says
 *                 where the note goes); then the note goes, the screen
 *                 returns to the library, and the toast there offers Undo.
 *   Delete for    irreversible, and it takes the recordings and the transcripts
 *   ever          with it, so it names what goes — the title is in the
 *                 sentence — and asks once, plainly, with focus on Cancel.
 */

/** The header's ⋮: the note's actions, with the dialogs they need. */
export function NoteMenu({
  note,
  triggerRef,
  onOpenPanel,
}: {
  note: NoteDetailWire;
  /** The screen's handle on the ⋮, for the drawer to hand focus back to. */
  triggerRef: RefObject<HTMLButtonElement | null>;
  onOpenPanel: (panel: NotePanelKind) => void;
}) {
  const { goHome, goTab } = useTabNavigation();
  const archive = useArchiveNote();
  const restore = useRestoreNote();
  const undo = useUndoDelete();
  const purge = useDeleteNoteForever();
  const pin = usePinNote();
  const regenerate = useRegenerateNote();
  // A pin made offline would pause until the network returned and then fire
  // with a version the cache may no longer hold (review 2026-09-24, R4-11).
  const online = useOnline();
  const [confirming, setConfirming] = useState<'delete' | 'purge' | 'regenerate' | null>(null);
  const regenerable = regenerableCount(note);
  // A capture that has sat past the stuck bound is one the server no longer
  // counts as in flight (CaptureStuck) and would take a new request; it must
  // not hold the item at "Regenerating…" for good.
  const regenerating = (note.captures ?? []).some(
    (capture) => !isTerminalStatus(capture.status) && !isStuck(capture),
  );
  // The dialog counts the landed recordings; the server also skips a
  // paragraph rewritten by hand, so its 202 can say zero where the dialog
  // said one — and a zero with no line under the menu looks like nothing
  // happened.
  const nothingToRegenerate = regenerate.isSuccess && regenerate.data.captures === 0;
  // Tidy up list (R8-F8, `useTidyList.ts`): only for a checklist with an open
  // item, since the model splits and groups what is still to do; off while
  // one runs and offline, like Regenerate.
  const startTidy = useStartTidy();
  const tidying = useTidying(note.id);
  const tidyable =
    note.kind === 'checklist' && parseChecklist(note.body).some((item) => !item.done);

  const busy =
    archive.isPending ||
    restore.isPending ||
    undo.isPending ||
    purge.isPending ||
    pin.isPending ||
    regenerate.isPending;
  const failure =
    archive.error ?? restore.error ?? undo.error ?? purge.error ?? pin.error ?? regenerate.error;

  const items: OverflowMenuItem[] = [
    { label: 'Details', onSelect: () => onOpenPanel('details') },
    { label: 'Share', onSelect: () => onOpenPanel('share') },
    ...(note.archived
      ? []
      : [
          {
            label: note.pinned ? 'Unpin' : 'Pin',
            disabled: busy || !online,
            onSelect: () => {
              pin.mutate({ note, pinned: !note.pinned });
            },
          },
          ...(tidyable
            ? [
                {
                  label: tidying ? 'Tidying…' : 'Tidy up list',
                  disabled: busy || !online || tidying,
                  onSelect: () => {
                    startTidy(note.id);
                  },
                },
              ]
            : []),
          {
            label: regenerating ? 'Regenerating…' : 'Regenerate from recordings…',
            disabled: busy || !online || regenerating || regenerable === 0,
            onSelect: () => {
              setConfirming('regenerate');
            },
          },
        ]),
    ...(note.archived
      ? [
          {
            label: restore.isPending ? 'Restoring…' : 'Restore',
            disabled: busy,
            onSelect: () => {
              restore.mutate(note.id);
            },
          },
          {
            label: 'Delete forever',
            destructive: true,
            disabled: busy,
            onSelect: () => {
              setConfirming('purge');
            },
          },
        ]
      : [
          {
            label: archive.isPending ? 'Deleting…' : 'Delete',
            destructive: true,
            disabled: busy,
            onSelect: () => {
              setConfirming('delete');
            },
          },
        ]),
  ];

  const archiveNow = (): void => {
    archive.mutate(note.id, {
      // Home by going back down to it (R8, F2), not by pushing or replacing:
      // a replace left [Home, Home], and a push would leave the note's own
      // URL, now archived, for Back to walk straight into. The toast outlives this
      // menu — it is the shell's — and Undo restores through this hook, whose
      // own `onSuccess` refetches the lists whether or not the menu is still
      // mounted; handed the note as it was, it re-pins a pinned one
      // (`useUndoDelete`).
      onSuccess: () => {
        showDeleted(() => {
          undo.mutate(note);
        });
        goHome();
      },
    });
  };

  return (
    <>
      <OverflowMenu label="Note actions" items={items} triggerRef={triggerRef} />

      {failure && (
        <p className="note-actions__error note-menu__error" role="alert">
          {failure instanceof ApiError ? failure.userMessage : 'That did not go through.'}
        </p>
      )}
      {!failure && nothingToRegenerate && (
        <p className="note-actions__error note-menu__error" role="status">
          Nothing to regenerate: every paragraph is in your own words now.
        </p>
      )}

      <DeleteConfirm
        open={confirming === 'delete'}
        title={note.title}
        onCancel={() => {
          setConfirming(null);
        }}
        onConfirm={() => {
          setConfirming(null);
          archiveNow();
        }}
      />

      <ConfirmDialog
        open={confirming === 'regenerate'}
        title="Regenerate from recordings?"
        body={regenerateSentence(regenerable)}
        confirmLabel="Regenerate"
        onCancel={() => {
          setConfirming(null);
        }}
        onConfirm={() => {
          setConfirming(null);
          regenerate.mutate(note.id);
        }}
      />

      <ConfirmDialog
        open={confirming === 'purge'}
        title="Delete this note forever?"
        body={`“${note.title}” and its recordings and transcripts are destroyed. This cannot be undone, and there is no copy on the server or on any other device you have signed in on.`}
        confirmLabel="Delete forever"
        destructive
        onCancel={() => {
          setConfirming(null);
        }}
        onConfirm={() => {
          setConfirming(null);
          purge.mutate(note.id, {
            // Back to the archive, which is where this note was, the way the
            // archive is always reached (R8, F2): a replace stacked a second
            // archive on the first. Staying put would leave the screen
            // showing a note the server no longer has.
            onSuccess: () => {
              goTab(ROUTES.archive);
            },
          });
        }}
      />
    </>
  );
}

/** The recordings a regeneration would clean again, as far as the wire shows. */
function regenerableCount(note: NoteDetailWire): number {
  if (note.verbatim) return 0;
  return (note.captures ?? []).filter((capture) => capture.status === 'appended').length;
}

/** What the confirm says: the count, what is replaced, what is kept. */
function regenerateSentence(count: number): string {
  const recordings = count === 1 ? '1 recording' : `${String(count)} recordings`;
  return `Re-does the AI cleanup of ${recordings} with the current settings; edits you made inside those paragraphs are replaced. Ticks are kept.`;
}
