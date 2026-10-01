import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { NoteDetailWire } from '@/api/schema.ts';
import { HOLD_ARM_MS, LOCK_DY_PX, MIN_TALK_MS } from '@/features/capture/holdTiming.ts';
import { captureState, endHold, fake, mountBar, resetHold, speak, spoken, touch, wait, where } from '@/test/hold.tsx';
import { useCaptureStore } from '@/features/capture/store.ts';
import { TEST_NOTES } from '@/test/providers.tsx';

import { PATHS } from './Icon.tsx';

beforeEach(resetHold);
afterEach(endHold);

/** The glyph a tab link is drawn with, as the path data on its SVG. */
function glyphOf(link: HTMLElement): string | null {
  return link.querySelector('svg path')?.getAttribute('d') ?? null;
}

const nav = () => screen.getByRole('navigation', { name: 'Main' });
const pill = () => document.querySelector('.tab-bar__into');

describe('the tab bar', () => {
  it('draws Home with the house glyph, not the document glyph the tab wore as Notes', () => {
    mountBar('/');
    const home = screen.getByRole('link', { name: 'Home' });
    expect(glyphOf(home)).toBe(PATHS.home);
    // The person for You, and nothing on the bar drawn as a page of lines.
    expect(glyphOf(screen.getByRole('link', { name: 'You' }))).toBe(PATHS.you);
    expect('notes' in PATHS).toBe(false);
  });

  it('keeps Home lit while reading a note, because the tab names the section', () => {
    mountBar('/notes/roof-repair');
    expect(screen.getByRole('link', { name: 'Home' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: 'You' })).not.toHaveAttribute('aria-current');
  });
});

/**
 * The mic is contextual: on a note it records into that note, and says so.
 * The note's own "Record into this" sat 30 px above a mic that recorded into
 * a new note (review 2026-09-21, T6); one control now, one destination.
 */
describe('the record button', () => {
  it('records into a new note from the library', async () => {
    mountBar('/');
    const record = screen.getByRole('button', { name: 'Record' });
    expect(screen.queryByText('Into this note')).toBeNull();
    await userEvent.setup({ advanceTimers: vi.advanceTimersByTime }).click(record);
    expect(where()).toBe('/capture');
  });

  it('records into the open note, and wears its caption, while a note is on screen', async () => {
    mountBar('/notes/roof-repair?tab=recordings');
    const record = screen.getByRole('button', { name: 'Record into this note' });
    expect(screen.getByText('Into this note')).toBeInTheDocument();
    await userEvent.setup({ advanceTimers: vi.advanceTimersByTime }).click(record);
    expect(where()).toBe('/capture?note=roof-repair');
  });

  it('records into a new note from an archived one, which the server would refuse', async () => {
    const fence: NoteDetailWire = {
      ...TEST_NOTES[0]!,
      id: 'old-fence',
      title: 'Old fence',
      archived: true,
      body: '',
      captures: [],
    };
    mountBar('/notes/old-fence', fence);
    const record = screen.getByRole('button', { name: 'Record' });
    expect(screen.queryByText('Into this note')).toBeNull();
    await userEvent.setup({ advanceTimers: vi.advanceTimersByTime }).click(record);
    expect(where()).toMatch(/^\/capture$/);
  });
});

