import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { DownloadButton } from './DownloadButton.tsx';

/**
 * Not a plain `<a href download>` — see the component's own doc comment for
 * why a cross-origin presigned URL makes that attribute a no-op. These tests
 * exercise the actual mechanism: fetch a blob, hand the browser a same-origin
 * `blob:` URL, and click a detached anchor rather than one left in the DOM.
 */

function spyOnAnchorClick() {
  const clicks: { href: string; download: string }[] = [];
  const original = HTMLAnchorElement.prototype.click;
  HTMLAnchorElement.prototype.click = function (this: HTMLAnchorElement) {
    clicks.push({ href: this.href, download: this.download });
  };
  return { clicks, restore: () => (HTMLAnchorElement.prototype.click = original) };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('DownloadButton', () => {
  it('saves the blob under the given filename', async () => {
    const user = userEvent.setup();
    URL.createObjectURL = vi.fn(() => 'blob:mock-url');
    URL.revokeObjectURL = vi.fn();
    const { clicks, restore } = spyOnAnchorClick();

    render(
      <DownloadButton
        label="Download note"
        filename={() => 'roof-repair.md'}
        blob={() => Promise.resolve(new Blob(['# Roof repair'], { type: 'text/markdown' }))}
      />,
    );
    await user.click(screen.getByRole('button', { name: 'Download note' }));

    expect(await screen.findByRole('status')).toHaveTextContent('Downloaded');
    expect(clicks).toEqual([{ href: 'blob:mock-url', download: 'roof-repair.md' }]);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:mock-url');
    restore();
  });

  it('reads the filename and blob at the moment of the click, not at render', async () => {
    // The note is still being edited while this button sits on screen — the
    // same reason CopyButton's `text` prop is a thunk rather than a value.
    const user = userEvent.setup();
    URL.createObjectURL = vi.fn(() => 'blob:mock-url');
    URL.revokeObjectURL = vi.fn();
    const { clicks, restore } = spyOnAnchorClick();

    let title = 'first';
    render(
      <DownloadButton
        label="Download note"
        filename={() => `${title}.md`}
        blob={() => Promise.resolve(new Blob([title]))}
      />,
    );
    title = 'second';
    await user.click(screen.getByRole('button', { name: 'Download note' }));

    await screen.findByRole('status');
    expect(clicks).toEqual([{ href: 'blob:mock-url', download: 'second.md' }]);
    restore();
  });

  it('says the download failed rather than doing nothing', async () => {
    const user = userEvent.setup();
    render(
      <DownloadButton
        label="Download audio"
        filename={() => 'audio.webm'}
        blob={() => Promise.reject(new Error('network error'))}
      />,
    );
    await user.click(screen.getByRole('button', { name: 'Download audio' }));

    expect(await screen.findByRole('status')).toHaveTextContent(/could not download/i);
  });

  it('says nothing until it has been used', () => {
    render(
      <DownloadButton
        label="Download note"
        filename={() => 'x.md'}
        blob={() => Promise.resolve(new Blob(['x']))}
      />,
    );
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('disables the button while the download is in flight', async () => {
    const user = userEvent.setup();
    let resolve: (blob: Blob) => void = () => {};
    const pending = new Promise<Blob>((res) => {
      resolve = res;
    });
    URL.createObjectURL = vi.fn(() => 'blob:mock-url');
    URL.revokeObjectURL = vi.fn();
    const { restore } = spyOnAnchorClick();

    render(
      <DownloadButton label="Download audio" filename={() => 'audio.webm'} blob={() => pending} />,
    );
    await user.click(screen.getByRole('button', { name: /download audio/i }));

    expect(screen.getByRole('button', { name: /downloading/i })).toBeDisabled();
    resolve(new Blob(['x']));
    await screen.findByRole('status');
    restore();
  });
});
