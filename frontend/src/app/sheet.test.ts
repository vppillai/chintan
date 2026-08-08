import { describe, expect, it } from 'vitest';

import {
  INITIAL_SHEET,
  isSheetLocked,
  pathForSheet,
  pathForTab,
  sheetForPath,
  sheetReducer,
  type SheetModel,
} from './sheet.ts';

describe('sheetForPath — the URL is the source of truth', () => {
  it.each([
    ['/', 'collapsed', 'notes', false],
    ['/notes', 'expanded', 'notes', false],
    ['/notes/', 'expanded', 'notes', false],
    ['/notes/roof-repair', 'expanded', 'notes', true],
    ['/search', 'expanded', 'search', false],
    ['/settings', 'expanded', 'you', false],
    ['/capture', 'locked', 'notes', false],
  ])('%s -> %s/%s (detail=%s)', (path, state, tab, detail) => {
    expect(sheetForPath(path)).toEqual({ state, tab, detail });
  });

  it('collapses to home for an unrecognised path rather than showing a blank sheet', () => {
    expect(sheetForPath('/nope/deeper')).toEqual(INITIAL_SHEET);
  });
});

describe('pathForSheet — every sheet state has a real URL', () => {
  it('round-trips each state', () => {
    const states: SheetModel[] = [
      { state: 'collapsed', tab: 'notes', detail: false },
      { state: 'expanded', tab: 'notes', detail: false },
      { state: 'expanded', tab: 'search', detail: false },
      { state: 'expanded', tab: 'you', detail: false },
      { state: 'locked', tab: 'notes', detail: false },
    ];
    for (const model of states) {
      expect(sheetForPath(pathForSheet(model))).toEqual(model);
    }
  });

  it('maps the three strip destinations', () => {
    expect(pathForTab('notes')).toBe('/notes');
    expect(pathForTab('search')).toBe('/search');
    expect(pathForTab('you')).toBe('/settings');
  });
});

describe('sheetReducer — collapsed / expanded', () => {
  it('expands to the requested tab', () => {
    expect(sheetReducer(INITIAL_SHEET, { type: 'expand', tab: 'search' })).toEqual({
      state: 'expanded',
      tab: 'search',
      detail: false,
    });
  });

  it('collapses from expanded and drops any stacked detail screen', () => {
    const expanded: SheetModel = { state: 'expanded', tab: 'notes', detail: true };
    expect(sheetReducer(expanded, { type: 'collapse' })).toEqual({
      state: 'collapsed',
      tab: 'notes',
      detail: false,
    });
  });

  it('stacks and unstacks the note detail screen on the notes tab', () => {
    const opened = sheetReducer(INITIAL_SHEET, { type: 'openNote' });
    expect(opened).toEqual({ state: 'expanded', tab: 'notes', detail: true });
    expect(sheetReducer(opened, { type: 'closeNote' })).toEqual({
      state: 'expanded',
      tab: 'notes',
      detail: false,
    });
  });

  it('is idempotent when collapsing an already-collapsed sheet', () => {
    expect(sheetReducer(INITIAL_SHEET, { type: 'collapse' })).toEqual(INITIAL_SHEET);
  });
});

describe('sheetReducer — locked during recording', () => {
  const recording = sheetReducer(
    { state: 'expanded', tab: 'search', detail: false },
    { type: 'startRecording' },
  );

  it('locks from any open state', () => {
    expect(recording.state).toBe('locked');
    expect(isSheetLocked(recording)).toBe(true);
    expect(sheetReducer(INITIAL_SHEET, { type: 'startRecording' }).state).toBe('locked');
  });

  it('refuses to expand while locked', () => {
    expect(sheetReducer(recording, { type: 'expand', tab: 'notes' })).toBe(recording);
  });

  it('refuses to collapse while locked', () => {
    expect(sheetReducer(recording, { type: 'collapse' })).toBe(recording);
  });

  it('refuses to open a note while locked', () => {
    expect(sheetReducer(recording, { type: 'openNote' })).toBe(recording);
  });

  it('unlocks to collapsed when recording stops', () => {
    expect(sheetReducer(recording, { type: 'stopRecording' })).toEqual({
      state: 'collapsed',
      tab: 'search',
      detail: false,
    });
  });

  it('ignores stopRecording when not recording', () => {
    expect(sheetReducer(INITIAL_SHEET, { type: 'stopRecording' })).toBe(INITIAL_SHEET);
  });
});
