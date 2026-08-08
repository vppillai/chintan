/**
 * Instant client-side search over the cached note corpus.
 *
 * Runs on every keystroke against data already in memory, so results appear
 * before the network answers and search keeps working with no connection at
 * all. The server call refines and extends this — it can see transcript text
 * the client never downloaded — but it is never what the user waits for.
 *
 * Pure, so the ranking is testable without a component.
 */

import type { NoteWire } from '@/api/schema.ts';

export interface MergedHit {
  noteId: string;
  title: string;
  excerpt: string;
  matchedIn: string[];
}

/** Characters of context to show either side of the hit. */
const CONTEXT = 48;

/**
 * A window of `text` containing `term`, with ellipses where it was cut.
 *
 * Sliced by code point rather than by UTF-16 unit, so an excerpt can never
 * split a surrogate pair and render a replacement character.
 */
export function excerptAround(text: string, term: string, context: number = CONTEXT): string {
  if (!text) return '';
  const characters = Array.from(text);
  if (!term) {
    return characters.length <= context * 2
      ? text
      : `${characters.slice(0, context * 2).join('')}…`;
  }

  const index = text.toLowerCase().indexOf(term.toLowerCase());
  if (index === -1) {
    return characters.length <= context * 2
      ? text
      : `${characters.slice(0, context * 2).join('')}…`;
  }

  // Convert the UTF-16 index to a code-point index before slicing.
  const before = Array.from(text.slice(0, index)).length;
  const termLength = Array.from(term).length;
  const start = Math.max(0, before - context);
  const end = Math.min(characters.length, before + termLength + context);

  return [
    start > 0 ? '…' : '',
    characters.slice(start, end).join(''),
    end < characters.length ? '…' : '',
  ].join('');
}

interface Scored extends MergedHit {
  score: number;
  updatedAt: string;
}

/**
 * Ranks cached notes against a query.
 *
 * Weighting is deliberately crude and explainable: a title match beats an
 * alias, which beats a tag, which beats body text. A prefix match on the title
 * outranks a mid-word one, because that is what someone typing a note's name
 * expects to see first.
 */
export function rankLocal(notes: readonly NoteWire[], query: string): MergedHit[] {
  const term = query.trim().toLowerCase();
  if (!term) return [];

  const results: Scored[] = [];

  for (const note of notes) {
    const title = note.title.toLowerCase();
    const matchedIn: string[] = [];
    let score = 0;

    if (title.startsWith(term)) {
      score += 100;
      matchedIn.push('title');
    } else if (title.includes(term)) {
      score += 60;
      matchedIn.push('title');
    }

    if ((note.aliases ?? []).some((alias) => alias.toLowerCase().includes(term))) {
      score += 40;
      matchedIn.push('alias');
    }

    if ((note.tags ?? []).some((tag) => tag.toLowerCase().includes(term))) {
      score += 30;
      matchedIn.push('tag');
    }

    const snippet = note.snippet ?? '';
    if (snippet.toLowerCase().includes(term)) {
      score += 15;
      matchedIn.push('body');
    }

    if (score === 0) continue;

    results.push({
      noteId: note.id,
      title: note.title,
      // Prefer showing the body context: the title is already the row's
      // heading, so repeating it as the excerpt wastes the line.
      excerpt: snippet.toLowerCase().includes(term)
        ? excerptAround(snippet, query.trim())
        : excerptAround(snippet, ''),
      matchedIn,
      score,
      updatedAt: note.updated_at,
    });
  }

  return results
    .sort((a, b) => {
      if (b.score !== a.score) return b.score - a.score;
      // Recency breaks ties: between two equally good matches, the one touched
      // last is the one being thought about. Timestamps are fixed-width RFC3339
      // by contract, so a string compare is a chronological compare.
      return b.updatedAt.localeCompare(a.updatedAt);
    })
    .map(({ score: _score, updatedAt: _updatedAt, ...hit }) => hit);
}
