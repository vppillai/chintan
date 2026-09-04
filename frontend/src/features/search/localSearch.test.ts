import { describe, expect, it } from 'vitest';

import type { NoteWire } from '@/api/schema.ts';

import { excerptAround, rankLocal } from './localSearch.ts';

function note(overrides: Partial<NoteWire> & { id: string; title: string }): NoteWire {
  return {
    updated_at: '2026-08-01T00:00:00.000Z',
    version: 1,
    archived: false,
    ...overrides,
  };
}

const CORPUS: NoteWire[] = [
  note({
    id: 'roof',
    title: 'Roof repair',
    snippet: 'Ridge tiles on the south slope have slipped.',
    tags: ['house'],
    updated_at: '2026-08-06T00:00:00.000Z',
  }),
  note({
    id: 'garden',
    title: 'Garden',
    snippet: 'Move the rosemary before it woodens. The roof gutter drips here.',
    tags: ['house', 'seasonal'],
    aliases: ['Back garden'],
    updated_at: '2026-08-05T00:00:00.000Z',
  }),
  note({ id: 'car', title: 'Car service', snippet: 'Rear pads at 3mm.', tags: ['admin'] }),
];

describe('rankLocal', () => {
  it('is empty for an empty query', () => {
    expect(rankLocal(CORPUS, '')).toEqual([]);
    expect(rankLocal(CORPUS, '   ')).toEqual([]);
  });

  it('ranks a title prefix above a body match', () => {
    const results = rankLocal(CORPUS, 'roof');
    expect(results.map((hit) => hit.noteId)).toEqual(['roof', 'garden']);
  });

  it('reports which fields matched', () => {
    const [hit] = rankLocal(CORPUS, 'roof');
    expect(hit?.matchedIn).toContain('title');

    const [gardenHit] = rankLocal(CORPUS, 'rosemary');
    expect(gardenHit?.matchedIn).toEqual(['body']);
  });

  it('matches aliases and tags', () => {
    expect(rankLocal(CORPUS, 'back garden')[0]?.matchedIn).toContain('alias');
    expect(rankLocal(CORPUS, 'admin')[0]?.noteId).toBe('car');
  });

  it('is case-insensitive', () => {
    expect(rankLocal(CORPUS, 'ROOF')[0]?.noteId).toBe('roof');
  });

  it('breaks ties by recency', () => {
    // Two notes tagged 'house'; the one edited last is the one being thought
    // about.
    const results = rankLocal(CORPUS, 'house');
    expect(results.map((hit) => hit.noteId)).toEqual(['roof', 'garden']);
  });

  it('excludes notes that do not match at all', () => {
    expect(rankLocal(CORPUS, 'zebra')).toEqual([]);
  });

  it('shows body context rather than repeating the title', () => {
    const [hit] = rankLocal(CORPUS, 'rosemary');
    expect(hit?.excerpt).toContain('rosemary');
  });
});

/**
 * Transcript text. The snippet is the first line of a note; a sentence said
 * three recordings in was only ever findable by the server. The backend is
 * adding the lowercased body to list items as `search_text`, and a cached full
 * note already carries `body`; both are optional, and both count as a body
 * match.
 */
describe('rankLocal over the body, when the corpus carries it', () => {
  const withText: NoteWire[] = [
    ...CORPUS,
    note({
      id: 'talk',
      title: 'Ideas for the talk',
      snippet: 'Open with the failure, not the fix.',
      tags: ['work'],
      search_text:
        'open with the failure, not the fix. the point is that the retry passed while the promise was false. mention the downpipe story.',
    }),
    note({
      id: 'cached-full',
      title: 'Dentist',
      snippet: 'Book the hygienist for the week after Portugal.',
      // A `NoteDetailWire` read back from the device carries the body itself.
      ...({ body: 'Book the hygienist for the week after Portugal. Ask about the crown.' } as object),
    }),
  ];

  it('finds a note by text that appears only in search_text', () => {
    const results = rankLocal(withText, 'downpipe');
    expect(results.map((hit) => hit.noteId)).toEqual(['talk']);
    expect(results[0]?.matchedIn).toEqual(['body']);
    // The row shows where it matched, not the unrelated first line.
    expect(results[0]?.excerpt).toContain('downpipe');
  });

  it('finds a cached full note by its body', () => {
    expect(rankLocal(withText, 'crown').map((hit) => hit.noteId)).toEqual(['cached-full']);
  });

  it('ranks a body match below a tag match', () => {
    const tagged = note({ id: 'tagged', title: 'Plumbing', tags: ['downpipe'] });
    const results = rankLocal([...withText, tagged], 'downpipe');
    expect(results.map((hit) => hit.noteId)).toEqual(['tagged', 'talk']);
  });

  it('counts a snippet hit and a body hit as one body match', () => {
    const [hit] = rankLocal(withText, 'failure');
    expect(hit?.noteId).toBe('talk');
    expect(hit?.matchedIn).toEqual(['body']);
  });

  it('keeps working for a corpus with no body text at all', () => {
    expect(rankLocal(CORPUS, 'downpipe')).toEqual([]);
    expect(rankLocal(CORPUS, 'roof').map((hit) => hit.noteId)).toEqual(['roof', 'garden']);
  });
});

describe('excerptAround', () => {
  it('returns short text unchanged', () => {
    expect(excerptAround('short text', 'text')).toBe('short text');
  });

  it('windows around the hit with ellipses', () => {
    const long = `${'a'.repeat(200)} needle ${'b'.repeat(200)}`;
    const excerpt = excerptAround(long, 'needle', 10);

    expect(excerpt).toContain('needle');
    expect(excerpt.startsWith('…')).toBe(true);
    expect(excerpt.endsWith('…')).toBe(true);
    expect(excerpt.length).toBeLessThan(60);
  });

  it('falls back to the head when the term is absent', () => {
    const long = 'x'.repeat(500);
    expect(excerptAround(long, 'nope', 10).endsWith('…')).toBe(true);
  });

  it('never splits a surrogate pair', () => {
    // Sliced by code point, so an excerpt cannot end in a replacement
    // character.
    const text = `${'🎧'.repeat(60)} needle ${'🎧'.repeat(60)}`;
    const excerpt = excerptAround(text, 'needle', 5);

    expect(excerpt).not.toContain('�');
    expect(Array.from(excerpt).every((character) => character !== '\uD83C')).toBe(true);
  });

  it('is empty-safe', () => {
    expect(excerptAround('', 'x')).toBe('');
  });
});
