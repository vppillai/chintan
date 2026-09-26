import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';

import { capture, mount } from '@/test/filing.tsx';

describe('a terminal capture is something the user can act on', () => {
  it('lets the user answer "which note should this go in?"', async () => {
    /*
     * The card asked the question, marked every pipeline stage complete, and
     * rendered zero buttons. `useSetCaptureTarget` wrapped the contract's target
     * endpoint and was called from nowhere in the app, and the schema lists
     * `needs_target` as terminal pending user action — so the capture, and the
     * thought in it, was stuck permanently.
     */
    const user = userEvent.setup();
    const { calls } = mount([capture({ id: 'srv-7', status: 'needs_target' })]);

    await screen.findByText(/which note should this go in/i);
    await user.click(screen.getByRole('button', { name: /choose a note/i }));

    await user.click(await screen.findByRole('button', { name: 'Roof repair' }));

    await waitFor(() => {
      expect(
        calls.some(
          (call) => call.method === 'POST' && call.url.endsWith('/v1/captures/srv-7/target'),
        ),
      ).toBe(true);
    });
  });

  it('can file the recording into a brand new note', async () => {
    const user = userEvent.setup();
    const { calls, fetchImpl } = mount([capture({ id: 'srv-8', status: 'needs_target' })]);

    await user.click(await screen.findByRole('button', { name: /choose a note/i }));
    await user.type(await screen.findByLabelText(/new note title/i), 'Loft insulation');
    await user.click(screen.getByRole('button', { name: 'Create' }));

    await waitFor(() => {
      expect(
        calls.some(
          (call) => call.method === 'POST' && call.url.endsWith('/v1/captures/srv-8/target'),
        ),
      ).toBe(true);
    });

    const target = fetchImpl.mock.calls.find(
      ([input]) => String(input).endsWith('/v1/captures/srv-8/target'),
    );
    expect(JSON.parse(String(target?.[1]?.body))).toEqual({ new_note_title: 'Loft insulation' });
  });

  it('does not mark every stage complete for a capture that stopped', async () => {
    // Four filled segments over "Which note should this go in?" says the
    // pipeline finished. It did not — it is waiting for the user.
    mount([capture({ status: 'needs_target' })]);
    await screen.findByText(/which note should this go in/i);
    expect(screen.queryByRole('list', { name: /filing progress/i })).toBeNull();
  });

  it('lets an unactionable capture be dismissed', async () => {
    // `no_content` has no retry, no target, and nothing to open, so without a
    // dismiss the row sat at the top of the library indefinitely.
    const user = userEvent.setup();
    mount([capture({ id: 'srv-quiet', status: 'no_content' })]);

    await screen.findByText(/nothing to save from that recording/i);
    await user.click(screen.getByRole('button', { name: 'Dismiss' }));

    await waitFor(() => {
      expect(screen.queryByText(/nothing to save from that recording/i)).toBeNull();
    });
  });
});

/**
 * The routing suggestion the pipeline pays an LLM call for.
 *
 * `SuggestedNoteID` and `SuggestedTitle` are computed, stored and returned by
 * the API, so the "where should this go?" prompt must lead with what the router
 * thought rather than an unranked list of every note the user has.
 */
describe('the row says where it thinks the recording goes', () => {
  it('leads with the note the router proposed', async () => {
    const user = userEvent.setup();
    const { calls, fetchImpl } = mount([
      capture({ id: 'srv-9', status: 'needs_target', suggested_note_id: 'roof-repair' }),
    ]);

    const add = await screen.findByRole('button', { name: /add to .*roof repair/i });

    // The unranked list is not the first thing on screen any more.
    expect(screen.queryByRole('button', { name: 'Roof repair' })).toBeNull();

    await user.click(add);

    await waitFor(() => {
      expect(
        calls.some(
          (call) => call.method === 'POST' && call.url.endsWith('/v1/captures/srv-9/target'),
        ),
      ).toBe(true);
    });

    const target = fetchImpl.mock.calls.find(([input]) =>
      String(input).endsWith('/v1/captures/srv-9/target'),
    );
    expect(JSON.parse(String(target?.[1]?.body))).toEqual({ note_id: 'roof-repair' });
  });

  it('leads with the title it would give a new note', async () => {
    const user = userEvent.setup();
    const { fetchImpl } = mount([
      capture({ id: 'srv-10', status: 'needs_target', suggested_title: 'Kitchen rebuild' }),
    ]);

    await user.click(await screen.findByRole('button', { name: /start .*kitchen rebuild/i }));

    await waitFor(() => {
      expect(
        fetchImpl.mock.calls.some(([input]) =>
          String(input).endsWith('/v1/captures/srv-10/target'),
        ),
      ).toBe(true);
    });

    const target = fetchImpl.mock.calls.find(([input]) =>
      String(input).endsWith('/v1/captures/srv-10/target'),
    );
    expect(JSON.parse(String(target?.[1]?.body))).toEqual({
      new_note_title: 'Kitchen rebuild',
    });
  });

  it('still lets the user disagree with it', async () => {
    const user = userEvent.setup();
    mount([capture({ id: 'srv-11', status: 'needs_target', suggested_title: 'Kitchen rebuild' })]);

    await user.click(await screen.findByRole('button', { name: /choose another note/i }));

    // The full library, and the new-note field, exactly as before.
    expect(await screen.findByRole('button', { name: 'Roof repair' })).toBeInTheDocument();
    expect(screen.getByLabelText(/new note title/i)).toBeInTheDocument();
  });

  it('falls back to the plain picker when the suggested note is not loaded', async () => {
    // The router can name a note beyond the first page of the library. Offering
    // `Add to ""` would be worse than offering the list.
    mount([
      capture({ id: 'srv-12', status: 'needs_target', suggested_note_id: 'page-two-note' }),
    ]);

    expect(await screen.findByRole('button', { name: /choose a note/i })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /add to/i })).toBeNull();
  });
});
