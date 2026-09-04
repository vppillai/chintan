/**
 * "412 words", for the note's meta line (backlog U7).
 *
 * A word is a run of non-whitespace, split with the Unicode flag so every
 * script's spaces count as spaces — Malayalam and Hindi text is spaced like
 * English and the count means the same thing there. Languages written without
 * spaces (Japanese, Chinese) come out as one word per line; that is honest
 * about what the count is, and the same number every editor gives them.
 */
export function countWords(text: string): number {
  const trimmed = text.trim();
  if (trimmed.length === 0) return 0;
  return trimmed.split(/\s+/u).length;
}

export function describeWords(count: number): string {
  return `${String(count)} ${count === 1 ? 'word' : 'words'}`;
}
