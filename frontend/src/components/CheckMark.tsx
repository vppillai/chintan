import { ICON_STROKE_WIDTH, PATHS } from '@/components/Icon.tsx';

/**
 * The box: a real checkbox, kept for what only it gives — the role, the
 * name, the keyboard, the state — and stretched invisibly over its 44 px
 * label, so the tap lands on the control itself; beside it the box a finger
 * sees, drawn here in the icon set's own pen (`ICON_STROKE_WIDTH`, not
 * scaling, round caps) so it reads as the same hand as every glyph. The tick
 * is `PATHS.check` with a `pathLength` of 1, which lets the stylesheet hide
 * it with one dash and draw it as a stroke when the box is ticked. No
 * browser's native box appears anywhere in the app.
 */
export function Check({ checked, name, onChange }: { checked: boolean; name: string; onChange: () => void }) {
  return (
    <label className="checklist__check">
      <input type="checkbox" className="checklist__box" checked={checked} onChange={onChange} />
      <CheckMark />
      <span className="visually-hidden">{name}</span>
    </label>
  );
}

/**
 * The box a finger sees, on its own: the `.checklist__box` input before it
 * in the same label is what the stylesheet reads the state from, so any
 * label built that way — the Items rows, the Split up rows, the switches in
 * the Cleaned tab and Details — shows the one drawn box.
 */
export function CheckMark() {
  return (
    <span className="checklist__mark" aria-hidden="true">
      <svg
        width={22}
        height={22}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth={ICON_STROKE_WIDTH}
        strokeLinecap="round"
        strokeLinejoin="round"
        focusable="false"
      >
        <rect x={1} y={1} width={22} height={22} rx={5.5} vectorEffect="non-scaling-stroke" />
        <path d={PATHS.check} pathLength={1} vectorEffect="non-scaling-stroke" />
      </svg>
    </span>
  );
}
