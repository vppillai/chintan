import { render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { TranscriptPanel } from './TranscriptPanel.tsx';
import type { TranscriptSegment } from './artifacts.ts';

const SEGMENTS: TranscriptSegment[] = [
  { id: 0, start: 0, end: 4, text: 'Ridge tiles on the south slope have slipped.' },
  { id: 1, start: 4, end: 8, text: 'Ellis quoted nine hundred.' },
];

function mount(props: Partial<React.ComponentProps<typeof TranscriptPanel>> = {}) {
  return render(
    <TranscriptPanel
      segments={SEGMENTS}
      cleanedText=""
      view="raw"
      onViewChange={vi.fn()}
      currentTime={0}
      onSeek={vi.fn()}
      hasSegments
      {...props}
    />,
  );
}

beforeEach(() => {
  // jsdom implements no scrolling, and the panel follows playback.
  Element.prototype.scrollIntoView = vi.fn();
});

describe('the Cleaned view is only offered when there is cleaned text', () => {
  it('hides the toggle when the capture has no cleaned artifact', () => {
    /*
     * `cleanedText` was hard-coded to `''` at the only call site, so the toggle
     * appeared on every capture with segments and always answered "No cleaned
     * text for this capture." — including captures the pipeline had cleaned
     * perfectly well. The user's reasonable conclusion was that cleanup had
     * failed or their text had been lost.
     */
    mount({ cleanedText: '' });
    expect(screen.queryByRole('button', { name: 'Cleaned' })).toBeNull();
    expect(screen.queryByText(/no cleaned text for this capture/i)).toBeNull();
  });

  it('offers it, and renders the text, once the artifact is there', () => {
    mount({
      cleanedText: 'Ridge tiles have slipped.\n\nEllis quoted nine hundred pounds.',
      view: 'cleaned',
    });

    expect(screen.getByRole('button', { name: 'Cleaned' })).toBeInTheDocument();
    expect(screen.getByText('Ellis quoted nine hundred pounds.')).toBeInTheDocument();
    // Cleanup rewrites the wording, so there is nothing to seek to.
    expect(screen.queryByRole('button', { name: /Ellis quoted nine hundred\./ })).toBeNull();
    expect(screen.getByText(/no reliable timestamps/i)).toBeInTheDocument();
  });

  it('falls back to the timestamped view rather than showing an empty panel', () => {
    // A stale `view: 'cleaned'` carried over from another capture must not
    // produce the empty tab the toggle no longer offers.
    mount({ cleanedText: '', view: 'cleaned' });
    expect(screen.getByText(/tap any line to jump/i)).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: /Ridge tiles on the south slope have slipped\./ }),
    ).toBeInTheDocument();
  });
});
