import type { Page } from '@playwright/test';

/**
 * Talks to the real API as the signed-in test user, using the token the app
 * itself stored, so a spec can seed or inspect data without going through the
 * UI it is trying to judge.
 */
export const API = 'https://3kg2xg9khf.execute-api.us-west-2.amazonaws.com';

async function bearer(page: Page): Promise<string> {
  // localStorage is only readable once the page is on the app's origin.
  if (!page.url().startsWith('http')) await page.goto('./');
  const raw = await page.evaluate(() => window.localStorage.getItem('chintan.tokens.v2'));
  if (!raw) throw new Error('no token set in localStorage — is the storageState stale?');
  const parsed = JSON.parse(raw) as { idToken: string };
  return `Bearer ${parsed.idToken}`;
}

export interface SeedNote {
  title: string;
  body?: string;
  tags?: string[];
}

export interface NoteWire {
  id: string;
  title: string;
  tags?: string[];
  snippet?: string;
  archived: boolean;
  version: number;
}

export async function createNote(page: Page, note: SeedNote): Promise<NoteWire> {
  const res = await page.request.post(`${API}/v1/notes`, {
    headers: {
      Authorization: await bearer(page),
      'Idempotency-Key': `qa-${Date.now()}-${Math.random().toString(36).slice(2)}`,
    },
    data: note,
  });
  if (!res.ok()) throw new Error(`POST /v1/notes ${res.status()}: ${await res.text()}`);
  return (await res.json()) as NoteWire;
}

export async function listNotes(page: Page, state: 'active' | 'archived' = 'active'): Promise<NoteWire[]> {
  const res = await page.request.get(`${API}/v1/notes?state=${state}`, {
    headers: { Authorization: await bearer(page) },
  });
  return ((await res.json()) as { items: NoteWire[] }).items;
}

export async function getNote(page: Page, id: string): Promise<Record<string, unknown>> {
  const res = await page.request.get(`${API}/v1/notes/${encodeURIComponent(id)}`, {
    headers: { Authorization: await bearer(page) },
  });
  return (await res.json()) as Record<string, unknown>;
}

export async function listCaptures(page: Page): Promise<Record<string, unknown>[]> {
  const res = await page.request.get(`${API}/v1/captures?status=all`, {
    headers: { Authorization: await bearer(page) },
  });
  return ((await res.json()) as { items: Record<string, unknown>[] }).items;
}
