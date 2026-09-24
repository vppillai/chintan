# Pinned notes

A note can be pinned to the top of Home and the pinned notes dragged into an
order. This note is the backend half. Code: `model.NoteIndex.PinnedAt` /
`PinRank` (`backend/internal/model/types.go`), `repository.NoteOrderKey`
(`backend/internal/repository/dynamo.go`), `NotesService.UpdateNote` and
`ReorderPins` (`backend/internal/service/notes.go`).

## Data model

`pinned_at` (the instant a person pinned the note; empty means not pinned) and
`pin_rank` (its place among the pinned notes) are promoted DynamoDB attributes
written together and only when pinned, like `kind`, so a row from before
2026-09-24 carries neither and needs no backfill. `NoteItemAttributes`
includes them, so `chintanctl reconcile` re-promotes them with the rest. The
wire says `pinned: boolean` on every `Note` and `pin_rank: integer | null`.

## Writes

`PATCH /v1/notes/{id} {pinned: true}` sets `pinned_at = now` and `pin_rank =
1000 × (pinned notes so far)`, so a new pin lands last; the count is one drain
of the partition, paid on a pin alone, and the fifty-first pin is refused (409
`you can pin up to fifty notes`). `{pinned: false}` clears both. Pinning a
pinned note changes nothing.

`POST /v1/notes/pins {ids}` is the drag: `pin_rank` becomes each note's
position × 1000, and the notes come back in that order. Every id must name one
of the caller's pinned, active notes, once, or the whole request is refused
(400) — a partial reorder is not an order anyone asked for. Each note is read
whole before it is written, because a listed note carries no `search_text` or
`cleaned_body` and `PutNote` writes the row it is given; a note already at its
rank is not rewritten, so moving one note writes one row. The step of a
thousand leaves room for an insert-between later without renumbering.

Archiving clears the pin, and a restored note comes back unpinned: the pin was
a place on Home, and the note has left Home.

## Order

`GET /v1/notes?state=active` orders on `repository.NoteOrderKey`: the pinned
tier first, by `pin_rank` ascending with the more recently pinned note ahead on
a tie, then the rest most recently touched first, the id breaking every
remaining tie. Both stores sort on the same exported key, so the in-memory
double every service test runs against orders as production does. The page
cursor now carries that key as the position of the last note served (`pos`)
rather than a touch instant; a cursor minted before the tier existed is read
as an unpinned-tier position, so a client mid-list across the deploy is not
refused. Search results are ranked by score and do not see the tier.
