# Data model

Where every byte lives: one DynamoDB table, one versioned S3 bucket, and
the keys that tie a row to its objects. Code: the key helpers and item
shapes in `backend/internal/repository/dynamo.go` (`userPK`, `noteSK`,
`noteItemAttrs`, `noteFromItem`), `dynamo_captures.go`,
`dynamo_devices.go`, `dynamo_misc.go`; the S3 key derivation in
`backend/internal/keys/keys.go`; the conditional writes in
`repository/s3.go`; the table and the bucket in
`infrastructure/template.yaml`. Written so that someone holding a backup
can say what each row is and what it names.

## The table

`chintan-<instance>-<environment>`, on-demand, `pk` and `sk` strings,
point-in-time recovery on, `DeletionProtectionEnabled`, `Retain` on delete.
One index, `gsi1` (`gsi1pk`, `gsi1sk`), and one TTL attribute, `ttl`. There
is no second index and no stream: the notes list drains a tenant's
partition from the base table and orders in Go (`dynamo_notes.go`,
`listNotes`, `DrainNotes`; page size `DefaultListLimit` 50, ceiling
`MaxListLimit` 200), and nothing cascades off TTL (retention.md).

A tenant is one partition, `pk = USER#<tenant>`; the tenant id is the
Cognito subject (`auth.Identity`, auth.md). `chintanctl backup`, `restore`
and `erase` walk that partition, so a row that belongs to the instance
rather than to a tenant lives under `pk = INSTANCE` and is left alone by
them.

| `pk` | `sk` | `type` | Shape and lifetime |
|---|---|---|---|
| `USER#<t>` | `SETTINGS` | `settings` | One JSON blob in `data` (`putJSONItem`). No TTL. |
| `USER#<t>` | `NOTE#<id>` | `note` | Blob plus the promoted attributes below. `ttl` only while archived. |
| `USER#<t>` | `CAPTURE#<id>` | `capture` | Blob plus promoted attributes; always in `gsi1`. No TTL: a capture goes when its note's cascade deletes it. |
| `USER#<t>` | `DEVICE#<id>` | `device` | Named attributes, no blob: `device_id`, `name`, `key_hash`, `created_at`, `last_used_at`, `last_used_from`, `expires_at`, the counters (`requests_day`, `requests_day_date`, `requests_month`, `bytes_month`, `month`), `revoked_at`, `version`. Live: `gsi1pk DEVICEKEY#<id>`, `gsi1sk KEY`. Revoked: no index keys, `ttl` = `revoked_at` + `model.RevokedDeviceRetention` (30 days). inbox.md owns the key scheme. |
| `USER#<t>` | `PUSHSUB#<id>` | `push_subscription` | Named attributes (`endpoint`, `p256dh`, `auth`, `label`, `failures`). No TTL (push.md). |
| `USER#<t>` | `IDEM#<key>` | `idem` | `idem_key`, `idem_fingerprint`, `idem_attempt`, `idem_done`, `idem_status`, `idem_claimed_at`, `idem_expires_at`, `idem_tenant_scope`, then `idem_response` and `idem_content_type` once answered. `ttl` = `repository.IdemTTL` (24 h); the claim lease `IdemClaimLease` is 60 s (idempotency.md). |
| `USER#<t>` | `ASK#<id>` | `ask` | One JSON blob; `ttl` = the ask's `expires_at`, `model.AskTTL` (24 h) after it is asked (ask.md). |
| `USER#<t>` | `USAGE#<yyyy-mm>` | `usage` | Flat counters, `op_<op>_…` prefixed, `granularity month`; `gsi1pk USAGE#<yyyy-mm>`, `gsi1sk TENANT#<t>` so an admin listing is a Query. Kept for good. |
| `USER#<t>` | `USAGE#<yyyy-mm-dd>` | `usage` | The day's counters, `granularity day`; `ttl` 400 days (`usage.dayRetention`). |
| `INSTANCE` | `SPEND#<yyyy-mm-dd>` | `spend` | `spend_micros`, the instance-wide daily provider spend the breaker adds to atomically (`pipeline.DynamoCounter`); `ttl` 90 days (`spendRetention`). |
| `INSTANCE` | `AWSCOST#<yyyy-mm>` | `aws_cost` | `month_micros`, `budget_micros`, `as_of`, rewritten daily by the `aws-cost` task. No TTL. |

usage-accounting.md owns what the usage, spend and cost rows mean and how
they are priced.

## The blob and the promoted attributes

A note or capture row carries its whole record as JSON in `data` and, next
to it, every attribute a list renders, filters or deletes by, as a
top-level attribute (`noteItemAttrs`, `captureItemAttrs`). Reads decode the
blob first and overlay only the attributes the read projected
(`noteFromItem`, `captureFromItem`): a field the projection left out
degrades to "not carried by this read", never to its zero value, and a row
written before an attribute existed still reads back whole from its blob.
`chintanctl reconcile --apply` rewrites the promoted set onto such a row
from the same two functions (`NoteItemAttributes`, `CaptureItemAttributes`),
so the store and its repair tool cannot disagree on a name.

