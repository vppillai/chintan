import { useNavigate } from 'react-router';

import { ROUTES } from '@/app/routes.ts';

import { Icon } from './Icon.tsx';

/**
 * The record target.
 *
 * `hero` is the home surface: large, centred, with nothing competing (§5.2).
 * `bar` is the same control seated in the expanded sheet's bottom bar — in
 * flow, not floating, so it can never overlay a note row.
 *
 * It is the only element in the app allowed to wear `--color-accent`.
 */
export function RecordButton({ variant = 'hero' }: { variant?: 'hero' | 'bar' }) {
  const navigate = useNavigate();

  return (
    <button
      type="button"
      className={`record-button record-button--${variant}`}
      onClick={() => {
        void navigate(ROUTES.capture);
      }}
    >
      <span className="record-button__dot" aria-hidden="true" />
      <Icon name="mic" size={variant === 'hero' ? 40 : 24} className="record-button__icon" />
      <span className={variant === 'hero' ? 'record-button__label' : 'visually-hidden'}>
        Record
      </span>
    </button>
  );
}
