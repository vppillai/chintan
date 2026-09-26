import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ComponentProps } from 'react';
import { describe, expect, it, vi } from 'vitest';

import { TestProviders } from '@/test/providers.tsx';

import { formatRowTime } from './groups.ts';
import { MoveSheet, optionMeta } from './MoveSheet.tsx';

/**
 * The line under each Move option: what tells five "Voice note …" rows apart
 * (review 2026-09-21, T42) — when the note was touched, how its text begins,
 * and the tags the line showed before.
 */
describe('a Move option’s meta line', () => {
  const at = '2026-08-06T09:14:00.000Z';
  const when = formatRowTime(at);

  it('cuts prose at forty characters on a trimmed edge, then the tags', () => {
    expect(
      optionMeta({
        updated_at: at,
        snippet: 'Ridge tiles on the south slope have slipped.  Two quotes before the rain.',
        tags: ['house'],
      }),
    ).toBe(`${when} · Ridge tiles on the south slope have slip… · house`);
  });

  it('says a checklist’s open items, not its syntax', () => {
    expect(
      optionMeta({
        updated_at: at,
        kind: 'checklist',
        snippet: '- [ ] Call Ellis\n- [x] Buy tiles\n- [ ] Book the scaffold',
      }),
    ).toBe(`${when} · Call Ellis · Book the scaffold`);
  });

  it('is only the date for a note with no text', () => {
    expect(optionMeta({ updated_at: at, snippet: '' })).toBe(when);
    expect(optionMeta({ updated_at: at })).toBe(when);
  });
});

/**
 * The sheet open over the test corpus (`TEST_NOTES`, whose "Roof repair" is
 * the note being moved out of), reporting what it asks for rather than
 * moving anything: the move itself, and the notice after it, are the
 * recordings tab's (`Recordings.test.tsx`).
 */
function mount(props: Partial<ComponentProps<typeof MoveSheet>> = {}) {
  const onChoose = vi.fn();
  const onCancel = vi.fn();
  render(
    <TestProviders>
      <MoveSheet
        open
        count={1}
        excludeNoteId="roof-repair"
        pending={false}
        error={null}
        onChoose={onChoose}
        onCancel={onCancel}
        {...props}
      />
    </TestProviders>,
  );
  return { onChoose, onCancel };
}

describe('moving into a note that does not exist yet (owner, 2026-09-24)', () => {
  it('heads the list with a New note… row that unfolds into a title field', async () => {
    const user = userEvent.setup();
    mount();
    const sheet = screen.getByRole('dialog', { name: 'Move this recording to…' });
    // There before the list has arrived: the row does not depend on there
    // being other notes, and it wears the option class whose min height is
    // the touch target.
    const row = within(sheet).getByRole('button', { name: 'New note…' });
    expect(row).toHaveClass('move-sheet__option');
    await within(sheet).findByRole('button', { name: /reading list/i });
    expect(within(sheet).getAllByRole('button')[0]).toBe(row);

    await user.click(row);
    const field = within(sheet).getByLabelText('Name the new note');
    expect(field).toHaveFocus();
    expect(field).toHaveAttribute('maxlength', '200');
    // One question at a time: the search and the list step aside.
    expect(within(sheet).queryByRole('searchbox')).toBeNull();
    expect(within(sheet).queryByRole('button', { name: /reading list/i })).toBeNull();
  });

  it('will not ask for a note with no name', async () => {
    const user = userEvent.setup();
    const { onChoose } = mount();
    await user.click(screen.getByRole('button', { name: 'New note…' }));
    const create = screen.getByRole('button', { name: 'Create and move' });
    expect(create).toBeDisabled();
    await user.type(screen.getByLabelText('Name the new note'), '   {Enter}');
    expect(create).toBeDisabled();
    expect(onChoose).not.toHaveBeenCalled();
  });

  it('asks for the move with the trimmed name as the new note, on Enter or the button', async () => {
    const user = userEvent.setup();
    const { onChoose } = mount();
    await user.click(screen.getByRole('button', { name: 'New note…' }));
    await user.type(screen.getByLabelText('Name the new note'), '  Trip notes {Enter}');
    expect(onChoose).toHaveBeenCalledWith({ new_note_title: 'Trip notes' }, 'Trip notes');

    await user.click(screen.getByRole('button', { name: 'Create and move' }));
    expect(onChoose).toHaveBeenCalledTimes(2);
    expect(onChoose).toHaveBeenLastCalledWith({ new_note_title: 'Trip notes' }, 'Trip notes');
  });

  it('an existing note is asked for by id, with its title for the notice', async () => {
    const user = userEvent.setup();
    const { onChoose } = mount();
    await user.click(await screen.findByRole('button', { name: /reading list/i }));
    expect(onChoose).toHaveBeenCalledWith({ note_id: 'reading-list' }, 'Reading list');
  });

  it('Escape and Back return to the list with focus on the row; Escape from the list cancels', async () => {
    const user = userEvent.setup();
    const { onCancel } = mount();
    await user.click(screen.getByRole('button', { name: 'New note…' }));
    await user.keyboard('{Escape}');
    expect(onCancel).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'New note…' })).toHaveFocus();
    expect(screen.getByRole('searchbox', { name: 'Search notes' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'New note…' }));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    expect(screen.getByRole('button', { name: 'New note…' })).toHaveFocus();

    await user.keyboard('{Escape}');
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it('a search no note matched becomes the name to start from', async () => {
    const user = userEvent.setup();
    mount();
    await user.type(screen.getByRole('searchbox', { name: 'Search notes' }), 'Trip notes');
    expect(await screen.findByText('No note matches “Trip notes”.')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'New note…' }));
    expect(screen.getByLabelText('Name the new note')).toHaveValue('Trip notes');
  });

  it('holds its controls while a move is in flight, and shows a refusal', () => {
    mount({ pending: true, error: 'Wait until it has finished filing.' });
    expect(screen.getByRole('alert')).toHaveTextContent('Wait until it has finished filing.');
    expect(screen.getByRole('button', { name: 'New note…' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Moving…' })).toBeDisabled();
  });
});
