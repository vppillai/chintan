import { readFileSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';
import process from 'node:process';

import { describe, expect, it } from 'vitest';

/*
 * The accent is a fill and the record state's colour (tokens.css). Two ways
 * it was misused, both invisible to the axe sweep because the states never
 * render there: as text, where Ink & Paper's #c2410c is 4.47:1 on the
 * surface, and as the fill of Delete, where Nocturne's is the record lime.
 */
const here = resolve(process.cwd(), 'src', 'styles');
const sheets: Record<string, string> = Object.fromEntries(
  readdirSync(here)
    .filter((file) => file.endsWith('.css'))
    .map((file) => [`./${file}`, readFileSync(resolve(here, file), 'utf8')]),
);

/** Every `selector { … }` block whose declarations include `declaration`. */
function rulesWith(declaration: RegExp): string[] {
  return Object.entries(sheets).flatMap(([file, css]) =>
    Array.from(css.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/([^{}]+)\{([^{}]*)\}/g))
      .filter(([, , body]) => declaration.test(body ?? ''))
      .map(([, selector]) => `${file} ${(selector ?? '').trim()}`),
  );
}

describe('the accent', () => {
  it('is never the colour of text, only of the waveform it draws', () => {
    expect(rulesWith(/(^|[;\s])color:\s*var\(--color-accent\)/)).toEqual(['./shell.css .waveform']);
  });

  it('is never the fill of a destructive control', () => {
    expect(rulesWith(/background-color:\s*var\(--color-accent(-strong)?\)/).filter((rule) => /destructive/.test(rule))).toEqual([]);
  });
});

describe('Nocturne', () => {
  const tokens = sheets['./tokens.css'] ?? '';
  const nocturne = tokens.slice(tokens.indexOf("[data-theme='nocturne'] {"));
  const value = (name: string): string | undefined =>
    new RegExp(`${name}:\\s*([^;]+);`).exec(nocturne)?.[1]?.trim();

  it('says destructive in its own colour, not the record lime and not body ink', () => {
    expect(value('--color-destructive')).not.toMatch(/accent|ink/);
    expect(value('--color-destructive')).not.toBe(value('--color-accent'));
  });
});

describe('the smallest size', () => {
  it('is for metadata, never for a control or a sentence asking for one', () => {
    // An error, a Retry, a toggle or a dismiss is what the person acts on;
    // at 11 px on a phone in daylight it was the hardest thing on screen to read.
    expect(
      rulesWith(/font-size:\s*var\(--font-size-xs\)/).filter((rule) =>
        /error|retry|dismiss|toggle-option|save-indicator/.test(rule),
      ),
    ).toEqual([]);
  });
});
