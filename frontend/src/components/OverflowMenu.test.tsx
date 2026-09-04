import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { OverflowMenu } from './OverflowMenu.tsx';

describe('OverflowMenu', () => {
  it('is a real menu: opens on the trigger, arrows move, a pick closes it', async () => {
    const user = userEvent.setup();
    const move = vi.fn();
    const remove = vi.fn();
    render(
      <OverflowMenu
        label="More for this row"
        items={[
          { label: 'Move to…', onSelect: move },
          { label: 'Delete', onSelect: remove, destructive: true },
        ]}
      />,
    );

    const trigger = screen.getByRole('button', { name: 'More for this row' });
    expect(trigger).toHaveAttribute('aria-haspopup', 'menu');
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
    await user.click(trigger);
    expect(trigger).toHaveAttribute('aria-expanded', 'true');

    const items = screen.getAllByRole('menuitem');
    expect(items[0]).toHaveFocus();
    await user.keyboard('{ArrowDown}');
    expect(items[1]).toHaveFocus();
    await user.keyboard('{ArrowDown}');
    expect(items[0]).toHaveFocus();
    await user.keyboard('{ArrowUp}');
    expect(items[1]).toHaveFocus();
    expect(items[1]).toHaveAttribute('data-destructive', 'true');

    await user.click(items[1]!);
    expect(remove).toHaveBeenCalledTimes(1);
    expect(move).not.toHaveBeenCalled();
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('closes on a tap outside', async () => {
    const user = userEvent.setup();
    render(
      <>
        <OverflowMenu label="More" items={[{ label: 'One', onSelect: () => {} }]} />
        <p>outside</p>
      </>,
    );
    await user.click(screen.getByRole('button', { name: 'More' }));
    expect(screen.getByRole('menu')).toBeInTheDocument();
    await user.click(screen.getByText('outside'));
    expect(screen.queryByRole('menu')).toBeNull();
  });
});
