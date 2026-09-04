import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { CopyButton } from './CopyButton.tsx';

/**
 * The owner asked for a one-tap copy. The failure modes matter as much as the
 * happy path: a copy with no confirmation cannot be told from one that failed,
 * and `writeText` genuinely rejects — outside a secure context, without a user
 * gesture, and sooner on Safari than anywhere else. When it does, the older
 * selection-copy route is tried before giving up.
 */

function withClipboard(writeText: ((text: string) => Promise<void>) | undefined) {
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: writeText ? { writeText } : undefined,
  });
}

/** jsdom has no `execCommand`; each test says what the document would do. */
function withExecCommand(result: boolean | undefined) {
  Object.defineProperty(document, 'execCommand', {
    configurable: true,
    value: result === undefined ? undefined : vi.fn(() => result),
  });
  return document.execCommand as unknown as ReturnType<typeof vi.fn> | undefined;
}

afterEach(() => {
  withExecCommand(undefined);
});

describe('CopyButton', () => {
  it('puts the text on the clipboard', async () => {
    const user = userEvent.setup();
    const writeText = vi.fn(async () => {});
    withClipboard(writeText);

    render(<CopyButton label="Copy note" text={() => 'Roof repair\n\nRidge tiles.'} />);
    await user.click(screen.getByRole('button', { name: 'Copy note' }));

    expect(writeText).toHaveBeenCalledWith('Roof repair\n\nRidge tiles.');
  });

  it('reads the text at the moment it is clicked, not at render', async () => {
    // The note is still being edited while this button sits on screen.
    const user = userEvent.setup();
    const writeText = vi.fn(async () => {});
    withClipboard(writeText);

    let body = 'first';
    render(<CopyButton label="Copy note" text={() => body} />);
    body = 'second';
    await user.click(screen.getByRole('button', { name: 'Copy note' }));

    expect(writeText).toHaveBeenCalledWith('second');
  });

  it('says it copied, on the button, and then goes back to its name', async () => {
    const user = userEvent.setup();
    withClipboard(async () => {});

    render(<CopyButton label="Copy note" text={() => 'x'} />);
    await user.click(screen.getByRole('button', { name: 'Copy note' }));

    // Where the thumb already is — and announced once for a screen reader.
    expect(await screen.findByRole('button', { name: 'Copied' })).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent('Copied');

    // Real time, not faked: the settle timer is the behaviour under test.
    await waitFor(
      () => {
        expect(screen.getByRole('button', { name: 'Copy note' })).toBeInTheDocument();
      },
      { timeout: 4_000 },
    );
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('falls back to a selection copy when the clipboard API is absent', async () => {
    // An insecure origin — and older WebKit — has no `navigator.clipboard` at
    // all. The selection route is what `execCommand('copy')` was for, and it
    // runs synchronously inside the tap so the gesture still counts.
    const user = userEvent.setup();
    withClipboard(undefined);
    const execCommand = withExecCommand(true);

    render(<CopyButton label="Copy note" text={() => 'from the fallback'} />);
    await user.click(screen.getByRole('button', { name: 'Copy note' }));

    expect(execCommand).toHaveBeenCalledWith('copy');
    expect(await screen.findByRole('button', { name: 'Copied' })).toBeInTheDocument();
    // The scratch textarea is gone, and focus is back on the control.
    expect(document.querySelector('.copy__scratch')).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Copied' }));
  });

  it('falls back to a selection copy when the clipboard API refuses', async () => {
    // Safari rejects `writeText` outside the exact gesture it was called from.
    const user = userEvent.setup();
    withClipboard(async () => {
      throw new DOMException('Denied', 'NotAllowedError');
    });
    const execCommand = withExecCommand(true);

    render(<CopyButton label="Copy note" text={() => 'x'} />);
    await user.click(screen.getByRole('button', { name: 'Copy note' }));

    expect(await screen.findByRole('button', { name: 'Copied' })).toBeInTheDocument();
    expect(execCommand).toHaveBeenCalledWith('copy');
  });

  it('says what to do instead when both routes fail', async () => {
    const user = userEvent.setup();
    withClipboard(async () => {
      throw new DOMException('Denied', 'NotAllowedError');
    });
    withExecCommand(false);

    render(<CopyButton label="Copy note" text={() => 'x'} />);
    await user.click(screen.getByRole('button', { name: 'Copy note' }));

    const result = await screen.findByRole('status');
    expect(result).toHaveTextContent(/could not copy/i);
    // Actionable, not a dead end.
    expect(result).toHaveTextContent(/copy it by hand/i);
    expect(screen.getByRole('button', { name: 'Copy note' })).toBeInTheDocument();
  });

  it('handles the clipboard API being absent and no selection copy either', async () => {
    // A naive `await navigator.clipboard.writeText(...)` throws a TypeError
    // rather than rejecting, and a catch on the promise alone would miss it.
    const user = userEvent.setup();
    withClipboard(undefined);
    withExecCommand(undefined);

    render(<CopyButton label="Copy note" text={() => 'x'} />);
    await user.click(screen.getByRole('button', { name: 'Copy note' }));

    expect(await screen.findByRole('status')).toHaveTextContent(/could not copy/i);
  });

  it('drops a stale "Copied" when it is renamed to copy something else', async () => {
    // The transcript toggle renames the same control from raw to cleaned. A
    // "Copied" carried across would claim the cleaned text had been copied.
    const user = userEvent.setup();
    withClipboard(async () => {});

    const { rerender } = render(<CopyButton label="Copy this transcript" text={() => 'raw'} />);
    await user.click(screen.getByRole('button', { name: 'Copy this transcript' }));
    expect(await screen.findByRole('button', { name: 'Copied' })).toBeInTheDocument();

    rerender(<CopyButton label="Copy this cleaned text" text={() => 'clean'} />);
    expect(screen.getByRole('button', { name: 'Copy this cleaned text' })).toBeInTheDocument();
  });

  it('says nothing until it has been used', () => {
    withClipboard(async () => {});
    render(<CopyButton label="Copy note" text={() => 'x'} />);

    expect(screen.queryByRole('status')).toBeNull();
  });
});
