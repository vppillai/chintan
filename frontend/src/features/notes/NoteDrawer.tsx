import { useQueryClient } from '@tanstack/react-query';
import {
  useCallback,
  useId,
  useRef,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
} from 'react';

import { queryKeys, useSettings } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { CheckMark } from '@/components/CheckMark.tsx';
import { CopyButton } from '@/components/CopyButton.tsx';
import { DownloadButton } from '@/components/DownloadButton.tsx';
import { Icon } from '@/components/Icon.tsx';
import { LanguageSelect } from '@/components/LanguageSelect.tsx';
import { canShare, ShareButton } from '@/components/ShareButton.tsx';
import { TagEditor } from '@/components/TagEditor.tsx';
import { useModalFocus } from '@/components/useModalFocus.ts';
import { languageName } from '@/features/settings/languages.ts';
import {
  GESTURE_SLOP_PX,
  SWIPE_COMMIT_FRACTION,
  SWIPE_FLICK_MAX_AGE_MS,
  SWIPE_FLICK_PX_PER_MS,
} from '@/hooks/gesture.ts';
import { useSwallowNextClick } from '@/hooks/swallowNextClick.ts';

import { checklistToProse, parseChecklist, proseToChecklist } from './checklist.ts';
import { parseMotionMs } from './ChecklistEditor.tsx';
import { checklistClipboard } from './checklistClipboard.ts';
import { cleanedDocument, cleanedMarkdown } from './cleaned.ts';
import { noteLanguageFieldId, notePanelHeadingId, type NotePanelKind } from './notePanel.ts';
import type { NoteEditor } from './useNoteEditor.ts';
import { useApplyTidy, useStartTidy } from './useTidyList.ts';

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
  const noteIdCaptionId = useId();
  const { draft } = editor.model;
  // A checklist has no Cleaned tab (R8-F8); the tasks view it may still
  // carry is Tidy's transport, not something to share.
  const checklist = (draft.kind ?? note.kind ?? 'note') === 'checklist';
  const cleaned = !checklist && note.cleaned?.body.trim() ? note.cleaned : null;
  const queryClient = useQueryClient();
  const startTidy = useStartTidy();
  /*
   * Here because the drawer is mounted for as long as the note screen is,
   * open or not, and already holds the editor: a tidy asked for from the ⋮
   * or by the switch below lands whichever tab is showing.
   */
  useApplyTidy(note, editor);

  const panelRef = useRef<HTMLElement>(null);
  const close = useCallback(() => {
    onOpenChange(null);
  }, [onOpenChange]);

  /*
   * Dragging the head down closes the sheet, as a phone's own sheets do; the
   * × and Escape stay. A finger or pen only — a mouse has the ×. The press
   * becomes a drag at the shared slop once it is more down than across, and
   * from there the sheet follows the finger (`translate` on the sheet, a
   * style write and nothing in React); letting go at `SWIPE_COMMIT_FRACTION`
   * of the sheet's height, or in a fresh downward flick at
   * `SWIPE_FLICK_PX_PER_MS` — the tab swipe's two numbers — slides it the
   * rest of the way and closes it when the slide ends (`transitionend`, with
   * a timer behind it for a browser that fires none); short of that it
   * settles back. Reduced motion makes both settles one frame through the
   * motion tokens, and the close runs the same `close` as the ×, so focus
   * goes back to the ⋮ either way — twice over: `changePanel` focuses the
   * menu's trigger, and `useModalFocus`'s cleanup restores what had focus
   * when the sheet opened, which `OverflowMenu` had already made that same
   * trigger before the pick ran. The click a lift fires is swallowed once,
   * so a drag that ends over the × is not also a tap on it.
   */
  const drag = useRef<{
    id: number;
    x0: number;
    y0: number;
    height: number;
    dy: number;
    lastY: number;
    lastT: number;
    vy: number;
    active: boolean;
  } | null>(null);
  const swallow = useSwallowNextClick(500);
  const settleBack = useCallback(() => {
    const panel = panelRef.current;
    if (!panel) return;
    panel.style.removeProperty('translate');
    delete panel.dataset['dragging'];
    drag.current = null;
  }, []);
  const dismiss = useCallback(
    (height: number) => {
      const panel = panelRef.current;
      if (!panel) return;
      drag.current = null;
      delete panel.dataset['dragging'];
      panel.style.translate = `0 ${height}px`;
      let done = false;
      const finish = (): void => {
        if (done) return;
        done = true;
        panel.removeEventListener('transitionend', onEnd);
        panel.style.removeProperty('translate');
        close();
      };
      // The sheet's own slide ending, not a child's hover colour settling.
      const onEnd = (event: TransitionEvent): void => {
        if (event.target === panel && event.propertyName === 'translate') finish();
      };
      panel.addEventListener('transitionend', onEnd);
      // Behind it, for a browser that fires no event: the slide's own
      // duration as computed on the sheet, plus a frame.
      setTimeout(finish, parseMotionMs(getComputedStyle(panel).transitionDuration) + 16);
    },
    [close],
  );
  const onHeadPointerDown = (event: ReactPointerEvent<HTMLDivElement>): void => {
    if (event.pointerType === 'mouse' || event.button !== 0) return;
    // A sheet with no height yet has no distance to drag against.
    const height = panelRef.current?.getBoundingClientRect().height ?? 0;
    if (height <= 0) return;
    swallow.reset();
    drag.current = {
      id: event.pointerId,
      x0: event.clientX,
      y0: event.clientY,
      height,
      dy: 0,
      lastY: event.clientY,
      lastT: event.timeStamp,
      vy: 0,
      active: false,
    };
  };
  const onHeadPointerMove = (event: ReactPointerEvent<HTMLDivElement>): void => {
    const g = drag.current;
    const panel = panelRef.current;
    if (!g || !panel || event.pointerId !== g.id) return;
    const dy = event.clientY - g.y0;
    if (!g.active) {
      const dx = Math.abs(event.clientX - g.x0);
      if (Math.abs(dy) < GESTURE_SLOP_PX && dx < GESTURE_SLOP_PX) return;
      if (dy < dx || dy < GESTURE_SLOP_PX) {
        // Up, or more across than down: not a close.
        drag.current = null;
        return;
      }
      g.active = true;
      event.currentTarget.setPointerCapture?.(event.pointerId);
      panel.dataset['dragging'] = '';
    }
    const dt = event.timeStamp - g.lastT;
    if (dt > 0) g.vy = (event.clientY - g.lastY) / dt;
    g.lastY = event.clientY;
    g.lastT = event.timeStamp;
    g.dy = Math.max(0, dy);
    panel.style.translate = `0 ${g.dy}px`;
  };
  const onHeadPointerUp = (event: ReactPointerEvent<HTMLDivElement>): void => {
    const g = drag.current;
    if (!g || event.pointerId !== g.id) return;
    if (!g.active) {
      drag.current = null;
      return;
    }
    swallow.arm();
    const fresh = event.timeStamp - g.lastT <= SWIPE_FLICK_MAX_AGE_MS;
    const far = g.dy >= g.height * SWIPE_COMMIT_FRACTION;
    const flick = fresh && g.vy >= SWIPE_FLICK_PX_PER_MS;
    if (far || flick) dismiss(g.height);
    else settleBack();
  };
  const onHeadClickCapture = (event: ReactMouseEvent<HTMLDivElement>): void => {
    if (swallow.take(event.nativeEvent)) {
      event.preventDefault();
      event.stopPropagation();
    }
  };

  if (!open) return null;

  return (
    <div className="note-drawer" hidden={hidden}>
      {/*
        A dialog while it is on screen: Tab stays inside, Escape closes it and
        focus goes back to the ⋮ that opened it, as the Move sheet and the
        confirm dialogs do (review 2026-10-01, FE-3). Not while hidden behind
        the recordings' selection bar — Escape must cancel the selection
        then, and R must still record — so the role and the hook step aside
        with it; the content stays mounted either way.
      */}
      <section
        ref={panelRef}
        className="note-panel"
        aria-labelledby={headingId}
        {...(hidden ? {} : { role: 'dialog', 'aria-modal': true })}
      >
        {!hidden && <ModalFocus panelRef={panelRef} onCancel={close} />}
        {/* The drag is a shortcut for the × beside it, never the only way to close. */}
        <div
          className="note-panel__head"
          onPointerDown={onHeadPointerDown}
          onPointerMove={onHeadPointerMove}
          onPointerUp={onHeadPointerUp}
          onPointerCancel={settleBack}
          onClickCapture={onHeadClickCapture}
        >
          {/* Focusable by script only: where Share lands a keyboard user, its controls a Tab away. */}
          <h2 id={headingId} className="note-panel__heading" tabIndex={-1}>
            {open === 'details' ? 'Details' : 'Share'}
          </h2>
          <button
            type="button"
            className="note-panel__close"
            aria-label={open === 'details' ? 'Close details' : 'Close share'}
            onClick={close}
          >
            <Icon name="close" size={18} />
          </button>
        </div>

        {/*
          Only the body scrolls, so the head is a row of the sheet that never
          moves and Close is always in reach (QA 2026-09-21, finding 7). The
          listener marks the sheet scrolled for the head's hairline where the
          browser has no scroll-driven animation (`notes.css`).
        */}
        <div
          className="note-panel__body"
          onScroll={(event) => {
            const body = event.currentTarget;
            body.parentElement?.toggleAttribute('data-scrolled', body.scrollTop > 0);
          }}
        >
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
                  const body = checklist ? proseToChecklist(draft.body) : checklistToProse(draft.body);
                  editor.edit({ kind: checklist ? 'checklist' : 'note', body });
                  const saved = editor.saveNow();
                  /*
                   * Then a tidy, since a dictated paragraph is one long item
                   * until it is split (R8-F8). Only once the server has the
                   * checklist — a save that failed would have the worker clean
                   * the prose — and only when some item is more than a word,
                   * since a list of single words has nothing to split.
                   */
                  if (checklist && parseChecklist(body).some((item) => /\S\s+\S/.test(item.text))) {
                    void saved.then(() => {
                      const stored = queryClient.getQueryData<NoteDetailWire>(queryKeys.note(note.id));
                      if (stored?.kind === 'checklist') startTidy(note.id, true);
                    });
                  }
                }}
              />
              {/*
                Last, because a note's id is a fact rarely needed: it is what a
                device's `X-Chintan-Note-Id` header carries to file into this
                note, and until now the only way to it was the address bar
                (owner, 2026-09-29). `CopyButton` handles the clipboard fallback
                and says Copied or failed.
              */}
              <section className="language-field" aria-labelledby={noteIdCaptionId}>
                <p id={noteIdCaptionId} className="tag-editor__label">
                  Note id
                </p>
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
            <ShareBody
              checklist={checklist}
              title={draft.title}
              body={draft.body}
              cleaned={cleaned?.body ?? null}
            />
          )}
        </div>
      </section>
    </div>
  );
}

