import { useEffect, useId, useRef, useState, type FormEvent } from 'react';

import { ApiError } from '@/api/problem.ts';
import { useCreateDevice, useDeleteDevice, useDevices } from '@/api/queries.ts';
import type { DeviceCreatedWire, DeviceWire } from '@/api/schema.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { CopyButton } from '@/components/CopyButton.tsx';
import { Icon } from '@/components/Icon.tsx';
import { config } from '@/config/env.ts';
import { describeAgo, formatRowTime } from '@/features/notes/groups.ts';

import { RowButton, SettingsCard } from './SettingsCard.tsx';
import { formatMegabytes } from './usage.ts';

/** The server's own limits (`docs/design/inbox.md`); said here so the row can say why it is disabled. */
export const MAX_DEVICES = 10;
export const DEVICE_NAME_MAX = 60;
/** Why Add and Rotate are held at the limit: a rotation briefly needs an eleventh row. */
const FULL_HINT = 'Ten is the limit; remove one first';

/**
 * The row's second line: what the key sent this month and when last, once it
 * has sent something this month; when it was added and whether it was ever
 * used, otherwise. "Sent" counts requests, which is captures near enough — a
 * two-step upload is two of them.
 */
export function deviceHint(device: DeviceWire): string {
  if (device.usage_month) {
    const { requests, bytes } = device.usage_month;
    const last = device.last_used_at ? ` · Last used ${describeAgo(device.last_used_at)}` : '';
    return `${String(requests)} sent this month · ${formatMegabytes(bytes)}${last}`;
  }
  const added = device.created_at ? `Added ${formatRowTime(device.created_at)} · ` : '';
  return `${added}${device.last_used_at ? `Last used ${describeAgo(device.last_used_at)}` : 'Never used'}`;
}

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

/**
 * "Wait, remove one first" for the server's 409; its own words for the rest.
 * A timeout is the one message not taken as is: the client's sentence
 * promises a retry, and neither write here gets one — a create is sent once
 * because the server will not replay it (`createDevice`), a remove has used
 * its retries by the time it is reported — so the request may well have
 * landed, and the honest next step is to look.
 */
export const UNCONFIRMED_TEXT = 'Could not confirm — check the list before trying again.';