A note's promoted attributes are the ones in `noteListProjection` (title,
aliases, tags, snippet, timestamps, the S3 keys, the archive fields, the
clean-view fields, the append stamp, `kind`, `pinned_at`, `pin_rank`) plus
three that are promoted only and never in the blob: `search_text` (up to
32 KB, read only by search and the offline corpus, search.md),
`cleaned_body` (up to 200 KB) and `language` (a reserved word, read through
`#lang`). `kind`, `pinned_at`, `pin_rank` and `language` are written only
when set, so their absence is the zero value (pins.md). A capture's promoted attributes
are its status, timestamps, the append bookkeeping (`append_token`,
`append_claimed_at`, `appended_at`), `duration_ms`, `error`, `source`,
`last_progress_at`, `excerpt`, `created_note` and the six object keys
(`audio_key`, `raw_key`, `routed_key`, `clean_key`, `segments_key`,
`peaks_key`). The per-stage timing record (`stage_at`) lives in the blob
alone (inbox.md).

## Versions and compare-and-set

Every note, capture and device row carries `version`. `PutNote`,
`PutCapture` and `PutDevice` write the whole row under the condition
`versionCondition(expected)` — `version = :expected`, or, for 0, also a row
with no `version` attribute — and increment it; a lost race is
`ErrVersionConflict`, which the API answers as 409 with the current version
(api-conventions.md) and the editor reconciles (append-vs-autosave.md).
`PutNote` additionally requires `cleaned_requested_at` to equal the one the
caller read, so a whole-row write cannot erase a clean request that landed
in between. The stamps that must not bump the version are `UpdateItem`s on
named attributes with their own conditions: `StampNoteAppend` and
`ClearNoteAppend` (the append stamp), `StampCleanRequest` and
`ClearCleanStamp`. The append claim (`ClaimCaptureAppend`,
`CompleteCaptureAppend`) is a versioned whole-row write under one more
condition, `appendClaimCondition` (worker.md). Tests:
`TestPutNoteVersionIncrements`,
`TestStampCleanRequestTouchesOnlyTheStampAndLeavesTheVersionAlone`,
`TestClaimCaptureAppendIsExclusive`.

## `gsi1`: three uses of one sparse index

The index is one, and its projection is treated as fixed: a live index's
projection cannot be changed, only deleted and rebuilt, which on a
populated table is an outage of the note screen.

1. **A note's captures.** Every capture row carries
   `gsi1pk = TENANT#<t>#NOTE#<noteId>`, `gsi1sk = CAPTURE#<createdAt>`,
   the note id empty for a capture still waiting for a destination, so
   `ListCapturesByNote(tenant, "")` finds those too. The projection is
   `INCLUDE`, never `ALL` — `ALL` would copy `data`, the largest attribute,
   into the index and over the wire on every list — and carries exactly
   what a capture list renders plus every object key a cascade delete has
   to unlink: `capture_id`, `note_id`, `status`, `error`, `mode`,
   `created_at`, `appended_at`, `duration_ms`, `version` and the six
   `*_key` attributes. (`mode` is projected but no row writes it.)
   `TestGSI1ProjectionCoversWhatTheCaptureListReads` reads the template's
   `NonKeyAttributes` (`dynamofake.TemplatePath`) and fails when the store
   reads a name the index does not carry;
   `TestListedCaptureCarriesEveryArtefactKeyTheCascadeDeleteNeeds` holds
   the keys.
2. **A device key to its tenant.** A live device row carries
   `gsi1pk = DEVICEKEY#<keyId>`, `gsi1sk = KEY`; `LookupDeviceKey` queries
   it and then reads the row from the base table. Revoking drops both
   attributes, so a revoked key is invisible to the index rather than
   checked (`TestDeviceRowsRoundTripAndTheKeyIndexIsSparse`).
3. **Which tenants exist.** A usage month row carries
   `gsi1pk = USAGE#<yyyy-mm>`, `gsi1sk = TENANT#<t>`; the storage snapshot
   lists tenants by querying thirteen months of it
   (`usage.TenantsWithUsage`, `storagesnap.lookbackMonths`) and
   `chintanctl usage` scans it for the one `--month` asked for, because a
   single-table design with no Scan grant cannot list partition keys.

### The hydrate step

