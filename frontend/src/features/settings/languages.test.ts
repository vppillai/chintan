import { describe, expect, it } from 'vitest';

import { AUTO_LANGUAGE, LANGUAGES, isLanguageCode, languageName } from './languages.ts';

describe('the curated language list', () => {
  it('holds the household languages and the usual suspects, each once', () => {
    const codes = LANGUAGES.map((language) => language.code);
    for (const wanted of ['en', 'ml', 'hi', 'ta', 'te', 'kn', 'es', 'fr', 'de', 'pt', 'ja', 'zh', 'ar', 'ko', 'it', 'ru']) {
      expect(codes).toContain(wanted);
    }
    expect(new Set(codes).size).toBe(codes.length);
    // Every entry is something the API accepts.
    for (const code of codes) expect(isLanguageCode(code)).toBe(true);
  });

  it('accepts exactly what the contract does', () => {
    expect(isLanguageCode(AUTO_LANGUAGE)).toBe(true);
    expect(isLanguageCode('ml')).toBe(true);
    expect(isLanguageCode('')).toBe(false);
    expect(isLanguageCode('eng')).toBe(false);
    expect(isLanguageCode('EN')).toBe(false);
  });
});

describe('naming a code', () => {
  it('uses the curated name, and a word for auto', () => {
    expect(languageName('ml')).toBe('Malayalam');
    expect(languageName(AUTO_LANGUAGE)).toBe('Auto-detect');
  });

  it('never renders a code the list does not know as blank', () => {
    // Set by hand elsewhere, say; the control still shows the value it is in.
    const name = languageName('sv');
    expect(name.length).toBeGreaterThan(0);
    expect(name).not.toBe('');
  });
});
