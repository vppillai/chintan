import { act, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it } from 'vitest';

import { Toast, dismissToast } from '@/components/Toast.tsx';

import { ChecklistEditor, doneStorageKey, parseMotionMs } from './ChecklistEditor.tsx';
import { initialEditor, type NoteDraft } from './autosave.ts';
import type { NoteEditor } from './useNoteEditor.ts';

/**
 * The editor against a stand-in for `useNoteEditor` that applies every
 * `edit({ body })` and counts every `saveNow()`, so what these assert is the
 * body the real editor would have been handed — the one thing this component
 * exists to produce — and when it would have been asked to save. `setBody`
 * is the outside world changing the note under the editor, as a refetch does.
 */
function mount(initial: string, noteId = 'shopping') {
  const log = { bodies: [] as string[], saves: 0, setBody: (_next: string): void => {} };
  function Harness() {
    const [body, setBody] = useState(initial);
    log.setBody = setBody;
    const editor: NoteEditor = {
      model: initialEditor(
        { title: 'Shopping', body, aliases: [], tags: [], kind: 'checklist' },
        1,
      ),
      edit: (patch: Partial<NoteDraft>) => {
        if (typeof patch.body !== 'string') return;
        log.bodies.push(patch.body);
        setBody(patch.body);
      },
      saveNow: async () => {
        log.saves += 1;
      },
      takeTheirs: () => {},
      keepMine: () => {},
      keepBoth: () => {},
    };
    return <ChecklistEditor editor={editor} noteId={noteId} />;
  }
  const view = render(
    <>
      <Harness />
      {/* The shell's, in the app; here so the Undo that Delete done offers can be pressed. */}
      <Toast />
    </>,
  );
  return { log, body: () => log.bodies.at(-1) ?? initial, unmount: view.unmount };
}

afterEach(() => {
  dismissToast();
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
    expect(screen.getByText('Marked done')).toHaveAttribute('role', 'status');
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
    // The click the browser fires as the mouse lifts is not a tap on the grip.
    fireEvent.click(screen.getByRole('button', { name: 'Move Milk' }));
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
      'Delete',
    ]);
    // The top row cannot move up.
    expect(within(menu).getByRole('menuitem', { name: 'Move up' })).toBeDisabled();
    expect(within(menu).getByRole('menuitem', { name: 'Move to top' })).toBeDisabled();

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

  it('says "1 done item" for one', async () => {
    const user = userEvent.setup();
    mount(LIST);
    await user.click(screen.getByRole('button', { name: 'Delete done' }));
    expect(screen.getByText('1 done item deleted', { selector: '.toast__text' })).toBeInTheDocument();
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
