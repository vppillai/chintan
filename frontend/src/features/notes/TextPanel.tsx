import { useEffect, useLayoutEffect, useMemo, useRef, type ReactNode, type RefObject } from 'react';

import { useAutoGrow } from '@/hooks/useAutoGrow.ts';
import { useReducedMotion } from '@/hooks/useReducedMotion.ts';

import { ChecklistEditor } from './ChecklistEditor.tsx';
import { findMatches } from './find.ts';
import { markMatches, useReportTotal, useScrollToActiveMatch, type FindTarget } from './FindBar.tsx';
import { additionTo } from './autosave.ts';
import { parseChecklist } from './checklist.ts';
import type { NoteEditor } from './useNoteEditor.ts';

/** A recording's addition to point at: the body from before it, and which Show this is. */
export interface Flash {
  before: string;
  n: number;
}

/** How long Show's mark stays on the addition. */
const FLASH_MS = 2000;

/**
 * The editable body: as tall as its text, and the page is what scrolls.
 *
 * Four things can stand in the body's box, and this panel is the switch
 * between them. While the find bar has a query the textarea gives way to a
 * read-only mirror of the same text (`FindMirror`), because a `<mark>`
 * cannot be drawn inside a textarea. A recording's addition is pointed at the
 * same way (`FlashMirror`, the banner's Show). A checklist's body is items,
 * not a text: `ChecklistEditor` stands in for the textarea, and the find
 * mirror still serves the bar — the raw lines, marked — so a word in a long
 * list can still be found. Each mirror owns the focus it takes and hands
 * back; the textarea is measured here, in the panel, so re-opening the Text
 * tab measures again: a hook in the screen would keep a ref to a textarea
 * that had left the document and never see the one that replaced it.
 */
export function TextPanel({
  noteId,
  editor,
  checklist,
  lang,
  find,
  onDismissFind,
  flash = null,
  onFlashDone = () => {},
}: {
  /** For the Items tab's Done disclosure, remembered per note. */
  noteId: string;
  editor: NoteEditor;
  checklist: boolean;
  lang: string | undefined;
  find: FindTarget | null;
  /** A tap on the mirror: close the bar and go back to editing. */
  onDismissFind: () => void;
  /** A recording's addition to point at, from the filing banner's Show. */
  flash?: Flash | null;
  onFlashDone?: () => void;
}) {
  const body = editor.model.draft.body;
  const bodyRef = useRef<HTMLTextAreaElement>(null);
  useAutoGrow(bodyRef, body);

  // Find outranks Show: its marks are the ones asked for.
  const flashing = flash !== null && find === null;
  const flashItems = useMemo(
    () => (flashing && checklist ? addedItems(flash.before, body) : null),
    [flashing, checklist, flash, body],
  );

  return (
    <FlashMirror
      flash={flashing ? flash : null}
      body={body}
      checklist={checklist}
      lang={lang}
      bodyRef={bodyRef}
      onDone={onFlashDone}
    >
      {find ? (
        <FindMirror body={body} lang={lang} find={find} bodyRef={bodyRef} onDismiss={onDismissFind} />
      ) : checklist ? (
        <ChecklistEditor
          noteId={noteId}
          body={body}
          currentBody={() => editor.current().body}
          flash={flashItems}
          onChange={(next) => {
            editor.edit({ body: next });
          }}
          onSave={() => void editor.saveNow()}
        />
      ) : (
        <>
          <label className="visually-hidden" htmlFor="note-body">
            Note body
          </label>
          <textarea
            id="note-body"
            ref={bodyRef}
            className="note-body-input prose"
            lang={lang}
            value={body}
            rows={6}
            onChange={(event) => {
              editor.edit({ body: event.target.value });
            }}
            onBlur={() => void editor.saveNow()}
          />
        </>
      )}
    </FlashMirror>
  );
}

/**
 * The body, read-only, with the find bar's matches marked in the same box.
 * Leaving — the bar closed, the query cleared, or a tap here — brings the
 * textarea back with the caret on the match that was current, so finding a
 * word and editing it is one gesture, not a find followed by a hunt.
 */
function FindMirror({
  body,
  lang,
  find,
  bodyRef,
  onDismiss,
}: {
  body: string;
  lang: string | undefined;
  find: FindTarget;
  /** The textarea that takes the mirror's place, for the caret. */
  bodyRef: RefObject<HTMLTextAreaElement | null>;
  onDismiss: () => void;
}) {
  const ref = useRef<HTMLElement>(null);
  const matches = useMemo(() => findMatches(body, find.query), [body, find.query]);
  useReportTotal(find, matches.length);
  useScrollToActiveMatch(ref, find.active, matches.length);

  /*
   * Where the caret goes when the textarea returns: the match that was
   * current while the mirror stood. Remembered in a ref because by the time
   * the textarea is back, the query and its matches are gone.
   */
  const lastActive = useRef<{ start: number; end: number } | null>(null);
  const current = matches[find.active];
  useEffect(() => {
    if (current) lastActive.current = current;
  }, [current]);
  useEffect(
    () => () => {
      // Run as the mirror leaves, once the textarea that replaces it holds
      // the ref; nothing to do when the whole panel is leaving too. The mirror
      // and the textarea are the same box, so the match is where the mark was
      // and the page need not move; `preventScroll` keeps the browser from
      // re-centring on the whole field.
      const textarea = bodyRef.current;
      if (!textarea) return;
      textarea.focus({ preventScroll: true });
      const range = lastActive.current;
      if (range) {
        try {
          textarea.setSelectionRange(range.start, range.end);
        } catch {
          /* A browser that will not place the caret still has the focus. */
        }
      }
    },
    [bodyRef],
  );

  return (
    <section
      ref={ref}
      className="note-body-mirror prose"
      lang={lang}
      aria-label="Note body, read-only while finding"
      // A pointer's way back to editing. The keyboard's is Escape in the
      // bar, which does the same thing; the mirror itself is text, not a
      // control, so a screen reader can read it and its marks.
      onClick={onDismiss}
    >
      {markMatches(body, matches, find.active)}
    </section>
  );
}

