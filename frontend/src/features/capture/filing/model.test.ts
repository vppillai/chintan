import { describe, expect, it } from 'vitest';

import { CAPTURE_STATUSES, isTerminalStatus } from '@/api/schema.ts';
import { STUCK_CREATED_AT, capture } from '@/test/filing.tsx';

import { STAGES, describeAgoShort, groupReceipts, noticeKind, stageIndex, tierCaptures } from './model.ts';

describe('stageIndex', () => {
  it('lights a segment for every status the pipeline can leave a capture in', () => {
    // `transcribed` and `cleaned` are the hand-offs between stages, and the
    // first is where an inbox text capture starts. Unknown to the strip they
    // drew every segment done with no label (live QA 2026-09-26, finding 1).
    for (const status of CAPTURE_STATUSES.filter((s) => !isTerminalStatus(s))) {
      expect(stageIndex(status), status).toBeLessThan(STAGES.length);
    }
    expect(STAGES[stageIndex('transcribed')]?.label).toBe('Filing');
    expect(STAGES[stageIndex('cleaned')]?.label).toBe('Saving');
    // A finished capture is past the strip: every segment done is the truth.
    expect(stageIndex('appended')).toBe(STAGES.length);
  });
});

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

  it('marks a group Started when one of its captures made the note, and carries the newest excerpt', () => {
    const [group] = groupReceipts([
      capture({ id: 'b', status: 'appended', note_id: 'n', appended_at: at('10:05'), excerpt: 'second' }),
      capture({ id: 'a', status: 'appended', note_id: 'n', appended_at: at('10:00'), created_note: true, excerpt: 'first' }),
    ]);
    expect(group?.createdNote).toBe(true);
    expect(group?.excerpt).toBe('second');
    expect(groupReceipts([capture({ status: 'appended', note_id: 'n' })])[0]).toMatchObject({
      createdNote: false,
      excerpt: null,
    });
  });
});

describe('describeAgoShort', () => {
  const now = Date.parse('2026-09-30T12:00:00.000Z');
  const ago = (ms: number) => describeAgoShort(new Date(now - ms).toISOString(), now);

  it('fits beside a one-line receipt', () => {
    expect(ago(20_000)).toBe('now');
    expect(ago(2 * 60_000)).toBe('2 min');
    expect(ago(3 * 3_600_000)).toBe('3 h');
    expect(ago(4 * 86_400_000)).toBe('4 d');
    expect(describeAgoShort('not a date', now)).toBe('');
  });
});

describe('noticeKind', () => {
  it('names the glyph for every status, a stuck capture and a started note', () => {
    const kinds = Object.fromEntries(
      CAPTURE_STATUSES.map((status) => [status, noticeKind(capture({ status }))]),
    );
    expect(kinds).toMatchObject({
      uploaded: 'moving',
      transcribing: 'moving',
      appending: 'moving',
      needs_target: 'needs',
      failed: 'failed',
      spend_capped: 'failed',
      no_content: 'failed',
      appended: 'filed',
    });
    // Stuck: still moving on the wire, but it needs the person now.
    expect(noticeKind(capture({ status: 'transcribing', created_at: STUCK_CREATED_AT }))).toBe('failed');
    const [group] = groupReceipts([capture({ status: 'appended', note_id: 'n1', created_note: true })]);
    expect(group && noticeKind(group)).toBe('started');
    const [filed] = groupReceipts([capture({ status: 'appended', note_id: 'n1' })]);
    expect(filed && noticeKind(filed)).toBe('filed');
  });
});

/** The tray's tiers, as `FilingRow` draws them: moving, needs the person, then the receipts. */
describe('tierCaptures', () => {
  it('sorts moving, stuck and failed rows, and shows one receipt as itself but folds two', () => {
    const moving = capture({ id: 'moving' });
    const stuck = capture({ id: 'stuck', created_at: STUCK_CREATED_AT });
    const failed = capture({ id: 'failed', status: 'failed' });
    const filed = capture({ id: 'filed', status: 'appended', note_id: 'n1' });
    const one = tierCaptures([moving, stuck, failed, filed]);
    expect(one.moving.map((row) => row.id)).toEqual(['moving']);
    // A stuck capture is non-terminal but needs the person, with the failed ones.
    expect(one.needsYou.map((row) => row.id)).toEqual(['stuck', 'failed']);
    expect(one.shown.map((group) => group.noteId)).toEqual(['n1']);
    expect(one.folded).toEqual([]);

    const two = tierCaptures([filed, capture({ id: 'filed-2', status: 'appended', note_id: 'n2' })]);
    expect(two.shown).toEqual([]);
    expect(two.folded.map((group) => group.noteId).sort()).toEqual(['n1', 'n2']);
    expect(two.groups).toBe(two.folded);
  });
});
