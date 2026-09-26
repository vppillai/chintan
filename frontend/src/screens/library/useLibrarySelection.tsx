import { useRef, useState } from 'react';

import {
  useBulkArchiveNotes,
  useBulkPurgeNotes,
  useBulkRestoreNotes,
  useUndoDelete,
} from '@/api/queries.ts';
import type { NoteState, NoteWire } from '@/api/schema.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import type { SelectOptions } from '@/components/NoteRow.tsx';
import { SelectionBar } from '@/components/SelectionBar.tsx';
import { showDeleted } from '@/components/Toast.tsx';

import { HOLD_TO_DELETE_ABOVE, HOLD_TO_DELETE_MS } from './holdToDelete.ts';

export type LibrarySelection = ReturnType<typeof useLibrarySelection>;

/**
 * Multi-select, for doing something to several notes at once rather than one
 * at a time from inside each note's own screen. Confined to component state
 * rather than the URL: leaving the screen is exactly when "which notes were
 * selected" should stop mattering.
 *
 * `visible` is the rows in the order they are on screen, so Shift-click
 * ranges over what the eye sees and the bar's actions take the selected rows
 * in that order.
 */
export function useLibrarySelection(visible: readonly NoteWire[]) {
  const [selecting, setSelecting] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  /** The last row toggled, for Shift-click to extend from. */
  const anchorRef = useRef<string | null>(null);
  const [confirming, setConfirming] = useState<'restore' | 'purge' | null>(null);
  const bulkArchive = useBulkArchiveNotes();
  const bulkRestore = useBulkRestoreNotes();
  const undo = useUndoDelete();
  const bulkPurge = useBulkPurgeNotes();

  const exitSelecting = (): void => {
    setSelecting(false);
    setSelectedIds(new Set());
    anchorRef.current = null;
  };

  const selectableIds = visible.map((note) => note.id);

  /*
   * A row asking to be selected — the first one starts the mode. With Shift
   * held (a mouse), every row between the last one toggled and this one is
   * selected too, in the order they are on screen; the anchor moves here
   * either way, so a second Shift-click extends from this row.
   */
  const toggleSelect = (noteId: string, { range }: SelectOptions): void => {
    const anchor = anchorRef.current;
    anchorRef.current = noteId;
    setSelecting(true);
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (range && anchor) {
        const from = selectableIds.indexOf(anchor);
        const to = selectableIds.indexOf(noteId);
        if (from !== -1 && to !== -1) {
          for (const id of selectableIds.slice(Math.min(from, to), Math.max(from, to) + 1)) {
            next.add(id);
          }
          return next;
        }
      }
      if (next.has(noteId)) next.delete(noteId);
      else next.add(noteId);
      return next;
    });
  };

  const allSelected = selectableIds.length > 0 && selectedIds.size === selectableIds.length;
  const toggleAll = (): void => {
    setSelectedIds(allSelected ? new Set() : new Set(selectableIds));
  };

  /*
   * Delete on Home is the archive, on the tap: the notes wait in the Archive
   * for thirty days, and the toast offers Undo, which restores the ones that
   * went — pinned again, where they were pinned (`useUndoDelete`). The
   * library stays mounted through the mutation, so the per-call `onSuccess`
   * is safe here where a row's is not (`NoteRow`).
   */
  const deleteSelected = (): void => {
    const chosen = visible.filter((note) => selectedIds.has(note.id));
    bulkArchive.mutate(
      chosen.map((note) => note.id),
      {
        onSuccess: (results) => {
          exitSelecting();
          const gone = chosen.filter((_note, i) => results[i]?.status === 'fulfilled');
          if (gone.length === 0) return;
          showDeleted(gone.length, () => {
            undo.mutate(gone);
          });
        },
      },
    );
  };
  const restoreSelected = (): void => {
    setConfirming(null);
    bulkRestore.mutate(Array.from(selectedIds), { onSuccess: exitSelecting });
  };
  const purgeSelected = (): void => {
    const ids = Array.from(selectedIds);
    setConfirming(null);
    bulkPurge.mutate(ids, { onSuccess: exitSelecting });
  };

  return {
    selecting,
    selectedIds,
    allSelected,
    /** One note selected reads as one note: "Delete it forever", not "them". */
    one: selectedIds.size === 1,
    onlySelected:
      selectedIds.size === 1 ? visible.find((note) => selectedIds.has(note.id)) : undefined,
    confirming,
    setConfirming,
    busy: bulkArchive.isPending || bulkRestore.isPending || bulkPurge.isPending,
    archiving: bulkArchive.isPending,
    restoring: bulkRestore.isPending,
    purging: bulkPurge.isPending,
    toggleSelect,
    toggleAll,
    exitSelecting,
    deleteSelected,
    restoreSelected,
    purgeSelected,
  };
}

