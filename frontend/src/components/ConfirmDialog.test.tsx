import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { ConfirmDialog } from './ConfirmDialog.tsx';

/**
 * The confirm on a destructive action: a sentence, Cancel under the Enter
 * key, one button that does it. There is no typed word anywhere any more
 * (owner, 2026-09-26), and no held button either (the bulk purge it gated
 * went with multi-select; owner, 2026-09-29).
 */

describe('ConfirmDialog', () => {
  it('is plain: no text field, focus on Cancel, and the confirm control fires at once', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();

    render(
      <ConfirmDialog
        open
        title="Delete this note forever?"
        body="Everything goes."
        confirmLabel="Delete forever"
        destructive
        onConfirm={onConfirm}
        onCancel={vi.fn()}
      />,
    );

    expect(screen.queryByRole('textbox')).toBeNull();
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Delete forever' }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });
});
