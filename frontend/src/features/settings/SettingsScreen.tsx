import { useCallback, useId, useRef, useState } from 'react';

import { useSaveSettings, useSettings, useUsage } from '@/api/queries.ts';
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

import { DevicesCard } from './DevicesCard.tsx';
import { ExportCard } from './ExportCard.tsx';
import { RowLink, Segmented, SettingsCard, SettingsRow } from './SettingsCard.tsx';
import { VersionFootnote } from './VersionFootnote.tsx';
import { AUTO_LANGUAGE, languageName } from './languages.ts';
import { formatDollars } from './usage.ts';

/** The two cleanup modes as segments, and what each one means, said under the row. */
const CLEANUP_MODES: readonly { value: CleanupMode; label: string; hint: string }[] = [
  { value: 'faithful', label: 'Faithful', hint: 'Fix only what was clearly misheard' },
  { value: 'polished', label: 'Polished', hint: 'Tidy the wording as well' },
];

/**
 * The retention tiers the server stores. `settings_validate.go` rounds any
 * other number down to one of these, so the free number field this used to
 * be showed "45 days" while the audio went on day 30 (round-3 T5): the
 * choice is now one of the five, and what is shown is what is stored.
 */
export const RETENTION_TIERS = [0, 7, 30, 90, 365] as const;

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

/** How long the "Saved" tick stays. */
const SAVED_TICK_MS = 2_500;

/**
 * You.
 *
 * The account first, then the cards: how a recording becomes text, how the
 * app looks, passkeys, the devices that may record into it, your data, and
 * where the app comes from. Each card is
 * a title, one line on what it is for, its controls as rows, and one sentence
 * of footnote with the rest behind More — so the screen is a list of shapes
 * to scan rather than a column of prose with a control every few hundred
 * pixels, which is what it had grown into, twice (round-3 T20: the Usage
 * card alone was a third of a three-screen page, and now sits behind one row
 * that opens `/usage`).
 *
 * Every control saves itself the moment it is changed — a tap on Polished is
 * a PUT, a theme is applied and then saved. There is no Save button and
 * nothing is ever "unsaved": the screen used to hold a draft behind a
 * Save/Discard pair, and tapping another tab with the draft dirty lost it
 * silently (QA D9), while the theme — applied on the device at once — read
 * "Unsaved changes" and then, after a reload, "All changes saved" for a value
 * the server had never been sent (QA D13). The status line, beside the title,
 * now only ever says what is happening: Loading…, Saving…, a brief Saved, or
 * the failure with a way to try again.
 */
export function SettingsScreen() {
  const { preference, resolved, setPreference } = useTheme();
  const { data: stored, isLoading, isError: loadFailed, refetch } = useSettings();
  const save = useSaveSettings();
  const usage = useUsage();

  /*
   * What the controls show: the stored record, or — while a PUT is in flight
   * or has failed — the record that was sent, so a change is on screen before
   * its PUT returns and stays there if the PUT fails, with the failure said
   * beside it. Once the PUT succeeds the cache holds what the server *stored*
   * (`useSaveSettings`), and that is what renders: the screen used to copy
   * the stored record into a draft exactly once, so a value the server had
   * coerced stayed on screen as typed (round-3 T5).
   */
  const draft: SettingsWire =
    (save.isPending || save.isError) && save.variables ? save.variables : (stored ?? DEFAULTS);

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

  /**
   * Applies a change and saves it at once. Nothing is saved before the stored
   * record has arrived: a PUT replaces the whole record, and one built on the
   * defaults would overwrite settings this device had not yet read.
   */
  const change = (patch: Partial<SettingsWire>): void => {
    if (!stored) return;
    commit({ ...draft, ...patch });
  };

  const themeLabelId = useId();
  const cleanupLabelId = useId();
  const retentionId = useId();
  const languageId = useId();

  const language = draft.default_language ?? 'en';
  const cleanup = CLEANUP_MODES.find((mode) => mode.value === draft.cleanup_mode) ?? CLEANUP_MODES[0]!;
  const retention = draft.retention_days;

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
                  commit(draft);
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
          <p>
            {language === AUTO_LANGUAGE
              ? 'Each recording is transcribed in whatever language it detects.'
              : `Recordings are transcribed as ${languageName(language)} unless the note they are made into says otherwise.`}
          </p>
        }
        more={
          <>
            <p>
              {language === AUTO_LANGUAGE
                ? 'Naming a language is faster and more accurate; a recording that mixes two is detected as one of them.'
                : 'A recording filed automatically always uses this, because it is transcribed before anyone knows which note it belongs to.'}{' '}
              Applies to every recording; a note can choose its own under Details.
            </p>
            <p>
              {retention === 0
                ? 'Recordings are kept indefinitely. Only the source audio is ever affected by this — note text is never deleted by it.'
                : `Source audio is deleted after ${String(retention)} days. Note text and transcripts are kept.`}{' '}
              Applies to recordings made from now on; earlier recordings keep the retention they
              were uploaded with.
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
        {/*
          Under the language row because the owner's live default was
          Auto-detect, which re-scripted real Malayalam as Tamil and dropped
          the Malayalam sentence from a mixed clip (round-3 T3). The app's
          own copy had recommended it for exactly that case.
        */}
        <p className="you-card__note">
          Mix Malayalam and English in one recording? Choose Malayalam. Auto-detect picks one
          language per recording and drops or re-scripts the other.
        </p>

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

        <SettingsRow label="Keep recordings for" labelFor={`${retentionId}-select`}>
          <select
            id={`${retentionId}-select`}
            className="settings-select"
            value={String(retention)}
            disabled={!stored}
            onChange={(event) => {
              change({ retention_days: Number(event.target.value) });
            }}
          >
            {RETENTION_TIERS.map((days) => (
              <option key={days} value={String(days)}>
                {days === 0 ? 'Keep forever' : `${String(days)} days`}
              </option>
            ))}
          </select>
        </SettingsRow>
        {/*
          The one way into `/talk` from inside the app (round-4 R4-16): the
          manifest shortcut needs a long press on an installed icon, and a
          person who only ever taps the mic never meets the hold gesture.
        */}
        <RowLink to={ROUTES.talk} label="Hold to talk" hint="Hold, speak, release to send" />
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

      {/* ---- Passkeys ------------------------------------------------------ */}
      <PasskeyCard />

      {/* ---- Devices & shortcuts: keys for the inbox ----------------------- */}
      <DevicesCard />

      {/* ---- Your data ----------------------------------------------------- */}
      <ExportCard />

      {/* ---- About & support ----------------------------------------------- */}
      {/*
        Usage is one row here rather than its own card (round-3 T20): the
        card was a third of the screen and buried Passkeys and About under
        it. The row carries the month's providers figure, and the whole card
        is a screen of its own at `/usage`.
      */}
      <SettingsCard
        title="About & support"
        lead="What this is, what it has cost, and which build this is."
      >
        <RowLink
          to={ROUTES.usage}
          label="Usage this month"
          value={usage.data ? formatDollars(usage.data.cost_micros) : undefined}
        />
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
