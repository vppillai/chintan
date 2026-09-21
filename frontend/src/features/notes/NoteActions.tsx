import { useId, useState } from 'react';
import { useNavigate } from 'react-router';

import { ApiError } from '@/api/problem.ts';
import { useArchiveNote, useDeleteNoteForever, useRestoreNote, useSettings } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { CopyButton } from '@/components/CopyButton.tsx';
import { DownloadButton } from '@/components/DownloadButton.tsx';
import { Icon } from '@/components/Icon.tsx';
import { LanguageSelect } from '@/components/LanguageSelect.tsx';
import { OverflowMenu, type OverflowMenuItem } from '@/components/OverflowMenu.tsx';
import { TagEditor } from '@/components/TagEditor.tsx';
import { languageName } from '@/features/settings/languages.ts';

import { checklistToProse, proseToChecklist } from './checklist.ts';
import { cleanedDocument, cleanedMarkdown } from './cleaned.ts';
import type { NoteEditor } from './useNoteEditor.ts';

/**
 * The note's actions: Details · Share · Archive (or Restore · Delete forever),
 * behind the ⋮ in the header, and the two disclosures they open in a drawer
 * at the foot of the screen.
 *
 * They were a sticky bar at the foot with a fourth, primary "Record into
 * this". On a phone that bar wrapped to two rows (96 px) and sat 30 px above
 * the tab bar's mic, which recorded into a *new* note — two record controls
 * with different glyphs, recording to different places, from one screen. The
 * mic is now contextual (`RecordButton`), the bar is gone, and these three
 * actions taken once a month live where the row's actions already do: in an
 * overflow menu. About 130 px of the phone went back to the note's body.
 *
 * Details and Share are still disclosures — the language, tag and alias
 * editors and the copy and download controls open where they always did,
 * above the tab bar, and close from their own heading. Which one is open
 * belongs to the screen (`open` / `onOpenChange`): the meta line under the
 * title opens Details when its language fact is tapped.
 *
 * Getting rid of a note, and getting it back, keep their two confirmation
 * disciplines, because these are two different promises:
 *
 *   Archive       reversible for as long as the purge window lasts, so it asks
 *                 once, plainly.
 *   Delete for    irreversible, and it takes the recordings and the transcripts
 *   ever          with it, so it names what goes and requires the note's title
 *                 to be typed before the control unlocks.
 */
export type NotePanelKind = 'details' | 'share';

/** The id of a note's language select, so the meta line can send focus to it. */
export function noteLanguageFieldId(noteId: string): string {
  return `note-language-${noteId}`;
}

/** The header's ⋮: the note's actions, with the dialogs they need. */
export function NoteMenu({
  note,
  onOpenPanel,
}: {
  note: NoteDetailWire;
  onOpenPanel: (panel: NotePanelKind) => void;
}) {
  const navigate = useNavigate();
  const archive = useArchiveNote();
  const restore = useRestoreNote();
  const purge = useDeleteNoteForever();
  const [confirming, setConfirming] = useState<'archive' | 'purge' | null>(null);

  const busy = archive.isPending || restore.isPending || purge.isPending;
  const failure = archive.error ?? restore.error ?? purge.error;

  const items: OverflowMenuItem[] = [
    { label: 'Details', onSelect: () => onOpenPanel('details') },
    { label: 'Share', onSelect: () => onOpenPanel('share') },
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
            label: archive.isPending ? 'Archiving…' : 'Archive',
            disabled: busy,
            onSelect: () => {
              setConfirming('archive');
            },
          },
        ]),
  ];

  return (
    <>
      <OverflowMenu label="Note actions" items={items} />

      {failure && (
        <p className="note-actions__error note-menu__error" role="alert">
          {failure instanceof ApiError ? failure.userMessage : 'That did not go through.'}
        </p>
      )}

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
            // `replace: true` on both paths is deliberate: the note's own URL
            // is now either archived or gone, and leaving it in the history
            // means Back walks straight into a screen that 404s.
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
    </>
  );
}

/**
 * The open disclosure — Details or Share — in a drawer at the foot of the
 * scroll region, above the tab bar. Nothing is rendered while neither is
 * open, so the note's body has the whole screen.
 */
