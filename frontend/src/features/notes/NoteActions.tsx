import { useState } from 'react';
import { useNavigate } from 'react-router';

import { ApiError } from '@/api/problem.ts';
import { useArchiveNote, useDeleteNoteForever, useRestoreNote } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';

import { describePurge, purgeCountdown } from './purge.ts';

/**
 * Getting rid of a note, and getting it back.
 *
 * v2 had no control for any of this. The backend served archive, restore and
 * purge, `endpoints.ts` wrapped all three, and nothing called them — so the app
 * was append-only and the note screen's own "may have been archived or purged"
 * described states it could not reach.
 *
 * Two confirmation disciplines, because these are two different promises:
 *
 *   Archive       reversible for as long as the purge window lasts, so it asks
 *                 once, plainly.
 *   Delete for    irreversible, and it takes the recordings and the transcripts
 *   ever          with it, so it names what goes and requires the note's title
 *                 to be typed before the control unlocks.
 */
export function NoteActions({ note }: { note: NoteDetailWire }) {
  const navigate = useNavigate();
  const archive = useArchiveNote();
  const restore = useRestoreNote();
  const purge = useDeleteNoteForever();

  const [confirming, setConfirming] = useState<'archive' | 'purge' | null>(null);

  const busy = archive.isPending || restore.isPending || purge.isPending;
  const failure = archive.error ?? restore.error ?? purge.error;

  if (note.archived) {
    return (
      <section className="note-actions" aria-label="Archived note">
        <p className="note-actions__state" role="status">
          This note is archived. {describePurge(purgeCountdown(note.purge_after))}.
        </p>

        <div className="screen__actions">
          <button
            type="button"
            className="screen__action"
            disabled={busy}
            onClick={() => {
              restore.mutate(note.id);
            }}
          >
            {restore.isPending ? 'Restoring…' : 'Restore'}
          </button>
          <button
            type="button"
            className="screen__action screen__action--destructive"
            disabled={busy}
            onClick={() => {
              setConfirming('purge');
            }}
          >
            Delete forever
          </button>
        </div>

        {failure && <Failure error={failure} />}

        <ConfirmDialog
          open={confirming === 'purge'}
          title="Delete this note forever?"
          body={`“${note.title}” and its recordings and transcripts are destroyed. This cannot be undone, and there is no copy on the server or on any other device you have signed in on.`}
          confirmLabel="Delete forever"
          requireText={note.title}
          requireLabel={`Type the note's title to confirm: ${note.title}`}
          destructive
          onCancel={() => {
            setConfirming(null);
          }}
          onConfirm={() => {
            setConfirming(null);
            purge.mutate(note.id, {
              // Back to the archive, which is where this note was. Staying put
              // would leave the screen showing a note the server no longer has.
              onSuccess: () => void navigate(ROUTES.archive, { replace: true }),
            });
          }}
        />
      </section>
    );
  }

  return (
    <section className="note-actions" aria-label="Note actions">
      <div className="screen__actions">
        <button
          type="button"
          className="screen__action"
          disabled={busy}
          onClick={() => {
            setConfirming('archive');
          }}
        >
          {archive.isPending ? 'Archiving…' : 'Archive'}
        </button>
      </div>

      {failure && <Failure error={failure} />}

      <ConfirmDialog
        open={confirming === 'archive'}
        title="Archive this note?"
        body="It leaves your notes and moves to the archive, where you can restore it until it is deleted."
        confirmLabel="Archive it"
        destructive
        onCancel={() => {
          setConfirming(null);
        }}
        onConfirm={() => {
          setConfirming(null);
          archive.mutate(note.id, {
            onSuccess: () => void navigate(ROUTES.notes, { replace: true }),
          });
        }}
      />
    </section>
  );
}

/**
 * `replace: true` above is deliberate on both paths: the note's own URL is now
 * either archived or gone, and leaving it in the history means Back walks
 * straight into a screen that 404s.
 */
function Failure({ error }: { error: unknown }) {
  return (
    <p className="note-actions__error" role="alert">
      {error instanceof ApiError ? error.userMessage : 'That did not go through.'}
    </p>
  );
}
