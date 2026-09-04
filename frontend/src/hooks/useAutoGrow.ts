import { useLayoutEffect, type RefObject } from 'react';

/**
 * Whether the browser sizes a textarea to its content on its own.
 *
 * `field-sizing: content` is the CSS answer; where it is understood the
 * stylesheet does the whole job and this hook does nothing. Exported so a
 * test can pin the fallback.
 */
export function supportsFieldSizing(): boolean {
  return (
    typeof CSS !== 'undefined' &&
    typeof CSS.supports === 'function' &&
    CSS.supports('field-sizing', 'content')
  );
}

/**
 * Grows a textarea with its text instead of scrolling inside itself.
 *
 * The note body was a fixed twelve rows with its own scrollbar: a long note
 * scrolled inside a box nested in a scrolling page — two scroll regions under
 * one thumb — and a two-line note reserved three hundred pixels of nothing,
 * pushing Recordings below the fold on a phone (QA D18). The document is the
 * thing that scrolls; the field is as tall as its text.
 *
 * Measured on every change of `value`, and on resize, because a narrower
 * viewport wraps the same text onto more lines. The minimum height comes from
 * the stylesheet; setting the height to the scroll height respects it.
 */
export function useAutoGrow(ref: RefObject<HTMLTextAreaElement | null>, value: string): void {
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element || supportsFieldSizing()) return;

    const fit = (): void => {
      // Collapse first, or a shrinking text keeps the previous height.
      element.style.blockSize = 'auto';
      element.style.blockSize = `${String(element.scrollHeight)}px`;
    };
    fit();

    window.addEventListener('resize', fit);
    return () => {
      window.removeEventListener('resize', fit);
    };
  }, [ref, value]);
}
