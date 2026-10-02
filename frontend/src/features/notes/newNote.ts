import type { NoteKind } from '@/api/schema.ts';

/**
 * What Home's + and the note screen share about a typed note: the title it
 * starts with, selected in the field so typing replaces it (the server
 * refuses an empty one), and the route state that says a note arrived that
 * way. A note still carrying the placeholder, with nothing in its body and
 * no recording, when the screen is left is discarded: it was never a note.
 */
export const NEW_NOTE_TITLE: Record<NoteKind, string> = {
  note: 'New note',
  checklist: 'New checklist',
};

export interface NewNoteState {
  focusTitle: true;
}

/** Whether a note made from the + has been left exactly as it was made. */
export function isUntouchedPlaceholder(note: {
  title: string;
  body: string;
  kind?: NoteKind;
  captures?: readonly unknown[] | undefined;
}): boolean {
  return (
    note.title === NEW_NOTE_TITLE[note.kind ?? 'note'] &&
    note.body.trim() === '' &&
    (note.captures?.length ?? 0) === 0
  );
}
