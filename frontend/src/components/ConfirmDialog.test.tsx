import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';

import { ConfirmDialog } from './ConfirmDialog.tsx';

/**
 * The gate on the one action in the app that cannot be undone.
 *
 * Everything here is about the dialog refusing to fire until the user has done
 * something only a deliberate user does. Escape and the focus trap are covered
 * by the existing behaviour; what is new is the typing gate, and the property
 * that matters most about it is that it does not survive a cancel.
 */

describe('ConfirmDialog with a typing gate', () => {
  it('keeps the confirm control disabled until the text matches', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();

    render(
      <ConfirmDialog
        open
        title="Delete this note forever?"
        body="Everything goes."
        confirmLabel="Delete forever"
        requireText="Old fence"
        destructive
        onConfirm={onConfirm}
        onCancel={vi.fn()}
      />,
    );

    const confirm = screen.getByRole('button', { name: 'Delete forever' });
    expect(confirm).toBeDisabled();

    await user.type(screen.getByRole('textbox'), 'Old fenc');
    expect(confirm).toBeDisabled();

    await user.type(screen.getByRole('textbox'), 'e');
    expect(confirm).toBeEnabled();

    await user.click(confirm);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it('accepts a different case and stray whitespace, since it is a speed bump', async () => {
    const user = userEvent.setup();

    render(
      <ConfirmDialog
        open
        title="Delete this note forever?"
        body="Everything goes."
        confirmLabel="Delete forever"
        requireText="Old fence"
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );

    await user.type(screen.getByRole('textbox'), '  OLD FENCE ');
    expect(screen.getByRole('button', { name: 'Delete forever' })).toBeEnabled();
  });

  it('forgets what was typed when it is cancelled and opened again', async () => {
    const user = userEvent.setup();

    function Host() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button type="button" onClick={() => { setOpen(true); }}>
            Delete forever
          </button>
          <ConfirmDialog
            open={open}
            title="Delete this note forever?"
            body="Everything goes."
            confirmLabel="Yes, delete it"
            requireText="Old fence"
            onConfirm={vi.fn()}
            onCancel={() => { setOpen(false); }}
          />
        </>
      );
    }

    render(<Host />);

    await user.click(screen.getByRole('button', { name: 'Delete forever' }));
    await user.type(screen.getByRole('textbox'), 'Old fence');
    expect(screen.getByRole('button', { name: 'Yes, delete it' })).toBeEnabled();

    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    await user.click(screen.getByRole('button', { name: 'Delete forever' }));

    // Reopening a half-answered dialog pre-unlocked is how a second, unintended
    // delete happens: the user taps the same place twice and the gate is
    // already open.
    expect(screen.getByRole('textbox')).toHaveValue('');
    expect(screen.getByRole('button', { name: 'Yes, delete it' })).toBeDisabled();
  });

  it('leaves an ungated dialog exactly as it was', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();

    render(
      <ConfirmDialog
        open
        title="Archive this note?"
        body="You can restore it."
        confirmLabel="Archive it"
        onConfirm={onConfirm}
        onCancel={vi.fn()}
      />,
    );

    expect(screen.queryByRole('textbox')).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Archive it' }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });
});