/** Push-to-talk drawn on the bar (R8, F5): the slots swap in place. */
describe('holding the record button', () => {
  async function press(): Promise<HTMLElement> {
    const disc = screen.getByRole('button', { name: /^Record/ });
    fireEvent.pointerDown(disc, { ...touch, clientX: 200, clientY: 800 });
    await wait(HOLD_ARM_MS + 50);
    expect(captureState()).toBe('recording');
    return disc;
  }

  it('swaps the tab slots for the hold, keeping the tabs in place but hidden and inert', async () => {
    mountBar('/');
    await press();
    expect(nav()).toHaveAttribute('data-hold', 'holding');
    expect(screen.getByText('‹ Slide to cancel')).toBeInTheDocument();
    // The tabs keep their boxes so the bar does not change height; they are
    // simply out of reach while the thumb is on the disc.
    const tabs = nav().querySelectorAll('a.tab-bar__tab');
    expect(tabs).toHaveLength(2);
    for (const tab of tabs) expect(tab).toHaveAttribute('inert');
    expect(pill()).toHaveTextContent(/^00:0\d$/);
    expect(spoken()).toHaveTextContent('Recording');
  });

  it('locks on a slide up by opening the capture screen on the same live take, into the note', async () => {
    mountBar('/notes/roof-repair');
    const disc = await press();
    const { localId } = useCaptureStore.getState().model;
    const recorder = fake.recorder;
    fireEvent.pointerMove(disc, { ...touch, clientX: 200, clientY: 800 - LOCK_DY_PX - 8 });
    expect(where()).toBe('/capture?note=roof-repair');
    fireEvent.pointerUp(disc, touch);
    // The same take, still recording: no second recorder, nothing discarded or sent.
    await wait(MIN_TALK_MS);
    expect(captureState()).toBe('recording');
    expect(useCaptureStore.getState().model.localId).toBe(localId);
    expect(useCaptureStore.getState().model.noteId).toBe('roof-repair');
    expect(fake.recorder).toBe(recorder);
    expect(fake.creates).toHaveLength(0);
  });

  it('keeps a held take when Escape belongs to something else: a field, or an open dialog or menu', async () => {
    mountBar('/');
    await press();
    // Escape closing the note's Find field.
    const find = document.createElement('input');
    find.type = 'search';
    document.body.append(find);
    fireEvent.keyDown(find, { key: 'Escape' });
    find.remove();
    // Escape closing a dialog, and a ⋮ menu.
    for (const role of ['dialog', 'menu']) {
      const open = document.createElement('div');
      open.setAttribute('role', role);
      document.body.append(open);
      fireEvent.keyDown(document.body, { key: 'Escape' });
      open.remove();
    }
    // Escape that a handler already took.
    const taken = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
    taken.preventDefault();
    document.body.dispatchEvent(taken);
    await wait(50);
    expect(captureState()).toBe('recording');
    expect(nav()).toHaveAttribute('data-hold', 'holding');
    // Escape that is the page's own still cancels.
    fireEvent.keyDown(document.body, { key: 'Escape' });
    await waitFor(() => {
      expect(captureState()).toBe('idle');
    });
  });

  it('labels the clock with the recording\'s own target, not the route\'s', async () => {
    mountBar('/notes/roof-repair');
    await press();
    expect(pill()).toHaveTextContent('Into this note');
    // Retargeted to a new note (as from the capture screen's chooser): the
    // route is still the note, the pill follows the recording.
    act(() => {
      useCaptureStore.getState().setTarget(null);
    });
    expect(pill()).not.toHaveTextContent('Into this note');
  });

  it('does not record from R while a dialog or menu is open', async () => {
    mountBar('/');
    const dialog = document.createElement('div');
    dialog.setAttribute('role', 'dialog');
    document.body.append(dialog);
    fireEvent.keyDown(document.body, { key: 'r' });
    await wait(HOLD_ARM_MS + 50);
    expect(captureState()).toBe('idle');
    fireEvent.keyUp(document.body, { key: 'r' });
    dialog.remove();
  });

  it('holds R from the page, but not from a field or with a modifier', async () => {
    mountBar('/');
    const field = document.createElement('input');
    document.body.append(field);
    fireEvent.keyDown(field, { key: 'r' });
    fireEvent.keyDown(document.body, { key: 'r', ctrlKey: true });
    fireEvent.keyDown(document.body, { key: 'R', shiftKey: true });
    await wait(HOLD_ARM_MS + 50);
    expect(captureState()).toBe('idle');
    fireEvent.keyUp(field, { key: 'r' });
    fireEvent.keyUp(document.body, { key: 'r' });
    field.remove();

    // A quick R does nothing at all: no recording, no screen.
    fireEvent.keyDown(document.body, { key: 'r' });
    await wait(50);
    fireEvent.keyUp(document.body, { key: 'r' });
    await wait(HOLD_ARM_MS);
    expect(captureState()).toBe('idle');
    expect(where()).toBe('/');

    fireEvent.keyDown(document.body, { key: 'r' });
    await wait(HOLD_ARM_MS + 50);
    expect(captureState()).toBe('recording');
    await wait(MIN_TALK_MS + 100);
    speak();
    fireEvent.keyUp(document.body, { key: 'r' });
    await waitFor(() => {
      expect(captureState()).toBe('uploaded');
    });
  });

  it('coaches the gesture on Home until a hold sends, and never on a note', async () => {
    const first = mountBar('/');
    expect(pill()).toHaveTextContent('Hold to talk · tap to record');
    expect(pill()).toHaveAttribute('aria-hidden', 'true');
    first.unmount();
    mountBar('/notes/roof-repair').unmount();

    mountBar('/');
    const disc = await press();
    await wait(MIN_TALK_MS + 100);
    speak();
    fireEvent.pointerUp(disc, touch);
    await waitFor(() => {
      expect(window.localStorage.getItem('chintan.coach.ptt')).toBe('done');
    });
    act(() => {
      vi.advanceTimersByTime(2_000);
    });
    expect(pill()).toBeNull();
  });
});
