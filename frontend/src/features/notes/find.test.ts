import { describe, expect, it } from 'vitest';

import {
  FIND_CLOSED,
  FIND_MATCH_CAP,
  describeMatches,
  findMatches,
  findReducer,
  foldText,
  splitRuns,
  type FindState,
} from './find.ts';

describe('finding a query in a text', () => {
  const cases: {
    name: string;
    text: string;
    query: string;
    expected: string[];
  }[] = [
    { name: 'a plain substring, every occurrence', text: 'the cat sat on the mat', query: 'at', expected: ['at', 'at', 'at'] },
    { name: 'regardless of case, either way', text: 'Ellis quoted. ELLIS again. ellis', query: 'Ellis', expected: ['Ellis', 'ELLIS', 'ellis'] },
    { name: 'an accented letter found by its plain one', text: 'Café au lait', query: 'cafe', expected: ['Café'] },
    { name: 'a plain letter found by an accented query', text: 'the cafe on the corner', query: 'café', expected: ['cafe'] },
    { name: 'several diacritics on one word', text: 'Ångström, Ångström', query: 'angstrom', expected: ['Ångström', 'Ångström'] },
    { name: 'a decomposed spelling finds a composed one', text: 'résumé', query: 'résumé', expected: ['résumé'] },
    { name: 'Vietnamese, stacked marks and all', text: 'Việt Nam', query: 'viet', expected: ['Việt'] },
    { name: 'German sharp s is its own letter', text: 'Straße', query: 'strasse', expected: [] },
    { name: 'Malayalam, exactly as written', text: 'മലയാളം എന്റെ ഭാഷ. മലയാളം.', query: 'മലയാളം', expected: ['മലയാളം', 'മലയാളം'] },
    { name: 'a Malayalam vowel sign is a letter, not a diacritic', text: 'കാ കി കു', query: 'കി', expected: ['കി'] },
    { name: 'so a word with a vowel sign is not found in one without', text: 'മല', query: 'മലാ', expected: [] },
    { name: 'a Malayalam virama is kept too', text: 'എന്റെ എനറെ', query: 'എന്റെ', expected: ['എന്റെ'] },
    { name: 'Hindi', text: 'मेरा नाम राम है। राम।', query: 'राम', expected: ['राम', 'राम'] },
    { name: 'a Hindi matra is a letter', text: 'कर कुर', query: 'कर', expected: ['कर'] },
    { name: 'across a newline boundary no match is invented', text: 'first\nsecond', query: 'first second', expected: [] },
    { name: 'a match may span a newline when the text has one', text: 'first\nsecond', query: 'first\nsecond', expected: ['first\nsecond'] },
    { name: 'the matched slice is the original spelling', text: 'CAFÉ café Cafe', query: 'cafe', expected: ['CAFÉ', 'café', 'Cafe'] },
    { name: 'matches do not overlap', text: 'aaaa', query: 'aa', expected: ['aa', 'aa'] },
    { name: 'an empty query matches nothing', text: 'anything at all', query: '', expected: [] },
    { name: 'a query of only diacritics matches nothing', text: 'anything at all', query: '\u0301', expected: [] },
    { name: 'a query that is not there', text: 'roof repair', query: 'gutter', expected: [] },
    { name: 'an emoji stays one code point either side', text: 'a🙂b a🙂b', query: '🙂b', expected: ['🙂b', '🙂b'] },
  ];

  for (const { name, text, query, expected } of cases) {
    it(name, () => {
      const matches = findMatches(text, query);
      expect(matches.map((match) => text.slice(match.start, match.end))).toEqual(expected);
      // Every match is a well-formed, forward range inside the text.
      for (const match of matches) {
        expect(match.start).toBeGreaterThanOrEqual(0);
        expect(match.end).toBeGreaterThan(match.start);
        expect(match.end).toBeLessThanOrEqual(text.length);
      }
    });
  }

  it('reports indices in the original text even where folding changed the length', () => {
    // "İ" lower-cases to two code units and folds back to one; the indices
    // after it must not drift.
    const text = 'İstanbul cafe';
    expect(findMatches(text, 'cafe')).toEqual([{ start: 9, end: 13 }]);
    expect(findMatches(text, 'istanbul')).toEqual([{ start: 0, end: 8 }]);
  });

  it('stops counting at the cap', () => {
    const text = 'x '.repeat(FIND_MATCH_CAP + 50);
    expect(findMatches(text, 'x')).toHaveLength(FIND_MATCH_CAP);
    expect(findMatches(text, 'x', 3)).toHaveLength(3);
    expect(findMatches(text, 'x', 0)).toHaveLength(0);
  });
});

