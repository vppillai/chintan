import { useNavigate } from 'react-router';

import { ROUTES } from '@/app/routes.ts';

import { Icon } from './Icon.tsx';

/**
 * The record target, seated in the middle of the tab bar on every screen — in
 * flow, not floating, so it can never overlay a note row.
 *
 * It is the only element in the app allowed to wear `--color-accent`.
 */
export function RecordButton() {
  const navigate = useNavigate();

  return (
    <button
      type="button"
      className="record-button"
      onClick={() => {
        void navigate(ROUTES.capture);
      }}
    >
      <Icon name="mic" size={26} className="record-button__icon" />
      <span className="visually-hidden">Record</span>
    </button>
  );
}
