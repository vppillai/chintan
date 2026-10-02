# Pinned notes

A note can be pinned to the top of Home and the pinned notes dragged into an
order. This note is the backend half. Code: `model.NoteIndex.PinnedAt` /
`PinRank` (`backend/internal/model/types.go`), `repository.NoteOrderKey`
(`backend/internal/repository/dynamo_notes.go`), `NotesService.UpdateNote`
(`backend/internal/service/notes.go`) and `ReorderPins`
(`backend/internal/service/notes_pins.go`).

## Data model

`pinned_at` (the instant a person pinned the note; empty means not pinned) and
`pin_rank` (its place among the pinned notes) are promoted DynamoDB attributes
written together and only when pinned, like `kind`, so a never-pinned row
carries neither and needs no backfill. `NoteItemAttributes`
includes them, so `chintanctl reconcile` re-promotes them with the rest. The
wire says `pinned: boolean` on every `Note` and `pin_rank: integer | null`.

## Writes

`PATCH /v1/notes/{id} {pinned: true}` sets `pinned_at` to the instant and
`pin_rank` to one `model.PinRankStep` (1000) past the highest rank in use, so a new pin lands last even
after an unpin has left a gap (count × 1000 would then equal an existing rank
and the tie-break would put the new pin above it); finding the highest is one
drain of the partition, paid on a pin alone, and a pin past
`model.MaxPinnedNotes` (50) is refused (409 `you can pin up to fifty notes`,
`ErrPinLimit`). `{pinned: false}` clears both. Pinning a
pinned note changes nothing but the row's `version`: the pin fields are left
as they are and `updated_at` holds, so the note does not re-file under Today.

`POST /v1/notes/pins {ids}` is the drag: `pin_rank` becomes each note's
position × `PinRankStep`, and the notes come back in that order. The list
names between one and `MaxPinnedNotes` ids (`ErrPinBatchSize`, 400), and
every id must name one of the caller's pinned, active notes, once, or the
whole request is refused (400) — a partial reorder is not an order anyone
asked for. Each note is read
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
remaining tie. Every service test runs the real store over the fake table
(`repository/dynamofake`), so it orders as production does. The page
cursor (`repository/cursor.go`, `encodeNoteCursor`) carries that key as the
position of the last note served (`pos`), scoped to the partition and the
shelf that issued it. `decodeNoteCursor` reads two older shapes: a cursor
with a touch instant (`at`) and an id is taken as an unpinned-tier position,
and one carrying DynamoDB key attributes is refused, so the client starts
again from the first page. Search results are ranked by score and do not see
the tier.
