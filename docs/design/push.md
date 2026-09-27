# Web Push: how a filed recording reaches a closed app

Status: built, dormant until the owner puts a VAPID key pair in SSM
(round 5, R5-RC-D1/D2, 2026-09-27). Code: `model.PushSubscription`
(`backend/internal/model/types.go`), `service.PushService`
(`backend/internal/service/push.go`), the routes (`backend/internal/handler/
push.go`), the worker's notifier (`backend/internal/pipeline/notify.go`) and
its sender (`backend/internal/push`), the service worker's `push` and
`notificationclick` handlers (`frontend/src/sw.ts`), the Notifications card
(`frontend/src/features/settings/NotificationsCard.tsx`) and the key script
(`scripts/vapid-keys.sh`). The evaluation this came out of is
`docs/design/async-updates.md`.

## Where it sits

Request/response is the transport for the open app: the captures poll and
the focus refetch (`async-updates.md`) are the floor, and nothing holds a
connection open. Web Push is the one addition that reaches a *closed* app,
which is what a ring's recording needs — the phone is in a pocket while the
ring files three things — and it is the one that fits a stack whose bill
rounds to zero: the worker already knows the moment an append completes, and
a VAPID push is one HTTPS POST from that moment to the browser's push
service, which is free. It adds one row kind, four small routes, one Go
dependency (`github.com/SherClockHolmes/webpush-go`, checked by
govulncheck like the rest), two service-worker handlers and one card on You.
No second API, no second identity system, no connection table. WebSocket,
SSE, AppSync and IoT Core were costed and rejected; the table is in
`async-updates.md`.

## Data

One row per browser: `pk USER#<sub>`, `sk PUSHSUB#<id>`, where `id` is the
first sixteen hex characters of SHA-256 of the endpoint. Attributes:
`endpoint`, `p256dh`, `auth` (the browser's `PushSubscription.toJSON()`),
`label` (what the browser calls itself — "Safari on iPhone" — for the card's
count), `created_at`, `last_success_at` (when the push service last accepted
a message), `failures` (sends refused since). Ten per tenant, like devices.
The row is written whole and has no version: the API upserts it by endpoint
and the worker rewrites the two counters, and a lost race between the two
costs at most one counter.

The id being a hash of the endpoint is what makes the API idempotent and
the client self-sufficient: a browser that subscribes again after a restart
writes the same row, and it can find or remove its own row by computing the
same digest (`pushSubscriptionId`, `crypto.subtle`) without the list ever
carrying an endpoint — anyone holding an endpoint and its keys could send to
that browser, so the list shows the push service's host and nothing more.

## Routes

`GET /v1/push/key` → `{public_key}`, or 404 `notifications are not
configured on this instance` until the pair exists. `GET
/v1/push/subscriptions` → the tenant's rows (host, label, times, failures).
`POST /v1/push/subscriptions` with the browser's own object plus `label` →
201, idempotent on the endpoint, 409 at ten. `DELETE
/v1/push/subscriptions/{id}` → 204, 404 for another tenant's. All behind the
JWT like the rest; `docs/api/openapi.yaml` has them, and the conformance and
contract tests cover every status.

## The worker

`notify` runs once at the end of `runCapture`, after `run` returned without
an error and the capture is `appended`, `needs_target` or `failed` (and from
`RejectOversizedCapture`, which fails a capture on its own). A retry of a
finished capture returns at the top of `runCapture` and a conceded delivery
leaves the announcement to its owner, so a Lambda retry cannot double-send.
It follows Home's own rule for what a person is not already watching
(R5-RC-2): a device's capture always, the app's own only when nobody chose
its note — a recording made into a note in the app is on screen while it
files. `no_content` and `spend_capped` are the app's to show.

The payload is `{type, capture_id, note_id, title}`: the note's title and
never its text, because a notification may be read from a lock screen. One
send per row, each under a five-second timeout, in order — there are at most
ten. A 2xx writes `last_success_at`; a 404 or 410 is the push service saying
the browser is gone and deletes the row (`PushSubscriptionsPruned`); anything
else counts `failures` on the row and `PushSendFailures`, and is not retried:
the list is the truth and the next open reads it. The endpoint never reaches
a log line — a failed POST's error text names its URL, so only the reason is
logged (`pushErrorText`) — and neither does the payload.

## Keys

A VAPID pair: the private key at `/chintan/<instance>/vapid_private_key`,
read by the worker only, and the public key at
`/chintan/<instance>/vapid_public_key`, read by the API for `GET /v1/push/key`
and by the worker for the signature. Both are `SecureString`s the owner
creates outside CloudFormation, as the provider keys are, and both are
*optional* reads (`internal/ssmparam`): an instance without them starts,
files recordings, answers 404 on the key and logs one line at the worker's
start. `template.yaml` names the paths in both functions' environments and
grants each role exactly the parameters it reads. `VAPID_SUBJECT` is the
app's origin, the contact RFC 8292 requires in the signature.

The owner's step is `scripts/vapid-keys.sh --instance <name>`, which
generates the pair with openssl and prints the two `aws ssm put-parameter`
commands to run; the script itself writes nothing to AWS. A new pair
invalidates every subscription, which the card then re-subscribes on its
next tap.

## The service worker

`push`: every open window is posted `{type: 'CAPTURES_CHANGED', note_id}`
first, so a Home in view shows the receipt the way it shows every other
filing (`usePushWakeup` invalidates the captures poll and refetches the
note). If a window is focused no notification is shown — Chrome allows a
push to pass without one only in that case; iOS shows one regardless, which
is accepted. Otherwise `showNotification` with the note's title (or "Filed",
"Needs a note", "Did not finish"), one fixed sentence per outcome, the
manifest's icon, and `tag` = the note id, so a ring day's fourth filing into
"Shopping list" replaces the third in the shade rather than stacking under
it — the grouping Home does. `notificationclick` focuses an open window and
navigates it to the note (`ROUTES.note`, under the registration's scope), or
opens one.

## The card

"Notifications" on You, above Devices & shortcuts (R5-RC-D2): one switch,
"Notify me when a recording files", with the hint "Also when one needs a
note chosen, or did not finish". The permission prompt must follow a tap,
and the switch is that tap: on runs `Notification.requestPermission()`,
`pushManager.subscribe({userVisibleOnly, applicationServerKey})` and the
POST; off runs the DELETE first and then `unsubscribe()`, so a browser is
never subscribed to a row the worker still sends to. The switch reads on
only when this browser holds a subscription *and* the server lists it; a
row the worker pruned reads off, which is the truth.

Four states replace the switch, in this order: not set up on this instance
(the key's 404), with the owner's step named; an iPhone or iPad outside the
installed app — "Add Chintan to your Home Screen first" — because iOS (16.4
and later) exposes push only to a Home Screen app; a browser without the
API; and permission denied, which only the browser's own settings undo, so
the switch is held off with that sentence. The foot counts the enrolled
devices and says that only the note's title is ever sent.

## iOS

Push works only in the installed app, on iOS 16.4 or later; Safari in the
browser has no `PushManager`, which is what the card detects. Every push
shows a notification even while the app is in front (the focused-window
skip is Chrome's), and iOS may drop a subscription after the app is unused
for weeks — the push service then answers 410 and the row goes; the switch
reads off on the next visit and one tap re-enrols.

## Failure modes

Permission denied or unsupported: the poll and the focus refetch are the
product, unchanged. A push not delivered: nothing is lost, Home is right on
the next focus. A subscription gone: the row is pruned, the switch reads
off. Duplicate pushes collapse by tag. The owner on two devices: two rows,
both notified, both collapse on open.
