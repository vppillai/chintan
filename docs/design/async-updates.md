# How the app learns that a recording landed

Status: the poll and the focus refetch are implemented (2026-09-26, round 5,
R5-RC-3); Web Push is proposed and awaits owner decision R5-RC-D1/D2. Code
today: `usePendingCaptures` and `capturePollInterval`
(`frontend/src/api/queries.ts`), the library's receipts (`FilingRow.tsx`,
`docs/design/capture-ux.md` "Receipts on Home"), the note screen's own poll
(`useNote`, the same function). Nothing in this note changes the wire.

A recording is appended by the worker, not by the client that is watching.
The app that made it has a row to watch; the app that did not — a phone in a
pocket while a ring files three things — has nothing but what it asks for.
This note records how it asks today, what that costs, what else was costed,
and the one addition that fits the stack.

## Today: one poll, a ladder, and the foreground

`usePendingCaptures` asks `GET /v1/captures?status=all&limit=20` — one
request, filtered on the client (`isFilingRelevant`), rather than four
server-side filters in parallel. Its cadence is a ladder keyed on how long ago
anything moving last made progress (`last_progress_at`, else `created_at`):

| Since the last progress | Interval | Why |
|---|---|---|
| a capture is under 30 s old | 1.5 s | the pipeline finishes in p50 1.9 s, p90 4.0 s; a 4 s poll added a median 2 s of pure waiting |
| under 2 min | 4 s | it is waiting on a provider |
| under 10 min | 15 s | two quiet minutes: something is slow, not stuck |
| 10 min and beyond | 60 s | it counts as stuck (`STUCK_AFTER_MS`); the row offers Retry at 15 min, and this is what re-renders it on time |
| nothing moving | off | — |

It never stops while anything is non-terminal. Before the ladder it stayed at
4 s for ever, so one capture stuck for hours kept an open Home at nine
hundred requests an hour, 9.7 KB each.

The same query refetches whenever the app comes to the foreground
(`refetchOnWindowFocus: 'always'`). It used to opt out — a rule from before
the inbox, when only this client could start a capture and a focus had
nothing new to learn. With a ring filing into the notes, focus is the one
moment a capture made while the app was in the background is owed a look;
the notes list already refetched on focus (the client default), which is why
the owner saw the notes move to the top of Today and no receipt until a pull.
TanStack pauses the interval while the document is hidden, so the background
costs nothing.

Two other readers. The note screen polls its own `GET /v1/notes/{id}` on the
same ladder while any of its captures is non-terminal, so a note opened while
its recording was still at "Uploaded" does not sit on the pre-recording body.
Pull-to-refresh on Home refetches everything at once; it is the gesture the
focus refetch makes unnecessary for this case, not a replacement for it.

## The baseline, measured (prod, seven days to 2026-09-26)

- 10,699 API requests. `OPTIONS` preflights 3,024 (28 %), `GET
  /v1/notes/{id}` 2,508, `GET /v1/notes` 1,995, `GET /v1/captures` 545 (5 %).
- One poll answer is 9,675 bytes: no `Content-Encoding` (the gateway does not
  compress), no `ETag`, no `Cache-Control`. 146–163 ms from a laptop.
- Worker "capture pipeline finished", n=189: elapsed p50 1.9 s, p90 4.0 s,
  max 48.8 s. Through a device key, end to end: 1.4–2.4 s into a chosen note,
  2.4–5.1 s routed; the routed path's long pole is the router's model call.
- The ring's own side is 10–15 s to transfer and transcribe before its webhook
  fires, and it sleeps after thirty idle minutes. The owner's "lot of delay"
  is that, plus our ~2 s, plus — before the focus refetch — an unbounded wait
  for a pull.

The poll is not a cost problem. At list prices the whole polling line is
under a cent a month.

## What was costed

Monthly at list prices, us-west-2, for 1 / 10 / 100 users. The test is
whether anything can reach a *closed* app — which is what a ring's recording
needs — and whether it fits a stack whose bill rounds to zero because nothing
holds a connection open.

| Option | Mechanism | 1 | 10 | 100 | Verdict |
|---|---|---|---|---|---|
| Smarter polling | the ladder above plus the focus refetch; ≈110 GETs per user per day (gateway $1/M, Lambda $0.20/M plus duration, DynamoDB ~5 RRU per page) | $0.006 | $0.06 | $0.60 | **Shipped.** Cannot reach a closed app. |
| Web Push (VAPID) | the worker POSTs to the browser's push service after an append and on `needs_target`/`failed`; the service worker shows the notification and wakes the page; ≤3 sends per capture, push services free, SSM standard parameters free | ≈$0.005 | $0.05 | $0.50 | **Proposed (R5-RC-D1/D2).** The only option that gives phone notifications. |
| API Gateway WebSocket | a second API with a `$connect` Lambda authorizer (no JWT authorizer on WebSocket APIs), a connections table with TTL, `PostToConnection` from the worker, Gone cleanup; $0.25/M connection-minutes plus $1/M messages | $0.005 | $0.05 | $0.45 | Rejected on weight: ~300 lines of infra, Go and TS for an open tab only, no OS notification. |
| SSE over a Lambda Function URL (response streaming) | one invocation per open connection, ≤15 min each, outside the gateway; 8 h/day × 0.5 GB × $0.0000133/GB-s | **$5.75** | **$57** | **$575** | Rejected on cost: it scales with connection time, the opposite of the stack. |
| AppSync subscriptions | a GraphQL API in front of a REST app; Cognito auth exists | $0.002 | $0.02 | $0.20 | Rejected: a second API paradigm for one event type. |
| IoT Core MQTT over WebSocket | Cognito Identity Pool, a per-user IoT policy, topic ACLs | $0.002 | $0.02 | $0.20 | Rejected: a second identity system. |

