/**
 * Transcription languages, as the two language controls offer them.
 *
 * Whisper takes an ISO-639-1 code and does better, and faster, when told one;
 * omitted, it detects a language per recording. The API mirrors that exactly:
 * a note's `language` or the tenant's `default_language` is a two-letter code
 * or `auto`, and a note may also hold the empty string to inherit the default
 * again (`docs/api/openapi.yaml`, `Note.language` and `Settings.default_language`).
 *
 * The list is curated rather than every ISO code: a select with a hundred
 * entries is a search box in disguise. It holds the languages of the owner's
 * household first — the Indian languages Whisper transcribes — then the
 * widely spoken ones, each with its name in its own script beside the English
 * one, so a speaker scanning the list finds their language by sight. Any code
 * the server holds that is not listed is still shown, by its `Intl` name, so a
 * value set elsewhere is never rendered as blank.
 */

export const AUTO_LANGUAGE = 'auto';

export interface Language {
  code: string;
  /** The English name, as the meta line and the settings sentence say it. */
  name: string;
  /** The name in its own language and script — the same as `name` for English. */
  native: string;
}

export const LANGUAGES: readonly Language[] = [
  { code: 'en', name: 'English', native: 'English' },
  { code: 'ml', name: 'Malayalam', native: 'മലയാളം' },
  { code: 'hi', name: 'Hindi', native: 'हिन्दी' },
  { code: 'ta', name: 'Tamil', native: 'தமிழ்' },
  { code: 'te', name: 'Telugu', native: 'తెలుగు' },
  { code: 'kn', name: 'Kannada', native: 'ಕನ್ನಡ' },
  { code: 'mr', name: 'Marathi', native: 'मराठी' },
  { code: 'bn', name: 'Bengali', native: 'বাংলা' },
  { code: 'gu', name: 'Gujarati', native: 'ગુજરાતી' },
  { code: 'pa', name: 'Punjabi', native: 'ਪੰਜਾਬੀ' },
  { code: 'ur', name: 'Urdu', native: 'اردو' },
  { code: 'ar', name: 'Arabic', native: 'العربية' },
  { code: 'es', name: 'Spanish', native: 'Español' },
  { code: 'fr', name: 'French', native: 'Français' },
  { code: 'de', name: 'German', native: 'Deutsch' },
  { code: 'pt', name: 'Portuguese', native: 'Português' },
  { code: 'it', name: 'Italian', native: 'Italiano' },
  { code: 'nl', name: 'Dutch', native: 'Nederlands' },
  { code: 'ru', name: 'Russian', native: 'Русский' },
  { code: 'ja', name: 'Japanese', native: '日本語' },
  { code: 'ko', name: 'Korean', native: '한국어' },
  { code: 'zh', name: 'Mandarin Chinese', native: '中文' },
  { code: 'id', name: 'Indonesian', native: 'Bahasa Indonesia' },
  { code: 'tr', name: 'Turkish', native: 'Türkçe' },
  { code: 'vi', name: 'Vietnamese', native: 'Tiếng Việt' },
  { code: 'th', name: 'Thai', native: 'ไทย' },
  { code: 'sv', name: 'Swedish', native: 'Svenska' },
  { code: 'pl', name: 'Polish', native: 'Polski' },
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

/**
 * The entry as the select shows it: "മലയാളം · Malayalam" — the language's
 * own name first, the English one after, and just the one where they are the
 * same or where only the runtime's name is known.
 */
export function languageLabel(code: string): string {
  const known = LANGUAGES.find((language) => language.code === code);
  if (known && known.native !== known.name) return `${known.native} · ${known.name}`;
  return languageName(code);
}
