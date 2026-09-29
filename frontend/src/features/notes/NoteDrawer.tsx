import { useId } from 'react';

import { useSettings } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { CheckMark } from '@/components/CheckMark.tsx';
import { CopyButton } from '@/components/CopyButton.tsx';
import { DownloadButton } from '@/components/DownloadButton.tsx';
import { Icon } from '@/components/Icon.tsx';
import { LanguageSelect } from '@/components/LanguageSelect.tsx';
import { TagEditor } from '@/components/TagEditor.tsx';
import { languageName } from '@/features/settings/languages.ts';

import { checklistToProse, proseToChecklist } from './checklist.ts';
import { cleanedDocument, cleanedMarkdown } from './cleaned.ts';
import type { NoteEditor } from './useNoteEditor.ts';

/**
 * The two disclosures the header's ⋮ opens (`NoteMenu`, `NoteActions.tsx`):
 * Details — the language, tag and alias editors, the Word for word and
 * Checklist switches and the note's id — and Share — the copy and download
 * controls. They open where they always did, in a drawer at the foot of the
 * scroll region above the tab bar, and close from their own heading. Which
 * one is open belongs to the screen (`open` / `onOpenChange`): the meta line
 * under the title opens Details when its language fact is tapped. So does
 * the focus: the drawer opens at the far end of the screen from the menu,
 * and the menuitem that opened it is gone, so the screen sends focus into it
 * (`noteLanguageFieldId`, `notePanelHeadingId`) and back to the menu's
 * trigger when it closes.
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
  const headingId = notePanelHeadingId(note.id);
  const { draft } = editor.model;
  const cleaned = note.cleaned?.body.trim() ? note.cleaned : null;

  if (!open) return null;

  return (
    <div className="note-drawer" hidden={hidden}>
      <section className="note-panel" aria-labelledby={headingId}>
        <div className="note-panel__head">
          {/* Focusable by script only: where Share lands a keyboard user, its controls a Tab away. */}
          <h2 id={headingId} className="note-panel__heading" tabIndex={-1}>
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
            <VerbatimSwitch
              checked={draft.verbatim ?? note.verbatim ?? false}
              onChange={(verbatim) => {
                editor.edit({ verbatim });
                void editor.saveNow();
              }}
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
            {/*
              Last, because a note's id is a fact rarely needed: it is what a
              device's `X-Chintan-Note-Id` header carries to file into this
              note, and until now the only way to it was the address bar
              (owner, 2026-09-29). `CopyButton` handles the clipboard fallback
              and says Copied or failed.
            */}
            <section className="language-field">
              <h2 className="tag-editor__label">Note id</h2>
              <code className="note-id">{note.id}</code>
              <CopyButton
                label="Copy note id"
                text={() => note.id}
                className="settings-status__action"
              />
              <p className="language-field__hint">
                For <code>X-Chintan-Note-Id</code>: a device that sends this header files
                everything into this note (You → Devices &amp; shortcuts).
              </p>
            </section>
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
 * reaches: every recording that lands in this note. One the router files
 * here was transcribed in the default before anyone knew where it was going,
 * and is transcribed again in this language when the two differ (review
 * 2026-09-21, T2, backend).
 *
 * The second line is the owner's lived problem (T3): under Auto-detect
 * Whisper picks one language per recording, and on real Malayalam it chose
 * Tamil script and dropped the Malayalam sentence from a mixed clip.
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
        For every recording that lands in this note — made into it, or filed here automatically.
      </p>
      <p className="language-field__hint">
        Mix Malayalam and English in one recording? Choose Malayalam. Auto-detect picks one
        language per recording and drops or re-scripts the other.
      </p>
    </section>
  );
}

/**
 * Skip the cleanup for this note. The wire has carried `verbatim` since the
 * start, README and About promise it, and no control ever set it (review
 * 2026-09-21, T11); the pipeline half — appending the raw transcript and
 * calling no model — lands with the backend stream. Between the language
 * and the checklist switch: it is about what a recording becomes, like both.
 */
function VerbatimSwitch({
  checked,
  onChange,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
}) {
  const id = useId();
  return (
    <section className="language-field">
      <h2 className="tag-editor__label">Word for word</h2>
      <label className="cleaned__auto" htmlFor={id}>
        <input
          id={id}
          type="checkbox"
          className="checklist__box"
          checked={checked}
          onChange={(event) => {
            onChange(event.target.checked);
          }}
        />
        <CheckMark />
        <span>Keep recordings as spoken</span>
      </label>
      <p className="language-field__hint">
        Skips the cleanup: a recording&rsquo;s transcript goes into this note exactly as it was
        transcribed, fillers and all.
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
          className="checklist__box"
          checked={checked}
          onChange={(event) => {
            onChange(event.target.checked);
          }}
        />
        <CheckMark />
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
function filenameFor(title: string): string {
  const cleaned = title.trim().replace(/[/\\:*?"<>|]/g, '').trim();
  return (cleaned || 'note').slice(0, 120);
}