/**
 * The Share disclosure. One primary action: the system share sheet where
 * the browser has one — a phone's, with its messaging apps — and Copy note
 * where it has not, so there is one obvious thing to tap and the rest of
 * the row is the quieter ways out. The text is the title, a blank line,
 * then the body: a body pasted somewhere else with no title loses what it
 * was about. A checklist copies as `☐` / `☑` lines, two spaces per level,
 * with an HTML list beside them for a target that takes rich text
 * (`checklistClipboard`), never the stored `- [ ]` markup; its hint says
 * so. The worker's rewrite, when there is one, is its own labelled group
 * under the note's: a control called Copy next to another called Copy
 * would mean neither.
 */
function ShareBody({
  checklist,
  title,
  body,
  cleaned,
}: {
  checklist: boolean;
  title: string;
  body: string;
  cleaned: string | null;
}) {
  const cleanedCaptionId = useId();
  const share = canShare();
  // Both forms at once for a checklist (`checklistClipboard`); prose is text alone.
  const payload = () => checklistClipboard(title, body);
  const text = () =>
    checklist ? payload().text : [title.trim(), body.trim()].filter(Boolean).join('\n\n');
  const html = checklist ? { html: () => payload().html } : {};
  const subject = () => title.trim() || 'Note';
  // The stored body as it is: a checklist's `- [ ]` lines are Markdown already.
  const markdown = () => `# ${title.trim()}\n\n${body.trim()}\n`;
  return (
    <div className="note-share">
      {share && <ShareButton title={subject} text={text} {...html} className="note-share__primary" />}
      <div className="note-share__row">
        <CopyButton
          label="Copy note"
          text={text}
          {...html}
          className={share ? 'screen__action' : 'note-share__primary'}
        />
        <DownloadButton
          label="Download note"
          filename={() => `${filenameFor(title)}.md`}
          blob={() => Promise.resolve(new Blob([markdown()], { type: 'text/markdown' }))}
        />
      </div>
      <p className="language-field__hint">
        {checklist
          ? 'Items copy as ☐ and ☑ lines, and paste as a list where rich text is accepted. The download is Markdown.'
          : 'The title, then the text. The download is Markdown.'}
      </p>
      {cleaned && (
        <section className="note-share__group" aria-labelledby={cleanedCaptionId}>
          <p id={cleanedCaptionId} className="tag-editor__label">
            Cleaned view
          </p>
          <div className="note-share__row">
            <CopyButton label="Copy cleaned view" text={() => cleanedDocument(title, cleaned)} />
            <DownloadButton
              label="Download cleaned view"
              filename={() => `${filenameFor(title)} (cleaned).md`}
              blob={() =>
                Promise.resolve(new Blob([cleanedMarkdown(title, cleaned)], { type: 'text/markdown' }))
              }
            />
          </div>
        </section>
      )}
    </div>
  );
}

/**
 * `useModalFocus` as a child that renders nothing, so the hook mounts with
 * the sheet on screen and unmounts when it is hidden or closed — the hook
 * asks for exactly that — while the sheet's own content stays mounted. The
 * screen then moves focus on to the language select or the heading; on
 * close the hook returns it to what had focus when the sheet opened, which
 * the ⋮ menu has already made its trigger.
 */
function ModalFocus({
  panelRef,
  onCancel,
}: {
  panelRef: RefObject<HTMLElement | null>;
  onCancel: () => void;
}) {
  useModalFocus(panelRef, onCancel);
  return null;
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
  const captionId = useId();
  return (
    <section className="language-field" aria-labelledby={captionId}>
      <p id={captionId} className="tag-editor__label">
        Word for word
      </p>
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
 * Prose or checklist. It changes what the rest of the screen is (Items for
 * Text, and no Cleaned tab) and what a recording into the note becomes (an
 * item, not a paragraph); turned on, it tidies the new list too. Last in Details: the language stays first,
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
  const captionId = useId();
  return (
    <section className="language-field" aria-labelledby={captionId}>
      <p id={captionId} className="tag-editor__label">
        Checklist
      </p>
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
