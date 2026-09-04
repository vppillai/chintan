/**
 * Transcription languages, as the two language controls offer them.
 *
 * Whisper takes an ISO-639-1 code and does better, and faster, when told one;
 * omitted, it detects a language per recording. The API mirrors that exactly:
 * a note's `language` or the tenant's `default_language` is a two-letter code
 * or `auto`, and a note may also hold the empty string to inherit the default
 * again (`docs/api/openapi.yaml`, `Note.language` and `Settings.default_language`).
 *
 * The list is curated rather than every ISO code: a select with a hundred and
 * eighty entries is a search box in disguise, and the owner's household speaks
 * a handful. Any code the server holds that is not listed is still shown, by
 * its `Intl` name, so a value set elsewhere is never rendered as blank.
 */

export const AUTO_LANGUAGE = 'auto';

export interface Language {
  code: string;
  name: string;
}

export const LANGUAGES: readonly Language[] = [
  { code: 'en', name: 'English' },
  { code: 'ml', name: 'Malayalam' },
  { code: 'hi', name: 'Hindi' },
  { code: 'ta', name: 'Tamil' },
  { code: 'te', name: 'Telugu' },
  { code: 'kn', name: 'Kannada' },
  { code: 'es', name: 'Spanish' },
  { code: 'fr', name: 'French' },
  { code: 'de', name: 'German' },
  { code: 'pt', name: 'Portuguese' },
  { code: 'it', name: 'Italian' },
  { code: 'ru', name: 'Russian' },
  { code: 'ja', name: 'Japanese' },
  { code: 'zh', name: 'Chinese' },
  { code: 'ko', name: 'Korean' },
  { code: 'ar', name: 'Arabic' },
];

/** `true` for a value the API accepts in either language field. */
export function isLanguageCode(value: string): boolean {
  return value === AUTO_LANGUAGE || /^[a-z]{2}$/.test(value);
}

/**
 * A code, said in words. "Auto-detect" for `auto`; the curated name where there
 * is one; otherwise whatever the runtime knows the language as, and failing
 * that the code itself — never an empty label.
 */
export function languageName(code: string): string {
  if (code === AUTO_LANGUAGE) return 'Auto-detect';
  const known = LANGUAGES.find((language) => language.code === code);
  if (known) return known.name;
  try {
    const name = new Intl.DisplayNames(undefined, { type: 'language' }).of(code);
    if (name && name !== code) return name;
  } catch {
    /* An unrecognised tag; fall through to the code. */
  }
  return code;
}
