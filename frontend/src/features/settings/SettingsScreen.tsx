import { useCallback, useEffect, useId, useRef, useState } from 'react';

import { useSaveSettings, useSettings } from '@/api/queries.ts';
import type { CleanupMode, SettingsWire } from '@/api/schema.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { config } from '@/config/env.ts';
import { Icon } from '@/components/Icon.tsx';
import { THEME_LABELS, THEME_PREFERENCES, type ThemePreference } from '@/theme/theme.ts';
import { useTheme } from '@/theme/useTheme.ts';

import { SignOutSetting } from '@/features/auth/SignOutSetting.tsx';

const CLEANUP_LABELS: Record<CleanupMode, string> = {
  faithful: 'Faithful — fix only what was clearly misheard',
  polished: 'Polished — tidy the wording as well',
};

const DEFAULTS: SettingsWire = {
  cleanup_mode: 'faithful',
  retention_days: 0,
  theme: 'ink',
  daily_spend_cap_micros: 0,
};

function equalSettings(a: SettingsWire, b: SettingsWire): boolean {
  return (
    a.cleanup_mode === b.cleanup_mode &&
    a.retention_days === b.retention_days &&
    a.theme === b.theme &&
    (a.daily_spend_cap_micros ?? 0) === (b.daily_spend_cap_micros ?? 0)
  );
}

export function SettingsScreen() {
  const { preference, resolved, setPreference } = useTheme();
  const { data: stored, isLoading } = useSettings();
  const save = useSaveSettings();

  const [draft, setDraft] = useState<SettingsWire>(DEFAULTS);
  const [pendingDiscard, setPendingDiscard] = useState(false);
  const loadedRef = useRef(false);

  useEffect(() => {
    if (!stored || loadedRef.current) return;
    loadedRef.current = true;
    setDraft(stored);
  }, [stored]);

  const baseline = stored ?? DEFAULTS;
  const dirty = !equalSettings(draft, baseline);

  const update = useCallback((patch: Partial<SettingsWire>) => {
    setDraft((previous) => ({ ...previous, ...patch }));
  }, []);

  // Warn before the tab closes on unsaved settings, for the same reason the
  // note editor does: a silent loss is worse than an interruption.
  useEffect(() => {
    if (!dirty) return;
    const onBeforeUnload = (event: BeforeUnloadEvent): void => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', onBeforeUnload);
    return () => {
      window.removeEventListener('beforeunload', onBeforeUnload);
    };
  }, [dirty]);

  const appearanceId = useId();
  const cleanupId = useId();
  const retentionId = useId();
  const spendId = useId();

  return (
    <div className="screen">
      <header className="screen__header">
        <h1>You</h1>
      </header>

      {/*
        A real, rendered indicator. v1 toggled a `.btn-warning` class that had
        no CSS rule behind it, so the unsaved state was invisible on every
        screen and people navigated away believing they had saved.
      */}
      <div className="settings-status" data-dirty={dirty || undefined} role="status" aria-live="polite">
        <span className="settings-status__text">
          {save.isPending
            ? 'Saving…'
            : save.isError
              ? "Couldn't save your settings"
              : dirty
                ? 'Unsaved changes'
                : 'All changes saved'}
        </span>

        {dirty && (
          <span className="settings-status__actions">
            <button
              type="button"
              className="settings-status__action"
              onClick={() => {
                setPendingDiscard(true);
              }}
            >
              Discard
            </button>
            <button
              type="button"
              className="settings-status__action settings-status__action--primary"
              onClick={() => {
                save.mutate(draft);
              }}
              disabled={save.isPending}
            >
              Save
            </button>
          </span>
        )}
      </div>

      {/* ---- Appearance: applied immediately, not part of the draft ------ */}
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
                  setPreference(option);
                  update({ theme: option });
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
                onClick={() => {
                  update({ cleanup_mode: mode });
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
            onChange={(event) => {
              update({ retention_days: clamp(Number(event.target.value), 0, 3650) });
            }}
          />
          <span className="settings-field__suffix">days</span>
        </div>
        <p className="settings-group__note">
          {draft.retention_days === 0
            ? 'Recordings are kept indefinitely. Only the source audio is affected — note text is never deleted by this.'
            : `Source audio is deleted after ${draft.retention_days} days. Note text and transcripts are kept.`}
        </p>
      </section>

      {/* ---- Spend cap --------------------------------------------------- */}
      <section className="settings-group" aria-labelledby={spendId}>
        <h2 id={spendId} className="settings-group__title">
          Daily spending cap
        </h2>
        {/*
          Read-only since v3 step 3: the cap is one number for the whole
          instance, set in the deploy config (`daily_spend_cap_micros`) and
          echoed back by the API. Before that it was a per-user field this
          screen edited, which the server now accepts and ignores — so the
          input was a control that did nothing.
        */}
        <p className="settings-group__note">
          {(draft.daily_spend_cap_micros ?? 0) === 0 ? (
            'No cap is set for this instance. Usage is still measured.'
          ) : (
            <>
              <span className="numeric">${microsToDollars(draft.daily_spend_cap_micros ?? 0)}</span>{' '}
              a day across transcription and cleanup. Captures stop once it is reached, and say
              so rather than failing vaguely. It is set in the instance configuration, not here.
            </>
          )}
        </p>
      </section>

      <SignOutSetting />

      {isLoading && <p className="screen__count">Loading your settings…</p>}

      <ConfirmDialog
        open={pendingDiscard}
        title="Discard your changes?"
        body="Your unsaved settings will be reset to the last saved values."
        confirmLabel="Discard changes"
        destructive
        onCancel={() => {
          setPendingDiscard(false);
        }}
        onConfirm={() => {
          setPendingDiscard(false);
          setDraft(baseline);
          setPreference(baseline.theme);
        }}
      />

      <VersionFootnote />
    </div>
  );
}

/**
 * What is running, at the very bottom and deliberately quiet.
 *
 * The first thing anyone needs when a bug report comes in, so it is real text
 * inside a `<code>` — selectable and copyable — rather than decoration. Faded
 * via `--color-faint`, which `tokens.css` documents as meeting AA in both
 * themes; "quiet" is not a licence to be unreadable.
 */
export function VersionFootnote() {
  return (
    <p className="version-footnote">
      <span className="visually-hidden">App version </span>
      <code>{config.version}</code>
    </p>
  );
}

function clamp(value: number, low: number, high: number): number {
  if (!Number.isFinite(value)) return low;
  return Math.min(high, Math.max(low, Math.round(value)));
}

/** The API meters in micros; people think in dollars. */
export function microsToDollars(micros: number): number {
  return Math.round(micros / 10_000) / 100;
}

export function dollarsToMicros(dollars: number): number {
  if (!Number.isFinite(dollars) || dollars < 0) return 0;
  return Math.round(dollars * 1_000_000);
}
