// Package purge unlinks the S3 objects belonging to a record DynamoDB TTL has
// expired.
//
// TTL deletes the index row and nothing else. Before this existed, an archived
// note reaching its thirty-day purge deadline lost its row and kept every
// object it named — audio, raw transcript, routed transcript, cleaned text,
// segments and peaks — billed monthly, referenced by nothing and reachable by
// nothing. Storage grew monotonically with every note the table quietly
// collected. `chintanctl reconcile` could find them afterwards, which is a
// backstop for a leak rather than a fix for one.
//
// The whole package turns on one distinction, and getting it backwards deletes
// a live user's audio:
//
//   - A TTL deletion is performed by DynamoDB itself. Its stream record carries
//     userIdentity.type == "Service" and userIdentity.principalId ==
//     "dynamodb.amazonaws.com". Those two fields are the only thing in a REMOVE
//     record that says "nobody asked for this; the clock did".
//   - Every other REMOVE — a user pressing delete forever, a cascade from
//     service.PurgeNoteArtifacts, a chintanctl erase — carries no userIdentity
//     at all, because it was performed by a principal that already deleted the
//     objects itself, or is about to. Acting on those would delete the audio of
//     a note somebody has just archived, on the ordinary archive path.
//
// So the filter is deliberately positive: a record is acted on only when it
// says out loud that the expiry service produced it. Anything else, including a
// record shape this code does not recognise, is left alone.
package purge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-lambda-go/events"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// The two values DynamoDB stamps on a stream record it produced itself by
// expiring an item. They are written out here rather than inlined so the test
// that proves a user delete is ignored can name the exact thing it is removing.
const (
	ttlIdentityType = "Service"
	ttlPrincipalID  = "dynamodb.amazonaws.com"
)

// removeEventName is the only stream event this handler acts on. An INSERT or a
// MODIFY carries no old image worth unlinking.
const removeEventName = "REMOVE"

// tenantKeyPrefix mirrors repository.userPK. The partition key is the only
// place a stream record carries the tenant, because the tenant id is not one of
// the attributes promoted onto a note or a capture item.
const tenantKeyPrefix = "USER#"

// NoteCascade is the half of service.NotesService this handler needs. It is an
// interface so the cascade is the production one — the same code a user's
// "delete forever" runs — rather than a second implementation that can drift
// from it.
type NoteCascade interface {
	PurgeNoteArtifacts(ctx context.Context, userID, noteID string, note model.NoteIndex) error
}

// Handler drains a DynamoDB stream.
type Handler struct {
	notes   NoteCascade
	objects repository.Objects
}

// New builds the stream handler. Both dependencies are required: without the
// cascade an expired note keeps its captures' audio, and without the object
// store there is nothing to delete with.
func New(notes NoteCascade, objects repository.Objects) (*Handler, error) {
	switch {
	case notes == nil:
		return nil, fmt.Errorf("purge: note cascade is required")
	case objects == nil:
		return nil, fmt.Errorf("purge: object store is required")
	}
	return &Handler{notes: notes, objects: objects}, nil
}

// Handle processes one stream batch and reports which records failed.
//
// The event source declares ReportBatchItemFailures, so a record that could not
// be unlinked is named by its sequence number and redelivered while the rest of
// the batch is not. That only works because the unlink is idempotent: a
// redelivered record re-issues deletes for objects that are already gone, and
// repository.Objects reports a missing object as ErrNotFound, which every
// delete path here treats as success. A handler that failed on "already
// deleted" would retry until the stream record aged out and then dead-letter a
// job that had in fact succeeded.
func (h *Handler) Handle(ctx context.Context, event events.DynamoDBEvent) (events.DynamoDBEventResponse, error) {
	var resp events.DynamoDBEventResponse

	for _, record := range event.Records {
		recCtx := obs.WithCorrelationID(ctx, obs.NewCorrelationID())

		if !IsTTLExpiry(record) {
			// Not ours. A user delete has already unlinked its own objects, and
			// acting on it here would delete the audio of a note that is merely
			// being archived.
			continue
		}

		if err := h.purge(recCtx, record.Change.OldImage); err != nil {
			obs.Log(recCtx).Error("could not unlink an expired record's objects; it will be retried",
				slog.String("sequence_number", record.Change.SequenceNumber),
				slog.String("error", err.Error()))
			resp.BatchItemFailures = append(resp.BatchItemFailures,
				events.DynamoDBBatchItemFailure{ItemIdentifier: record.Change.SequenceNumber})
		}
	}

	return resp, nil
}

// IsTTLExpiry reports whether DynamoDB's own expiry service produced this
// record, as opposed to a principal that asked for the delete.
//
// Exported because it is the single decision this package exists to get right,
// and a test that can only reach it through a whole batch is a test that proves
// less than it looks like it does.
func IsTTLExpiry(record events.DynamoDBEventRecord) bool {
	if record.EventName != removeEventName {
		return false
	}
	// A nil userIdentity is the ordinary case: somebody called DeleteItem.
	if record.UserIdentity == nil {
		return false
	}
	return record.UserIdentity.Type == ttlIdentityType &&
		record.UserIdentity.PrincipalID == ttlPrincipalID
}

