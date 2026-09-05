import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { Link } from 'react-router';

import { useSaveSettings, useSettings } from '@/api/queries.ts';
import type { CleanupMode, SettingsWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';
import { LanguageSelect } from '@/components/LanguageSelect.tsx';
import { THEME_LABELS, THEME_PREFERENCES, type ThemePreference } from '@/theme/theme.ts';
import { useTheme } from '@/theme/useTheme.ts';

import { PasskeyCard } from '@/features/auth/PasskeyCard.tsx';
import { SignOutSetting } from '@/features/auth/SignOutSetting.tsx';

import { UsageSection } from './UsageSection.tsx';
import { VersionFootnote } from './VersionFootnote.tsx';
import { AUTO_LANGUAGE, languageName } from './languages.ts';

const CLEANUP_LABELS: Record<CleanupMode, string> = {
  faithful: 'Faithful — fix only what was clearly misheard',
  polished: 'Polished — tidy the wording as well',
};

const DEFAULTS: SettingsWire = {
  cleanup_mode: 'faithful',
  retention_days: 0,
  theme: 'ink',
  default_language: 'en',
  daily_spend_cap_micros: 0,
};

/**
 * How long the retention field waits after a keystroke before it is saved.
 * The other controls are one tap and save at once; a number being typed
 * would otherwise PUT "3" on the way to "30".
 */
export const RETENTION_SAVE_DELAY_MS = 600;

/** How long the "Saved" tick stays. */
const SAVED_TICK_MS = 2_500;

/**
 * You.
 *
 * Every control saves itself the moment it is changed — a tap on Polished is
 * a PUT, a theme is applied and then saved, a retention number goes once the
 * typing pauses. There is no Save button and nothing is ever "unsaved": the
 * screen used to hold a draft behind a Save/Discard pair, and tapping another
 * tab with the draft dirty lost it silently (QA D9), while the theme —
 * applied on the device at once — read "Unsaved changes" and then, after a
 * reload, "All changes saved" for a value the server had never been sent
 * (QA D13). The status line now only ever says what is happening: Saving…, a
 * brief Saved, or the failure with a way to try again.
 */
export function SettingsScreen() {
  const { preference, resolved, setPreference } = useTheme();
  const { data: stored, isLoading } = useSettings();
  const save = useSaveSettings();

  /*
   * What the controls show: the stored record, plus whatever the user has
   * changed since — a change is on screen before its PUT returns, and stays
   * there if the PUT fails, with the failure said beside it.
   */
  const [draft, setDraft] = useState<SettingsWire>(DEFAULTS);
  const loadedRef = useRef(false);
  const latest = useRef(draft);
  useEffect(() => {
    if (!stored || loadedRef.current) return;
    loadedRef.current = true;
    latest.current = stored;
    setDraft(stored);
  }, [stored]);

  const [savedTick, setSavedTick] = useState(false);
  const tickTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const showSaved = useCallback(() => {
    setSavedTick(true);
    if (tickTimer.current) clearTimeout(tickTimer.current);
    tickTimer.current = setTimeout(() => {
      setSavedTick(false);
    }, SAVED_TICK_MS);
  }, []);

  const { mutate } = save;
  const commit = useCallback(
    (next: SettingsWire) => {
      setSavedTick(false);
      mutate(next, { onSuccess: showSaved });
    },
    [mutate, showSaved],
  );

  /** A retention value typed but not yet sent, and the timer that will send it. */
  const debounce = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pending = useRef<SettingsWire | null>(null);

  const flush = useCallback(() => {
    if (debounce.current) clearTimeout(debounce.current);
    debounce.current = null;
    const waiting = pending.current;
    pending.current = null;
    if (waiting) commit(waiting);
  }, [commit]);

  /**
   * Applies a change to the screen and saves it — at once, or after the
   * retention field's pause. Nothing is saved before the stored record has
   * arrived: a PUT replaces the whole record, and one built on the defaults
   * would overwrite settings this device had not yet read.
   */
  const change = useCallback(
    (patch: Partial<SettingsWire>, { debounced = false }: { debounced?: boolean } = {}) => {
      const next = { ...latest.current, ...patch };
      latest.current = next;
      setDraft(next);
      if (!loadedRef.current) return;
      if (debounce.current) clearTimeout(debounce.current);
      if (!debounced) {
        pending.current = null;
        commit(next);
        return;
      }
      pending.current = next;
      debounce.current = setTimeout(flush, RETENTION_SAVE_DELAY_MS);
    },
    [commit, flush],
  );

  // A number half-typed when the screen goes away — another tab, the app
  // backgrounded — is sent rather than dropped, the same way the note editor
  // flushes its debounce.
  useEffect(() => {
    const onHidden = (): void => {
      if (document.visibilityState === 'hidden') flush();
    };
    document.addEventListener('visibilitychange', onHidden);
    return () => {
      document.removeEventListener('visibilitychange', onHidden);
      flush();
      if (tickTimer.current) clearTimeout(tickTimer.current);
    };
  }, [flush]);

  const appearanceId = useId();
  const cleanupId = useId();
  const retentionId = useId();
  const languageId = useId();
  const aboutId = useId();

  return (
    <div className="screen">
      <header className="screen__header">
        <h1>You</h1>
      </header>

      {/*
        What the last change did. Empty when there is nothing to say: the
        controls below are the state, and a line reading "All changes saved"
        under a theme the server had never seen was the misleading part.
      */}
      <div className="settings-status" role="status" aria-live="polite">
        {save.isPending ? (
          <span className="settings-status__text">Saving…</span>
        ) : save.isError ? (
          <>
            <span className="settings-status__text">Couldn&rsquo;t save your settings</span>
            <button
              type="button"
              className="settings-status__action"
              onClick={() => {
                commit(latest.current);
              }}
            >
              Try again
            </button>
          </>
        ) : savedTick ? (
          <span className="settings-status__text settings-status__text--saved">
            <Icon name="check" size={16} className="settings-status__tick" />
            Saved
          </span>
        ) : null}
      </div>

      {/* ---- Appearance: applied on the device at once, and saved -------- */}
      <section className="settings-group" aria-labelledby={appearanceId}>
        <h2 id={appearanceId} className="settings-group__title">
          Appearance
        </h2>
        <ul className="option-list" role="list">
          {THEME_PREFERENCES.map((option: ThemePreference) => (
            <li key={option}>
              <button
                type="button"
                className="option"
                aria-pressed={option === preference}
                onClick={() => {
                  // On the device at once, then to the server like the rest.
                  setPreference(option);
                  change({ theme: option });
                }}
              >
                <span className="option__label">{THEME_LABELS[option]}</span>
                {option === preference && (
                  <Icon name="check" size={18} className="option__check" />
                )}
              </button>
            </li>
          ))}
        </ul>
        <p className="settings-group__note">Currently showing {THEME_LABELS[resolved]}.</p>
      </section>

      {/* ---- Cleanup ----------------------------------------------------- */}
      <section className="settings-group" aria-labelledby={cleanupId}>
        <h2 id={cleanupId} className="settings-group__title">
          Cleanup
        </h2>
        <ul className="option-list" role="list">
          {(['faithful', 'polished'] as CleanupMode[]).map((mode) => (
            <li key={mode}>
              <button
                type="button"
                className="option"
                aria-pressed={draft.cleanup_mode === mode}
                disabled={!stored}
                onClick={() => {
                  change({ cleanup_mode: mode });
                }}
              >
                <span className="option__label">{CLEANUP_LABELS[mode]}</span>
                {draft.cleanup_mode === mode && (
                  <Icon name="check" size={18} className="option__check" />
                )}
              </button>
            </li>
          ))}
        </ul>
      </section>

      {/* ---- Transcription language -------------------------------------- */}
      {/*
        Above the retention field, not below it: the owner's trial found no
        multilingual support in the app, and this — the one control that is
        the whole of it — sat under a number field about audio retention.
        The second line says where a note can differ, by the name of the
        panel it happens in.
      */}
      <section className="settings-group" aria-labelledby={languageId}>
        <h2 id={languageId} className="settings-group__title">
          Transcription language
        </h2>
        <div className="settings-field">
          <label className="visually-hidden" htmlFor={`${languageId}-select`}>
            Default transcription language
          </label>
          <LanguageSelect
            id={`${languageId}-select`}
            value={draft.default_language ?? 'en'}
            disabled={!stored}
            onChange={(default_language) => {
              change({ default_language });
            }}
          />
        </div>
        <p className="settings-group__note">
          {(draft.default_language ?? 'en') === AUTO_LANGUAGE
            ? 'Each recording is transcribed in whatever language it detects. Naming a language is faster and more accurate; a recording that mixes two is detected as one of them.'
            : `Recordings are transcribed as ${languageName(draft.default_language ?? 'en')} unless the note they are made into says otherwise. A recording filed automatically always uses this, because it is transcribed before anyone knows which note it belongs to.`}
        </p>
        <p className="settings-group__note">
          Applies to every recording; a note can choose its own under Details.
        </p>
      </section>

      {/* ---- Retention --------------------------------------------------- */}
      <section className="settings-group" aria-labelledby={retentionId}>
        <h2 id={retentionId} className="settings-group__title">
          Keep recordings for
        </h2>
        <div className="settings-field">
          <label className="visually-hidden" htmlFor={`${retentionId}-input`}>
            Days to keep source audio
          </label>
          <input
            id={`${retentionId}-input`}
            className="settings-input numeric"
            type="number"
            min={0}
            max={3650}
            value={draft.retention_days}
            disabled={!stored}
            onChange={(event) => {
              change(
                { retention_days: clamp(Number(event.target.value), 0, 3650) },
                { debounced: true },
              );
            }}
            onBlur={flush}
          />
          <span className="settings-field__suffix">days</span>
        </div>
        <p className="settings-group__note">
          {draft.retention_days === 0
            ? 'Recordings are kept indefinitely. Only the source audio is affected — note text is never deleted by this.'
            : `Source audio is deleted after ${draft.retention_days} days. Note text and transcripts are kept.`}
        </p>
      </section>

      {/* ---- Usage ------------------------------------------------------- */}
      {/*
        The read-only spend-cap sentence used to be here. Since v3 step 3 the
        cap is one number for the whole instance, set in the deploy config and
        echoed by the API, so the line said something about the deploy rather
        than about the person. Their own usage does; the cap is mentioned once,
        without its amount, on About (U13b).
      */}
      <UsageSection />

      <PasskeyCard />

      <SignOutSetting />

      {/* ---- About ------------------------------------------------------- */}
      <section className="settings-group" aria-labelledby={aboutId}>
        <h2 id={aboutId} className="settings-group__title">
          About
        </h2>
        <Link to={ROUTES.about} className="option">
          <span className="option__label">About Chintan</span>
          <span className="option__hint">What it does, where your data lives</span>
        </Link>
      </section>

      {isLoading && <p className="screen__count">Loading your settings…</p>}

      <VersionFootnote />
    </div>
  );
}

function clamp(value: number, low: number, high: number): number {
  if (!Number.isFinite(value)) return low;
  return Math.min(high, Math.max(low, Math.round(value)));
}