/**
 * The bar above the tab bar while selecting, and the two confirms. In the
 * active view the one action is Delete — the archive, on the tap, with Undo
 * in the toast (owner, 2026-09-26: no typed word anywhere). In the archive
 * the actions are Restore and Delete forever, the one thing here that cannot
 * be undone: it asks once, plainly, and for more than ten notes the confirm
 * is a press held for a second rather than a tap (`holdMs`), because "select
 * all, delete forever" on a full archive is the one tap in the app whose slip
 * cannot be taken back, and a hold is a gesture no slip makes. The bar sits
 * above the tab bar, not at the end of the list (backlog U2, Q6).
 */
export function LibrarySelectionBar({
  selection,
  view,
  asking,
}: {
  selection: LibrarySelection;
  view: NoteState;
  /** The Ask panel stands in for the list, and the bar goes with the list. */
  asking: boolean;
}) {
  const { selectedIds, one, onlySelected } = selection;
  return (
    <>
      {!asking && selection.selecting && (
        <SelectionBar
          label="Bulk actions"
          count={selectedIds.size}
          allSelected={selection.allSelected}
          onSelectAll={selection.toggleAll}
          onCancel={selection.exitSelecting}
        >
          {view === 'active' ? (
            <button
              type="button"
              className="selection-bar__action selection-bar__action--destructive"
              disabled={selectedIds.size === 0 || selection.busy}
              onClick={selection.deleteSelected}
            >
              {selection.archiving ? 'Deleting…' : 'Delete'}
            </button>
          ) : (
            <>
              <button
                type="button"
                className="selection-bar__action"
                disabled={selectedIds.size === 0 || selection.busy}
                onClick={() => {
                  selection.setConfirming('restore');
                }}
              >
                {selection.restoring ? 'Restoring…' : 'Restore'}
              </button>
              <button
                type="button"
                className="selection-bar__action selection-bar__action--destructive"
                disabled={selectedIds.size === 0 || selection.busy}
                onClick={() => {
                  selection.setConfirming('purge');
                }}
              >
                {selection.purging ? 'Deleting…' : 'Delete forever'}
              </button>
            </>
          )}
        </SelectionBar>
      )}

      <ConfirmDialog
        open={selection.confirming === 'restore'}
        title={`Restore ${countLabel(selectedIds.size)}?`}
        body={
          one
            ? 'It leaves the archive and returns to your notes.'
            : 'They leave the archive and return to your notes.'
        }
        confirmLabel={one ? 'Restore it' : 'Restore them'}
        onCancel={() => {
          selection.setConfirming(null);
        }}
        onConfirm={selection.restoreSelected}
      />

      {/*
        As the row's own dialog does, the sentence names the one note (QA
        2026-09-21, finding 14).
      */}
      <ConfirmDialog
        open={selection.confirming === 'purge'}
        title={`Delete ${countLabel(selectedIds.size)} forever?`}
        body={`${
          one
            ? `${onlySelected ? `“${onlySelected.title}” and its` : 'Its'} recordings and transcripts are`
            : 'Their recordings and transcripts are'
        } destroyed. This cannot be undone, and there is no copy on the server or on any other device you have signed in on.`}
        confirmLabel={
          selectedIds.size > HOLD_TO_DELETE_ABOVE
            ? `Hold to delete ${String(selectedIds.size)} notes`
            : one
              ? 'Delete it forever'
              : 'Delete them forever'
        }
        holdMs={selectedIds.size > HOLD_TO_DELETE_ABOVE ? HOLD_TO_DELETE_MS : undefined}
        destructive
        onCancel={() => {
          selection.setConfirming(null);
        }}
        onConfirm={selection.purgeSelected}
      />
    </>
  );
}

function countLabel(count: number): string {
  return `${String(count)} ${count === 1 ? 'note' : 'notes'}`;
}