Four capture attributes a note's page needs are not in the projection:
`source` (which device sent the recording), `last_progress_at` (what the
app's poll cadence and stuck rule run on), `excerpt` and `created_note`
(what the filing banner says). `ListCapturesByNote` overlays them on each
page with one `BatchGetItem` keyed off the projected ids
(`hydrateUnprojectedCaptureFields`, projection
`sk, #source, last_progress_at, excerpt, created_note`), and a row that
has no such attribute keeps what the index gave. One extra read per page is
cheaper than a second index, a two-step deploy and a switch of the index
name, so this is the design, not a stopgap; fold the four into
`NonKeyAttributes` only if the index is ever rebuilt for another reason.
`TestListCapturesByNoteKeepsTheSource`,
`TestListCapturesByNoteKeepsLastProgressAt` (against the template's real
projection) and `TestListCapturesByNoteDoesNotHydratePerItem` hold it.

A tenant-wide capture list reads the base table, not the index, because a
`needs_target` capture has no note to be indexed under (`ListCaptures`),
and the cascade delete also finds captures the index cannot see
(`ListUnindexedCaptures`, `attribute_not_exists(gsi1pk)`).

## TTL

`ttl` is shared by every expiring kind and is always the backstop, not the
mechanism: an archived note's `ttl` is `purge_after_epoch` plus
`ttlGraceSeconds` (fourteen days) so the sweep deletes the objects before
TTL can drop the row that names them (retention.md owns the sweep).
Restoring a note removes both attributes (`PutNote`); `ExpiredNotes`
filters on `purge_after_epoch`, never on `ttl`.

## The bucket

`chintan-content-<instance>-<environment>-<account>`, versioned, with the
lifecycle rules retention.md describes. Every key is derived in
`internal/keys` from ids checked against `^[a-zA-Z0-9_-]+$`, so no id can
leave its tenant's prefix (`TestNotePathsAreUserScopedAndNavigable`,
`TestRejectsEmptyOrSlashIDs`):

```
tenants/<tenant>/notes/<noteId>/note.md        the body; rewritten in place, versions are the undo
tenants/<tenant>/notes/<noteId>/meta.json      title, aliases, tags, verbatim, language, kind, updated_at
tenants/<tenant>/captures/<captureId>/audio.<ext>     the upload (webm, m4a, mp3 …), tagged
tenants/<tenant>/captures/<captureId>/raw.txt         the transcript as spoken
tenants/<tenant>/captures/<captureId>/routed.txt      the transcript with the spoken instruction removed
tenants/<tenant>/captures/<captureId>/clean.txt       the cleaned text or the extracted items
tenants/<tenant>/captures/<captureId>/clean.prev.txt  the previous items, kept across a resumed append
tenants/<tenant>/captures/<captureId>/segments.json   timestamps for tap-to-seek, from the raw transcript
tenants/<tenant>/captures/<captureId>/peaks.json      the waveform the browser computed
tenants/<tenant>/exports/<exportId>/{export.json,job.json}
```

The capture row's `*_key` attributes name these objects exactly; a cascade
delete removes those keys and never lists a prefix. Audio is written by
the presigned PUT with `chintan-artifact=capture-audio` and
`chintan-retention=<tier>`, and the worker adds `chintan-processed=true`
only after transcription succeeds (`MarkProcessed` reads the tags back and
merges, `TestS3MarkProcessedMergesWithoutDroppingTheRetentionTags`);
export objects carry `chintan-artifact=export` and expire after a day. The
bucket notifies the worker's live alias on `ObjectCreated` for
`tenants/*/audio.{webm,m4a,mp3,ogg,wav}` and nothing else, so the
worker's own writes cannot re-trigger it (worker.md).

`note.md` is the one object two writers share and is never written blind:
`GetWithETag` hands out the ETag, `PutIfMatch` presents it, and a stale
write is `ErrPreconditionFailed`, retried from a fresh read
(`TestS3PutIfMatch`) — the invariant append-vs-autosave.md's loop rests on.

## Reading a backup

A tenant is `USER#<t>` plus `tenants/<t>/` in the bucket, and nothing else;
`INSTANCE` rows are the account's. `chintanctl backup` copies the
partition whole — every `sk` kind above, with no filter and no kind
switch, one verbatim item per line of `items.jsonl` — and the prefix
whole, current objects only, byte for byte under `objects/`
(`cmd/chintanctl/backup.go`, `enumerate.go`). It does not read `note.md`'s
noncurrent S3 versions, which are a note's only undo (retention.md);
those live in the bucket alone.

For a note row, `s3_markdown_key` is the body, `s3_meta_key` the
`meta.json` sidecar (title, aliases, tags, verbatim, language, kind,
updated_at — a convenience copy, not the source of truth) and `version` is
what the next writer must present; for a capture row the six keys are its
objects and `status` says how far it got; a row with `data` but no
`note_id` or `capture_id` predates the promoted attributes and is complete
in its blob. The `<t>` in every key is a Cognito subject, so a restore
into a new user pool orphans every row and object: the recreated user has
a new `sub` and sees none of them (auth.md). `chintanctl reconcile`
reports the disagreements (objects with no row, rows whose objects are
gone, captures filed into a missing note) and `docs/ops/backup-restore.md`
is the rehearsed procedure.
