import { useId, useState, type FormEvent } from 'react';

import { ApiError } from '@/api/problem.ts';
import { useCreateDevice, useDeleteDevice, useDevices } from '@/api/queries.ts';
import type { DeviceWire } from '@/api/schema.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { CopyButton } from '@/components/CopyButton.tsx';
import { config } from '@/config/env.ts';
import { describeAgo, formatRowTime } from '@/features/notes/groups.ts';

import { RowButton, SettingsCard } from './SettingsCard.tsx';

/** The server's own limits (`docs/design/inbox.md`); said here so the row can say why it is disabled. */
export const MAX_DEVICES = 10;
export const DEVICE_NAME_MAX = 60;

/** The one-shot inbox: POST the audio bytes, one header, done. */
export function inboxAudioUrl(apiUrl: string = config.apiUrl): string {
  return `${apiUrl}/v1/inbox/audio`;
}

export function inboxTextUrl(apiUrl: string = config.apiUrl): string {
  return `${apiUrl}/v1/inbox/text`;
}

/** What the person pastes their key over in every recipe. */
const KEY_PLACEHOLDER = 'YOUR_KEY';

/**
 * A terminal recipe: one file, one request. `m4a` because that is what a
 * phone's voice memo and iOS's Record Audio both produce; the inbox takes
 * webm, ogg, mp4, mpeg and wav as well.
 */
export function curlRecipe(apiUrl: string = config.apiUrl): string {
  return [
    `curl -X POST "${inboxAudioUrl(apiUrl)}" \\`,
    `  -H "Authorization: Bearer ${KEY_PLACEHOLDER}" \\`,
    `  -H "Content-Type: audio/m4a" \\`,
    `  --data-binary @recording.m4a`,
  ].join('\n');
}

/** "Wait, remove one first" for the server's 409; its own words for the rest. */
function failureText(error: unknown): string {
  if (error instanceof ApiError) return error.userMessage;
  return 'That did not go through. Try again.';
}

/**
 * "Devices & shortcuts", on You.
 *
 * The app is the recorder, and until now the only one: a watch, a ring, a
 * phone shortcut or a dictation app had no way in. A device key is that way
 * in — a bearer token any HTTP client can send with one header, which the
 * inbox accepts audio or text under and files exactly like a recording made
 * here (2026-09-24 contract, section A). The card lists the keys by name,
 * mints a new one, and revokes one; the recipes under the list are how the
 * common clients are pointed at it.
 *
 * The key is shown once. The server stores a hash and cannot show it again,
 * so the box says so in the same breath as it shows the key, offers Copy
 * where the thumb is, and stays until Done: a person who taps away before
 * copying has lost nothing but a minute, because the next step is Remove and
 * Add again.
 */
