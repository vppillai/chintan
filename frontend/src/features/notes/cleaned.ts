import type { CleanedMode, CleanedWire } from '@/api/schema.ts';

/**
 * The cleaned view's contract with the worker, as pure functions.
 *
 * `POST /v1/notes/{id}/clean` answers 202 and says nothing more; the result
 * arrives on the note's `cleaned` a few seconds later. The screen asks for
 * the note every `CLEAN_POLL_MS` for up to `CLEAN_POLL_TIMEOUT_MS`, and
 * `cleanSettled` is how it knows to stop.
 */

export const CLEAN_POLL_MS = 2_000;
export const CLEAN_POLL_TIMEOUT_MS = 60_000;

export const CLEANED_MODE_LABELS: Record<CleanedMode, string> = {
  structured: 'Structured',
  polished: 'Polished',
  tasks: 'Split up',
};

/**
 * The modes a plain note's switch offers. `tasks` is not one of them: it is
 * the only mode a checklist is cleaned in and the server refuses it for
 * prose, so the switch never shows it and a checklist never shows the switch.
 */
export const PLAIN_CLEANED_MODES: readonly CleanedMode[] = ['structured', 'polished'];

/** What each mode does, in a sentence — the empty state's and the switch's. */
export const CLEANED_MODE_HINTS: Record<CleanedMode, string> = {
  structured: 'The whole note rewritten into headings and lists.',
  polished: 'The whole note as it is, with the prose tidied.',
  tasks: 'The list rewritten as one task per action, in your words.',
};

/**
 * Whether the worker has answered a regeneration that was queued while the
 * view looked like `before`: a view where there was none, a new
 * `generated_at`, a stale view made current, or an error to show.
 */
export function cleanSettled(
  before: CleanedWire | null,
  after: CleanedWire | null | undefined,
): boolean {
  if (!after) return false;
  if (after.error) return true;
  if (!before) return true;
  if (after.generated_at !== before.generated_at) return true;
  return before.stale && !after.stale;
}

/**
 * The cleaned view as text to copy or save: the title first, as "Copy note"
 * does, unless the view already opens with a top-level heading of its own —
 * a structured rewrite usually does, and a document with its title twice
 * reads as a mistake.
 */
export function cleanedDocument(title: string, body: string): string {
  const trimmedTitle = title.trim();
  const trimmedBody = body.trim();
  if (!trimmedTitle || /^#\s/.test(trimmedBody)) return trimmedBody;
  return `${trimmedTitle}\n\n${trimmedBody}`;
}

export function cleanedMarkdown(title: string, body: string): string {
  const trimmedTitle = title.trim();
  const trimmedBody = body.trim();
  if (!trimmedTitle || /^#\s/.test(trimmedBody)) return `${trimmedBody}\n`;
  return `# ${trimmedTitle}\n\n${trimmedBody}\n`;
}
