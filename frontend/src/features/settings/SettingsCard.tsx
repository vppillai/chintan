import { useId, type ReactNode } from 'react';
import { Link } from 'react-router';

import { Icon, type IconName } from '@/components/Icon.tsx';

/**
 * The building blocks of You: a card, the rows inside it, and a segmented
 * control for a choice between two or three named options.
 *
 * Before this, every group on You was a small-caps label over a stack of
 * full-width pills — three pills for a theme, two for a cleanup mode — and
 * the explanatory sentences sat between the controls, so the screen was
 * mostly prose with a control every few hundred pixels. A card puts the
 * title, its one-line reason for existing, the controls and their footnote
 * in one hairline-bordered shape that reads at a glance, and each row is
 * label left, control right, the way every settings screen a phone ships
 * with does it, so nothing has to be learned.
 *
 * Each card is a labelled `section`, which is a landmark: the titles are
 * distinct by construction, so a screen reader's landmark list reads as a
 * table of contents for the screen.
 */
export function SettingsCard({
  title,
  lead,
  foot,
  children,
  className,
}: {
  title: ReactNode;
  /** One sentence under the title on what the card is for. */
  lead?: ReactNode;
  /** The card's footnote: the sentences that qualify what the controls do. */
  foot?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  const headingId = useId();
  return (
    <section
      className={className ? `you-card ${className}` : 'you-card'}
      aria-labelledby={headingId}
    >
      <header className="you-card__head">
        <h2 id={headingId} className="you-card__title">
          {title}
        </h2>
        {lead && <p className="you-card__lead">{lead}</p>}
      </header>
      <div className="you-card__body">{children}</div>
      {foot && <div className="you-card__foot">{foot}</div>}
    </section>
  );
}

/**
 * One row: a label on the left, the control on the right, 44 px tall at
 * least, wrapping onto two lines when the control is wider than the space
 * beside the label (the language select on a 320 px phone).
 *
 * `labelFor` makes the label a real `<label>` for a form control; a group of
 * buttons has no single control to point at, so it gets a plain element
 * with `labelId`, and the group names itself with `aria-labelledby`.
 */
export function SettingsRow({
  label,
  hint,
  labelFor,
  labelId,
  children,
}: {
  label: ReactNode;
  /** A quieter second line under the label — what the current choice means. */
  hint?: ReactNode;
  labelFor?: string;
  labelId?: string;
  children: ReactNode;
}) {
  const text = (
    <>
      <span className="you-row__label-text">{label}</span>
      {hint && <span className="you-row__hint">{hint}</span>}
    </>
  );
  return (
    <div className="you-row">
      {labelFor ? (
        <label className="you-row__label" htmlFor={labelFor}>
          {text}
        </label>
      ) : (
        <span className="you-row__label" id={labelId}>
          {text}
        </span>
      )}
      <div className="you-row__control">{children}</div>
    </div>
  );
}

interface RowActionProps {
  label: ReactNode;
  hint?: ReactNode;
  /** The glyph at the row's end: a chevron for a screen, an arrow for a site. */
  icon?: IconName;
}

/** A row that is a link: to another screen in the app, or out to the web. */
export function RowLink({
  to,
  external,
  label,
  hint,
  icon,
}: RowActionProps & { to: string; external?: boolean }) {
  const body = (
    <>
      <span className="you-row__label">
        <span className="you-row__label-text">{label}</span>
        {hint && <span className="you-row__hint">{hint}</span>}
      </span>
      <Icon name={icon ?? (external ? 'external' : 'chevron-right')} size={18} className="you-row__glyph" />
    </>
  );
  if (external) {
    return (
      <a className="you-row you-row--action" href={to} target="_blank" rel="noreferrer">
        {body}
      </a>
    );
  }
  return (
    <Link className="you-row you-row--action" to={to}>
      {body}
    </Link>
  );
}

/** A row that is a button. */
export function RowButton({
  onClick,
  disabled,
  label,
  hint,
  icon = 'chevron-right',
}: RowActionProps & { onClick: () => void; disabled?: boolean }) {
  return (
    <button type="button" className="you-row you-row--action" disabled={disabled} onClick={onClick}>
      <span className="you-row__label">
        <span className="you-row__label-text">{label}</span>
        {hint && <span className="you-row__hint">{hint}</span>}
      </span>
      <Icon name={icon} size={18} className="you-row__glyph" />
    </button>
  );
}

export interface SegmentOption<T extends string> {
  value: T;
  label: string;
  /** Drawn before the label: the theme picker's disc of each theme's paper. */
  swatch?: ReactNode;
}

/**
 * Two or three options in one shape, the chosen one filled. Real buttons
 * with `aria-pressed`, in a group named by the row's label: a radio group
 * would say the same thing, but these are one tap each and save at once,
 * which is a button's contract rather than a form field's.
 */
export function Segmented<T extends string>({
  options,
  value,
  onChange,
  disabled,
  labelledBy,
}: {
  options: readonly SegmentOption<T>[];
  value: T;
  onChange: (value: T) => void;
  disabled?: boolean;
  labelledBy?: string;
}) {
  return (
    <div className="segmented" role="group" aria-labelledby={labelledBy}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          className="segmented__option"
          aria-pressed={option.value === value}
          disabled={disabled}
          onClick={() => {
            onChange(option.value);
          }}
        >
          {option.swatch}
          <span className="segmented__label">{option.label}</span>
        </button>
      ))}
    </div>
  );
}
