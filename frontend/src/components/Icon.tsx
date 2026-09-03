import type { SVGProps } from 'react';

/**
 * Icons are SVG. No emoji as iconography (spec §5.6).
 *
 * Every icon is `aria-hidden`: the accessible name always comes from the
 * control's own text or `aria-label`, never from the glyph.
 */

export type IconName =
  | 'notes'
  | 'you'
  | 'mic'
  | 'play'
  | 'stop'
  | 'back'
  | 'check'
  | 'trash';

const PATHS: Record<IconName, string> = {
  notes: 'M6 3h9l4 4v14H6zM15 3v4h4M9 12h7M9 16h7',
  you: 'M12 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8zM4 21a8 8 0 0 1 16 0',
  mic: 'M12 3a3 3 0 0 1 3 3v6a3 3 0 0 1-6 0V6a3 3 0 0 1 3-3zM5 11a7 7 0 0 0 14 0M12 18v3',
  // Playback, not recording — a filled triangle rather than the microphone
  // glyph, which NoteDetailScreen's audio player borrowed from the record
  // button and which read as "record" on a control that only ever plays back
  // audio that already exists.
  play: 'M7 4.5v15l13-7.5z',
  stop: 'M7 7h10v10H7z',
  back: 'M15 5 8 12l7 7',
  check: 'M5 12.5 10 17.5 19 7',
  trash: 'M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13M10 11v6M14 11v6',
};

export interface IconProps extends Omit<SVGProps<SVGSVGElement>, 'name'> {
  name: IconName;
  size?: number;
}

export function Icon({ name, size = 22, ...rest }: IconProps) {
  return (
    <svg
      aria-hidden="true"
      focusable="false"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.6}
      strokeLinecap="round"
      strokeLinejoin="round"
      {...rest}
    >
      <path d={PATHS[name]} />
    </svg>
  );
}
