import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useEffect, useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { StatusRegion, announce } from '@/components/StatusRegion.tsx';
import { Toast, dismissToast } from '@/components/Toast.tsx';

import { doneStorageKey } from './ChecklistDone.tsx';
import { ChecklistEditor, parseMotionMs } from './ChecklistEditor.tsx';

/**
 * The editor against a stand-in for the note editor that applies every
 * `onChange(body)` and counts every `onSave()`, so what these assert is the
 * body the real editor would have been handed — the one thing this component
 * exists to produce — and when it would have been asked to save. `setBody`
 * is the outside world changing the note under the editor, as a refetch does;
 * `current` is the body as the note editor's mirror holds it, which Undo asks
 * for (`currentBody`), so it follows both the editor's writes and `setBody`.
 * A save re-renders once it has settled, as the real editor's does
 * (`performSave` → `commit` → `dispatch`), so a focus target left armed past
 * the render it was set for shows up here as it would in the app. That render
 * comes from an effect after the one the act rendered, inside the same `act`
 * scope, rather than from a detached microtask no test's scope covers.
 */
function mount(initial: string, noteId = 'shopping') {
  const log = { bodies: [] as string[], saves: 0, current: initial, setBody: (_next: string): void => {} };
  function Harness() {
    const [body, setBody] = useState(initial);
    const [settled, setSettled] = useState(0);
    log.setBody = (next) => {
      log.current = next;
      setBody(next);
    };
    // The body is the trigger: the editor asks to save in the handler that
    // changed it, so the render after that handler is the one to settle from.
    useEffect(() => {
      if (settled !== log.saves) setSettled(log.saves);
    }, [body, settled]);
    return (
      <ChecklistEditor
        noteId={noteId}
        body={body}
        currentBody={() => log.current}
        onChange={(next) => {
          log.bodies.push(next);
          log.current = next;
          setBody(next);
        }}
        onSave={() => {
          log.saves += 1;
        }}
      />
    );
  }
  const view = render(
    <>
      <Harness />
      {/* The shell's, in the app; here so the Undo that Delete done offers can be pressed, and so what the editor says can be read. */}
      <Toast />
      <StatusRegion />
    </>,
  );
  return { log, body: () => log.bodies.at(-1) ?? initial, unmount: view.unmount };
}

afterEach(() => {
  dismissToast();
  announce('');
  sessionStorage.clear();
});

const mouse = { pointerType: 'mouse', button: 0, pointerId: 1 };

/** The open rows' words, top to bottom — the add row's empty field last. */
function openValues(): string[] {
  return within(items())
    .getAllByRole('textbox')
    .map((box) => (box as HTMLTextAreaElement).value);
}

const LIST = '- [ ] Milk\n- [x] Eggs\n- [ ] Bread';

function items(): HTMLElement {
  return screen.getByRole('list', { name: 'Items' });
}

function doneSection(): HTMLElement {
  return screen.getByRole('region', { name: /^Done/ });
}

describe('ChecklistEditor', () => {
  it('shows the open items as rows to edit and the done ones below, struck through', () => {
    mount(LIST);
    const open = within(items());
    expect(open.getAllByRole('textbox').map((box) => (box as HTMLInputElement).value)).toEqual([
      'Milk',
      'Bread',
      '', // the add row
    ]);
    const milk = open.getByRole('checkbox', { name: 'Milk' });
    expect(milk).not.toBeChecked();
    // A real control, drawn: the input is visually hidden under its label and
    // the box beside it is the component's own SVG, so no native box shows.
    expect(milk.closest('label')?.querySelector('.checklist__mark svg rect')).not.toBeNull();
    expect(open.getByRole('textbox', { name: 'Add an item' })).toBeInTheDocument();

    const done = within(doneSection());
    expect(screen.getByRole('heading', { name: /Done \(1\)/ })).toBeInTheDocument();
    expect(done.getByRole('checkbox', { name: 'Eggs' })).toBeChecked();
    expect(done.getByText('Eggs', { selector: 'span.checklist__text' }).closest('li')).toHaveClass(
      'checklist__row--done',
    );
    expect(done.getByRole('button', { name: 'Delete Eggs' })).toBeInTheDocument();
    // Not editable once done: the text is a span, not a field.
    expect(done.queryByRole('textbox')).toBeNull();
  });

  it('ticks an item in place, saves at once, says so, and after a beat moves it down to Done', async () => {
    const user = userEvent.setup();
    const { log, body } = mount(LIST);

    await user.click(screen.getByRole('checkbox', { name: 'Milk' }));
    expect(body()).toBe('- [x] Milk\n- [x] Eggs\n- [ ] Bread');
    expect(log.saves).toBe(1);
    // Said once, by the toast; the status region is left to the toast's word.
    expect(screen.getByText('Milk done', { selector: '.toast__text' })).toBeInTheDocument();
    expect(screen.queryByText('Marked done')).toBeNull();
    // The row stays where the finger is, ticked and struck, for as long as
    // the tick takes to draw — a row that moved at once would mount in Done
    // already ticked, and nothing would be seen to happen.
    const milk = within(items()).getByRole('checkbox', { name: 'Milk' });
    expect(milk).toBeChecked();
    expect(milk.closest('li')).toHaveClass('checklist__row--done');
    expect(screen.getByRole('heading', { name: /Done \(1\)/ })).toBeInTheDocument();
    expect(await within(doneSection()).findByRole('checkbox', { name: 'Milk' })).toBeChecked();
    expect(screen.getByRole('heading', { name: /Done \(2\)/ })).toBeInTheDocument();
    expect(within(items()).queryByRole('checkbox', { name: 'Milk' })).toBeNull();

    await user.click(within(doneSection()).getByRole('checkbox', { name: 'Eggs' }));
    expect(body()).toBe('- [x] Milk\n- [ ] Eggs\n- [ ] Bread');
    expect(screen.getByText('Reopened')).toHaveAttribute('role', 'status');
    // Reopened the same way: unticked under Done for the beat, then back up.
    expect(within(doneSection()).getByRole('checkbox', { name: 'Eggs' })).not.toBeChecked();
    expect(await within(items()).findByRole('checkbox', { name: 'Eggs' })).not.toBeChecked();
    expect(within(doneSection()).queryByRole('checkbox', { name: 'Eggs' })).toBeNull();
  });

  it('Enter starts a new item under this one and puts the caret in it', async () => {
    const user = userEvent.setup();
    const { body } = mount(LIST);

    const milk = screen.getByRole('textbox', { name: 'Item 1' });
    await user.click(milk);
    await user.keyboard('{Enter}');
    expect(body()).toBe('- [ ] Milk\n- [ ] \n- [x] Eggs\n- [ ] Bread');
    const fresh = screen.getByRole('textbox', { name: 'Item 2' });
    expect(fresh).toHaveFocus();
    expect(fresh).toHaveValue('');

    await user.keyboard('Butter');
    expect(body()).toBe('- [ ] Milk\n- [ ] Butter\n- [x] Eggs\n- [ ] Bread');
  });

  it('Backspace in an emptied item removes it and steps back to the one above', async () => {
    const user = userEvent.setup();
    const { log, body } = mount('- [ ] Milk\n- [ ] \n- [ ] Bread');

    const empty = screen.getByRole('textbox', { name: 'Item 2' });
    await user.click(empty);
    await user.keyboard('{Backspace}');
    expect(body()).toBe('- [ ] Milk\n- [ ] Bread');
    expect(screen.getByRole('textbox', { name: 'Item 1' })).toHaveFocus();
    // The removal saves; the focus move blurs a row too, which saves again
    // and is harmless — the editor sends nothing for a draft already saved.
    expect(log.saves).toBeGreaterThanOrEqual(1);
    // Backspace in an item with text is just editing.
    await user.keyboard('{Backspace}');
    expect(body()).toBe('- [ ] Mil\n- [ ] Bread');
  });

  it('the add row appends on Enter and stays put for the next one; blur saves', async () => {
    const user = userEvent.setup();
    const { log, body } = mount(LIST);

    const add = screen.getByRole('textbox', { name: 'Add an item' });
    await user.click(add);
    await user.keyboard('Jam{Enter}');
    expect(body()).toBe(`${LIST}\n- [ ] Jam`);
    expect(add).toHaveValue('');
    expect(add).toHaveFocus();

    await user.keyboard('Tea');
    await user.tab();
    expect(body()).toBe(`${LIST}\n- [ ] Jam\n- [ ] Tea`);
    expect(log.saves).toBeGreaterThanOrEqual(1);
  });

  it('leaving an item saves it', async () => {
    const user = userEvent.setup();
    const { log, body } = mount(LIST);
    await user.type(screen.getByRole('textbox', { name: 'Item 2' }), ' rolls');
    expect(body()).toBe('- [ ] Milk\n- [x] Eggs\n- [ ] Bread rolls');
    expect(log.saves).toBe(0);
    await user.tab();
    expect(log.saves).toBe(1);
  });

  it('deletes a done item for good', async () => {
    const user = userEvent.setup();
    const { log, body } = mount(LIST);
    await user.click(screen.getByRole('button', { name: 'Delete Eggs' }));
    expect(body()).toBe('- [ ] Milk\n- [ ] Bread');
    expect(log.saves).toBe(1);
    expect(screen.queryByRole('region', { name: /^Done/ })).toBeNull();
  });

  it('an item is a wrapping field: a pasted line break becomes a space, and Enter still starts a new item', async () => {
    const user = userEvent.setup();
    const { body } = mount(LIST);
    const milk = screen.getByRole('textbox', { name: 'Item 1' });
    // A textarea, so a dictated sentence wraps instead of clipping; one row
    // tall until its words need more.
    expect(milk.tagName).toBe('TEXTAREA');
    expect(milk).toHaveAttribute('rows', '1');

    await user.click(milk);
    await user.paste(' and\ncream');
    expect(body()).toBe('- [ ] Milk and cream\n- [x] Eggs\n- [ ] Bread');
    expect(milk).toHaveValue('Milk and cream');

    await user.keyboard('{Enter}');
    expect(body()).toBe('- [ ] Milk and cream\n- [ ] \n- [x] Eggs\n- [ ] Bread');
    expect(screen.getByRole('textbox', { name: 'Item 2' })).toHaveFocus();
  });

  it('shows a legacy prose line as an open item and normalises it on the first write', async () => {
    const user = userEvent.setup();
    const { body } = mount('Ridge tiles have slipped.\n\nCall Ellis');
    expect(screen.getByRole('textbox', { name: 'Item 1' })).toHaveValue('Ridge tiles have slipped.');
    await user.click(screen.getByRole('checkbox', { name: 'Call Ellis' }));
    expect(body()).toBe('- [ ] Ridge tiles have slipped.\n- [x] Call Ellis');
  });
});

