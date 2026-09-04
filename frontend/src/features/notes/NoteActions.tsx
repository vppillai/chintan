import { useId, useState } from 'react';
import { useNavigate } from 'react-router';

import { ApiError } from '@/api/problem.ts';
import { useArchiveNote, useDeleteNoteForever, useRestoreNote, useSettings } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { CopyButton } from '@/components/CopyButton.tsx';
import { DownloadButton } from '@/components/DownloadButton.tsx';
import { LanguageSelect } from '@/components/LanguageSelect.tsx';
import { TagEditor } from '@/components/TagEditor.tsx';
import { languageName } from '@/features/settings/languages.ts';

import { describePurge, purgeCountdown } from './purge.ts';
import type { NoteEditor } from './useNoteEditor.ts';

/**
 * The note's action bar: Tags · Share · Archive · Record into this.
 *
 * Sticky at the foot of the note, so none of it is buried under a long body
 * and six transcripts — "Record into this" in particular is the one-tap path
 * to add to a note you are already reading, and a control the user has to
 * scroll two screens to find is not one tap.
 *
 * Tags and Share are disclosures: the tag and alias editors and the copy and
 * download controls open above the bar when asked for and are otherwise out
 * of the way. The two used to sit inline between the body and Archive, which
 * put a copy control adjacent to a destructive one — how a stray thumb
 * destroys a note it meant to keep. Archive is the far side of the bar from
 * Record for the same reason.
 *
 * Getting rid of a note, and getting it back, keep their two confirmation
 * disciplines, because these are two different promises:
 *
 *   Archive       reversible for as long as the purge window lasts, so it asks
 *                 once, plainly.
 *   Delete for    irreversible, and it takes the recordings and the transcripts
 *   ever          with it, so it names what goes and requires the note's title
 *                 to be typed before the control unlocks.
 *
 * All three operations — archive, restore and purge — are served by the
 * backend, wrapped in `endpoints.ts`, and reachable from here. Without these
 * controls the app is append-only and the note screen's own "may have been
 * archived or purged" describes states it cannot reach.
 */
