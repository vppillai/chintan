import { describe, expect, it } from 'vitest';

import { capture } from '@/test/filing.tsx';

import { groupReceipts } from './model.ts';

/**
 * One receipt per note. The row that draws the groups is tested through the
 * poll in `FilingRow.test.tsx`; this is the grouping alone.
 */
describe('groupReceipts', () => {
  const at = (iso: string) => `2026-09-26T${iso}:00.000Z`;

  it('makes one group per note, newest landing first, and counts the captures', () => {
    const groups = groupReceipts([
      capture({ id: 'k2', status: 'appended', note_id: 'kitchen', appended_at: at('10:05') }),
      capture({ id: 'r1', status: 'appended', note_id: 'roof', appended_at: at('10:09') }),
      capture({ id: 'k1', status: 'appended', note_id: 'kitchen', appended_at: at('10:01') }),
      capture({ id: 's1', status: 'appended', note_id: 'shop', appended_at: at('09:00') }),
    ]);
    expect(groups.map((group) => group.noteId)).toEqual(['roof', 'kitchen', 'shop']);
    expect(groups[1]?.captureIds).toEqual(['k2', 'k1']);
    expect(groups[1]?.latestAt).toBe(at('10:05'));
  });

  it('keys the row by the first capture in server order, so the one that just landed keeps its node', () => {
    const [group] = groupReceipts([
      capture({ id: 'newest', status: 'appended', note_id: 'n', appended_at: at('12:00') }),
      capture({ id: 'older', status: 'appended', note_id: 'n', appended_at: at('11:00') }),
    ]);
    expect(group?.newestId).toBe('newest');
  });

  it('orders by the latest landing in the group, not its first row', () => {
    // Server order is newest first, but a group's row is wherever its
    // newest capture is; the group with the most recent landing leads.
    const groups = groupReceipts([
      capture({ id: 'a1', status: 'appended', note_id: 'a', appended_at: at('10:00') }),
      capture({ id: 'b1', status: 'appended', note_id: 'b', appended_at: at('09:00') }),
      capture({ id: 'b2', status: 'appended', note_id: 'b', appended_at: at('10:30') }),
    ]);
    expect(groups.map((group) => group.noteId)).toEqual(['b', 'a']);
  });

  it('prefers appended_at over created_at, and falls back to created_at without it', () => {
    const groups = groupReceipts([
      capture({ id: 'x', status: 'appended', note_id: 'x', created_at: at('08:00') }),
      capture({
        id: 'y',
        status: 'appended',
        note_id: 'y',
        created_at: at('07:00'),
        appended_at: at('08:30'),
      }),
    ]);
    expect(groups.map((group) => group.noteId)).toEqual(['y', 'x']);
    expect(groups[1]?.latestAt).toBe(at('08:00'));
  });

  it('skips everything that is not an appended capture with a note', () => {
    expect(
      groupReceipts([
        capture({ id: 'moving', status: 'transcribing', note_id: 'n' }),
        capture({ id: 'failed', status: 'failed', note_id: 'n' }),
        capture({ id: 'nowhere', status: 'appended' }),
      ]),
    ).toEqual([]);
  });
});