Not done either: `ETag`/304 and gzip on the poll. `writeJSON` could set
`ETag` and `Cache-Control: private, no-cache` and the browser would make the
conditional GET itself, cutting 9.7 KB to ~300 B on an unchanged answer — but
the gateway and the Lambda still run, and with the ladder the poll is ~80
requests a day. Add when a tenant's bytes matter.

## Web Push, as it would be built

Status: proposed; awaiting owner decision R5-RC-D1 (build it) and R5-RC-D2
(where the permission is asked). The worker already knows the moment an
append completes; a VAPID push is one HTTPS POST from that hook to the
browser's push service, which is free. It adds one row kind, three small
routes, one Go dependency, a service-worker handler and one card on You — no
second API, no second identity system, no connection table.

**Data.** `pk USER#<sub>`, `sk PUSH#<id>` where `id` is the first sixteen
hex characters of SHA-256 of the endpoint; `endpoint`, `p256dh`, `auth`,
`label` (the UA family), `created_at`, `last_sent_at`, `failures`, and a
`ttl` ninety days out, rolled on each send. At most ten rows per tenant, as
for devices.

**Routes.** `PUT /v1/push/subscriptions` upserts by endpoint (body:
`PushSubscription.toJSON()` plus `label`); `DELETE
/v1/push/subscriptions/{id}`; `GET /v1/push/vapid` answers `{public_key}`.
Under `$default`; `docs/api/openapi.yaml` and the contract fixtures gain three
operations.

**Keys.** The private key is a `SecureString` at
`/chintan/<instance>/vapid_private_key`, read by the worker only — its role
already reads two SSM paths; this is a third. The public key goes in
`config/instances/<name>.yaml`, through a template Parameter, to the API's
`VAPID_PUBLIC_KEY` environment variable, so the API keeps reading nothing
from SSM. `scripts/push-keys.sh --apply` generates the P-256 pair, puts the
private key and prints the public one.

**Worker.** `pipeline/notify.go`, hooked after `finishAppend` and where a
capture becomes `failed` or `needs_target`: Query `PUSH#`, send to each
subscription in parallel with a five-second timeout through
`github.com/SherClockHolmes/webpush-go` (MIT, small; it depends on the
golang-jwt already in `go.mod` and on `x/crypto`'s HKDF). The payload is
`{kind, note_id, note_title, capture_id}` — the title and never the text,
since it may be read from a lock screen. A 404 or 410 deletes the row; any
other error counts `PushSendFailures` and is not retried, because the list is
the truth and the next focus reads it. Only for captures Home would show:
device-sourced or untargeted (the R5-RC-2 rule). A Lambda retry cannot
double-send: a finished capture returns at the top of `run`.

**Service worker (`sw.ts`).** `push`: if a window client is focused, post
`{type: 'CAPTURES_CHANGED'}` to it and show no notification (Chrome allows
this; iOS shows one anyway, accepted); otherwise `showNotification` with
`tag: note_id` and `renotify`, bumping the count from
`registration.getNotifications({tag})` so the OS shade groups by note exactly
as Home does. `notificationclick`: focus a client and navigate, or
`openWindow(ROUTES.note(id))`. `pushsubscriptionchange`: re-subscribe and
PUT. On the page, one hook invalidates the captures query and calls
`refreshAppendedNote` on the message.

**Permission.** A Notifications card on You beside Devices & shortcuts, one
switch. On: `Notification.requestPermission()`, then
`pushManager.subscribe({userVisibleOnly: true, applicationServerKey})`, then
the PUT. Off: unsubscribe and DELETE. Available when `PushManager` is in
`window`; on iOS also only when installed (`display-mode: standalone`, iOS
16.4+), else a caption "Add Chintan to your Home Screen first".

**Failure modes.** Permission denied or unsupported: the poll and the focus
refetch are the product, unchanged. A push not delivered: nothing is lost;
Home is right on the next focus. A subscription gone (410): the row is
deleted and the app re-subscribes on its next start. Duplicate pushes
collapse by `tag`. The owner on two devices: two rows, both notified, both
collapse on open. Optional: `navigator.setAppBadge(undismissed receipts)`
where the browser supports it.

## Position

Request/response stays the transport for the open app, with the ladder and
the focus refetch as the floor. Web Push is the one addition that reaches a
closed app and fits the stack, and it is written down here whether or not it
is built. Everything that holds a connection open was costed and is out.
Measurement comes before tuning: the ring's `recordedAt`, per-stage
timestamps on the row and the `CaptureQueueDelay`/`CaptureEndToEnd` metrics
(round 5, LAT-1/LAT-2) make the Pebble-versus-Chintan split a number.
