import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ShareButton, canShare } from './ShareButton.tsx';

function withShare(share: ((data: ShareData) => Promise<void>) | undefined) {
  Object.defineProperty(navigator, 'share', { configurable: true, value: share });
}

afterEach(() => {
  withShare(undefined);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
});

describe('ShareButton', () => {
  it('is offered only where the browser has a share sheet', () => {
    expect(canShare()).toBe(false);
    withShare(async () => {});
    expect(canShare()).toBe(true);
  });

  it('hands the title and the plain text to the share sheet', async () => {
    const user = userEvent.setup();
    const share = vi.fn(async () => {});
    withShare(share);
    render(<ShareButton title={() => 'Shopping'} text={() => '☐ Milk'} html={() => '<ul><li>Milk</li></ul>'} />);
    await user.click(screen.getByRole('button', { name: 'Share…' }));
    expect(share).toHaveBeenCalledWith({ title: 'Shopping', text: '☐ Milk' });
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('says nothing when the sheet is dismissed, and copies instead when it will not open', async () => {
    // Before the clipboard is stubbed: `userEvent.setup()` installs its own.
    const user = userEvent.setup();
    withShare(async () => {
      throw new DOMException('cancelled', 'AbortError');
    });
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    const { rerender } = render(<ShareButton title={() => 'Roof'} text={() => 'Roof\n\nTiles'} />);
    await user.click(screen.getByRole('button', { name: 'Share…' }));
    expect(writeText).not.toHaveBeenCalled();
    expect(screen.queryByRole('status')).toBeNull();

    withShare(async () => {
      throw new DOMException('no sheet here', 'NotAllowedError');
    });
    rerender(<ShareButton title={() => 'Roof'} text={() => 'Roof\n\nTiles'} />);
    await user.click(screen.getByRole('button', { name: 'Share…' }));
    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith('Roof\n\nTiles');
    });
    expect(screen.getByRole('status')).toHaveTextContent(/copied instead/);
  });
});
