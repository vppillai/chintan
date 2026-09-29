# The inbox: device keys and external recorders

Any device or app that can make one HTTPS request with one header can drop a
recording or a line of text into a person's notes, and it is filed exactly as
a recording made in the app: transcribed, routed, cleaned, appended. This
note is the backend half. Code: `model.Device` (`backend/internal/model/
types.go`), `service.DeviceService` (`backend/internal/service/devices.go`),
`CaptureService.IngestAudio` / `IngestText` (`backend/internal/service/
capture.go`), the routes and the key check (`backend/internal/handler/
inbox.go`, `devices.go`), the gateway routes (`infrastructure/template.yaml`,
`Inbox*Route`). The README's "Connect a device" has the recipes.

## Why a second credential

The app signs in with Cognito and holds a session; a watch, a ring's phone
app or an iOS Shortcut cannot run that flow, and handing one of them the
session token would hand it everything the session can do — read every note,
change settings, delete the library. A device key is the opposite shape: one
header, revocable on its own, and good for exactly one thing.

## Device keys

`POST /v1/devices {name}` mints `ck_<id>_<secret>` — the id is `dev_` and
twelve hex characters, the secret twenty-four random bytes in hex (hex
rather than base64url so the secret can hold no underscore and the last
underscore always splits the two) — and returns it once. The row (`pk
USER#<sub>`, `sk DEVICE#<id>`) stores the key's SHA-256 and never the key;
the operation takes no `Idempotency-Key`, so no replay record holds it
either. The row also carries sparse GSI1 keys, `gsi1pk DEVICEKEY#<id>`,
`gsi1sk KEY`, so the inbox can find the tenant from the key alone: one index
read for the row's keys, one `GetItem` for the row (the index projects a
capture's attributes, not a device's, and a device row is smaller than an
index rebuild). Ten live devices per tenant.

`expires_in_days` (1–365, optional) on the POST sets `expires_at` on the row
once, from the creation instant, and it never moves. The check refuses a
key past it with the same fixed 401 as a revoked one and counts it as
`expired` — after the secret has matched, so the reason means the device's
own key past its date, not a guess at one. The row stays listed, and counts
against the ten, so the Devices card can say "Expires in 12 days" or
"Expired" and offer Remove. There is no default: the ring you use daily
stays perpetual, and a key handed to a one-off script gets thirty days
(WH-A, round 5).

`GET /v1/devices` lists id, name, when issued, when last used and from
where, when it expires, and what the key sent this month — never the key or
its hash; a key that is lost is revoked and a new one issued. The month's
figures (`usage_month`: requests, their body bytes, the month) come from
three counters on the device row (`requests_month`, `bytes_month`, `month`)
that the check writes in the same `PutDevice` as the day's counter,
`last_used_at` and `last_used_from`, so they cost no extra write; a new
month starts them over on the key's next request, and the service lists an
earlier month's counters as null.

`last_used_from` is the *neighbourhood* the last accepted request came from,
overwritten each time and never a history: the gateway's
`requestContext.http.sourceIp`, which `httpadapter.V2` copies bare into
`r.RemoteAddr`, reduced by `service.neighbourhood` to the IPv4 /24 written
as `203.0.113.x` or the IPv6 /48 written as `2001:db8:1::x` before anything
is stored (an IPv4-in-IPv6 address is unmapped first; what does not parse
stores nothing). Deliberately coarse: a /24 tells your phone's carrier from
a stranger on the card — "Last used 2 h ago from 203.0.113.x" — and keeps a
precise address out of the table, the wire and the logs. The full address
is read in that one function and nowhere else, and device rows are in no
export: the export job reads notes, captures and recordings, never
`DEVICE#` rows (WH-B, round 5). "Sent" counts accepted
requests, which is captures near enough: a two-step upload is two. Bytes
are the bodies the inbox itself read — a one-shot route's recording or
text, but for the two-step route only the small JSON that opens the
capture, since the PUT goes to the bucket and never through the inbox — so
a device that uploads that way shows kilobytes a month, not the size of its
recordings. `DELETE
/v1/devices/{id}` revokes: `revoked_at` is set, the GSI1 keys are dropped so
the lookup cannot see the row, and a TTL thirty days out lets DynamoDB drop
it. Revoking a revoked device is 204 again. The Devices card's Rotate key is
those two in the right order, with no wire of its own: it mints a new key for
the same device name and revokes the old one when the person taps Done, so
the device is never without a working key while the new one is pasted in.

