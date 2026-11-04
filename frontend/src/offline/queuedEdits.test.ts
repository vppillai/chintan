import { IDBFactory } from 'fake-indexeddb';
import { beforeEach, describe, expect, it } from 'vitest';

import { resetDatabaseHandle } from './db.ts';
import { enqueueReplacing } from './queue.ts';
import { clearQueuedEdit, queuedEditBody, queuedEditId } from './queuedEdits.ts';

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  resetDatabaseHandle();
});

const edit = (body: string) =>
  enqueueReplacing({
    id: queuedEditId('n1'),
    kind: 'updateNote',
    payload: { noteId: 'n1', body: { version: 5, body } },
  });

describe('forgetting a queued edit a direct save superseded', () => {
  it('keeps an entry queued after the one the save carried', async () => {
    const carried = await edit('first');
    await edit('second');

    expect(await clearQueuedEdit('n1', carried.idempotencyKey)).toBe(false);
    expect((await queuedEditBody('n1'))?.body).toBe('second');
  });

  it('forgets the entry the save carried, and any entry when none is named', async () => {
    const carried = await edit('first');
    expect(await clearQueuedEdit('n1', carried.idempotencyKey)).toBe(true);
    expect(await queuedEditBody('n1')).toBeNull();

    await edit('again');
    expect(await clearQueuedEdit('n1')).toBe(true);
    expect(await queuedEditBody('n1')).toBeNull();
  });
});
