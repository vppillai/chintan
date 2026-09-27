import { act, fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { INITIAL_CAPTURE } from '@/features/capture/machine.ts';
import { useCaptureStore } from '@/features/capture/store.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { PATHS } from './Icon.tsx';
import { RecordButton } from './RecordButton.tsx';

function Where() {
  const { pathname, search } = useLocation();
  return <output>{pathname + search}</output>;
}

function mount(noteId: string | null = null) {
  return render(
    <TestProviders api={testApiContext(undefined)}>
      <MemoryRouter initialEntries={['/']}>
        <RecordButton noteId={noteId} />
        <Routes>
          <Route path="*" element={<Where />} />
        </Routes>
      </MemoryRouter>
    </TestProviders>,
  );
}

const where = () => document.querySelector('output')?.textContent;

afterEach(() => {
  vi.useRealTimers();
  useCaptureStore.setState({ model: INITIAL_CAPTURE });
});

/**
 * A plain Record control: a tap opens the recorder, and nothing else happens
 * on it. It held to talk from #85 to owner feedback 2026-09-27 ("I want it
 * only on the widget, not the main app"); the hold is `/talk`'s alone now.
 */
describe('the record button', () => {
  it('is named Record, wears the microphone, and opens the capture screen on a tap', async () => {
    mount();
    const record = screen.getByRole('button', { name: 'Record' });
    expect(record.querySelector('svg path')).toHaveAttribute('d', PATHS.mic);
    expect(record).toHaveTextContent('Record');
    await userEvent.click(record);
    expect(where()).toBe('/capture');
  });

  it('opens the recorder into the open note, and is named for it', async () => {
    mount('roof-repair');
    await userEvent.click(screen.getByRole('button', { name: 'Record into this note' }));
    expect(where()).toBe('/capture?note=roof-repair');
  });

  it('does not hold to talk: a long press never touches the microphone, and is still a tap', () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mount();
    const record = screen.getByRole('button', { name: 'Record' });
    fireEvent.pointerDown(record, { pointerId: 1, pointerType: 'touch', button: 0 });
    act(() => {
      vi.advanceTimersByTime(2_000);
    });
    // Nothing above the bar, nothing recording, and the name did not change.
    expect(useCaptureStore.getState().model.state).toBe('idle');
    expect(document.querySelector('[role="status"]')).toBeNull();
    expect(screen.getByRole('button', { name: 'Record' })).not.toHaveAttribute('data-holding');
    fireEvent.pointerUp(record, { pointerId: 1, pointerType: 'touch' });
    fireEvent.click(record);
    expect(where()).toBe('/capture');
  });
});