## The check

`Authorization: Bearer ck_…` on the three inbox routes — or the same value
bare (`Authorization: ck_…`: a webhook that takes header key/value pairs,
the Pebble Index ring's for one, has nowhere to learn the Bearer scheme), or
`X-Device-Key: ck_…` for a non-browser client whose Authorization header is
spoken for, read first when both are sent (the header is not in the CORS
allow-list and is not added to it: a browser page has the session and no
business on the inbox). Three spellings, one credential
(`deviceKeyOf`); the threat model is unchanged because the key is still the
only thing checked, and the refusal never says which spelling failed. The
key is parsed, the id looked up through the index, and the presented key's hash compared
with the stored one by `crypto/subtle.ConstantTimeCompare`. Malformed,
unknown, revoked, expired and wrong-secret all answer the same fixed 401
`unknown device key`; nothing in the wording or the timing says which. The key reaches
`Authenticate` and nothing else — not the context, not a log line; a capture
records `source: device:<id>` and log lines name the id.

Each key has a per-UTC-day counter on its row (`requests_day`,
`requests_day_date`); the 201st request in a day is 429 `this device has
reached today's limit`, refused before anything is written, so a key
hammered past the limit costs one read per request and no write. The counter
and `last_used_at` are written under the row's version, so a revoke that
lands between the check's read and its write is not overwritten: the write
loses, the row is read again and the revoke is seen; a revoke that loses to
the counter write is retried the same way. Two hundred requests is one every seven
minutes around the clock, which bounds what a leaked key costs in provider
spend before its owner notices; the instance's daily spend cap still applies
above it, and every inbox capture runs through the breaker like any other.

Once the key is accepted the device's tenant is the request's identity, so
everything downstream — the request counter on GET /v1/usage, idempotent
replay, the capture service — behaves as for the app.

## The three routes

The gateway admits `/v1/inbox/*` without its JWT authorizer
(`AuthorizationType: NONE`, like the health probes); the document puts the
three operations under the `deviceKey` scheme and
`openapi_conformance_test.go` asserts the gateway's open routes are exactly
those plus the probes.

- `POST /v1/inbox/captures` is `POST /v1/captures` for a device: the row and
  the presigned PUTs, for a client that can make two requests. The 201 is the
  same `CaptureCreated`.
- `POST /v1/inbox/audio` takes the recording as the body (≤ 4 MiB: the
  gateway hands a binary body to the function base64-encoded under its 6 MB
  request cap, so 5 MiB never arrived; a longer recording takes the two-step
  route) with `Content-Type` naming one of
  `audio/webm`, `audio/ogg`, `audio/mp4`, `audio/m4a`, `audio/mpeg`,
  `audio/wav`, `audio/x-wav`, and `X-Chintan-Note-Id`, `X-Chintan-Language`,
  `X-Chintan-Duration-Ms` as optional headers. The API writes the object to
  the capture's own audio key — the row first, then the object, as the client
  flow orders them — with the same retention tags a presigned PUT carries, so
  the bucket notifies the worker and the lifecycle rules expire it as for any
  upload. No peaks key is recorded: nothing computed an envelope. 202 with
  the capture at `uploaded`. The same route takes a `multipart/form-data`
  body, the shape a webhook posts (`inboxAudioForm`): the `audio` part, under
  its own `Content-Type` (required, as the raw body's is — RFC 7578 makes an
  untyped part `text/plain`, and the ring sends `audio/mp4` explicitly — so
  a part with none is the raw path's 400), is ingested exactly as a raw body
  is; a `transcription` part alone goes the text route below (invalid UTF-8
  becomes U+FFFD, as the JSON decoder makes it on `/v1/inbox/text`),
  so a ring set to send only its transcription still files a note; with
  both, the audio wins and the sender's transcription is dropped, because
  the pipeline transcribes with the note's language. The Pebble Index ring's
  `recordedAt` (epoch milliseconds) goes to the row's timing record, as the
  `X-Chintan-Recorded-At` header does on either one-shot route (see Timing
  record below); `client` is ignored. The whole form is read under the same
  4 MiB cap, envelope included, since the gateway's limit applies to the
  body as the gateway sees it.
  Retries: a webhook that cannot set `Idempotency-Key` (the ring's cannot)
  files a second capture when it retries. A sender that sets one must resend
  the identical bytes — the key is bound to a fingerprint of the raw body,
  and a re-encoded multipart form has a new boundary, so the retry is 409
  `this Idempotency-Key was used for a different request`.
- `POST /v1/inbox/text` takes 1 to 20,000 characters, filed into the body's
  `note_id` or, when the body has none, the note `X-Chintan-Note-Id` names
  (the same tenant-scoped lookup and the same 404 as the audio route; the
  form's transcription-only road takes the header the same way; the app shows
  the id in the note's Details). The text
  is written where the transcript would be (`RawKey`), the row starts at `transcribed`
  with no audio key and no duration, and the API invokes the worker since no
  object lands to wake it. The pipeline gates transcription on `RawKey` being
  empty, so it routes, cleans and appends the text as it would a transcript;
  the destination note's own language never sends it to the provider either
  (`wantsNoteLanguage` needs an audio key), "Transcribe again" refuses it
  (409, as for expired audio), and the audio download is 404. On the wire the
  capture says `has_audio: false`, so the recordings row shows the transcript
  and no player.

An untargeted inbox capture is always routed, cue or no cue, and saying the
note's name first — "App feedback the split up is slow" — is a supported way
to file into it (`docs/design/prompts.md`, Routing; round 6, R6-RT-1/RT-2).

Every inbox capture carries `source: device:<id>` (the wire says `app` for
the app's own, and for every capture from before the field existed), and the
Recordings tab says "From ⟨device⟩" by name, reading `GET /v1/devices` once a
device-sourced row is on screen (`useDevices`, `queries/devices.ts`); and
Home shows a receipt for every inbox capture, targeted or not — nobody
watched a device's recording land on the note (`docs/design/capture-ux.md`,
"Receipts on Home").

## Timing record

Two fields on the capture row, in the record blob only, say when things
happened: `recorded_at`, the sender's own claim of when it recorded
(`X-Chintan-Recorded-At` on either one-shot route, epoch milliseconds or RFC
3339, or the ring form's `recordedAt`; dropped, never a 400, when it does
not parse or sits more than seven days before or five minutes after the
server's clock), and `stage_at`, status → the moment the pipeline first
wrote that status, stamped by every persist and by the append's completion.
The worker emits two metrics from them, `CaptureQueueDelay` (row written →
worker picked it up) and `CaptureEndToEnd` (row written → appended), with a
`Source` dimension of `app` or `device` — never the device id — and its
"capture pipeline finished" line carries `queue_ms`, `source` and, when the
sender said when it recorded, `device_lag_ms`. Both metrics start at the
row's `created_at`, so a capture retried days after it failed reports the
whole wait, not the retry's own run, and one such retry can own a month's
p95 by itself. `chintanctl latency --month yyyy-mm` reads the rows back as
per-hop percentiles (device lag, queue, transcribe, route, clean, append,
total) by source, the device id included there; its `queue` and `total`
inflate on a retry the same way. None of it reaches the wire, the app or
About: `captureOf` leaves both fields out, and the wire test pins that.

## Threat model, in one place

- The key is hashed at rest and shown once; a table read yields no usable
  credential.
- It is revocable on its own, immediately, without touching the session.
- A leaked key can add to the notes and cannot read them: no inbox route
  returns note content, a body or a title — a capture row, and on the
  two-step route a presigned PUT to a key the caller already had to be
  issued.
- Per-key daily cap, body caps (4 MiB audio, 20,000 characters of text), a
  content-type allowlist, and the instance's spend cap above all of it bound
  what abuse costs.
- Before any of that, the gateway throttles each of the three inbox routes
  at one request a second (burst ten) — some forty times what ten devices
  at two hundred requests a day can need — because a bad-key POST is billed
  (the invocation and the body transfer) before the handler refuses it; a
  CloudWatch alarm on the gateway's 4xx count (a thousand in five minutes)
  e-mails when a flood is under way. The trade-off: the ceiling is per
  route and shared by every caller, so a flood at the public URL pauses the
  inbox for the owner's own devices while it lasts; the app itself uses
  other routes and is untouched. If that ever matters, CloudFront in front
  of the API with a WAF rate rule per source address is the path.
- Below the gateway's threshold, refusals are still visible from the Lambda
  side: every refused key counts one `InboxKeyRefused` with a `Reason` of
  `malformed`, `unknown`, `revoked`, `expired`, `wrong_secret` or `daily_limit` (plus
  the dimensionless rollup the alarm reads), and a WARN `device key refused`
  names the device id and the reason once the id parsed — a malformed key
  logs nothing, so a probe cannot write bytes of its choosing into the log.
  A revoked key counts as `revoked` while the index still carries its entry
  (the moment after the revoke) and as `unknown` once the revoke has dropped
  the row's index keys, so a device still sending a long-revoked key shows
  under `unknown`. The `InboxKeyRefusedAlarm` (Sum ≥ 25 per 15 minutes on
  the rollup) e-mails when a key is being guessed or a device is still
  sending with a revoked or exhausted one. The 401 itself is unchanged.
- No key material in logs or responses beyond the 201 that issues it; the
  device id is what identifies a device everywhere else.

### `source` and `last_progress_at` on a note's page

`Capture.source` (since 2026-09-24) and `Capture.last_progress_at` (since
2026-09-27) live in the row's blob and as top-level attributes too. gsi1's
INCLUDE projection carries neither, and CloudFormation cannot change a live
index's projection, so `ListCapturesByNote` overlays both on the page with one
`BatchGetItem` (`hydrateUnprojectedCaptureFields`) rather than saying "app" for
every row, as it did on the day the inbox shipped, or `null` for every
recording's progress, which made the app measure a regeneration's age from
`created_at` and call a healthy run on a ten-minute-old note stuck from its
first second (QA 2026-09-27, F1). A row last written before its attribute was
promoted has no top-level value and reads as it did before: "app", and
`created_at`; the pipeline rewrites the whole row on every hand-off, so a
recording that moves gets both.

The overlay is the permanent answer at this scale, not a stopgap waiting for
an index rebuild (round 6, decision R6-OD-2 (c)). CloudFormation cannot widen
a live index's projection, so carrying the two in the index would mean a
second index, a two-step deploy and a switch of the index-name constant, all
to save one `BatchGetItem` per page of a note's recordings, a read that costs
well under a cent a month. The code is `hydrateUnprojectedCaptureFields` in
`backend/internal/repository/dynamo_captures.go`, about sixty lines that
`TestListCapturesByNoteKeepsTheSource` and
`TestListCapturesByNoteKeepsLastProgressAt` (`capture_list_test.go`, against
the template's real projection) hold. Rebuild the index only when a DynamoDB
change is needed for another reason, and fold both into `NonKeyAttributes` and
delete the overlay then.
