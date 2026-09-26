import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { TRANSCRIPT_HINT_KEY, TranscriptPanel } from './TranscriptPanel.tsx';
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
  localStorage.clear();
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

describe('a capture with no segments does not contradict itself', () => {
  it('does not tell the reader to tap a line when there is nothing to tap', () => {
    // Previously the header note always said "Tap any line to jump there" in
    // raw view, regardless of whether any lines existed — directly above the
    // empty state's own "there is nothing to jump to." Same screen, opposite
    // instructions.
    mount({ segments: [], hasSegments: false });
    expect(screen.queryByText(/tap any line to jump/i)).toBeNull();
    expect(screen.getByText(/there is nothing to jump\s+to/i)).toBeInTheDocument();
  });

  it('does not claim the recording predates timestamps', () => {
    // Every capture the app ever made had timestamps; the parser was dropping
    // them. The empty state now states what is missing, not why it thinks so.
    mount({ segments: [], hasSegments: false });
    expect(screen.queryByText(/before timestamps were captured/i)).toBeNull();
    expect(screen.getByText(/no timestamps are available for this recording/i)).toBeInTheDocument();
  });
});

describe('words sent as words', () => {
  it('shows the text with no sentence about timestamps', () => {
    // A text-only device row is forced into the cleaned view, whose caption
    // explained cleanup losing the timestamps — words posted as words never
    // had any (review 2026-09-24, R4-24).
    mount({
      segments: [],
      hasSegments: false,
      cleanedText: 'Buy milk on the way home.',
      view: 'cleaned',
      textOnly: true,
    });
    expect(screen.getByText('Buy milk on the way home.')).toBeInTheDocument();
    expect(screen.queryByText(/no reliable timestamps/i)).toBeNull();
    expect(screen.queryByText(/tap any line/i)).toBeNull();
  });
});

describe('the panel is the transcript and its switch, nothing else', () => {
  it('has no copy control of its own; that is the row menu’s (T41)', () => {
    mount({ cleanedText: 'Cleaned.' });
    expect(screen.queryByRole('button', { name: /copy/i })).toBeNull();
    expect(screen.getByRole('group', { name: 'Transcript view' })).toBeInTheDocument();
  });

  it('shows "tap any line" until a line is tapped on this device, then not again', async () => {
    const user = userEvent.setup();
    const onSeek = vi.fn();
    const view = mount({ onSeek });
    expect(screen.getByText(/tap any line to jump/i)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /Ellis quoted nine hundred\./ }));
    expect(onSeek).toHaveBeenCalledWith(4);
    expect(screen.queryByText(/tap any line to jump/i)).toBeNull();
    expect(localStorage.getItem(TRANSCRIPT_HINT_KEY)).toBe('1');

    // A later recording, a later day: the sentence has done its job.
    view.unmount();
    mount();
    expect(screen.queryByText(/tap any line to jump/i)).toBeNull();
    // The cleaned view's reason for having no timestamps is not a tutorial.
    view.unmount();
    mount({ cleanedText: 'Cleaned.', view: 'cleaned' });
    expect(screen.getByText(/no reliable timestamps/i)).toBeInTheDocument();
  });
});
