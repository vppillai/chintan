import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it } from 'vitest';

import { ChecklistEditor, parseMotionMs } from './ChecklistEditor.tsx';
import { initialEditor, type NoteDraft } from './autosave.ts';
import type { NoteEditor } from './useNoteEditor.ts';

/**
 * The editor against a stand-in for `useNoteEditor` that applies every
 * `edit({ body })` and counts every `saveNow()`, so what these assert is the
 * body the real editor would have been handed — the one thing this component
 * exists to produce — and when it would have been asked to save.
 */
function mount(initial: string) {
  const log = { bodies: [] as string[], saves: 0 };
  function Harness() {
    const [body, setBody] = useState(initial);
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
    return <ChecklistEditor editor={editor} />;
  }
  render(<Harness />);
  return { log, body: () => log.bodies.at(-1) ?? initial };
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
    expect(screen.getByRole('status')).toHaveTextContent('Marked done');
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
    expect(screen.getByRole('status')).toHaveTextContent('Reopened');
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

describe('parseMotionMs', () => {
  it('reads the token in milliseconds or seconds, as the built sheet minifies it', () => {
    expect(parseMotionMs('220ms')).toBe(220);
    expect(parseMotionMs('.22s')).toBe(220);
    expect(parseMotionMs(' 0.5s ')).toBe(500);
    expect(parseMotionMs('1ms')).toBe(1);
    expect(parseMotionMs('')).toBe(220);
  });
});
