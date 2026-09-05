/**
 * Find inside a note, as pure functions.
 *
 * The text is compared after folding — lower-cased, decomposed (NFD) and with
 * the diacritics that decomposition peels off a Latin, Greek or Cyrillic
 * letter removed — so "cafe" finds "Café" and "angstrom" finds "Ångström".
 * Only the generic combining-diacritic blocks are stripped. The vowel signs
 * of the Indic scripts are combining marks too, but they are letters in
 * their own right: stripping them would make "മല" and "മലാ" the same word,
 * and a search in Malayalam or Hindi would light up half the note. Both the
 * text and the query are folded the same way, so a composed and a decomposed
 * spelling of the same word still meet.
 *
 * Folding changes lengths — one letter can become none — so every folded
 * character remembers where in the original it came from, and a match is
 * reported in the original's indices: what the mirror highlights and where
 * the caret goes back to.
 */

/** How many matches are counted before the count stops: "999+" is a count. */
export const FIND_MATCH_CAP = 999;

export interface FindMatch {
  /** Index of the first matched character in the original text. */
  start: number;
  /** Index just past the last matched character in the original text. */
  end: number;
}

export interface FoldedText {
  folded: string;
  /**
   * `offsets[i]` is the original index of the code point that produced
   * `folded[i]`; `offsets[folded.length]` is the original's length, so the
   * end of any folded range maps too.
   */
  offsets: number[];
}

/**
 * Combining Diacritical Marks and their supplements — what NFD produces for
 * a precomposed Latin, Greek or Cyrillic letter. Script-specific marks live
 * in their own blocks and are left alone. Ranges rather than a character
 * class: a class of combining characters is what `no-misleading-character-class`
 * exists to refuse.
 */
const DIACRITIC_BLOCKS: readonly [from: number, to: number][] = [
  [0x0300, 0x036f],
  [0x1ab0, 0x1aff],
  [0x1dc0, 0x1dff],
  [0x20d0, 0x20ff],
  [0xfe20, 0xfe2f],
];

function isDiacritic(codePoint: number): boolean {
  return DIACRITIC_BLOCKS.some(([from, to]) => codePoint >= from && codePoint <= to);
}

/** The character lower-cased and decomposed, with its generic diacritics gone. */
function foldCharacter(character: string): string {
  let piece = '';
  for (const part of character.toLowerCase().normalize('NFD')) {
    if (!isDiacritic(part.codePointAt(0) ?? 0)) piece += part;
  }
  return piece;
}

export function foldText(text: string): FoldedText {
  let folded = '';
  const offsets: number[] = [];
  let index = 0;
  // One code point at a time, so a letter that folds to two characters — or
  // to none — still maps back to where it began.
  for (const character of text) {
    const piece = foldCharacter(character);
    for (let produced = 0; produced < piece.length; produced += 1) offsets.push(index);
    folded += piece;
    index += character.length;
  }
  offsets.push(text.length);
  return { folded, offsets };
}

/**
 * Every place `query` occurs in `text`, left to right, without overlap, up to
 * `cap`. An empty query — or one that folds to nothing — matches nowhere.
 */
export function findMatches(text: string, query: string, cap = FIND_MATCH_CAP): FindMatch[] {
  const needle = foldText(query).folded;
  const matches: FindMatch[] = [];
  if (needle.length === 0 || cap <= 0) return matches;

  const { folded, offsets } = foldText(text);
  let from = 0;
  while (matches.length < cap) {
    const at = folded.indexOf(needle, from);
    if (at === -1) break;
    matches.push({ start: offsets[at] ?? 0, end: offsets[at + needle.length] ?? text.length });
    from = at + needle.length;
  }
  return matches;
}

export interface TextRun {
  text: string;
  /** Which match this run is, counting from `first`; `null` for the text between. */
  match: number | null;
}

/**
 * The text cut into runs for rendering: plain text between matches, and each
 * match numbered from `first` — the number of matches in the text nodes
 * before this one, when a document is searched node by node.
 */
export function splitRuns(text: string, matches: readonly FindMatch[], first = 0): TextRun[] {
  const runs: TextRun[] = [];
  let cursor = 0;
  matches.forEach((match, index) => {
    if (match.start > cursor) runs.push({ text: text.slice(cursor, match.start), match: null });
    runs.push({ text: text.slice(match.start, match.end), match: first + index });
    cursor = match.end;
  });
  if (cursor < text.length) runs.push({ text: text.slice(cursor), match: null });
  return runs;
}

/** "3 of 12", "1 of 999+", "No matches" — or nothing before anything is typed. */
export function describeMatches(query: string, active: number, total: number): string {
  if (query === '') return '';
  if (total === 0) return 'No matches';
  const ceiling = total >= FIND_MATCH_CAP ? `${String(FIND_MATCH_CAP)}+` : String(total);
  return `${String(active + 1)} of ${ceiling}`;
}

/**
 * The find bar's state and the moves on it. A reducer rather than four
 * `useState`s because the moves are coupled: a new query starts from the
 * first match, a new total clamps the active one, and next and previous
 * wrap around the total the panel last reported.
 */
export interface FindState {
  open: boolean;
  query: string;
  active: number;
  total: number;
}

export type FindAction =
  | { type: 'open' }
  | { type: 'close' }
  | { type: 'toggle' }
  | { type: 'query'; query: string }
  | { type: 'total'; total: number }
  | { type: 'next' }
  | { type: 'previous' }
  /** The content changed under the bar — another tab — so start over. */
  | { type: 'rewind' };

export const FIND_CLOSED: FindState = { open: false, query: '', active: 0, total: 0 };

export function findReducer(state: FindState, action: FindAction): FindState {
  switch (action.type) {
    case 'open':
      return state.open ? state : { ...state, open: true };
    case 'close':
      return state.open ? { ...state, open: false } : state;
    case 'toggle':
      return { ...state, open: !state.open };
    case 'query':
      return action.query === state.query ? state : { ...state, query: action.query, active: 0 };
    case 'total': {
      const total = Math.max(0, action.total);
      const active = Math.min(state.active, Math.max(0, total - 1));
      return total === state.total && active === state.active ? state : { ...state, total, active };
    }
    case 'next':
      return state.total === 0 ? state : { ...state, active: (state.active + 1) % state.total };
    case 'previous':
      return state.total === 0
        ? state
        : { ...state, active: (state.active - 1 + state.total) % state.total };
    case 'rewind':
      return state.active === 0 ? state : { ...state, active: 0 };
    default:
      return state;
  }
}
