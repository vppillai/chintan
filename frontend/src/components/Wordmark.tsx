import { config } from '@/config/env.ts';

/**
 * The mark: the Bindu C (R5-BR-L1). The wordmark's serif C as an open ring —
 * a mouth, an ear, a record ring — with the bindu, the point a thought starts
 * from, at its mouth. Two elements, so it survives 16 px in both themes.
 *
 * Drawn on the icon set's 24-unit grid at its 1.75-unit stroke, but the stroke
 * scales with the box: a mark is drawn once and scaled, where a UI glyph keeps
 * a fixed hairline (`Icon.tsx`). Ink only, through `currentColor`, so it
 * follows the theme; the accent dot is the launcher icon's alone
 * (`public/icon.svg`), so the record disc stays the one accent on screen.
 * `aria-hidden` because the name beside it is the name.
 *
 * `size` takes a CSS length as well as a number, so the wordmark can size it
 * in ems. The design record is `docs/design/branding/bindu-mark.svg`.
 */
export function Mark({ size = 24 }: { size?: number | string }) {
  return (
    <svg aria-hidden="true" focusable="false" width={size} height={size} viewBox="0 0 24 24">
      <path
        d="M18.4 17.3A8.25 8.25 0 1 1 18.4 6.7"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
      />
      <circle cx="13.6" cy="12" r="1.85" fill="currentColor" />
    </svg>
  );
}

/**
 * The brand lockup: the mark, then the app's name in the notes' serif, in
 * ink, at the head of every screen — the shell's banner elsewhere, the
 * library's own heading row on Home, where the banner is empty (round-3 T17).
 * One component so the two places cannot drift.
 *
 * The mark's box is 1 em: the ring spans 18.25 of the 24 units, so its outer
 * edge lands at 0.76 em, the serif's cap height, and it stays there whatever
 * size the lockup is set at.
 */
export function Wordmark() {
  return (
    <span className="wordmark">
      <Mark size="1em" />
      {config.appName}
    </span>
  );
}
