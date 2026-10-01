import { isEditing } from './useKeyboardInset.ts';

/**
 * Whether a key press belongs to a field rather than to the app: one that is
 * typed into (`isEditing`), or a select, whose keys pick an option. A
 * checkbox, a radio or a button is not a field — Escape on a selection's
 * checkbox must still end the selection (PR 218 review).
 */
function inField(target: EventTarget | null): boolean {
  return isEditing(target) || target instanceof HTMLSelectElement;
}

/**
 * Whether a key belongs to something else on the page: a field, a dialog or
 * menu that is open, or a handler that already took it. Escape closing Find,
 * a ⋮ menu or a dialog must not also cancel a hold or a selection, and R
 * typed or pressed behind a modal must not start a recording. The one rule
 * for every document-level key handler (`useHoldToTalk`, `SelectionBar`).
 */
export function keyTaken(event: KeyboardEvent): boolean {
  return (
    event.defaultPrevented ||
    event.repeat ||
    inField(event.target) ||
    document.querySelector('[role="dialog"], [role="menu"]') !== null
  );
}
