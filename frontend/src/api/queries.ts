/**
 * TanStack Query bindings. Server state lives here; nothing else caches it.
 *
 * One module per resource under `queries/`, re-exported whole so every
 * importer keeps `@/api/queries.ts`. `keys.ts` holds the query keys and the
 * invalidations the resources share.
 */

export * from './queries/keys.ts';
export * from './queries/notes.ts';
export * from './queries/settings.ts';
export * from './queries/ask.ts';
export * from './queries/captures.ts';
export * from './queries/devices.ts';
export * from './queries/push.ts';
