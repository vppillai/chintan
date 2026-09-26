import { act, fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ConfirmDialog } from './ConfirmDialog.tsx';

/**
 * The confirm on a destructive action. Plain by default — a sentence, Cancel
 * under the Enter key, one button that does it — and, for the bulk purge of
 * many notes, a button that has to be held rather than tapped. There is no
 * typed word anywhere any more (owner, 2026-09-26).
 */

function mountHold(onConfirm: () => void, ms = 1000) {
  return render(
    <ConfirmDialog
      open
      title="Delete 12 notes forever?"
      body="Everything goes."
      confirmLabel="Hold to delete 12 notes"
      holdMs={ms}
      destructive
      onConfirm={onConfirm}
      onCancel={vi.fn()}
    />,
  );
}

afterEach(() => {
  vi.useRealTimers();
});

describe('ConfirmDialog', () => {
  it('is plain: no text field, focus on Cancel, and the confirm control fires at once', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();

    render(
      <ConfirmDialog
        open
        title="Delete this note forever?"
        body="Everything goes."
        confirmLabel="Delete forever"
        destructive
        onConfirm={onConfirm}
        onCancel={vi.fn()}
      />,
    );

    expect(screen.queryByRole('textbox')).toBeNull();
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Delete forever' }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it('with holdMs, a tap does nothing and a press held for the duration confirms', () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const onConfirm = vi.fn();
    mountHold(onConfirm);
    const button = screen.getByRole('button', { name: 'Hold to delete 12 notes' });

    fireEvent.click(button);
    expect(onConfirm).not.toHaveBeenCalled();

    fireEvent.pointerDown(button, { button: 0 });
    expect(button).toHaveAttribute('data-holding');
    act(() => {
      vi.advanceTimersByTime(999);
    });
    expect(onConfirm).not.toHaveBeenCalled();
    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it('letting go early confirms nothing, and the fill lets go with it', () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const onConfirm = vi.fn();
    mountHold(onConfirm);
    const button = screen.getByRole('button', { name: 'Hold to delete 12 notes' });

    fireEvent.pointerDown(button, { button: 0 });
    act(() => {
      vi.advanceTimersByTime(500);
    });
    fireEvent.pointerUp(button);
    expect(button).not.toHaveAttribute('data-holding');
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('is held from the keyboard too: Space down for the duration, not a tap of it', () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const onConfirm = vi.fn();
    mountHold(onConfirm);
    const button = screen.getByRole('button', { name: 'Hold to delete 12 notes' });

    fireEvent.keyDown(button, { key: ' ' });
    fireEvent.keyUp(button, { key: ' ' });
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(onConfirm).not.toHaveBeenCalled();

    fireEvent.keyDown(button, { key: 'Enter' });
    // The browser repeats a held key; a repeat must not restart the hold.
    fireEvent.keyDown(button, { key: 'Enter', repeat: true });
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });
});
