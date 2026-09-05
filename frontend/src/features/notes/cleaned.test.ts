import { describe, expect, it } from 'vitest';

import type { CleanedWire } from '@/api/schema.ts';

import { cleanSettled, cleanedDocument, cleanedMarkdown } from './cleaned.ts';
import { describeAgo } from './groups.ts';

const VIEW: CleanedWire = {
  body: '# Roof repair\n\n- Ridge tiles.',
  mode: 'structured',
  generated_at: '2026-09-04T10:00:00.000Z',
  stale: false,
};

describe('cleanSettled — when to stop asking after a 202', () => {
  it('is settled when a view appears where there was none', () => {
    expect(cleanSettled(null, VIEW)).toBe(true);
    expect(cleanSettled(null, null)).toBe(false);
    expect(cleanSettled(null, undefined)).toBe(false);
  });

  it('is settled when the generation time moves', () => {
    expect(cleanSettled(VIEW, VIEW)).toBe(false);
    expect(cleanSettled(VIEW, { ...VIEW, generated_at: '2026-09-04T10:00:05.000Z' })).toBe(true);
  });

  it('is settled when a stale view is made current, even at the same time', () => {
    expect(cleanSettled({ ...VIEW, stale: true }, VIEW)).toBe(true);
    expect(cleanSettled({ ...VIEW, stale: true }, { ...VIEW, stale: true })).toBe(false);
  });

  it('stops early on an error the backend reports', () => {
    expect(cleanSettled(VIEW, { ...VIEW, error: 'The provider refused the request.' })).toBe(true);
  });
});

describe('the cleaned view as a document to copy or save', () => {
  it('leads with the title unless the view already has one', () => {
    expect(cleanedDocument('Roof repair', 'Ridge tiles.\n')).toBe('Roof repair\n\nRidge tiles.');
    expect(cleanedDocument('Roof repair', VIEW.body)).toBe(VIEW.body);
    expect(cleanedMarkdown('Roof repair', 'Ridge tiles.')).toBe('# Roof repair\n\nRidge tiles.\n');
    expect(cleanedMarkdown('Roof repair', VIEW.body)).toBe(`${VIEW.body}\n`);
    expect(cleanedMarkdown('', 'Ridge tiles.')).toBe('Ridge tiles.\n');
  });
});

describe('describeAgo', () => {
  // Noon local time, so "yesterday" is the same calendar day in every zone.
  const now = new Date(2026, 8, 4, 12).getTime();
  const ago = (ms: number): string => describeAgo(new Date(now - ms).toISOString(), now);

  it('counts up from just now through minutes, hours and days', () => {
    expect(ago(10_000)).toBe('just now');
    expect(ago(60_000)).toBe('1 minute ago');
    expect(ago(3 * 60_000)).toBe('3 minutes ago');
    expect(ago(2 * 3_600_000)).toBe('2 hours ago');
    expect(ago(30 * 3_600_000)).toBe('yesterday');
    expect(ago(3 * 86_400_000)).toBe('3 days ago');
  });

  it('gives a date once it is more than a week', () => {
    // "on 15 Aug" or "on Aug 15", as the locale writes it.
    expect(ago(20 * 86_400_000)).toMatch(/^on (\d{1,2} \w{3}|\w{3} \d{1,2})$/);
    expect(describeAgo('not a date')).toBe('');
  });
});
