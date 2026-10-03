/**
 * What the note screen needs to know about its drawer without loading it:
 * which disclosure is open and where focus goes when one opens. The drawer
 * itself (`NoteDrawer.tsx`) is a lazy chunk, so these live apart from it.
 */

export type NotePanelKind = 'details' | 'share';

/** The id of a note's language select, so the meta line can send focus to it. */
export function noteLanguageFieldId(noteId: string): string {
  return `note-language-${noteId}`;
}

/** The id of the open drawer's heading, so the screen can send focus to it. */
export function notePanelHeadingId(noteId: string): string {
  return `note-panel-heading-${noteId}`;
}
