import { ROUTES } from './routes.ts';

/**
 * The library sheet state machine (spec §5.2).
 *
 *   collapsed  Home. The sheet is a persistent strip: Notes · Search · You.
 *   expanded   Full library. The record button sits centred in the bottom bar,
 *              so it is never a floating button overlaying content.
 *   locked     Recording. The sheet is shut and cannot be opened; the surface
 *              is the capture screen.
 *
 * This module is deliberately pure. The sheet's state is *derived from the
 * URL* rather than held beside it, which is what makes Back work: popping a
 * history entry recomputes the sheet, so Back collapses the sheet or pops a
 * screen instead of falling out of the app.
 */

export type SheetState = 'collapsed' | 'expanded' | 'locked';

/** The three destinations on the collapsed strip. */
export type SheetTab = 'notes' | 'search' | 'you';

export const SHEET_TABS: readonly SheetTab[] = ['notes', 'search', 'you'];

export const SHEET_TAB_LABELS: Record<SheetTab, string> = {
  notes: 'Notes',
  search: 'Search',
  you: 'You',
};

export interface SheetModel {
  state: SheetState;
  /** Which strip destination the expanded sheet is showing. */
  tab: SheetTab;
  /** True while a note detail screen is stacked on top of the library. */
  detail: boolean;
}

export type SheetEvent =
  | { type: 'expand'; tab: SheetTab }
  | { type: 'collapse' }
  | { type: 'openNote' }
  | { type: 'closeNote' }
  | { type: 'startRecording' }
  | { type: 'stopRecording' };

export const INITIAL_SHEET: SheetModel = {
  state: 'collapsed',
  tab: 'notes',
  detail: false,
};

/**
 * Transitions.
 *
 * The one rule that is not obvious: while `locked`, every sheet event is
 * ignored except `stopRecording`. §5.2 says the sheet "locks shut" during
 * recording, and a half-open library over a live microphone is exactly the
 * kind of state that loses a recording.
 */
export function sheetReducer(model: SheetModel, event: SheetEvent): SheetModel {
  if (model.state === 'locked') {
    if (event.type === 'stopRecording') {
      return { ...model, state: 'collapsed' };
    }
    return model;
  }

  switch (event.type) {
    case 'expand':
      return { state: 'expanded', tab: event.tab, detail: false };
    case 'collapse':
      return { ...model, state: 'collapsed', detail: false };
    case 'openNote':
      return { state: 'expanded', tab: 'notes', detail: true };
    case 'closeNote':
      return { state: 'expanded', tab: 'notes', detail: false };
    case 'startRecording':
      return { ...model, state: 'locked' };
    case 'stopRecording':
      return model;
    default: {
      const exhaustive: never = event;
      return exhaustive;
    }
  }
}

/** True when the sheet refuses to open or close. */
export function isSheetLocked(model: SheetModel): boolean {
  return model.state === 'locked';
}

function normalisePath(pathname: string): string {
  if (pathname.length > 1 && pathname.endsWith('/')) {
    return pathname.slice(0, -1);
  }
  return pathname;
}

/**
 * The URL is the source of truth. Given a pathname, this is the sheet.
 *
 * Anything unrecognised collapses to home rather than throwing, so a stale
 * bookmark or a bad deep link lands on the record surface — the one screen
 * that always works — instead of a blank sheet.
 */
export function sheetForPath(pathname: string): SheetModel {
  const path = normalisePath(pathname);

  if (path === ROUTES.capture) {
    return { state: 'locked', tab: 'notes', detail: false };
  }
  if (path === ROUTES.notes) {
    return { state: 'expanded', tab: 'notes', detail: false };
  }
  if (path.startsWith(`${ROUTES.notes}/`)) {
    return { state: 'expanded', tab: 'notes', detail: true };
  }
  if (path === ROUTES.search) {
    return { state: 'expanded', tab: 'search', detail: false };
  }
  if (path === ROUTES.settings) {
    return { state: 'expanded', tab: 'you', detail: false };
  }
  return INITIAL_SHEET;
}

/** The inverse: the canonical URL for a sheet state. */
export function pathForSheet(model: SheetModel): string {
  if (model.state === 'locked') return ROUTES.capture;
  if (model.state === 'collapsed') return ROUTES.home;
  switch (model.tab) {
    case 'notes':
      return ROUTES.notes;
    case 'search':
      return ROUTES.search;
    case 'you':
      return ROUTES.settings;
    default: {
      const exhaustive: never = model.tab;
      return exhaustive;
    }
  }
}

export function pathForTab(tab: SheetTab): string {
  return pathForSheet({ state: 'expanded', tab, detail: false });
}
