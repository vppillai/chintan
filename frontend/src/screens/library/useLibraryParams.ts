import { useSearchParams } from 'react-router';

import type { NoteKind, NoteState } from '@/api/schema.ts';
import { ARCHIVED_VIEW, ASK_MODE } from '@/app/routes.ts';

export interface LibraryParams {
  /** Active notes or the archive. */
  view: NoteState;
  tag: string | null;
  kind: NoteKind | null;
  /** The field is in its Ask mode. */
  asking: boolean;
  /** The search filter, as typed; `''` when there is none. */
  query: string;
  /** Rewrites the filters in the URL; `null` or `''` removes one. */
  setFilter: (changes: Record<string, string | null>) => void;
}

/**
 * The library's filters, read from and written to the URL (`q`, `kind`,
 * `tag`, `view`, `mode`): a filter is shareable, survives reload and is what
 * Back returns to.
 *
 * Filters are *replaced* in the URL, not pushed. Typing must not turn Back
 * into a character-by-character undo, and a chip is a way of looking at the
 * list, not a place the user went — Back from the library should leave the
 * library, not step through every filter they tried on the way.
 *
 * `flushSync`, because the next change is built from this render's params.
 * React Router commits a navigation inside a transition, which React may
 * hold for tens of milliseconds on a busy device, so a keystroke landing
 * before that render had committed read the *previous* filter and put it
 * back: clearing the field and typing the next word produced
 * "flashingzebra7" once in the QA pass, and pressing All then typing
 * restored `view=archived`. Committing synchronously closes the window
 * (`App.tsx` mounts the `react-router/dom` provider, which is what makes
 * the option do anything).
 */
export function useLibraryParams(): LibraryParams {
  const [params, setParams] = useSearchParams();
  const view: NoteState = params.get('view') === ARCHIVED_VIEW ? 'archived' : 'active';
  const tag = params.get('tag');
  const kind: NoteKind | null = params.get('kind') === 'checklist' ? 'checklist' : null;
  const asking = params.get('mode') === ASK_MODE;
  const query = params.get('q') ?? '';

  const setFilter = (changes: Record<string, string | null>): void => {
    const next = new URLSearchParams(params);
    for (const [key, value] of Object.entries(changes)) {
      if (value === null || value === '') next.delete(key);
      else next.set(key, value);
    }
    setParams(next, { replace: true, flushSync: true });
  };

  return { view, tag, kind, asking, query, setFilter };
}
