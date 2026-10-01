# Retention and the purge sweep

What is kept for how long, and the three mechanisms that enforce it. Code:
`model.RetentionTiers` (`backend/internal/model/types.go`), the upload tags
(`backend/internal/upload/upload.go`, `RetentionTagKey`), the bucket's
lifecycle rules (`infrastructure/template.yaml`, `LifecycleConfiguration`),
the sweep (`backend/internal/purge`) and the archive deadline
(`service.ArchiveRetention`, `backend/internal/service/notes_delete.go`).

## Audio: tiers, enforced by S3

The per-user `retention_days` setting (`GET /v1/settings`; 0 = keep) is
rounded **down** to one of four tiers, `RetentionTiers = {7, 30, 90, 365}`
(`RetentionTierFor`): a promise to delete is honoured no later than asked. A
tier is a tag, not a number: the presigned PUT signs `chintan-retention=<tier>`
and `chintan-artifact=capture-audio` onto the recording, and one lifecycle
rule per tier (`ExpireCaptureAudio7` … `365`) expires the current object
after that many days. A third tag, `chintan-processed=true`, is set by the
worker (`pipeline.markAudioProcessedIfSafe`) only once transcription has
succeeded, and every tier rule requires all three, so audio the pipeline
never processed is kept whatever its age. An untagged object matches no
rule and lives forever. Two tests hold the template to the Go side:
`TestEveryRetentionTierHasALifecycleRule` and
`TestEveryLifecycleRuleRequiresTheProcessedTag`.

## Everything else: the nine rules

Four tier rules; `ExpireExportSnapshots` (an export's `export.json` and
`job.json`, tagged `chintan-artifact=export`, one day); and, because the
bucket is versioned, `ExpireCaptureAudioVersions` (a superseded or deleted
recording's old version, seven days), `ExpireNoncurrentVersions` (every
other noncurrent version, **90 days**: every append, save, move and merge
rewrites `note.md` in place, so these versions are a note's only undo;
README "Note history" has the `list-object-versions` recipe),
`AbortStaleMultipartUploads` (seven days) and `ExpireDeleteMarkers`. The
90-day rule is the untagged catch-all rather than a note rule because S3
applies the shortest matching expiry and a filter cannot say "everything but
notes".

## Notes: archive, then the sweep

Delete archives: `ArchiveNote` stamps `purge_after` thirty days out
(`ArchiveRetention`) and Restore clears it. A weekly EventBridge rule
(`ExpirySweepRule`, `rate(7 days)`) invokes the worker with
`{"task":"sweep-expired"}`; `purge.Sweeper.Sweep` scans for rows whose
`purge_after_epoch` has passed (`ExpiredNotes`), runs the same cascade a
user's "Delete forever" runs (`NotesService.PurgeNoteArtifacts`: exactly the
S3 keys the note and its captures name, never a prefix listing) and then
deletes the row. DynamoDB TTL is only the backstop, set a fortnight after
the deadline (`repository.ttlGraceSeconds`) so the sweep gets two chances
first — TTL alone would drop the row and orphan every object it named. A
sweep that fails returns the error, so Lambda retries it and the dead-letter
alarm sees one that exhausts its attempts (README "Alarms").

## Edges

The cascade also deletes the note's capture rows, including ones the index
cannot see (`ListUnindexedCaptures`), and treats "already gone" as success so
a retried purge makes progress. A permanent delete removes current objects
only: the note's `note.md` versions linger up to 90 days (README "Note history").
