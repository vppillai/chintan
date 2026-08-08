/**
 * Typed payloads for the offline queue.
 *
 * A queued mutation is deserialised from IndexedDB, so it arrives as
 * `unknown`. Giving each kind a declared payload and one narrowing function
 * means the runner is exhaustively checked at compile time instead of casting
 * its way through `Record<string, never>`.
 */

import type { CaptureTargetWire, NoteUpdateWire } from '@/api/schema.ts';
import type { QueuedMutation, QueuedMutationKind } from './db.ts';

export interface MutationPayloads {
  updateNote: { noteId: string; body: NoteUpdateWire };
  archiveNote: { noteId: string };
  restoreNote: { noteId: string };
  setCaptureTarget: { captureId: string; target: CaptureTargetWire };
  retryCapture: { captureId: string };
  /** Placeholder: capture uploads resume from disk, not from this queue. */
  createCapture: { localId: string };
}

export type TypedMutation = {
  [K in QueuedMutationKind]: { kind: K; payload: MutationPayloads[K] } & Omit<
    QueuedMutation,
    'kind' | 'payload'
  >;
}[QueuedMutationKind];

function hasString(value: unknown, key: string): boolean {
  return (
    typeof value === 'object' &&
    value !== null &&
    typeof (value as Record<string, unknown>)[key] === 'string'
  );
}

/**
 * Narrows a stored mutation, or returns null when its payload does not match
 * the shape its kind promises — which happens if an older build wrote it.
 * A malformed entry is dropped rather than replayed with missing fields.
 */
export function narrow(mutation: QueuedMutation): TypedMutation | null {
  const { payload } = mutation;
  switch (mutation.kind) {
    case 'updateNote':
      return hasString(payload, 'noteId') ? (mutation as TypedMutation) : null;
    case 'archiveNote':
    case 'restoreNote':
      return hasString(payload, 'noteId') ? (mutation as TypedMutation) : null;
    case 'setCaptureTarget':
    case 'retryCapture':
      return hasString(payload, 'captureId') ? (mutation as TypedMutation) : null;
    case 'createCapture':
      return hasString(payload, 'localId') ? (mutation as TypedMutation) : null;
    default:
      return null;
  }
}
