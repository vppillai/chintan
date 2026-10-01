import { useEffect } from 'react';

/**
 * How much of the layout viewport the on-screen keyboard covers, as a CSS
 * custom property on `<html>`.
 *
 * Chrome on Android (since 108, `interactive-widget=resizes-visual` is the
 * default) and iOS both keep the layout viewport at its full height while
 * the keyboard is up; only the visual viewport shrinks. The shell is `100dvh`,
 * so `.app__main`'s bottom edge sits under the keyboard, and the browser's own
 * caret reveal scrolls only as far as that edge — the caret line lands flush
 * against the keyboard's top or under it (R6-NAV-2, K3). `shell.css` gives
 * the scroll container this much `padding-block-end` and
 * `scroll-padding-block-end`; the note's sheet (`notes.css` `.note-panel`)
 * caps its height by it and needs no lift of its own, since a sticky foot
 * rests above the region's padding.
 *
 * The inset is the window's. Where the tab bar is rendered `.app__main` ends
 * a bar's height above the window's bottom, so the padding overshoots by
 * that much; the bar is not on every screen, so it is not subtracted. The
 * `scroll` listener rewrites the property while iOS pans the visual viewport
 * under the keyboard, which resizes the padding mid-pan — the iPhone check
 * watches the scroll position while panning.
 *
 * `innerHeight − visualViewport.height − visualViewport.offsetTop` is the part
 * of the layout viewport below the visual viewport's bottom: the keyboard,
 * once the browser has panned the page up to it. Zero when the visual
 * viewport is merely zoomed, where the same arithmetic would read the zoom
 * as a keyboard.
 */

export const KEYBOARD_INSET_PROPERTY = '--keyboard-inset';

/**
 * Set on `<html>` while the keyboard is up, for rules that only apply then
 * (the banner's mic, `shell.css` `.banner-record`; R8, F3). Over 80 px, so an
 * accessory bar or a rounding at a zoom step does not count as a keyboard.
 */
export const KEYBOARD_ATTRIBUTE = 'data-keyboard';
const KEYBOARD_MIN_PX = 80;

/**
 * Set on `<html>` while one of the note's own fields — the title, the body,
 * a checklist row — has focus, so with `data-keyboard` the two together mean
 * "typing into the note": the keyboard is up for this field. Read from focus
 * events here, once, rather than by a `:has(…:focus)` chain in the sheet
 * (review 2026-10-01, FE-13). The scope is the note's fields on purpose:
 * Find's box, a tag field or the drawer's are typed into too, but the mic
 * records *into the note being written*, and was never offered from them.
 */
export const EDITING_ATTRIBUTE = 'data-editing';
const NOTE_FIELDS = '.note-title-input, .note-body-input, .checklist-editor';

/** Whether focus on `target` is typing into the note (see `EDITING_ATTRIBUTE`). */
function isEditingNote(target: EventTarget | null): boolean {
  return (
    (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement) &&
    target.closest(NOTE_FIELDS) !== null
  );
}

/** The focusable `<input>` types that take no typed text, so no keyboard rises for them. */
const UNTYPED_INPUTS = new Set(['button', 'checkbox', 'color', 'file', 'image', 'radio', 'range', 'reset', 'submit']);

/**
 * Whether `target` is a field text is typed into: a textarea, an `<input>`
 * of a typed kind, or contenteditable. A checkbox, a radio or a button is
 * not, so a key pressed on one belongs to the app (`keyTaken`).
 */
export function isEditing(target: EventTarget | null): boolean {
  if (target instanceof HTMLTextAreaElement) return true;
  if (target instanceof HTMLInputElement) return !UNTYPED_INPUTS.has(target.type);
  // Strictly a boolean: `toggleAttribute` with an undefined `force` toggles.
  return target instanceof HTMLElement && target.isContentEditable === true;
}

export function useKeyboardInset(): void {
  useEffect(() => {
    const root = document.documentElement;
    // `focusout` fires before the next field has focus, so the field it is
    // going to is read from the event rather than from `activeElement`.
    const onFocusIn = (event: FocusEvent): void => {
      root.toggleAttribute(EDITING_ATTRIBUTE, isEditingNote(event.target));
    };
    const onFocusOut = (event: FocusEvent): void => {
      root.toggleAttribute(EDITING_ATTRIBUTE, isEditingNote(event.relatedTarget));
    };
    document.addEventListener('focusin', onFocusIn);
    document.addEventListener('focusout', onFocusOut);
    return () => {
      document.removeEventListener('focusin', onFocusIn);
      document.removeEventListener('focusout', onFocusOut);
      root.removeAttribute(EDITING_ATTRIBUTE);
    };
  }, []);

  useEffect(() => {
    const viewport = window.visualViewport;
    if (!viewport) return;
    const root = document.documentElement;
    let last = -1;
    const update = (): void => {
      // ponytail: zoom + keyboard together reads 0, revisit if seen. iOS's
      // own focus zoom on any control under 16 px is the zoom that hits this
      // guard, and it outlives the field — so every text input takes
      // `--font-size-md` (DB6-5), and the next sub-16 px input is what to
      // catch in review; `maximum-scale=1` is not the answer, it kills pinch.
      const zoomed = Math.abs(viewport.scale - 1) > 0.01;
      const inset = zoomed
        ? 0
        : Math.max(0, Math.round(window.innerHeight - viewport.height - viewport.offsetTop));
      if (inset === last) return;
      last = inset;
      root.style.setProperty(KEYBOARD_INSET_PROPERTY, `${String(inset)}px`);
      root.toggleAttribute(KEYBOARD_ATTRIBUTE, inset > KEYBOARD_MIN_PX);
    };
    update();
    viewport.addEventListener('resize', update);
    viewport.addEventListener('scroll', update);
    window.addEventListener('resize', update);
    return () => {
      viewport.removeEventListener('resize', update);
      viewport.removeEventListener('scroll', update);
      window.removeEventListener('resize', update);
      root.style.removeProperty(KEYBOARD_INSET_PROPERTY);
      root.removeAttribute(KEYBOARD_ATTRIBUTE);
    };
  }, []);
}