export function DevicesCard() {
  const devices = useDevices();
  const create = useCreateDevice();
  const remove = useDeleteDevice();
  const nameId = useId();

  const [adding, setAdding] = useState(false);
  const [name, setName] = useState('');
  /** The device just created, whose key is on screen until Done. */
  const [minted, setMinted] = useState<DeviceWire | null>(null);
  /** The device Remove was tapped on, awaiting the confirmation. */
  const [removing, setRemoving] = useState<DeviceWire | null>(null);

  const items = devices.data?.items ?? [];
  const full = items.length >= MAX_DEVICES;

  const submit = (event: FormEvent): void => {
    event.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) return;
    create.mutate(
      { name: trimmed },
      {
        onSuccess: (device) => {
          setMinted(device);
          setAdding(false);
          setName('');
        },
      },
    );
  };

  const cancelAdding = (): void => {
    setAdding(false);
    setName('');
    create.reset();
  };

  return (
    <SettingsCard
      title="Devices & shortcuts"
      lead="A key lets a watch, a phone shortcut or any other app drop a recording straight into your notes."
      foot={
        <p>
          Each key is shown once and can be removed here at any time. A device may send two
          hundred recordings a day.
        </p>
      }
    >
      {devices.isError ? (
        <p className="you-card__note" role="alert">
          Couldn&rsquo;t load your devices.{' '}
          <button
            type="button"
            className="text-link"
            onClick={() => {
              void devices.refetch();
            }}
          >
            Try again
          </button>
        </p>
      ) : devices.isLoading ? (
        <p className="you-card__note">Loading your devices…</p>
      ) : items.length === 0 ? (
        <p className="you-card__note">No devices yet. Add one to get a key.</p>
      ) : (
        <ul className="devices" role="list" aria-label="Your devices">
          {items.map((device) => (
            <li key={device.id} className="you-row">
              <span className="you-row__label">
                <span className="you-row__label-text">{device.name}</span>
                <span className="you-row__hint">
                  {device.created_at ? `Added ${formatRowTime(device.created_at)} · ` : ''}
                  {device.last_used_at ? `Last used ${describeAgo(device.last_used_at)}` : 'Never used'}
                </span>
              </span>
              <div className="you-row__control">
                <button
                  type="button"
                  className="settings-status__action"
                  aria-label={`Remove ${device.name}`}
                  disabled={remove.isPending}
                  onClick={() => {
                    setRemoving(device);
                  }}
                >
                  Remove
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}

      {remove.isError && (
        <p className="you-card__note" role="alert">
          {failureText(remove.error)}
        </p>
      )}

      {minted && (
        <div className="device-key" role="status">
          <p>
            The key for <strong>{minted.name}</strong>. Copy it now — it will not be shown again.
          </p>
          <code className="device-key__value">{minted.key}</code>
          <div className="device-key__actions">
            <CopyButton
              label="Copy key"
              text={() => minted.key ?? ''}
              className="settings-status__action settings-status__action--primary"
            />
            <button
              type="button"
              className="settings-status__action"
              onClick={() => {
                setMinted(null);
              }}
            >
              Done
            </button>
          </div>
        </div>
      )}

      {adding ? (
        <form className="device-form" onSubmit={submit}>
          <label className="device-form__label" htmlFor={nameId}>
            What is this device? The name is only for this list.
          </label>
          <div className="device-form__row">
            <input
              id={nameId}
              className="you-input device-form__input"
              type="text"
              value={name}
              maxLength={DEVICE_NAME_MAX}
              placeholder="Watch, Shortcut on the phone…"
              autoComplete="off"
              // The row that was just tapped became this field; the caret should be in it.
              autoFocus
              onChange={(event) => {
                setName(event.target.value);
              }}
            />
            <button
              type="submit"
              className="settings-status__action settings-status__action--primary"
              disabled={create.isPending || name.trim().length === 0}
            >
              {create.isPending ? 'Creating…' : 'Create key'}
            </button>
            <button type="button" className="settings-status__action" onClick={cancelAdding}>
              Cancel
            </button>
          </div>
          {create.isError && (
            <p className="device-form__error" role="alert">
              {failureText(create.error)}
            </p>
          )}
        </form>
      ) : (
        <RowButton
          label="Add a device"
          hint={full ? 'Ten is the limit; remove one first' : 'Get a key for a watch, a shortcut or another app'}
          icon="plus"
          disabled={full || devices.isLoading}
          onClick={() => {
            setMinted(null);
            setAdding(true);
          }}
        />
      )}

      <Recipes />

      <ConfirmDialog
        open={removing !== null}
        title={`Remove ${removing?.name ?? 'this device'}?`}
        body="Its key stops working at once, so anything still sending with it will be refused. Recordings it already sent stay in your notes."
        confirmLabel="Remove it"
        destructive
        onCancel={() => {
          setRemoving(null);
        }}
        onConfirm={() => {
          if (removing) remove.mutate(removing.id);
          setRemoving(null);
        }}
      />
    </SettingsCard>
  );
}

/**
 * How the common clients are pointed at the inbox. Three recipes behind
 * disclosures, each with its copy button, and one sentence for everything
 * else — the point of a bearer key is that nothing here is special.
 */
function Recipes() {
  const audioUrl = inboxAudioUrl();
  const curl = curlRecipe();
  return (
    <div className="recipes">
      <h3 className="recipes__title">Connect a device</h3>
      <p className="recipes__lead">
        Everything posts to the same address with your key in one header. The recording is
        transcribed and filed exactly as one made here.
      </p>

      <details className="you-card__more recipe">
        <summary className="you-card__more-summary">From a terminal (curl)</summary>
        <div className="you-card__more-body">
          <pre className="recipe__code">{curl}</pre>
          <div>
            <CopyButton label="Copy command" text={() => curl} className="settings-status__action" />
          </div>
        </div>
      </details>

      <details className="you-card__more recipe">
        <summary className="you-card__more-summary">iPhone or Apple Watch (Shortcuts)</summary>
        <div className="you-card__more-body">
          <ol className="recipe__steps">
            <li>
              In Shortcuts, tap + and add <strong>Record Audio</strong> (Finish Recording: On Tap).
            </li>
            <li>
              Add <strong>Get Contents of URL</strong>. URL: <code>{audioUrl}</code>. Method:
              POST. Headers: <code>Authorization</code> = <code>Bearer {KEY_PLACEHOLDER}</code>,{' '}
              <code>Content-Type</code> = <code>audio/m4a</code>. Request Body: File → Recorded
              Audio.
            </li>
            <li>
              Name it &ldquo;{config.appName}&rdquo; and put it on the Home Screen, the Action
              button or Back Tap; a watch runs it from its Shortcuts app.
            </li>
          </ol>
          <div>
            <CopyButton label="Copy address" text={() => audioUrl} className="settings-status__action" />
          </div>
        </div>
      </details>

      <details className="you-card__more recipe">
        <summary className="you-card__more-summary">Android (HTTP Shortcuts app)</summary>
        <div className="you-card__more-body">
          <ol className="recipe__steps">
            <li>
              Install <strong>HTTP Shortcuts</strong> (free, on Google Play and F-Droid) and create
              a shortcut. Method: POST. URL: <code>{audioUrl}</code>.
            </li>
            <li>
              Request Headers: <code>Authorization</code> = <code>Bearer {KEY_PLACEHOLDER}</code>,{' '}
              <code>Content-Type</code> = <code>audio/m4a</code>. Request Body: File, with the
              source set to record audio when it runs (older versions offer a file picker
              instead).
            </li>
            <li>Place it on the home screen or as a Quick Settings tile.</li>
          </ol>
          <div>
            <CopyButton label="Copy address" text={() => audioUrl} className="settings-status__action" />
          </div>
        </div>
      </details>

      <p className="recipes__note">
        Watches, rings and other apps: anything that can POST a file with one header works.
        Send audio to <code>{audioUrl}</code>, or text as{' '}
        <code>{'{"text": "…"}'}</code> to <code>{inboxTextUrl()}</code>, with{' '}
        <code>Authorization: Bearer {KEY_PLACEHOLDER}</code>.
      </p>
    </div>
  );
}
