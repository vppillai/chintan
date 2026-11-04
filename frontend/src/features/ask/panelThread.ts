/**
 * What only the Ask panel does with a thread: the source chips' labels and
 * Save as note. Apart from `thread.ts` so that the panel's lazy chunk carries
 * them, not every launch: Home holds the thread (`useAskThread`) from its
 * first paint, the panel only once Ask is open.
 */

import type { AskSourceWire, NoteCreateWire } from '@/api/schema.ts';
import { toPlainText } from '@/features/notes/markdown.ts';

import type { AskTurn } from './thread.ts';

/** `POST /v1/notes` caps the title; a question can run longer than that. */
const TITLE_LIMIT = 200;

/**
 * What each source chip says: the title, and where two sources share a
 * title, enough to tell them apart — the note's date from the device where
 * it has one, then the first words where their snippets differ, and a
 * number only when the device knows nothing that distinguishes them
 * (round-3 T21: "(2)" told nobody which note to open).
 */
export function sourceLabels(
  sources: readonly AskSourceWire[],
  when: (noteId: string) => string | null,
  snippetOf: (noteId: string) => string | null = () => null,
): string[] {
  const titles = new Map<string, number>();
  for (const source of sources) titles.set(source.title, (titles.get(source.title) ?? 0) + 1);

  const labels = sources.map((source) => {
    if ((titles.get(source.title) ?? 0) < 2) return source.title;
    const date = when(source.note_id);
    return date ? `${source.title} · ${date}` : source.title;
  });

  // Labels still shared after the date — the same day, or no date on the
  // device — are told apart by their snippets where the device holds them.
  const shared = new Map<string, number[]>();
  labels.forEach((label, index) => {
    const source = sources[index];
    if (!source || (titles.get(source.title) ?? 0) < 2) return;
    shared.set(label, [...(shared.get(label) ?? []), index]);
  });
  for (const indexes of shared.values()) {
    if (indexes.length < 2) continue;
    const words = distinguishingWords(indexes.map((index) => snippetOf(sources[index]!.note_id)));
    if (!words) continue;
    indexes.forEach((index, at) => {
      labels[index] = `${labels[index] ?? ''} · ${words[at] ?? ''}`;
    });
  }

  const seen = new Map<string, number>();
  return labels.map((label, index) => {
    const source = sources[index];
    if (!source || (titles.get(source.title) ?? 0) < 2) return label;
    const nth = (seen.get(label) ?? 0) + 1;
    seen.set(label, nth);
    // The first of a set keeps the bare label; only the repeats are numbered,
    // unless the label is the bare title — then every one is a repeat.
    return nth > 1 || label === source.title ? `${label} (${String(nth)})` : label;
  });
}

/** How many words of a snippet a chip shows from the first difference. */
const DISTINGUISHING_WORDS = 3;

/**
 * For snippets that all exist, the few words from the first position at
 * which every one differs from every other; null when a snippet is missing
 * or no such position exists (two identical snippets, or one a prefix of
 * another), when the caller falls back to numbering.
 */
function distinguishingWords(snippets: readonly (string | null)[]): string[] | null {
  const words = snippets.map((snippet) => (snippet ?? '').split(/\s+/).filter(Boolean));
  if (words.some((list) => list.length === 0)) return null;
  const longest = Math.max(...words.map((list) => list.length));
  for (let at = 0; at < longest; at += 1) {
    const here = words.map((list) => list[at]?.toLocaleLowerCase() ?? '');
    if (here.every(Boolean) && new Set(here).size === here.length) {
      return words.map((list) => list.slice(at, at + DISTINGUISHING_WORDS).join(' '));
    }
  }
  return null;
}

/** Every cited note, once, in the order it was first cited. */
export function sourcesOf(turns: readonly AskTurn[]): AskSourceWire[] {
  const seen = new Set<string>();
  const sources: AskSourceWire[] = [];
  for (const turn of turns) {
    for (const source of turn.sources) {
      if (seen.has(source.note_id)) continue;
      seen.add(source.note_id);
      sources.push(source);
    }
  }
  return sources;
}

/** The tag a saved thread carries, so retrieval can tell an answer from a note. */
export const ASK_NOTE_TAG = 'ask';

/**
 * The thread as a note: the first question is the title, the body is each
 * exchange as `Q:` and `A:` paragraphs, and the notes the answers drew on are
 * listed at the end so the reader can go and check. Plain text throughout —
 * a note's body is what the Text tab and the library snippet show verbatim,
 * where Markdown's asterisks and dashes are just asterisks and dashes — so
 * the answers' Markdown is read by the same parser that renders it and
 * written back as words (`toPlainText`). Turns without an answer
 * are left out: a failed question is not worth keeping. `null` when nothing
 * was answered, so there is nothing to save.
 *
 * Tagged `ask`, because an answer saved as a note is note text the next
 * question would otherwise retrieve as if it were a source, and one answer
 * feeding the next is a loop. The tag is what retrieval can exclude on.
 */
export function noteFromThread(turns: readonly AskTurn[]): NoteCreateWire | null {
  const answered = turns.filter((turn) => turn.status === 'answered' && turn.answer !== null);
  const first = turns[0];
  if (!first || answered.length === 0) return null;

  const exchanges = answered.map(
    (turn) => `Q: ${turn.question}\n\nA: ${toPlainText(turn.answer ?? '')}`,
  );
  const sources = sourcesOf(answered);
  const parts = [...exchanges];
  if (sources.length > 0) {
    parts.push(`Sources:\n${sources.map((source) => source.title).join('\n')}`);
  }
  return {
    title: first.question.slice(0, TITLE_LIMIT),
    body: parts.join('\n\n'),
    tags: [ASK_NOTE_TAG],
  };
}
