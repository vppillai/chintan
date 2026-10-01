import { matchPath, useLocation } from 'react-router';

import { useNote } from '@/api/queries.ts';
import { ROUTES } from '@/app/routes.ts';

/**
 * The note a recording started here goes into: the open note, or `null` for
 * a new one. The record disc and anything else that records from the shell
 * read it, so they can never disagree about where a recording lands.
 *
 * Not into an archived note: the server refuses the recording, and the
 * uploader would then offer a Resend that can never land. The note screen's
 * own query answers this, so asking costs no second request.
 */
export function useRecordTarget(): string | null {
  const { pathname } = useLocation();
  const noteId = matchPath(ROUTES.notePattern, pathname)?.params.id ?? null;
  const { data: note } = useNote(noteId ?? undefined);
  return noteId !== null && !note?.archived ? noteId : null;
}
