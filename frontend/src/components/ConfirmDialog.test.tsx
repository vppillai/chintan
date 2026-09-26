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

    // A tap, as a browser delivers one: down, up, then the click.
    fireEvent.pointerDown(button, { button: 0 });
    fireEvent.pointerUp(button);
    fireEvent.click(button, { detail: 1 });
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

  it('confirms at once from the keyboard and from a click no pointer made — the hold gates pointers only', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    mountHold(onConfirm);
    const button = screen.getByRole('button', { name: 'Hold to delete 12 notes' });

    // Focus lands on Cancel; reaching the confirm takes a deliberate Tab.
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus();
    await user.tab();
    expect(button).toHaveFocus();
    await user.keyboard('{Enter}');
    expect(onConfirm).toHaveBeenCalledTimes(1);
    await user.keyboard(' ');
    expect(onConfirm).toHaveBeenCalledTimes(2);

    // What a screen reader or a switch sends: a click with no pointer before it.
    fireEvent.click(button);
    expect(onConfirm).toHaveBeenCalledTimes(3);
  });

  it('a press that wanders off the button and back, then lets go, is still a tap and confirms nothing', () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const onConfirm = vi.fn();
    mountHold(onConfirm);
    const button = screen.getByRole('button', { name: 'Hold to delete 12 notes' });

    fireEvent.pointerDown(button, { button: 0 });
    fireEvent.pointerLeave(button);
    expect(button).not.toHaveAttribute('data-holding');
    fireEvent.pointerUp(button);
    fireEvent.click(button, { detail: 1 });
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(onConfirm).not.toHaveBeenCalled();
  });
});
