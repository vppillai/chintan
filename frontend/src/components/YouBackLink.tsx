import { Link } from 'react-router';

import { ROUTES } from '@/app/routes.ts';
import { isPlainClick, useTabNavigation } from '@/app/useTabNavigation.ts';

import { Icon } from './Icon.tsx';

/**
 * "‹ You" on About and Usage, the two screens reached from You.
 *
 * It goes Back rather than pushing You again: as a plain link it stacked a
 * second You on the first, and Back then walked through both (R8, F2). Still
 * a real link, so a modified click opens You in a new tab.
 */
export function YouBackLink() {
  const { goBackTo } = useTabNavigation();
  return (
    <Link
      to={ROUTES.settings}
      className="back-link"
      onClick={(event) => {
        if (!isPlainClick(event)) return;
        event.preventDefault();
        goBackTo(ROUTES.settings);
      }}
    >
      <Icon name="back" size={18} />
      <span className="visually-hidden">Back to </span>You
    </Link>
  );
}