describe('reordering by the grip', () => {
  it('a drag past the next row shows the new order at once and writes the body once, on release', () => {
    const { log, body } = mount(LIST);
    const list = items();
    const grip = screen.getByRole('button', { name: 'Move Milk' });
    expect(grip).toHaveAccessibleDescription(/arrow keys/);

    // jsdom lays nothing out — every row's midpoint is 0 — so a move to a
    // positive y is "below every row" and a negative one "above every row".
    fireEvent.pointerDown(grip, { ...mouse, clientX: 10, clientY: 0 });
    fireEvent.pointerMove(list, { ...mouse, clientX: 10, clientY: 40 });
    expect(openValues()).toEqual(['Bread', 'Milk', '']);
    expect(grip.closest('li')).toHaveAttribute('data-dragging');
    // Nothing is written while the row is lifted.
    expect(log.bodies).toEqual([]);

    fireEvent.pointerUp(list, { ...mouse, clientX: 10, clientY: 40 });
    // One write: Milk takes Bread's slot; Eggs, done, keeps its line where it was.
    expect(log.bodies).toEqual(['- [x] Eggs\n- [ ] Bread\n- [ ] Milk']);
    expect(body()).toBe('- [x] Eggs\n- [ ] Bread\n- [ ] Milk');
    expect(log.saves).toBe(1);
    expect(openValues()).toEqual(['Bread', 'Milk', '']);
    // The click the browser fires as the mouse lifts (`detail` 1) is not a tap on the grip.
    fireEvent.click(screen.getByRole('button', { name: 'Move Milk' }), { detail: 1 });
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('a tap on the grip opens the row’s menu, whose Move down writes the body', async () => {
    const user = userEvent.setup();
    const { log, body } = mount(LIST);
    const grip = screen.getByRole('button', { name: 'Move Milk' });

    fireEvent.pointerDown(grip, { ...mouse, clientX: 10, clientY: 0 });
    fireEvent.pointerUp(items(), { ...mouse, clientX: 10, clientY: 0 });
    expect(log.bodies).toEqual([]);
    const menu = screen.getByRole('menu');
    expect(within(menu).getAllByRole('menuitem').map((item) => item.textContent)).toEqual([
      'Move up',
      'Move down',
      'Move to top',
      'Move to bottom',
      'Make a sub-item',
      'Move up a level',
      'Delete',
    ]);
    // The top row cannot move up, and the first item can never be a sub-item.
    expect(within(menu).getByRole('menuitem', { name: 'Move up' })).toBeDisabled();
    expect(within(menu).getByRole('menuitem', { name: 'Move to top' })).toBeDisabled();
    expect(within(menu).getByRole('menuitem', { name: 'Make a sub-item' })).toBeDisabled();
    expect(within(menu).getByRole('menuitem', { name: 'Move up a level' })).toBeDisabled();

    await user.click(within(menu).getByRole('menuitem', { name: 'Move down' }));
    expect(body()).toBe('- [x] Eggs\n- [ ] Bread\n- [ ] Milk');
    expect(log.saves).toBe(1);
    // Focus follows the row, so the next move starts from where it landed.
    expect(screen.getByRole('button', { name: 'Move Milk' })).toHaveFocus();
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('Delete in the grip’s menu removes the row and puts focus on the next one', async () => {
    const user = userEvent.setup();
    const { log, body } = mount(LIST);
    const grip = screen.getByRole('button', { name: 'Move Milk' });
    fireEvent.pointerDown(grip, { ...mouse, clientX: 10, clientY: 0 });
    fireEvent.pointerUp(items(), { ...mouse, clientX: 10, clientY: 0 });

    await user.click(screen.getByRole('menuitem', { name: 'Delete' }));
    expect(body()).toBe('- [x] Eggs\n- [ ] Bread');
    expect(log.saves).toBe(1);
    expect(screen.getByRole('textbox', { name: 'Item 1' })).toHaveValue('Bread');
    expect(screen.getByRole('textbox', { name: 'Item 1' })).toHaveFocus();
  });

  it('Escape mid-drag drops the row where it was and writes nothing', () => {
    const { log } = mount(LIST);
    const list = items();
    fireEvent.pointerDown(screen.getByRole('button', { name: 'Move Milk' }), {
      ...mouse,
      clientX: 10,
      clientY: 0,
    });
    fireEvent.pointerMove(list, { ...mouse, clientX: 10, clientY: 40 });
    expect(openValues()).toEqual(['Bread', 'Milk', '']);

    fireEvent.keyDown(document, { key: 'Escape' });
    expect(openValues()).toEqual(['Milk', 'Bread', '']);
    fireEvent.pointerUp(list, { ...mouse, clientX: 10, clientY: 40 });
    expect(log.bodies).toEqual([]);
    expect(log.saves).toBe(0);
  });

  it('the arrow keys on a grip move the row one slot and keep the keyboard on it', async () => {
    const user = userEvent.setup();
    const { log, body } = mount('- [ ] Milk\n- [x] Eggs\n- [ ] Bread\n- [ ] Butter');
    const grip = screen.getByRole('button', { name: 'Move Milk' });
    grip.focus();

    await user.keyboard('{ArrowDown}');
    expect(body()).toBe('- [x] Eggs\n- [ ] Bread\n- [ ] Milk\n- [ ] Butter');
    expect(log.saves).toBe(1);
    expect(screen.getByRole('button', { name: 'Move Milk' })).toHaveFocus();
    await user.keyboard('{ArrowDown}');
    expect(body()).toBe('- [x] Eggs\n- [ ] Bread\n- [ ] Butter\n- [ ] Milk');
    // At the bottom, a further step does nothing and writes nothing.
    await user.keyboard('{ArrowDown}');
    expect(log.saves).toBe(2);
    await user.keyboard('{ArrowUp}');
    expect(body()).toBe('- [x] Eggs\n- [ ] Bread\n- [ ] Milk\n- [ ] Butter');
  });

  it('done rows have no grip', () => {
    mount(LIST);
    expect(within(items()).getAllByRole('button', { name: /^Move / })).toHaveLength(2);
    expect(within(doneSection()).queryByRole('button', { name: /^Move / })).toBeNull();
  });

  it('a body that changes under a lifted row drops it', () => {
    const { log } = mount(LIST);
    const list = items();
    fireEvent.pointerDown(screen.getByRole('button', { name: 'Move Milk' }), {
      ...mouse,
      clientX: 10,
      clientY: 0,
    });
    fireEvent.pointerMove(list, { ...mouse, clientX: 10, clientY: 40 });
    expect(openValues()).toEqual(['Bread', 'Milk', '']);

    // A recording lands by refetch while the row is in the air.
    act(() => {
      log.setBody(`${LIST}\n- [ ] Jam`);
    });
    expect(openValues()).toEqual(['Milk', 'Bread', 'Jam', '']);
    expect(list).not.toHaveAttribute('data-dragging');
    fireEvent.pointerUp(list, { ...mouse, clientX: 10, clientY: 40 });
    expect(log.bodies).toEqual([]);
  });
});

describe('sub-items', () => {
  const PARTY = '- [ ] Party\n- [ ] Plates\n- [x] Eggs\n- [ ] Bread';

  it('Tab in a field makes the item a sub-item of the one above, Shift+Tab brings it up; both save at once', async () => {
    const user = userEvent.setup();
    const { log, body } = mount(PARTY);
    const plates = screen.getByRole('textbox', { name: 'Item 2' });
    await user.click(plates);

    // The keys are described on the field, where they act, not only on the grip.
    expect(plates).toHaveAccessibleDescription(/Tab makes this item a sub-item/);
    expect(screen.getByRole('button', { name: 'Move Plates' })).not.toHaveAccessibleDescription(/Tab/);

    await user.keyboard('{Tab}');
    expect(body()).toBe('- [ ] Party\n  - [ ] Plates\n- [x] Eggs\n- [ ] Bread');
    expect(log.saves).toBe(1);
    // The same field, still focused, now named for what it is, and its row set in.
    expect(plates).toHaveFocus();
    expect(plates).toHaveAccessibleName('Sub-item 2');
    expect(plates.closest('li')).toHaveAttribute('data-depth', '1');
    expect(screen.getByText('Made a sub-item of “Party”')).toHaveAttribute('role', 'status');

    // Already one level under the row above: Tab can nest no deeper, so it is the browser's
    // Tab and focus leaves the field rather than sticking in it.
    await user.keyboard('{Tab}');
    expect(body()).toBe('- [ ] Party\n  - [ ] Plates\n- [x] Eggs\n- [ ] Bread');
    expect(plates).not.toHaveFocus();

    await user.click(plates);
    await user.keyboard('{Shift>}{Tab}{/Shift}');
    expect(body()).toBe(PARTY);
    expect(plates).toHaveFocus();
    expect(plates).toHaveAccessibleName('Item 2');
    expect(plates.closest('li')).not.toHaveAttribute('data-depth');
    expect(screen.getByText('Now a top-level item')).toHaveAttribute('role', 'status');
    // The Tab that left the field blurred it, which saves too; the un-nest saved again.
    expect(log.saves).toBe(3);

    // At the top level Shift+Tab is the browser's too.
    await user.keyboard('{Shift>}{Tab}{/Shift}');
    expect(body()).toBe(PARTY);
    expect(plates).not.toHaveFocus();
  });

  it('the first item can never be a sub-item', async () => {
    const user = userEvent.setup();
    const { log, body } = mount(PARTY);
    await user.click(screen.getByRole('textbox', { name: 'Item 1' }));
    await user.keyboard('{Tab}');
    expect(body()).toBe(PARTY);
    // Nothing written: the Tab was the browser's, and focus left the field.
    expect(log.bodies).toEqual([]);
    expect(screen.getByRole('textbox', { name: 'Item 1' })).not.toHaveFocus();
  });

  it('nests under the row shown above, not the body’s previous line when that one is done', async () => {
    const user = userEvent.setup();
    const { body } = mount(LIST);
    // Milk and Bread are the open rows; Eggs sits in Done between them in the body.
    const bread = screen.getByRole('textbox', { name: 'Item 2' }) as HTMLTextAreaElement;
    await user.click(bread);
    bread.setSelectionRange(2, 2);
    await user.keyboard('{Tab}');
    expect(body()).toBe('- [ ] Milk\n  - [ ] Bread\n- [x] Eggs');
    // The row's line moved up past Eggs's, so its field is a fresh element;
    // focus followed, and the caret is where it was, mid-word.
    const fresh = screen.getByRole('textbox', { name: 'Sub-item 2' }) as HTMLTextAreaElement;
    expect(fresh).toHaveFocus();
    expect(fresh).toHaveValue('Bread');
    expect([fresh.selectionStart, fresh.selectionEnd]).toEqual([2, 2]);
  });

  it('the menu nests a row whose line moves past a done one, and the keyboard stays on its grip once the save settles', async () => {
    const user = userEvent.setup();
    const { log, body } = mount(LIST);
    const grip = screen.getByRole('button', { name: 'Move Bread' });
    fireEvent.pointerDown(grip, { ...mouse, clientX: 10, clientY: 0 });
    fireEvent.pointerUp(items(), { ...mouse, clientX: 10, clientY: 0 });
    await user.click(screen.getByRole('menuitem', { name: 'Make a sub-item' }));
    expect(body()).toBe('- [ ] Milk\n  - [ ] Bread\n- [x] Eggs');
    expect(log.saves).toBe(1);
    // Bread's index changed, which remade its field — a target the Tab path
    // would focus — but the menu asked for the grip, and the save's render
    // must not hand focus to the field after it.
    expect(screen.getByRole('button', { name: 'Move Bread' })).toHaveFocus();
    expect(screen.getByRole('textbox', { name: 'Sub-item 2' })).not.toHaveFocus();
  });

  it('the grip’s menu nests and un-nests the row and keeps the keyboard on its grip', async () => {
    const user = userEvent.setup();
    const { body } = mount(PARTY);
    const grip = screen.getByRole('button', { name: 'Move Plates' });
    fireEvent.pointerDown(grip, { ...mouse, clientX: 10, clientY: 0 });
    fireEvent.pointerUp(items(), { ...mouse, clientX: 10, clientY: 0 });
    let menu = screen.getByRole('menu');
    expect(within(menu).getByRole('menuitem', { name: 'Move up a level' })).toBeDisabled();
    await user.click(within(menu).getByRole('menuitem', { name: 'Make a sub-item' }));
    expect(body()).toBe('- [ ] Party\n  - [ ] Plates\n- [x] Eggs\n- [ ] Bread');
    expect(screen.getByRole('button', { name: 'Move Plates' })).toHaveFocus();

    await user.click(screen.getByRole('button', { name: 'Move Plates' }));
    menu = screen.getByRole('menu');
    expect(within(menu).getByRole('menuitem', { name: 'Make a sub-item' })).toBeDisabled();
    await user.click(within(menu).getByRole('menuitem', { name: 'Move up a level' }));
    expect(body()).toBe(PARTY);
  });

  it('ticking a parent ticks its sub-items and the block is held, then moves to Done together', async () => {
    const user = userEvent.setup();
    const { log, body } = mount('- [ ] Party\n  - [ ] Plates\n  - [ ] Cups\n- [ ] Bread');
    await user.click(screen.getByRole('checkbox', { name: 'Party' }));
    expect(body()).toBe('- [x] Party\n  - [x] Plates\n  - [x] Cups\n- [ ] Bread');
    expect(log.saves).toBe(1);
    // For the beat, all three stay in the open list, ticked and struck.
    const open = within(items());
    expect(open.getByRole('checkbox', { name: 'Plates' })).toBeChecked();
    expect(open.getByRole('checkbox', { name: 'Cups' }).closest('li')).toHaveClass('checklist__row--done');
    expect(screen.queryByRole('region', { name: /^Done/ })).toBeNull();
    // Then the block moves as one, its depth kept.
    const done = within(await screen.findByRole('region', { name: /^Done/ }));
    expect(done.getAllByRole('checkbox')).toHaveLength(3);
    expect(done.getByRole('checkbox', { name: 'Party' }).closest('li')).not.toHaveAttribute('data-depth');
    expect(done.getByRole('checkbox', { name: 'Plates' }).closest('li')).toHaveAttribute('data-depth', '1');
    expect(done.getByRole('checkbox', { name: 'Plates' }).closest('li')).toHaveAttribute('aria-level', '2');
    expect(done.getByRole('checkbox', { name: 'Cups' }).closest('li')).toHaveAttribute('data-depth', '1');
    expect(open.queryByRole('checkbox', { name: 'Party' })).toBeNull();

    // Reopening a sub-item reopens its parent with it; the other stays done.
    await user.click(done.getByRole('checkbox', { name: 'Plates' }));
    expect(body()).toBe('- [ ] Party\n  - [ ] Plates\n  - [x] Cups\n- [ ] Bread');
    expect(await open.findByRole('checkbox', { name: 'Party' })).not.toBeChecked();
    expect(open.getByRole('checkbox', { name: 'Plates' })).not.toBeChecked();
    expect(within(doneSection()).getByRole('checkbox', { name: 'Cups' })).toBeChecked();
  });

  it('Enter in a parent starts its first sub-item; deleting a parent brings its sub-items up a level', async () => {
    const user = userEvent.setup();
    const { body } = mount('- [ ] Party\n  - [ ] Plates\n  - [ ] Cups');
    await user.click(screen.getByRole('textbox', { name: 'Item 1' }));
    await user.keyboard('{Enter}');
    expect(body()).toBe('- [ ] Party\n  - [ ] \n  - [ ] Plates\n  - [ ] Cups');
    const fresh = screen.getByRole('textbox', { name: 'Sub-item 2' });
    expect(fresh).toHaveFocus();
    // Backspace in the empty sub-item removes it and steps back to Party.
    await user.keyboard('{Backspace}');
    expect(body()).toBe('- [ ] Party\n  - [ ] Plates\n  - [ ] Cups');
    expect(screen.getByRole('textbox', { name: 'Item 1' })).toHaveFocus();

    fireEvent.pointerDown(screen.getByRole('button', { name: 'Move Party' }), { ...mouse, clientX: 10, clientY: 0 });
    fireEvent.pointerUp(items(), { ...mouse, clientX: 10, clientY: 0 });
    await user.click(screen.getByRole('menuitem', { name: 'Delete' }));
    expect(body()).toBe('- [ ] Plates\n- [ ] Cups');
    expect(openValues()).toEqual(['Plates', 'Cups', '']);
    expect(screen.getByRole('textbox', { name: 'Item 1' })).toHaveFocus();
  });

  it('a move that lands a row on another level says so', async () => {
    const user = userEvent.setup();
    const { body } = mount('- [ ] A\n- [ ] B\n  - [ ] B1');
    fireEvent.pointerDown(screen.getByRole('button', { name: 'Move A' }), { ...mouse, clientX: 10, clientY: 0 });
    fireEvent.pointerUp(items(), { ...mouse, clientX: 10, clientY: 0 });
    await user.click(screen.getByRole('menuitem', { name: 'Move to bottom' }));
    // The bottom slot is a sub-item's, so A is one now — the menu offered no level.
    expect(body()).toBe('- [ ] B\n  - [ ] B1\n  - [ ] A');
    expect(screen.getByText('Now a sub-item')).toHaveAttribute('role', 'status');

    const grip = screen.getByRole('button', { name: 'Move A' });
    expect(grip).toHaveFocus();
    await user.keyboard('{ArrowUp}');
    expect(body()).toBe('- [ ] B\n  - [ ] A\n  - [ ] B1');
    // Still a sub-item: nothing new to say.
    expect(screen.getByText('Now a sub-item')).toBeInTheDocument();
    await user.keyboard('{ArrowUp}');
    expect(body()).toBe('- [ ] A\n- [ ] B\n  - [ ] B1');
    expect(screen.getByText('Now a top-level item')).toHaveAttribute('role', 'status');
  });

  it('a parent dragged onto its own sub-item’s slot goes back where it was; dragged past the block’s end it moves', () => {
    const { log, body } = mount('- [ ] Party\n  - [ ] Plates\n- [ ] Bread');
    const list = items();
    // Rows 40 px tall, stacked from the top, so a pointer can stop on one slot.
    within(list)
      .getAllByRole('listitem')
      .filter((row) => row.hasAttribute('data-drag-id'))
      .forEach((row, i) => {
        vi.spyOn(row, 'getBoundingClientRect').mockReturnValue({ top: i * 40, height: 40 } as DOMRect);
      });

    fireEvent.pointerDown(screen.getByRole('button', { name: 'Move Party' }), { ...mouse, clientX: 10, clientY: 20 });
    fireEvent.pointerMove(list, { ...mouse, clientX: 10, clientY: 61 });
    // The draft shows Party under its own sub-item, which nothing can mean.
    expect(openValues()).toEqual(['Plates', 'Party', 'Bread', '']);
    fireEvent.pointerUp(list, { ...mouse, clientX: 10, clientY: 61 });
    expect(log.bodies).toEqual([]);
    expect(openValues()).toEqual(['Party', 'Plates', 'Bread', '']);

    fireEvent.pointerDown(screen.getByRole('button', { name: 'Move Party' }), { ...mouse, clientX: 10, clientY: 20 });
    fireEvent.pointerMove(list, { ...mouse, clientX: 10, clientY: 101 });
    expect(openValues()).toEqual(['Plates', 'Bread', 'Party', '']);
    fireEvent.pointerUp(list, { ...mouse, clientX: 10, clientY: 101 });
    expect(body()).toBe('- [ ] Bread\n- [ ] Party\n  - [ ] Plates');
  });

  it('a parent moves down past its own sub-items as a block, and a block that ends the list cannot move down', async () => {
    const user = userEvent.setup();
    const { body } = mount('- [ ] Party\n  - [ ] Plates\n- [ ] Bread');
    const grip = screen.getByRole('button', { name: 'Move Party' });
    grip.focus();
    await user.keyboard('{ArrowDown}');
    expect(body()).toBe('- [ ] Bread\n- [ ] Party\n  - [ ] Plates');
    // Focus follows the parent, which now stands second.
    expect(screen.getByRole('button', { name: 'Move Party' })).toHaveFocus();
    expect(openValues()).toEqual(['Bread', 'Party', 'Plates', '']);

    fireEvent.pointerDown(screen.getByRole('button', { name: 'Move Party' }), { ...mouse, clientX: 10, clientY: 0 });
    fireEvent.pointerUp(items(), { ...mouse, clientX: 10, clientY: 0 });
    const menu = screen.getByRole('menu');
    expect(within(menu).getByRole('menuitem', { name: 'Move down' })).toBeDisabled();
    expect(within(menu).getByRole('menuitem', { name: 'Move to bottom' })).toBeDisabled();
    expect(within(menu).getByRole('menuitem', { name: 'Move up' })).toBeEnabled();
  });
});

describe('nesting by the grip', () => {
  const TWO = '- [ ] Milk\n- [ ] Bread';

  it('a drag sideways by one step makes the row a sub-item of the one above: previewed in place, one write on release, said', () => {
    const { log, body } = mount(TWO);
    const list = items();
    const grip = screen.getByRole('button', { name: 'Move Bread' });
    expect(grip).toHaveAccessibleDescription(/right and left arrows, to change its level/);

    fireEvent.pointerDown(grip, { ...mouse, clientX: 10, clientY: 50 });
    fireEvent.pointerMove(list, { ...mouse, clientX: 40, clientY: 52 });
    // The row keeps its slot and shows the level it would take; nothing is written.
    expect(grip.closest('li')).toHaveAttribute('data-preview-depth', '1');
    expect(grip.closest('li')).toHaveAttribute('data-dragging');
    expect(openValues()).toEqual(['Milk', 'Bread', '']);
    expect(log.bodies).toEqual([]);

    fireEvent.pointerUp(list, { ...mouse, clientX: 40, clientY: 52 });
    expect(log.bodies).toEqual(['- [ ] Milk\n  - [ ] Bread']);
    expect(body()).toBe('- [ ] Milk\n  - [ ] Bread');
    expect(log.saves).toBe(1);
    expect(screen.getByText('Made a sub-item of “Milk”')).toHaveAttribute('role', 'status');
    const row = screen.getByRole('button', { name: 'Move Bread' }).closest('li');
    expect(row).toHaveAttribute('data-depth', '1');
    expect(row).not.toHaveAttribute('data-preview-depth');
    // A decided drag is never a tap.
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('the first row never goes in: the drag previews nothing and writes nothing', () => {
    const { log } = mount(TWO);
    const list = items();
    const grip = screen.getByRole('button', { name: 'Move Milk' });
    fireEvent.pointerDown(grip, { ...mouse, clientX: 10, clientY: 10 });
    fireEvent.pointerMove(list, { ...mouse, clientX: 40, clientY: 12 });
    expect(grip.closest('li')).not.toHaveAttribute('data-preview-depth');
    fireEvent.pointerUp(list, { ...mouse, clientX: 40, clientY: 12 });
    expect(log.bodies).toEqual([]);
    expect(log.saves).toBe(0);
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('a drag left by one step on a sub-item brings it up a level', () => {
    const { log } = mount('- [ ] Milk\n  - [ ] Bread');
    const list = items();
    const grip = screen.getByRole('button', { name: 'Move Bread' });
    fireEvent.pointerDown(grip, { ...mouse, clientX: 50, clientY: 50 });
    fireEvent.pointerMove(list, { ...mouse, clientX: 20, clientY: 48 });
    expect(grip.closest('li')).toHaveAttribute('data-preview-depth', '0');
    fireEvent.pointerUp(list, { ...mouse, clientX: 20, clientY: 48 });
    expect(log.bodies).toEqual([TWO]);
    expect(log.saves).toBe(1);
    expect(screen.getByText('Now a top-level item')).toHaveAttribute('role', 'status');
  });

  it('a sideways wobble short of a step is neither a tap nor a change', () => {
    const { log } = mount(TWO);
    const list = items();
    fireEvent.pointerDown(screen.getByRole('button', { name: 'Move Bread' }), { ...mouse, clientX: 10, clientY: 50 });
    fireEvent.pointerMove(list, { ...mouse, clientX: 25, clientY: 50 });
    fireEvent.pointerUp(list, { ...mouse, clientX: 25, clientY: 50 });
    expect(screen.queryByRole('menu')).toBeNull();
    expect(log.bodies).toEqual([]);
    expect(log.saves).toBe(0);
  });

  it('a drag that is mostly downward still reorders, whatever it drifts sideways', () => {
    const { log } = mount(TWO);
    const list = items();
    fireEvent.pointerDown(screen.getByRole('button', { name: 'Move Milk' }), { ...mouse, clientX: 10, clientY: 0 });
    fireEvent.pointerMove(list, { ...mouse, clientX: 12, clientY: 60 });
    expect(openValues()).toEqual(['Bread', 'Milk', '']);
    expect(screen.getByRole('button', { name: 'Move Milk' }).closest('li')).not.toHaveAttribute('data-preview-depth');
    fireEvent.pointerUp(list, { ...mouse, clientX: 12, clientY: 60 });
    expect(log.bodies).toEqual(['- [ ] Bread\n- [ ] Milk']);
  });

  it('the right and left arrows on a focused grip nest and un-nest the row and keep the keyboard on it', async () => {
    const user = userEvent.setup();
    const { log, body } = mount(TWO);
    screen.getByRole('button', { name: 'Move Bread' }).focus();

    await user.keyboard('{ArrowRight}');
    expect(body()).toBe('- [ ] Milk\n  - [ ] Bread');
    expect(log.saves).toBe(1);
    expect(screen.getByRole('button', { name: 'Move Bread' })).toHaveFocus();
    expect(screen.getByText('Made a sub-item of “Milk”')).toHaveAttribute('role', 'status');
    // One under the row above is the limit: a second press changes nothing and writes
    // nothing — and says why, since nothing moved and a screen reader would
    // otherwise hear nothing at all.
    await user.keyboard('{ArrowRight}');
    expect(body()).toBe('- [ ] Milk\n  - [ ] Bread');
    expect(log.saves).toBe(1);
    expect(screen.getByText('Already a sub-item')).toHaveAttribute('role', 'status');

    await user.keyboard('{ArrowLeft}');
    expect(body()).toBe(TWO);
    expect(log.saves).toBe(2);
    expect(screen.getByRole('button', { name: 'Move Bread' })).toHaveFocus();
    await user.keyboard('{ArrowLeft}');
    expect(screen.getByText('Already a top-level item')).toHaveAttribute('role', 'status');
    await user.keyboard('{ArrowDown}');
    expect(screen.getByText('Already at the bottom')).toHaveAttribute('role', 'status');

    // The first row has nothing to go under, and nowhere up to go.
    screen.getByRole('button', { name: 'Move Milk' }).focus();
    await user.keyboard('{ArrowRight}');
    expect(body()).toBe(TWO);
    expect(log.saves).toBe(2);
    expect(screen.getByText('Nothing above to nest under')).toHaveAttribute('role', 'status');
    await user.keyboard('{ArrowUp}');
    expect(screen.getByText('Already at the top')).toHaveAttribute('role', 'status');
    expect(body()).toBe(TWO);
  });

  it('says a done row above cannot be nested under, while the tick holds it there for the beat', async () => {
    const user = userEvent.setup();
    const { body } = mount(TWO);
    // Milk just ticked stays in the open list for the beat, done; the right
    // arrow on Bread finds it above and refuses for that reason, not for
    // want of a row above (DB6-22).
    await user.click(screen.getByRole('checkbox', { name: 'Milk' }));
    screen.getByRole('button', { name: 'Move Bread' }).focus();
    await user.keyboard('{ArrowRight}');
    expect(body()).toBe('- [x] Milk\n- [ ] Bread');
    expect(screen.getByText('Cannot nest under a done item')).toHaveAttribute('role', 'status');
  });

  it('a step is the indent token in the page’s own pixels: a larger root font asks for a longer drag', () => {
    // As the browser's text-size setting has it: 1.5rem at a 20 px root is
    // 30 px, and the token itself may move; a drag that would have nested at
    // 24 px must not, and one past the real step must.
    document.documentElement.style.setProperty('--space-6', '3rem');
    document.documentElement.style.fontSize = '20px';
    try {
      const { log } = mount(TWO);
      const list = items();
      const grip = screen.getByRole('button', { name: 'Move Bread' });
      fireEvent.pointerDown(grip, { ...mouse, clientX: 10, clientY: 50 });
      fireEvent.pointerMove(list, { ...mouse, clientX: 50, clientY: 52 });
      expect(grip.closest('li')).not.toHaveAttribute('data-preview-depth');
      fireEvent.pointerMove(list, { ...mouse, clientX: 75, clientY: 52 });
      expect(grip.closest('li')).toHaveAttribute('data-preview-depth', '1');
      fireEvent.pointerUp(list, { ...mouse, clientX: 75, clientY: 52 });
      expect(log.bodies).toEqual(['- [ ] Milk\n  - [ ] Bread']);
    } finally {
      document.documentElement.style.removeProperty('--space-6');
      document.documentElement.style.removeProperty('font-size');
    }
  });
});

describe('three levels', () => {
  const PARTY = '- [ ] Party\n  - [ ] Costco\n- [ ] Plates';

  /** Lifts the row's grip, moves the pointer `dx` sideways, and returns the row while it is in the air. */
  function dragSideways(name: string, dx: number): HTMLElement {
    const grip = screen.getByRole('button', { name: `Move ${name}` });
    fireEvent.pointerDown(grip, { ...mouse, clientX: 100, clientY: 50 });
    fireEvent.pointerMove(items(), { ...mouse, clientX: 100 + dx, clientY: 51 });
    return grip.closest('li') as HTMLElement;
  }
  function release(dx: number): void {
    fireEvent.pointerUp(items(), { ...mouse, clientX: 100 + dx, clientY: 51 });
  }

  it('Tab twice walks a row 0 → 1 → 2, a sibling and then a child; a third Tab is the browser’s', async () => {
    const user = userEvent.setup();
    const { log, body } = mount('- [ ] Party\n- [ ] Costco\n- [ ] Plates');
    await user.click(screen.getByRole('textbox', { name: 'Item 2' }));
    await user.keyboard('{Tab}');
    expect(body()).toBe('- [ ] Party\n  - [ ] Costco\n- [ ] Plates');

    const plates = screen.getByRole('textbox', { name: 'Item 3' });
    await user.click(plates);
    // Under a sub-item, one step in is its sibling, not its child.
    await user.keyboard('{Tab}');
    expect(body()).toBe('- [ ] Party\n  - [ ] Costco\n  - [ ] Plates');
    expect(screen.getByText('Made a sub-item of “Party”')).toHaveAttribute('role', 'status');
    await user.keyboard('{Tab}');
    expect(body()).toBe('- [ ] Party\n  - [ ] Costco\n    - [ ] Plates');
    expect(screen.getByText('Made a sub-item of “Costco”')).toHaveAttribute('role', 'status');
    expect(plates).toHaveFocus();
    expect(plates).toHaveAccessibleName('Sub-item 3, level 3');
    expect(plates.closest('li')).toHaveAttribute('data-depth', '2');
    expect(plates.closest('li')).toHaveAttribute('aria-level', '3');
    expect(screen.getByRole('textbox', { name: 'Item 1' }).closest('li')).toHaveAttribute('aria-level', '1');
    const saves = log.saves;

    // The third level is the last: the key keeps its meaning and focus moves on.
    await user.keyboard('{Tab}');
    expect(body()).toBe('- [ ] Party\n  - [ ] Costco\n    - [ ] Plates');
    expect(plates).not.toHaveFocus();
    // Leaving the field saved it; nothing else was written.
    expect(log.saves).toBe(saves + 1);

    // Shift+Tab is one level out, in place, and says under what.
    await user.click(plates);
    await user.keyboard('{Shift>}{Tab}{/Shift}');
    expect(body()).toBe('- [ ] Party\n  - [ ] Costco\n  - [ ] Plates');
    expect(screen.getByText('Moved up a level, under “Party”')).toHaveAttribute('role', 'status');
  });

  it('a parent takes its whole block with it, a level at a time, in and out', async () => {
    const user = userEvent.setup();
    const { body } = mount('- [ ] Party\n- [ ] Costco\n  - [ ] Meat');
    await user.click(screen.getByRole('textbox', { name: 'Item 2' }));
    await user.keyboard('{Tab}');
    expect(body()).toBe('- [ ] Party\n  - [ ] Costco\n    - [ ] Meat');
    expect(screen.getByRole('textbox', { name: 'Sub-item 3, level 3' })).toHaveValue('Meat');
    await user.keyboard('{Shift>}{Tab}{/Shift}');
    expect(body()).toBe('- [ ] Party\n- [ ] Costco\n  - [ ] Meat');
  });

  it('a grip drag 50 px right under a sub-item previews the third level and writes it once', () => {
    const { log, body } = mount(PARTY);
    const row = dragSideways('Plates', 50);
    expect(row).toHaveAttribute('data-preview-depth', '2');
    expect(log.bodies).toEqual([]);
    release(50);
    expect(log.bodies).toEqual(['- [ ] Party\n  - [ ] Costco\n    - [ ] Plates']);
    expect(body()).toBe('- [ ] Party\n  - [ ] Costco\n    - [ ] Plates');
    expect(log.saves).toBe(1);
    // One release, two levels: said once, where it ended.
    expect(screen.getByText('Made a sub-item of “Costco”')).toHaveAttribute('role', 'status');
    expect(screen.getByRole('button', { name: 'Move Plates' }).closest('li')).not.toHaveAttribute('data-preview-depth');
  });

  it('a drag further than the row may go stops at one under the row above', () => {
    const { log } = mount(PARTY);
    // Three steps asked for; one under Costco is the most there is.
    expect(dragSideways('Plates', 80)).toHaveAttribute('data-preview-depth', '2');
    release(80);
    expect(log.bodies).toEqual(['- [ ] Party\n  - [ ] Costco\n    - [ ] Plates']);
  });

  it('30 px left from the third level writes the second; far left writes the top in one release', () => {
    const DEEP = '- [ ] Party\n  - [ ] Costco\n    - [ ] Candles';
    const { log } = mount(DEEP);
    // The preview is the absolute depth: one out from the third is the second, not the top.
    expect(dragSideways('Candles', -30)).toHaveAttribute('data-preview-depth', '1');
    release(-30);
    expect(log.bodies).toEqual(['- [ ] Party\n  - [ ] Costco\n  - [ ] Candles']);
    expect(screen.getByText('Moved up a level, under “Party”')).toHaveAttribute('role', 'status');

    expect(dragSideways('Candles', -100)).toHaveAttribute('data-preview-depth', '0');
    release(-100);
    expect(log.bodies.at(-1)).toBe('- [ ] Party\n  - [ ] Costco\n- [ ] Candles');
    expect(screen.getByText('Now a top-level item')).toHaveAttribute('role', 'status');
  });

  it('a parent dragged sideways previews its sub-items at their new depths too', () => {
    const { log } = mount('- [ ] Top\n- [ ] Party\n  - [ ] Plates');
    const row = dragSideways('Party', 30);
    expect(row).toHaveAttribute('data-preview-depth', '1');
    // The child is not lifted, but release moves it, so it is drawn where it goes.
    const plates = screen.getByRole('textbox', { name: 'Sub-item 3' }).closest('li');
    expect(plates).toHaveAttribute('data-preview-depth', '2');
    expect(screen.getByRole('textbox', { name: 'Item 1' }).closest('li')).not.toHaveAttribute('data-preview-depth');
    release(30);
    expect(log.bodies).toEqual(['- [ ] Top\n  - [ ] Party\n    - [ ] Plates']);
  });

  it('a drag asking for more than the sub-items allow moves as far as fits; the arrow keys stay strict', async () => {
    const TWO = '- [ ] Top\n  - [ ] Sub\n- [ ] Costco\n  - [ ] Meat';
    const { log, body } = mount(TWO);
    // Two steps right would put Meat at a fourth level: one is what fits.
    expect(dragSideways('Costco', 50)).toHaveAttribute('data-preview-depth', '1');
    expect(screen.getByRole('textbox', { name: 'Sub-item 4' }).closest('li')).toHaveAttribute('data-preview-depth', '2');
    release(50);
    expect(log.bodies).toEqual(['- [ ] Top\n  - [ ] Sub\n  - [ ] Costco\n    - [ ] Meat']);
    // One more press would push Meat past the third level: refused, not cut down.
    const user = userEvent.setup();
    screen.getByRole('button', { name: 'Move Costco' }).focus();
    await user.keyboard('{ArrowRight}');
    expect(body()).toBe('- [ ] Top\n  - [ ] Sub\n  - [ ] Costco\n    - [ ] Meat');
    expect(screen.getByText('Its sub-items are already three levels deep')).toHaveAttribute('role', 'status');
  });

  it('20 px sideways is short of a step: no preview, nothing written', () => {
    const { log } = mount(PARTY);
    expect(dragSideways('Plates', 20)).not.toHaveAttribute('data-preview-depth');
    release(20);
    expect(log.bodies).toEqual([]);
    expect(log.saves).toBe(0);
  });

  it('the grip’s right and left arrows each move one level, and the third is the last', async () => {
    const user = userEvent.setup();
    const { body } = mount(PARTY);
    screen.getByRole('button', { name: 'Move Plates' }).focus();
    await user.keyboard('{ArrowRight}');
    expect(body()).toBe('- [ ] Party\n  - [ ] Costco\n  - [ ] Plates');
    await user.keyboard('{ArrowRight}');
    expect(body()).toBe('- [ ] Party\n  - [ ] Costco\n    - [ ] Plates');
    expect(screen.getByRole('button', { name: 'Move Plates' })).toHaveFocus();
    await user.keyboard('{ArrowRight}');
    expect(body()).toBe('- [ ] Party\n  - [ ] Costco\n    - [ ] Plates');
    expect(screen.getByText('Already three levels deep')).toHaveAttribute('role', 'status');
    await user.keyboard('{ArrowLeft}');
    expect(body()).toBe('- [ ] Party\n  - [ ] Costco\n  - [ ] Plates');
    expect(screen.getByText('Moved up a level, under “Party”')).toHaveAttribute('role', 'status');
  });

  it('refuses, and says why, an indent that would push a sub-item past the third level', async () => {
    const user = userEvent.setup();
    const DEEP = '- [ ] Top\n- [ ] Party\n  - [ ] Costco\n    - [ ] Plates';
    const { log, body } = mount(DEEP);
    const grip = screen.getByRole('button', { name: 'Move Party' });
    grip.focus();
    await user.keyboard('{ArrowRight}');
    expect(body()).toBe(DEEP);
    expect(log.bodies).toEqual([]);
    expect(screen.getByText('Its sub-items are already three levels deep')).toHaveAttribute('role', 'status');
    // No preview for a drag that would be refused, and nothing on release.
    expect(dragSideways('Party', 30)).not.toHaveAttribute('data-preview-depth');
    release(30);
    expect(log.bodies).toEqual([]);
    // The menu's item is disabled for the same reason.
    fireEvent.pointerDown(screen.getByRole('button', { name: 'Move Party' }), { ...mouse, clientX: 10, clientY: 0 });
    fireEvent.pointerUp(items(), { ...mouse, clientX: 10, clientY: 0 });
    expect(screen.getByRole('menuitem', { name: 'Make a sub-item' })).toBeDisabled();
    // From the field the Tab is the browser's, and nothing is said.
    await user.keyboard('{Escape}');
    const field = screen.getByRole('textbox', { name: 'Item 2' });
    await user.click(field);
    await user.keyboard('{Tab}');
    expect(field).not.toHaveFocus();
    expect(body()).toBe(DEEP);
  });
});

describe('the Done section', () => {
  it('is a disclosure, open by default, that remembers being closed for the session', async () => {
    const user = userEvent.setup();
    const { unmount } = mount(LIST, 'party');
    const toggle = screen.getByRole('button', { name: /Done \(1\)/ });
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    expect(within(doneSection()).getByRole('checkbox', { name: 'Eggs' })).toBeVisible();

    await user.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    expect(within(doneSection()).queryByRole('list')).toBeNull();
    expect(within(doneSection()).getByRole('list', { hidden: true })).not.toBeVisible();
    expect(sessionStorage.getItem(doneStorageKey('party'))).toBe('collapsed');

    // The heading still names the count, for the region and the page outline.
    expect(screen.getByRole('heading', { name: /Done \(1\)/ })).toBeInTheDocument();

    unmount();
    const again = mount(LIST, 'party');
    expect(screen.getByRole('button', { name: /Done \(1\)/ })).toHaveAttribute('aria-expanded', 'false');
    // Another note is not affected.
    again.unmount();
    mount(LIST, 'shopping');
    expect(screen.getByRole('button', { name: /Done \(1\)/ })).toHaveAttribute('aria-expanded', 'true');
  });

  it('Uncheck all reopens every item in one write and says so', async () => {
    const user = userEvent.setup();
    const { log, body } = mount('- [x] Milk\n- [x] Eggs\n- [ ] Bread');
    await user.click(screen.getByRole('button', { name: 'Uncheck all' }));
    expect(body()).toBe('- [ ] Milk\n- [ ] Eggs\n- [ ] Bread');
    expect(log.saves).toBe(1);
    expect(screen.getByText('All items reopened')).toHaveAttribute('role', 'status');
    expect(screen.queryByRole('region', { name: /^Done/ })).toBeNull();
  });

  it('Delete done drops the done lines, offers Undo in the toast, and Undo puts the body back', async () => {
    const user = userEvent.setup();
    const { log, body } = mount('- [x] Milk\n- [x] Eggs\n- [ ] Bread');
    await user.click(screen.getByRole('button', { name: 'Delete done' }));
    expect(body()).toBe('- [ ] Bread');
    expect(log.saves).toBe(1);
    expect(screen.queryByRole('region', { name: /^Done/ })).toBeNull();
    expect(screen.getByText('2 done items deleted', { selector: '.toast__text' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Undo' }));
    expect(body()).toBe('- [x] Milk\n- [x] Eggs\n- [ ] Bread');
    expect(log.saves).toBe(2);
    expect(screen.getByRole('heading', { name: /Done \(2\)/ })).toBeInTheDocument();
  });

  it('Undo after Delete done refuses when the list changed under it meanwhile, and says so', async () => {
    const user = userEvent.setup();
    const { log, body } = mount('- [x] Milk\n- [x] Eggs\n- [ ] Bread');
    await user.click(screen.getByRole('button', { name: 'Delete done' }));
    expect(body()).toBe('- [ ] Bread');
    // A recording filed into the list lands by refetch inside the six
    // seconds: the captured body written back would carry Jam away.
    act(() => {
      log.setBody('- [ ] Bread\n- [ ] Jam');
    });
    await user.click(screen.getByRole('button', { name: 'Undo' }));
    expect(log.bodies).toEqual(['- [ ] Bread']);
    expect(log.saves).toBe(1);
    expect(screen.getByText('The list changed since — nothing undone.', { selector: '.toast__text' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Undo' })).toBeNull();
  });

  it('Undo after Delete done still stands after the person’s own next act, and takes that back with it', async () => {
    const user = userEvent.setup();
    const { log, body } = mount('- [x] Milk\n- [x] Eggs\n- [ ] Bread');
    await user.click(screen.getByRole('button', { name: 'Delete done' }));
    // A tick's own Undo is weak: it does not take Delete done's place.
    await user.click(screen.getByRole('checkbox', { name: 'Bread' }));
    expect(body()).toBe('- [x] Bread');
    expect(screen.getByText('2 done items deleted', { selector: '.toast__text' })).toBeInTheDocument();
    // With the toast kept away, the tick is said by the status region instead.
    expect(screen.getByText('Marked done', { selector: '[aria-live]' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Undo' }));
    expect(body()).toBe('- [x] Milk\n- [x] Eggs\n- [ ] Bread');
    expect(log.saves).toBe(3);
  });

  it('a tick\'s Undo refuses after the person\'s own later edit, rather than undo it too', async () => {
    const user = userEvent.setup();
    const { log } = mount('- [ ] Milk\n- [ ] Bread');
    await user.click(screen.getByRole('checkbox', { name: 'Milk' }));
    await user.type(screen.getByRole('textbox', { name: 'Add an item' }), 'Jam{Enter}');
    await user.click(screen.getByRole('button', { name: 'Undo' }));
    expect(log.bodies.at(-1)).toBe('- [x] Milk\n- [ ] Bread\n- [ ] Jam');
    expect(screen.getByText('The list changed since — nothing undone.', { selector: '.toast__text' })).toBeInTheDocument();
  });

  it('a long item is cut short in the toast', async () => {
    const user = userEvent.setup();
    mount('- [ ] Call the plumber about the leaking tap under the kitchen sink before Friday');
    await user.click(screen.getByRole('checkbox', { name: /^Call the plumber/ }));
    expect(screen.getByText('Call the plumber about the leaking tap… done', { selector: '.toast__text' })).toBeInTheDocument();
  });

  it('a tick says "<item> done" with Undo, and Undo puts the row back where it was', async () => {
    const user = userEvent.setup();
    const { log, body } = mount('- [ ] Milk\n- [ ] Eggs\n- [ ] Bread');
    await user.click(screen.getByRole('checkbox', { name: 'Eggs' }));
    expect(body()).toBe('- [ ] Milk\n- [x] Eggs\n- [ ] Bread');
    expect(screen.getByText('Eggs done', { selector: '.toast__text' })).toBeInTheDocument();
    // The toast is a live region: the tick is said once, not twice.
    expect(screen.queryByText('Marked done')).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Undo' }));
    expect(body()).toBe('- [ ] Milk\n- [ ] Eggs\n- [ ] Bread');
    expect(log.saves).toBe(2);
    // Back in the open list once the hold (the motion token's length) is over.
    await waitFor(() => {
      expect(openValues()).toEqual(['Milk', 'Eggs', 'Bread', '']);
    });
  });

  it('a tick\'s Undo refuses when the list changed under it meanwhile', async () => {
    const user = userEvent.setup();
    const { log } = mount('- [ ] Milk\n- [ ] Bread');
    await user.click(screen.getByRole('checkbox', { name: 'Milk' }));
    act(() => {
      log.setBody('- [x] Milk\n- [ ] Bread\n- [ ] Jam');
    });
    await user.click(screen.getByRole('button', { name: 'Undo' }));
    expect(log.bodies).toEqual(['- [x] Milk\n- [ ] Bread']);
    expect(screen.getByText('The list changed since — nothing undone.', { selector: '.toast__text' })).toBeInTheDocument();
  });

  it('reopening a done item offers no Undo', async () => {
    const user = userEvent.setup();
    mount(LIST);
    await user.click(within(doneSection()).getByRole('checkbox', { name: 'Eggs' }));
    expect(screen.queryByRole('button', { name: 'Undo' })).toBeNull();
  });

  it('says "1 done item" for one', async () => {
    const user = userEvent.setup();
    mount(LIST);
    await user.click(screen.getByRole('button', { name: 'Delete done' }));
    expect(screen.getByText('1 done item deleted', { selector: '.toast__text' })).toBeInTheDocument();
  });
});

describe('the rows a recording added', () => {
  it('are marked while the banner\'s Show points at them', () => {
    render(
      <ChecklistEditor
        noteId="shopping"
        body={'- [ ] Milk\n- [ ] Bread'}
        currentBody={() => ''}
        flash={new Set(['Bread'])}
        onChange={() => {}}
        onSave={() => {}}
      />,
    );
    const rows = within(items()).getAllByRole('listitem');
    expect(rows[0]).not.toHaveAttribute('data-flash');
    expect(rows[1]).toHaveAttribute('data-flash');
  });
});

describe('parseMotionMs', () => {
  it('reads the token in milliseconds or seconds, as the built sheet minifies it', () => {
    expect(parseMotionMs('220ms')).toBe(220);
    expect(parseMotionMs('.22s')).toBe(220);
    expect(parseMotionMs(' 0.5s ')).toBe(500);
    expect(parseMotionMs('1ms')).toBe(1);
    expect(parseMotionMs('')).toBe(220);
  });
});
