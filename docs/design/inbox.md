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

`GET /v1/devices` lists id, name, when issued and when last used — never the
key or its hash; a key that is lost is revoked and a new one issued. `DELETE
/v1/devices/{id}` revokes: `revoked_at` is set, the GSI1 keys are dropped so
the lookup cannot see the row, and a TTL thirty days out lets DynamoDB drop
it. Revoking a revoked device is 204 again.

## The check

`Authorization: Bearer ck_…` on the three inbox routes — or the same value
bare (`Authorization: ck_…`: a webhook that takes header key/value pairs,
the Pebble Index ring's for one, has nowhere to learn the Bearer scheme), or
`X-Device-Key: ck_…` for a client whose Authorization header is spoken for,
read first when both are sent. Three spellings, one credential
(`deviceKeyOf`); the threat model is unchanged because the key is still the
only thing checked, and the refusal never says which spelling failed. The
key is parsed, the id looked up through the index, and the presented key's hash compared
with the stored one by `crypto/subtle.ConstantTimeCompare`. Malformed,
unknown, revoked and wrong-secret all answer the same fixed 401 `unknown
device key`; nothing in the wording or the timing says which. The key reaches
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
  its own `Content-Type` (`audio/mp4` when it has none), is ingested exactly
  as a raw body is; a `transcription` part alone goes the text route below,
  so a ring set to send only its transcription still files a note; with
  both, the audio wins and the sender's transcription is dropped, because
  the pipeline transcribes with the note's language. The Pebble Index ring's
  `recordedAt` and `client` are ignored — a capture has no recorded-at
  field. The whole form is read under the same 4 MiB cap, envelope included,
  since the gateway's limit applies to the body as the gateway sees it.
- `POST /v1/inbox/text` takes 1 to 20,000 characters. The text is written
  where the transcript would be (`RawKey`), the row starts at `transcribed`
  with no audio key and no duration, and the API invokes the worker since no
  object lands to wake it. The pipeline gates transcription on `RawKey` being
  empty, so it routes, cleans and appends the text as it would a transcript;
  the destination note's own language never sends it to the provider either
  (`wantsNoteLanguage` needs an audio key), "Transcribe again" refuses it
  (409, as for expired audio), and the audio download is 404. On the wire the
  capture says `has_audio: false`, so the recordings row shows the transcript
  and no player.

Every inbox capture carries `source: device:<id>` (the wire says `app` for
the app's own, and for every capture from before the field existed), so the
row can say "From ⟨device⟩" once the frontend reads `GET /v1/devices`.

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
- No key material in logs or responses beyond the 201 that issues it; the
  device id is what identifies a device everywhere else.

### `source` on a note's page

`Capture.source` lives in the row's blob and, since 2026-09-24, as a top-level
attribute too. gsi1's INCLUDE projection does not carry it, and CloudFormation
cannot change a live index's projection, so `ListCapturesByNote` overlays the
page's sources with one `BatchGetItem` (`hydrateCaptureSources`) rather than
saying "app" for every row the way it did on the day the inbox shipped. When
gsi1 is next rebuilt by hand, add `source` to `NonKeyAttributes` and delete the
overlay.
