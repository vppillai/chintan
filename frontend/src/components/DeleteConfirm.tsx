import { ConfirmDialog } from './ConfirmDialog.tsx';

/**
 * The "are you sure" before Delete on Home — the row's ⋮ and swipe tray and
 * the note header's ⋮ all ask through this one component, so the question is
 * worded once, and it always names the note.
 *
 * Delete is the archive, and until 2026-09-27 it happened on the tap with
 * only the toast's Undo as the way back (owner, 2026-09-26: no typed word).
 * The owner then asked for the question ("When deleting, ask are you sure.
 * Don't directly archive"): a slip on the row's tray, or a tap on the wrong
 * menu item, was sending notes to the Archive with nothing in between. So it
 * asks, plainly — no field, focus on Cancel, Escape and Enter on Cancel both
 * back out — and the body says where the note goes, because "Delete" and
 * "kept for 30 days" have to be read together. The Undo toast still follows
 * a confirmed delete: it costs nothing and covers the tap that was a slip on
 * the second button too.
 */
export function DeleteConfirm({
  open,
  title,
  onConfirm,
  onCancel,
}: {
  open: boolean;
  title: string;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <ConfirmDialog
      open={open}
      title={`Delete “${title}”?`}
      body="It is kept in the Archive for 30 days, then gone for good."
      confirmLabel="Delete"
      destructive
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  );
}