/**
 * Show on "Added at the end": the recording's addition, marked for
 * `FLASH_MS` and scrolled to (R7-6b). Prose is drawn in the find mirror's
 * box with the addition marked, since a textarea cannot colour a range; a
 * checklist marks the rows the recording added in place, so this renders its
 * children as they are. Always mounted, around whatever the panel shows, so
 * a flash that ends does not remount the field under the caret.
 *
 * Focus follows Show, so a keyboard or a screen reader lands on the addition
 * too, and nothing drops to <body> when the banner's button goes: onto the
 * mirror while it stands, then into the textarea with the caret at the
 * paragraph's start; on a checklist, into the first added row's field at
 * once, since the rows stay.
 */
function FlashMirror({
  flash,
  body,
  checklist,
  lang,
  bodyRef,
  onDone,
  children,
}: {
  /** The flash under way, or null when there is none. */
  flash: Flash | null;
  body: string;
  checklist: boolean;
  lang: string | undefined;
  bodyRef: RefObject<HTMLTextAreaElement | null>;
  onDone: () => void;
  children: ReactNode;
}) {
  const reduced = useReducedMotion();
  const flashing = flash !== null;
  const range = useMemo(
    () => (flashing && !checklist ? addedRange(flash.before, body) : null),
    [flashing, checklist, flash, body],
  );
  const mirrorRef = useRef<HTMLElement>(null);
  const caretAfter = useRef<number | null>(null);
  useLayoutEffect(() => {
    if (!flashing) return;
    const target = document.querySelector<HTMLElement>('[data-flash]');
    if (target && typeof target.scrollIntoView === 'function') {
      target.scrollIntoView({ block: 'center', behavior: reduced ? 'auto' : 'smooth' });
    }
    // With nothing to mark (every added item was already on the list, or a
    // body with no text) focus still lands in the text, never on <body>.
    if (checklist) {
      const field =
        target?.querySelector<HTMLTextAreaElement>('textarea') ??
        document.querySelector<HTMLTextAreaElement>('.checklist textarea');
      field?.focus({ preventScroll: true });
    } else if (range) {
      caretAfter.current = range.start;
      mirrorRef.current?.focus({ preventScroll: true });
    } else {
      bodyRef.current?.focus({ preventScroll: true });
    }
    const timer = setTimeout(onDone, FLASH_MS);
    return () => {
      clearTimeout(timer);
    };
    // A new Show (`n`) is a new flash; the body settling under it is not.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [flashing, flash?.n]);
  useEffect(() => {
    if (flashing) return;
    const at = caretAfter.current;
    caretAfter.current = null;
    const textarea = bodyRef.current;
    if (at === null || !textarea) return;
    textarea.focus({ preventScroll: true });
    try {
      textarea.setSelectionRange(at, at);
    } catch {
      /* A browser that will not place the caret still has the focus. */
    }
  }, [flashing, bodyRef]);

  if (!range) return children;
  return (
    <section
      ref={mirrorRef}
      tabIndex={-1}
      className="note-body-mirror prose"
      lang={lang}
      aria-label="Note body"
      // Any tap goes back to editing at once.
      onClick={onDone}
    >
      {body.slice(0, range.start)}
      <mark className="note-flash" data-flash="">
        {body.slice(range.start, range.end)}
      </mark>
      {body.slice(range.end)}
    </section>
  );
}

/**
 * Where in `body` a recording's addition is: what it added to `before` at
 * the end, found where it now stands; or, when the body changed some other
 * way meanwhile, its last paragraph, which is where the worker appends.
 */
export function addedRange(before: string, body: string): { start: number; end: number } | null {
  const addition = additionTo(before, body);
  const start = addition === null ? -1 : body.lastIndexOf(addition);
  if (addition !== null && start >= 0) return { start, end: start + addition.length };
  const end = body.trimEnd().length;
  if (end === 0) return null;
  const gap = body.lastIndexOf('\n\n', end - 1);
  return { start: gap < 0 ? 0 : gap + 2, end };
}

/** The open items of `body` whose words `before` did not have: what a recording merged in. */
export function addedItems(before: string, body: string): ReadonlySet<string> {
  const had = new Set(parseChecklist(before).map((item) => item.text.trim()));
  return new Set(
    parseChecklist(body)
      .filter((item) => !item.done && !had.has(item.text.trim()))
      .map((item) => item.text.trim()),
  );
}