describe('folding', () => {
  it('maps every folded character back to an original index and ends at the length', () => {
    const text = 'Ångström';
    const { folded, offsets } = foldText(text);
    expect(folded).toBe('angstrom');
    expect(offsets).toHaveLength(folded.length + 1);
    expect(offsets.at(-1)).toBe(text.length);
    for (let index = 1; index < offsets.length; index += 1) {
      expect(offsets[index]).toBeGreaterThanOrEqual(offsets[index - 1] ?? 0);
    }
  });
});

describe('cutting text into runs', () => {
  it('numbers the matches from where the previous node left off', () => {
    const text = 'cat and cat';
    const runs = splitRuns(text, findMatches(text, 'cat'), 4);
    expect(runs).toEqual([
      { text: 'cat', match: 4 },
      { text: ' and ', match: null },
      { text: 'cat', match: 5 },
    ]);
  });

  it('is the whole text, in order, whatever the matches', () => {
    const text = 'Ridge tiles on the south slope have slipped.';
    for (const query of ['e', 'Ridge', 'slipped.', 'zzz', '']) {
      const runs = splitRuns(text, findMatches(text, query));
      expect(runs.map((run) => run.text).join('')).toBe(text);
    }
  });
});

describe('describing the count', () => {
  const cases: [query: string, active: number, total: number, expected: string][] = [
    ['', 0, 0, ''],
    ['x', 0, 0, 'No matches'],
    ['x', 0, 1, '1 of 1'],
    ['x', 2, 12, '3 of 12'],
    ['x', 0, FIND_MATCH_CAP, '1 of 999+'],
  ];
  for (const [query, active, total, expected] of cases) {
    it(`${JSON.stringify(query)} at ${String(active)} of ${String(total)} → ${JSON.stringify(expected)}`, () => {
      expect(describeMatches(query, active, total)).toBe(expected);
    });
  }
});

describe('the find bar’s state', () => {
  const open: FindState = { open: true, query: 'cat', active: 0, total: 3 };

  it('starts a new query from the first match', () => {
    expect(findReducer({ ...open, active: 2 }, { type: 'query', query: 'dog' })).toEqual({
      ...open,
      query: 'dog',
      active: 0,
    });
  });

  it('wraps next and previous around the total', () => {
    expect(findReducer({ ...open, active: 2 }, { type: 'next' }).active).toBe(0);
    expect(findReducer({ ...open, active: 0 }, { type: 'previous' }).active).toBe(2);
    expect(findReducer({ ...open, active: 1 }, { type: 'next' }).active).toBe(2);
  });

  it('goes nowhere with no matches', () => {
    const none = { ...open, total: 0 };
    expect(findReducer(none, { type: 'next' })).toBe(none);
    expect(findReducer(none, { type: 'previous' })).toBe(none);
  });

  it('clamps the active match when the panel reports fewer', () => {
    expect(findReducer({ ...open, active: 2 }, { type: 'total', total: 2 }).active).toBe(1);
    expect(findReducer({ ...open, active: 2 }, { type: 'total', total: 0 }).active).toBe(0);
    // The same total is the same state, so an effect reporting it does not loop.
    expect(findReducer(open, { type: 'total', total: 3 })).toBe(open);
  });

  it('keeps the query across close and open, and rewinds for another tab', () => {
    const closed = findReducer(open, { type: 'close' });
    expect(closed.open).toBe(false);
    expect(closed.query).toBe('cat');
    expect(findReducer(closed, { type: 'toggle' }).open).toBe(true);
    expect(findReducer({ ...open, active: 2 }, { type: 'rewind' }).active).toBe(0);
    expect(findReducer(FIND_CLOSED, { type: 'open' })).toEqual({ ...FIND_CLOSED, open: true });
  });
});