export function NoteDrawer({
  note,
  editor,
  hidden = false,
  open,
  onOpenChange,
}: {
  note: NoteDetailWire;
  editor: NoteEditor;
  /** Stepping aside for another bar at the foot of the screen; state is kept. */
  hidden?: boolean;
  open: NotePanelKind | null;
  onOpenChange: (open: NotePanelKind | null) => void;
}) {
  const headingId = useId();
  const { draft } = editor.model;
  const cleaned = note.cleaned?.body.trim() ? note.cleaned : null;

  if (!open) return null;

  return (
    <div className="note-drawer" hidden={hidden}>
      <section className="note-panel" aria-labelledby={headingId}>
        <div className="note-panel__head">
          <h2 id={headingId} className="note-panel__heading">
            {open === 'details' ? 'Details' : 'Share'}
          </h2>
          <button
            type="button"
            className="note-panel__close"
            aria-label={open === 'details' ? 'Close details' : 'Close share'}
            onClick={() => {
              onOpenChange(null);
            }}
          >
            <Icon name="close" size={18} />
          </button>
        </div>

        {open === 'details' ? (
          <>
            <NoteLanguage
              id={noteLanguageFieldId(note.id)}
              value={draft.language ?? ''}
              onChange={(language) => {
                editor.edit({ language });
                void editor.saveNow();
              }}
            />
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
            <ChecklistSwitch
              checked={(draft.kind ?? note.kind ?? 'note') === 'checklist'}
              onChange={(checklist) => {
                // The body is converted with the kind, in the one PATCH: a
                // checklist whose body is still prose would show every
                // paragraph as one open item and normalise it on the first
                // write, which is the conversion done by surprise.
                editor.edit({
                  kind: checklist ? 'checklist' : 'note',
                  body: checklist ? proseToChecklist(draft.body) : checklistToProse(draft.body),
                });
                void editor.saveNow();
              }}
            />
          </>
        ) : (
          /*
           * Title first, then the body: a body pasted somewhere else with no
           * title loses what it was about, and re-typing that is exactly the
           * friction this is meant to remove.
           */
          <div className="note-copy">
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
            {/*
              The worker's rewrite, when there is one, named for what it is: a
              control called "Copy" next to another called "Copy" would mean
              neither. Stale or not — the user can see which it is on its tab.
            */}
            {cleaned && (
              <>
                <CopyButton
                  label="Copy cleaned view"
                  text={() => cleanedDocument(draft.title, cleaned.body)}
                />
                <DownloadButton
                  label="Download cleaned view"
                  filename={() => `${filenameFor(draft.title)} (cleaned).md`}
                  blob={() =>
                    Promise.resolve(
                      new Blob([cleanedMarkdown(draft.title, cleaned.body)], {
                        type: 'text/markdown',
                      }),
                    )
                  }
                />
              </>
            )}
          </div>
        )}
      </section>
    </div>
  );
}

/**
 * The note's transcription language, first in the Details disclosure with the
 * other facts about the note that are not its text.
 *
 * The inherit entry names what it inherits — "Default (Malayalam)" — read from
 * the You screen's setting, so the choice is between real languages rather
 * than between a language and a word. The helper line says what the setting
 * reaches: only a recording made *into* this note. A recording that is routed
 * here afterwards was transcribed before anyone knew where it was going, so
 * only the default could apply to it.
 */
function NoteLanguage({
  id,
  value,
  onChange,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
}) {
  const { data: settings } = useSettings();
  const inherited = settings?.default_language ?? 'en';

  return (
    <section className="language-field">
      <label className="tag-editor__label" htmlFor={id}>
        Transcription language
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
 * Prose or checklist. It changes what the rest of the screen is (Items and
 * Split up for Text and Cleaned) and what a recording into the note becomes
 * (an item, not a paragraph). Last in Details: the language stays first,
 * where the owner's trial finally found it, and a note is converted once.
 *
 * The same drawn checkbox as the Cleaned tab's auto-refresh switch: the
 * native control stretched invisibly over the whole 44 px label, so the tap
 * lands on the control itself and the mark beside the words is what a finger
 * sees.
 */
function ChecklistSwitch({
  checked,
  onChange,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
}) {
  const id = useId();
  return (
    <section className="language-field">
      <h2 className="tag-editor__label">Checklist</h2>
      <label className="cleaned__auto" htmlFor={id}>
        <input
          id={id}
          type="checkbox"
          className="cleaned__auto-box"
          checked={checked}
          onChange={(event) => {
            onChange(event.target.checked);
          }}
        />
        <span className="cleaned__auto-mark" aria-hidden="true">
          <Icon name="check" size={16} />
        </span>
        <span>This note is a checklist</span>
      </label>
      <p className="language-field__hint">
        Each paragraph becomes an item to tick off, and a recording into this note adds one.
        Turning it off makes every item a paragraph again.
      </p>
    </section>
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