function failureText(error: unknown): string {
  if (error instanceof ApiError) return error.kind === 'timeout' ? UNCONFIRMED_TEXT : error.userMessage;
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
 *
 * Rotate key is that pair in the right order, new key first: it mints a
 * device with the same name, shows the key, and revokes the old device only
 * on Done, so the device is never without a working key while the person
 * pastes the new one in. No wire of its own — a POST and then a DELETE.
 */
export function DevicesCard() {
  const devices = useDevices();
  const create = useCreateDevice();
  const remove = useDeleteDevice();
  const nameId = useId();

  const [adding, setAdding] = useState(false);
  const [name, setName] = useState('');
  /** The device just created, whose key is on screen until Done. */
  const [minted, setMinted] = useState<DeviceCreatedWire | null>(null);
  /** The device whose key `minted` replaces, revoked on Done; null for a plain Add. */
  const [rotating, setRotating] = useState<DeviceWire | null>(null);
  /** The device Remove was tapped on, awaiting the confirmation. */
  const [removing, setRemoving] = useState<DeviceWire | null>(null);
  const keyRef = useRef<HTMLDivElement>(null);

  // The key box mounts already filled, which a live region often does not
  // announce; focusing it reads the sentence and the key, and puts the next
  // Tab on Copy key.
  useEffect(() => {
    if (minted) keyRef.current?.focus();
  }, [minted]);

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
        // "Check the list" is only honest if the list is current: a create
        // that timed out may have minted a row this device never saw.
        onError: (error) => {
          if (error instanceof ApiError && error.kind === 'timeout') void devices.refetch();
        },
      },
    );
  };

  const cancelAdding = (): void => {
    setAdding(false);
    setName('');
    create.reset();
  };

  // One send, as Add is (R4-5). A failed create rotates nothing: the old key
  // is untouched and the server's words show under the list.
  const rotate = (device: DeviceWire): void => {
    setAdding(false);
    setMinted(null);
    setRotating(null);
    create.reset();
    create.mutate(
      { name: device.name },
      {
        onSuccess: (created) => {
          setMinted(created);
          setRotating(device);
        },
        onError: (error) => {
          if (error instanceof ApiError && error.kind === 'timeout') void devices.refetch();
        },
      },
    );
  };

  const done = (): void => {
    // The old key goes only now, once the new one has been on screen. A
    // remove that fails shows the remove error and leaves both rows listed,
    // so the old device can be removed by hand.
    if (rotating) remove.mutate(rotating.id);
    setRotating(null);
    setMinted(null);
    // The mutation's result still held the key; "it will not be shown again"
    // should mean it is gone from memory too.
    create.reset();
  };

  return (
    <SettingsCard
      title="Devices & shortcuts"
      lead="A key lets a watch, a phone shortcut or any other app drop a recording straight into your notes."
      foot={
        <p>
          Each key is shown once and can be removed here at any time. A device may send two
          hundred requests a day (recordings or text), up to 4 MiB each.
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
                <span className="you-row__hint">{deviceHint(device)}</span>
              </span>
              <div className="you-row__control">
                <button
                  type="button"
                  className="settings-status__action"
                  aria-label={`Rotate key for ${device.name}`}
                  title={full ? FULL_HINT : undefined}
                  disabled={full || create.isPending || remove.isPending}
                  onClick={() => {
                    rotate(device);
                  }}
                >
                  Rotate key
                </button>
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
      {!adding && create.isError && (
        <p className="you-card__note" role="alert">
          {failureText(create.error)}
        </p>
      )}

      {minted && (
        <div className="device-key" role="status" tabIndex={-1} ref={keyRef}>
          <p>
            {rotating ? 'The new key for ' : 'The key for '}
            <strong>{minted.name}</strong>. Copy it now — it will not be shown again.
            {rotating ? ' The old key keeps working until you tap Done.' : ''}
          </p>
          <code className="device-key__value">{minted.key}</code>
          <div className="device-key__actions">
            <CopyButton
              label="Copy key"
              text={() => minted.key}
              className="settings-status__action settings-status__action--primary"
            />
            <button type="button" className="settings-status__action" onClick={done}>
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
          hint={full ? FULL_HINT : 'Get a key for a watch, a shortcut or another app'}
          icon="plus"
          disabled={full || devices.isLoading}
          onClick={() => {
            setMinted(null);
            setRotating(null);
            create.reset();
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
 * How the common clients are pointed at the inbox. Four recipes behind
 * disclosures, each with its copy button, and one sentence for everything
 * else — the point of a bearer key is that nothing here is special. The
 * ring's is the one that posts a form rather than a file; the inbox reads
 * both, so the recipe is still an address and a header.
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
      <p className="recipes__lead">
        To file into one note every time, add the header <code>X-Chintan-Note-Id</code> with the
        note&rsquo;s id (the last part of its address).
      </p>

      <details className="you-card__more recipe">
        <summary className="you-card__more-summary">
          <Icon name="chevron-right" size={16} className="recipe__chevron" />
          From a terminal (curl)
        </summary>
        <div className="you-card__more-body">
          <pre className="recipe__code">{curl}</pre>
          <div>
            <CopyButton label="Copy command" text={() => curl} className="settings-status__action" />
          </div>
        </div>
      </details>

      <details className="you-card__more recipe">
        <summary className="you-card__more-summary">
          <Icon name="chevron-right" size={16} className="recipe__chevron" />
          iPhone or Apple Watch (Shortcuts)
        </summary>
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
        <summary className="you-card__more-summary">
          <Icon name="chevron-right" size={16} className="recipe__chevron" />
          Android (HTTP Shortcuts app)
        </summary>
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

      <details className="you-card__more recipe">
        <summary className="you-card__more-summary">
          <Icon name="chevron-right" size={16} className="recipe__chevron" />
          Pebble Index 01 ring
        </summary>
        <div className="you-card__more-body">
          <ol className="recipe__steps">
            <li>
              In the Pebble app, open <strong>Index</strong> → <strong>Webhook</strong>. URL:{' '}
              <code>{audioUrl}</code>.
            </li>
            <li>
              Add a header <code>Authorization</code> = <code>Bearer {KEY_PLACEHOLDER}</code>. The
              bare key, without &ldquo;Bearer&rdquo;, works too.
            </li>
            <li>
              Send the <strong>Recording</strong> (or <strong>Both</strong>). A note from the ring
              lands like any recording: transcribed, filed and cleaned.
            </li>
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
