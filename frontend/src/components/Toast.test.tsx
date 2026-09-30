import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { TOAST_MS, Toast, dismissToast, showToast } from './Toast.tsx';

/**
 * The shell's one transient notice: a polite live region that is always
 * there, its action a real button, and gone on its own after `TOAST_MS`.
 */

afterEach(() => {
  dismissToast();
  vi.useRealTimers();
});

describe('Toast', () => {
  it('is a polite live region that is mounted before it has anything to say', () => {
    render(<Toast />);
    const region = screen.getByRole('status');
    expect(region).toHaveAttribute('aria-live', 'polite');
    expect(region).toBeEmptyDOMElement();
  });

  it('a weak notice never takes the place of a standing Undo, and gives way to any notice', () => {
    render(<Toast />);
    act(() => {
      showToast({ message: '2 done items deleted', action: { label: 'Undo', onSelect: () => undefined } });
      showToast({ message: 'Milk done', weak: true, action: { label: 'Undo', onSelect: () => undefined } });
    });
    expect(screen.getByRole('status')).toHaveTextContent('2 done items deleted');

    act(() => {
      dismissToast();
      showToast({ message: 'Milk done', weak: true, action: { label: 'Undo', onSelect: () => undefined } });
      showToast({ message: 'Eggs done', weak: true, action: { label: 'Undo', onSelect: () => undefined } });
    });
    expect(screen.getByRole('status')).toHaveTextContent('Eggs done');
    act(() => {
      showToast({ message: 'Deleted', action: { label: 'Undo', onSelect: () => undefined } });
    });
    expect(screen.getByRole('status')).toHaveTextContent('Deleted');
  });

  it('shows the notice with its action, which fires once and dismisses it', async () => {
    const user = userEvent.setup();
    const undo = vi.fn();
    render(<Toast />);

    act(() => {
      showToast({ message: 'Deleted · kept in Archive for 30 days', action: { label: 'Undo', onSelect: undo } });
    });
    expect(screen.getByRole('status')).toHaveTextContent('Deleted · kept in Archive for 30 days');

    await user.click(screen.getByRole('button', { name: 'Undo' }));
    expect(undo).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('button', { name: 'Undo' })).toBeNull();
  });

  it('pauses while Undo has focus, and a later notice starts its own clock after Undo closed one', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<Toast />);

    act(() => {
      showToast({ message: 'Deleted', action: { label: 'Undo', onSelect: () => undefined } });
    });
    const undo = screen.getByRole('button', { name: 'Undo' });
    act(() => {
      undo.focus();
    });
    act(() => {
      vi.advanceTimersByTime(TOAST_MS + 500);
    });
    expect(screen.getByRole('status')).toHaveTextContent('Deleted');

    // Undo unmounts the card mid-click: no blur fires, so the attention it
    // recorded must not carry over to the next notice.
    await user.click(undo);
    expect(screen.getByRole('status')).toBeEmptyDOMElement();

    act(() => {
      showToast({ message: '2 notes deleted' });
    });
    act(() => {
      vi.advanceTimersByTime(TOAST_MS + 500);
    });
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });

  it('clears itself after TOAST_MS', () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(<Toast />);

    act(() => {
      showToast({ message: 'Deleted' });
    });
    act(() => {
      vi.advanceTimersByTime(TOAST_MS - 1);
    });
    expect(screen.getByRole('status')).toHaveTextContent('Deleted');
    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });
});
