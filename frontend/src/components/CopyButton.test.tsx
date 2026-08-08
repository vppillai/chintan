import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { CopyButton } from './CopyButton.tsx';

/**
 * The owner asked for a one-tap copy. The failure modes matter as much as the
 * happy path: a copy with no confirmation cannot be told from one that failed,
 * and `writeText` genuinely rejects — outside a secure context, without a user
 * gesture, and sooner on Safari than anywhere else.
 */

function withClipboard(writeText: (text: string) => Promise<void>) {
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  });
}

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

  it('says it copied', async () => {
    const user = userEvent.setup();
    withClipboard(async () => {});

    render(<CopyButton label="Copy note" text={() => 'x'} />);
    await user.click(screen.getByRole('button', { name: 'Copy note' }));

    expect(await screen.findByRole('status')).toHaveTextContent('Copied');
  });

  it('says what to do instead when the browser refuses', async () => {
    const user = userEvent.setup();
    withClipboard(async () => {
      throw new DOMException('Denied', 'NotAllowedError');
    });

    render(<CopyButton label="Copy note" text={() => 'x'} />);
    await user.click(screen.getByRole('button', { name: 'Copy note' }));

    const result = await screen.findByRole('status');
    expect(result).toHaveTextContent(/could not copy/i);
    // Actionable, not a dead end.
    expect(result).toHaveTextContent(/copy it by hand/i);
  });

  it('handles the clipboard API being absent, not merely rejecting', async () => {
    // An insecure origin has no `navigator.clipboard` at all, so a naive
    // `await navigator.clipboard.writeText(...)` throws a TypeError rather
    // than rejecting, and a catch on the promise alone would miss it.
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });

    render(<CopyButton label="Copy note" text={() => 'x'} />);
    await user.click(screen.getByRole('button', { name: 'Copy note' }));

    expect(await screen.findByRole('status')).toHaveTextContent(/could not copy/i);
  });

  it('says nothing until it has been used', () => {
    withClipboard(async () => {});
    render(<CopyButton label="Copy note" text={() => 'x'} />);

    expect(screen.queryByRole('status')).toBeNull();
  });
});