// purge unlinks whatever the removed item named.
//
// Only the keys carried on this record are touched. Nothing here derives a
// prefix, lists a bucket, or infers a sibling object: an expiry that deleted by
// prefix would take a second tenant's objects with it the first time a key
// layout changed.
func (h *Handler) purge(ctx context.Context, old map[string]events.DynamoDBAttributeValue) error {
	if len(old) == 0 {
		// NEW_AND_OLD_IMAGES is what the table is configured for, so an absent
		// old image is a misconfiguration rather than an empty record. Retrying
		// cannot fix it and there is nothing to delete, so say so and stop.
		obs.Log(ctx).Warn("expired record carried no old image; nothing to unlink")
		return nil
	}

	switch itemType := attrString(old, "type"); itemType {
	case "note":
		return h.purgeNote(ctx, old)
	case "capture":
		return h.purgeCapture(ctx, old)
	default:
		// Idempotency records and WebAuthn challenges also carry a ttl and also
		// expire through this stream. They own no S3 objects, so there is
		// nothing to do and their expiry is not an error.
		obs.Log(ctx).Debug("expired record owns no objects",
			slog.String("item_type", itemType))
		return nil
	}
}

// purgeNote runs the same cascade a permanent delete runs.
//
// It is delegated rather than reimplemented because the note's own two objects
// are the smaller half of the problem: the captures filed against the note are
// not themselves expired by TTL — only the note carries the ttl attribute — so
// unlinking just the markdown and the metadata would leave every recording the
// note ever received behind, which is all of the audio and nearly all of the
// bytes.
func (h *Handler) purgeNote(ctx context.Context, old map[string]events.DynamoDBAttributeValue) error {
	tenantID := tenantFromPK(attrString(old, "pk"))
	noteID := attrString(old, "note_id")
	if tenantID == "" || noteID == "" {
		obs.Log(ctx).Warn("expired note record did not identify a note; leaving its objects alone")
		return nil
	}

	note := model.NoteIndex{
		ID: noteID,
		// The cascade unlinks exactly these two by name. An item written before
		// the attributes were promoted carries neither, and the empty string is
		// skipped rather than guessed at.
		S3MarkdownKey: attrString(old, "s3_markdown_key"),
		S3MetaKey:     attrString(old, "s3_meta_key"),
	}

	if err := h.notes.PurgeNoteArtifacts(ctx, tenantID, noteID, note); err != nil {
		return fmt.Errorf("purge: expired note %s: %w", noteID, err)
	}
	obs.Log(ctx).Info("unlinked an expired note's objects and captures",
		slog.String("note_id", noteID))
	return nil
}

// captureObjectAttributes are the six S3 keys a capture owns, in the order the
// cascade in service.PurgeNoteArtifacts unlinks them.
//
// They are top-level attributes rather than fields of the `data` blob precisely
// so a projection can carry them, which is what lets a cascade delete unlink
// every artefact without a second read per capture. The same promotion is what
// makes them readable here, off a stream record, with no table access at all.
var captureObjectAttributes = []string{
	"audio_key",
	"raw_key",
	"routed_key",
	"clean_key",
	"segments_key",
	"peaks_key",
}

// purgeCapture unlinks one capture's artefacts.
//
// No capture item carries a ttl today — only an archived note does — so this
// path is not reached by the current schema. It is here because the cost of
// being wrong about that is a permanently orphaned recording, and because a
// capture-level ttl is the obvious next use of the same attribute.
func (h *Handler) purgeCapture(ctx context.Context, old map[string]events.DynamoDBAttributeValue) error {
	for _, attr := range captureObjectAttributes {
		key := attrString(old, attr)
		if key == "" {
			continue
		}
		if err := h.objects.Delete(ctx, key); err != nil && !errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("purge: expired capture object %s: %w", attr, err)
		}
	}
	obs.Log(ctx).Info("unlinked an expired capture's objects",
		slog.String("capture_id", attrString(old, "capture_id")))
	return nil
}

// attrString reads a string attribute, answering "" for one that is absent or
// is not a string. The alternative is DynamoDBAttributeValue.String(), which
// panics on the wrong type — and a panic here fails the whole batch over an
// attribute the handler did not need.
func attrString(item map[string]events.DynamoDBAttributeValue, name string) string {
	av, ok := item[name]
	if !ok || av.DataType() != events.DataTypeString {
		return ""
	}
	return av.String()
}

// tenantFromPK recovers the tenant from the partition key. It is the only place
// the tenant appears on a note item: `pk` is USER#<tenantId>, and no attribute
// duplicates it.
func tenantFromPK(pk string) string {
	if !strings.HasPrefix(pk, tenantKeyPrefix) {
		return ""
	}
	return strings.TrimPrefix(pk, tenantKeyPrefix)
}