export function NoteActions({
  note,
  editor,
  hidden = false,
}: {
  note: NoteDetailWire;
  editor: NoteEditor;
  /** Stepping aside for another bar at the foot of the screen; state is kept. */
  hidden?: boolean;
}) {
  const navigate = useNavigate();
  const archive = useArchiveNote();
  const restore = useRestoreNote();
  const purge = useDeleteNoteForever();

  const tagsId = useId();
  const shareId = useId();
  const [open, setOpen] = useState<'tags' | 'share' | null>(null);
  const [confirming, setConfirming] = useState<'archive' | 'purge' | null>(null);

  const busy = archive.isPending || restore.isPending || purge.isPending;
  const failure = archive.error ?? restore.error ?? purge.error;
  const { draft } = editor.model;

  return (
    <div className="note-bar-anchor" hidden={hidden}>
      {open === 'tags' && (
        <div id={tagsId} className="note-panel">
          <TagEditor
            label="Tags"
            values={draft.tags}
            placeholder="Add a tag"
            onChange={(tags) => {
              editor.edit({ tags });
            }}
            onCommit={() => void editor.saveNow()}
          />
          <TagEditor
            label="Also called"
            values={draft.aliases}
            placeholder="Add another name"
            maxLength={120}
            onChange={(aliases) => {
              editor.edit({ aliases });
            }}
            onCommit={() => void editor.saveNow()}
          />
          <NoteLanguage
            value={draft.language ?? ''}
            onChange={(language) => {
              editor.edit({ language });
              void editor.saveNow();
            }}
          />
        </div>
      )}

      {open === 'share' && (
        /*
         * Title first, then the body: a body pasted somewhere else with no
         * title loses what it was about, and re-typing that is exactly the
         * friction this is meant to remove.
         */
        <div id={shareId} className="note-panel note-copy">
          <CopyButton
            label="Copy note"
            text={() => [draft.title.trim(), draft.body.trim()].filter(Boolean).join('\n\n')}
          />
          <DownloadButton
            label="Download note"
            filename={() => `${filenameFor(draft.title)}.md`}
            blob={() =>
              Promise.resolve(
                new Blob([`# ${draft.title.trim()}\n\n${draft.body.trim()}\n`], {
                  type: 'text/markdown',
                }),
              )
            }
          />
        </div>
      )}

      {note.archived && (
        <p className="note-actions__state" role="status">
          This note is archived. {describePurge(purgeCountdown(note.purge_after))}.
        </p>
      )}

      {failure && <Failure error={failure} />}

      <div className="note-bar" role="toolbar" aria-label="Note actions">
        <button
          type="button"
          className="note-bar__action"
          aria-expanded={open === 'tags'}
          aria-controls={tagsId}
          onClick={() => {
            setOpen((current) => (current === 'tags' ? null : 'tags'));
          }}
        >
          Tags
        </button>
        <button
          type="button"
          className="note-bar__action"
          aria-expanded={open === 'share'}
          aria-controls={shareId}
          onClick={() => {
            setOpen((current) => (current === 'share' ? null : 'share'));
          }}
        >
          Share
        </button>

        {note.archived ? (
          <>
            <button
              type="button"
              className="note-bar__action"
              disabled={busy}
              onClick={() => {
                restore.mutate(note.id);
              }}
            >
              {restore.isPending ? 'Restoring…' : 'Restore'}
            </button>
            <button
              type="button"
              className="note-bar__action note-bar__action--destructive"
              disabled={busy}
              onClick={() => {
                setConfirming('purge');
              }}
            >
              Delete forever
            </button>
          </>
        ) : (
          <>
            <button
              type="button"
              className="note-bar__action"
              disabled={busy}
              onClick={() => {
                setConfirming('archive');
              }}
            >
              {archive.isPending ? 'Archiving…' : 'Archive'}
            </button>
            {/*
              The server refuses an archived note as a capture target, so the
              control is only offered where it can work.
            */}
            <button
              type="button"
              className="note-bar__action note-bar__action--primary"
              onClick={() => void navigate(ROUTES.captureInto(note.id))}
            >
              <span aria-hidden="true">＋ </span>Record into this
            </button>
          </>
        )}
      </div>

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
    </div>
  );
}

/**
 * The note's transcription language, in the Tags disclosure with the other
 * facts about the note that are not its text.
 *
 * The inherit entry names what it inherits — "Default (Malayalam)" — read from
 * the You screen's setting, so the choice is between real languages rather
 * than between a language and a word. The helper line says what the setting
 * reaches: only a recording made *into* this note. A recording that is routed
 * here afterwards was transcribed before anyone knew where it was going, so
 * only the default could apply to it.
 */
function NoteLanguage({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const id = useId();
  const { data: settings } = useSettings();
  const inherited = settings?.default_language ?? 'en';

  return (
    <section className="language-field">
      <label className="tag-editor__label" htmlFor={id}>
        Language
      </label>
      <LanguageSelect
        id={id}
        value={value}
        inherit={{ label: `Default (${languageName(inherited)})` }}
        onChange={onChange}
      />
      <p className="language-field__hint">
        For recordings made into this note — Record into this, or chosen as the target. A
        recording filed automatically is transcribed in your default language.
      </p>
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

/**
 * A dictated title, made safe as a filename.
 *
 * The title comes from speech, unbounded — no reserved characters, no length
 * limit, sometimes not even Latin script. `/` and `\` would nest or break a
 * path; a title trimmed to nothing (all punctuation, or empty) still needs a
 * name a save dialog can show.
 */
export function filenameFor(title: string): string {
  const cleaned = title.trim().replace(/[/\\:*?"<>|]/g, '').trim();
  return (cleaned || 'note').slice(0, 120);
}
