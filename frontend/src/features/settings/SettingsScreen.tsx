import { useCallback, useEffect, useId, useRef, useState } from 'react';

import { useSaveSettings, useSettings } from '@/api/queries.ts';
import type { CleanupMode, SettingsWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';
import { LanguageSelect } from '@/components/LanguageSelect.tsx';
import { config } from '@/config/env.ts';
import { AccountHeader } from '@/features/auth/AccountHeader.tsx';
import { PasskeyCard } from '@/features/auth/PasskeyCard.tsx';
import { REPOSITORY_URL } from '@/screens/AboutScreen.tsx';
import { THEME_LABELS, THEME_PREFERENCES, type ThemePreference } from '@/theme/theme.ts';
import { useTheme } from '@/theme/useTheme.ts';

import { RowLink, Segmented, SettingsCard, SettingsRow } from './SettingsCard.tsx';
import { UsageSection } from './UsageSection.tsx';
import { VersionFootnote } from './VersionFootnote.tsx';
import { AUTO_LANGUAGE, languageName } from './languages.ts';

/** The two cleanup modes as segments, and what each one means, said under the row. */
const CLEANUP_MODES: readonly { value: CleanupMode; label: string; hint: string }[] = [
  { value: 'faithful', label: 'Faithful', hint: 'Fix only what was clearly misheard' },
  { value: 'polished', label: 'Polished', hint: 'Tidy the wording as well' },
];

/**
 * The theme segments. "Follow system" is the preference's full name (it is
 * what the "Currently showing" sentence resolves); in a three-way control
 * beside two theme names, "System" is the label that fits and reads.
 */
const THEME_SEGMENT_LABELS: Record<ThemePreference, string> = {
  ink: THEME_LABELS.ink,
  nocturne: THEME_LABELS.nocturne,
  system: 'System',
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
 * The account first, then five cards: how a recording becomes text, how the
 * app looks, what this month has cost, passkeys, and where the app comes
 * from. Each card is a title, one line on what it is for, its controls as
 * rows, and a footnote for the sentences that qualify them — so the screen
 * is a list of shapes to scan rather than a column of prose with a control
 * every few hundred pixels, which is what it had grown into.
 *
 * Every control saves itself the moment it is changed — a tap on Polished is
 * a PUT, a theme is applied and then saved, a retention number goes once the
 * typing pauses. There is no Save button and nothing is ever "unsaved": the
 * screen used to hold a draft behind a Save/Discard pair, and tapping another
 * tab with the draft dirty lost it silently (QA D9), while the theme —
 * applied on the device at once — read "Unsaved changes" and then, after a
 * reload, "All changes saved" for a value the server had never been sent
 * (QA D13). The status line, beside the title, now only ever says what is
 * happening: Loading…, Saving…, a brief Saved, or the failure with a way to
 * try again.
 */
export function SettingsScreen() {
  const { preference, resolved, setPreference } = useTheme();
  const { data: stored, isLoading, isError: loadFailed, refetch } = useSettings();
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

  const themeLabelId = useId();
  const cleanupLabelId = useId();
  const retentionId = useId();
  const languageId = useId();

  const language = draft.default_language ?? 'en';
  const cleanup = CLEANUP_MODES.find((mode) => mode.value === draft.cleanup_mode) ?? CLEANUP_MODES[0]!;

  return (
    <div className="screen you">
      <header className="screen__header you__header">
        <h1>You</h1>
        {/*
          What the last change did, beside the title so nothing below moves
          when it speaks. Empty when there is nothing to say: the controls are
          the state, and a line reading "All changes saved" under a theme the
          server had never seen was the misleading part.
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
          ) : loadFailed ? (
            <>
              <span className="settings-status__text">Couldn&rsquo;t load your settings</span>
              <button
                type="button"
                className="settings-status__action"
                onClick={() => {
                  void refetch();
                }}
              >
                Try again
              </button>
            </>
          ) : isLoading ? (
            <span className="settings-status__text">Loading your settings…</span>
          ) : null}
        </div>
      </header>

      <AccountHeader />

      {/* ---- Recording & transcription ------------------------------------- */}
      <SettingsCard
        title="Recording & transcription"
        lead="How a recording becomes text, and how long the audio is kept."
        foot={
          <>
            <p>
              {language === AUTO_LANGUAGE
                ? 'Each recording is transcribed in whatever language it detects. Naming a language is faster and more accurate; a recording that mixes two is detected as one of them.'
                : `Recordings are transcribed as ${languageName(language)} unless the note they are made into says otherwise. A recording filed automatically always uses this, because it is transcribed before anyone knows which note it belongs to.`}{' '}
              Applies to every recording; a note can choose its own under Details.
            </p>
            <p>
              {draft.retention_days === 0
                ? 'Recordings are kept indefinitely. Only the source audio is affected — note text is never deleted by this.'
                : `Source audio is deleted after ${String(draft.retention_days)} days. Note text and transcripts are kept.`}
            </p>
          </>
        }
      >
        {/*
          The language first: the owner's trial found no multilingual support
          in the app, and this — the one control that is the whole of it —
          sat under a number field about audio retention.
        */}
        <SettingsRow label="Transcription language" labelFor={`${languageId}-select`}>
          <LanguageSelect
            id={`${languageId}-select`}
            value={language}
            disabled={!stored}
            onChange={(default_language) => {
              change({ default_language });
            }}
          />
        </SettingsRow>

        <SettingsRow label="Cleanup" hint={cleanup.hint} labelId={cleanupLabelId}>
          <Segmented
            options={CLEANUP_MODES}
            value={draft.cleanup_mode}
            disabled={!stored}
            labelledBy={cleanupLabelId}
            onChange={(cleanup_mode) => {
              change({ cleanup_mode });
            }}
          />
        </SettingsRow>

        <SettingsRow label="Keep recordings for" labelFor={`${retentionId}-input`}>
          <span className="you-field">
            <input
              id={`${retentionId}-input`}
              className="you-input numeric"
              type="number"
              inputMode="numeric"
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
            <span className="you-field__suffix">days</span>
          </span>
        </SettingsRow>
      </SettingsCard>

      {/* ---- Appearance: applied on the device at once, and saved ---------- */}
      <SettingsCard
        title="Appearance"
        lead="Applied here at once, and saved with your settings."
        foot={<p>Currently showing {THEME_LABELS[resolved]}.</p>}
      >
        <SettingsRow label="Theme" labelId={themeLabelId}>
          <Segmented
            options={THEME_PREFERENCES.map((option) => ({
              value: option,
              label: THEME_SEGMENT_LABELS[option],
              swatch: <span className="theme-swatch" data-swatch={option} aria-hidden="true" />,
            }))}
            value={preference}
            labelledBy={themeLabelId}
            onChange={(option) => {
              // On the device at once, then to the server like the rest.
              setPreference(option);
              change({ theme: option });
            }}
          />
        </SettingsRow>
      </SettingsCard>

      {/* ---- Usage -------------------------------------------------------- */}
      {/*
        The read-only spend-cap sentence used to be here. Since v3 step 3 the
        cap is one number for the whole instance, set in the deploy config and
        echoed by the API, so the line said something about the deploy rather
        than about the person. Their own usage does; the cap is mentioned once,
        without its amount, on About (U13b).
      */}
      <UsageSection />

      {/* ---- Passkeys ------------------------------------------------------ */}
      <PasskeyCard />

      {/* ---- About & support ----------------------------------------------- */}
      <SettingsCard
        title="About & support"
        lead="What this is, where the code lives, and which build this is."
      >
        <RowLink
          to={ROUTES.about}
          label={`About ${config.appName}`}
          hint="What it does, where your data lives"
        />
        <RowLink to={REPOSITORY_URL} external label="Source on GitHub" />
        <SettingsRow label="Version">
          <VersionFootnote />
        </SettingsRow>
      </SettingsCard>
    </div>
  );
}

function clamp(value: number, low: number, high: number): number {
  if (!Number.isFinite(value)) return low;
  return Math.min(high, Math.max(low, Math.round(value)));
}
