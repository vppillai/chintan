import { describe, expect, it } from 'vitest';

import { AUTO_LANGUAGE, LANGUAGES, isLanguageCode, languageLabel, languageName } from './languages.ts';

describe('the curated language list', () => {
  it('holds the household languages and the usual suspects, each once', () => {
    const codes = LANGUAGES.map((language) => language.code);
    for (const wanted of [
      'en', 'ml', 'hi', 'ta', 'te', 'kn', 'mr', 'bn', 'gu', 'pa', 'ur', 'ar', 'es', 'fr', 'de', 'pt',
      'it', 'nl', 'ru', 'ja', 'ko', 'zh', 'id', 'tr', 'vi', 'th', 'sv', 'pl',
    ]) {
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
    const name = languageName('cy');
    expect(name.length).toBeGreaterThan(0);
    expect(name).not.toBe('');
  });

  it('labels a listed language in its own script and in English, and English once', () => {
    expect(languageLabel('ml')).toBe('മലയാളം · Malayalam');
    expect(languageLabel('hi')).toBe('हिन्दी · Hindi');
    expect(languageLabel('en')).toBe('English');
    expect(languageLabel(AUTO_LANGUAGE)).toBe('Auto-detect');
    // Every listed language has a native name to show.
    for (const language of LANGUAGES) expect(language.native.length).toBeGreaterThan(0);
  });
});
