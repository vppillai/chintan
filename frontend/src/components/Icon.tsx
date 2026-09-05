import type { SVGProps } from 'react';

/**
 * Icons are SVG. No emoji as iconography.
 *
 * One set, one weight. Every glyph is drawn on a 24-unit grid with a 1.75 px
 * stroke, round caps and round joins, and — because the same glyph is used at
 * 16, 18, 20, 22 and 30 px — the stroke does not scale with the box:
 * `vector-effect: non-scaling-stroke` keeps it 1.75 device-independent pixels
 * at every size, so a 16 px chevron beside a 22 px tab icon reads as the same
 * pen. Before this the weight was 1.6 units and thinned with the box, so the
 * small icons came out at just over a pixel next to the tab bar's.
 *
 * Every icon is `aria-hidden`: the accessible name always comes from the
 * control's own text or `aria-label`, never from the glyph.
 */

export const ICON_STROKE_WIDTH = 1.75;

export type IconName =
  | 'home'
  | 'you'
  | 'mic'
  | 'play'
  | 'stop'
  | 'back'
  | 'check'
  | 'trash'
  | 'more'
  | 'download'
  | 'move'
  | 'plus'
  | 'archive'
  | 'restore'
  | 'search'
  | 'close'
  | 'chevron-up'
  | 'chevron-down';

export const PATHS: Record<IconName, string> = {
  // A house: roof, two walls, a door. The Home tab is the way back to the
  // start of the app, and it wore the document glyph from when the tab was
  // called Notes — a page icon under the word "Home" read as two different
  // places. The roof's apex sits a unit above the grid's centre line so the
  // glyph's optical centre matches the person beside it.
  home: 'M4 11.5 12 4.5l8 7M5.5 10.2V20h13V10.2M10 20v-5h4v5',
  you: 'M12 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8zM4 21a8 8 0 0 1 16 0',
  mic: 'M12 3a3 3 0 0 1 3 3v6a3 3 0 0 1-6 0V6a3 3 0 0 1 3-3zM5 11a7 7 0 0 0 14 0M12 18v3',
  // Playback, not recording — a triangle rather than the microphone glyph,
  // which NoteDetailScreen's audio player borrowed from the record button and
  // which read as "record" on a control that only ever plays back audio that
  // already exists. Outlined like every other glyph; its optical centre sits
  // a unit right of the box's, as a play triangle's should.
  play: 'M8 5v14l11-7z',
  // Square, rounded by the joins, drawn a unit in from the play triangle's
  // extent so the two read as the same size when they swap.
  stop: 'M7 7h10v10H7z',
  back: 'M15 5 8 12l7 7',
  check: 'M5 12.5 10 17.5 19 7',
  trash: 'M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13M10 11v6M14 11v6',
  plus: 'M12 5v14M5 12h14',
  // Three dots on the vertical centre line: the overflow control. Each dot is
  // a zero-length stroke, so the round caps draw it at the stroke's own width
  // and it stays in step with every other glyph's weight.
  more: 'M12 5.5v.01M12 12v.01M12 18.5v.01',
  // An arrow into a tray.
  download: 'M12 4v11M7.5 10.5 12 15l4.5-4.5M5 19h14',
  // An arrow leaving a bracket for a bar: out of this note, into another.
  move: 'M9 5H5v14h4M10 12h9M15.5 8.5 19 12l-3.5 3.5',
  // A box under its lid, with the pull on the front: where a note goes when
  // it is archived. The swipe tray's Archive action wears it.
  archive: 'M4 5h16v4H4zM5.5 9v10h13V9M10 13h4',
  // An arrow running back around the clock: restore, the reverse of archive.
  // The head sits at the arc's start so the two glyphs read as a pair.
  restore: 'M4 12a8 8 0 1 0 8-8 8.7 8.7 0 0 0-6 2.4L4 8.5M4 3.5v5h5',
  // A lens and its handle: find inside the note. The circle's centre sits up
  // and left of the grid's so the handle can reach the corner at the stroke's
  // own angle.
  search: 'M10.5 4a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13zM15.5 15.5 20 20',
  close: 'M6 6l12 12M18 6 6 18',
  // The back chevron, turned: previous and next match.
  'chevron-up': 'M5 15l7-7 7 7',
  'chevron-down': 'M5 9l7 7 7-7',
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
      strokeWidth={ICON_STROKE_WIDTH}
      strokeLinecap="round"
      strokeLinejoin="round"
      {...rest}
    >
      <path d={PATHS[name]} vectorEffect="non-scaling-stroke" />
    </svg>
  );
}
