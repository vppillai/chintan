package purge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

const (
	testTenant = "user1"
	testNote   = "note_1"
)

// fixture is a real NotesService over in-memory storage, carrying one archived
// note with one capture, and every object both of them name.
type fixture struct {
	store   *memory.Store
	objects *memory.Objects
	notes   *service.NotesService
	handler *Handler
	note    model.NoteIndex
	capture model.CaptureIndex
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()

	store := memory.NewStore()
	objects := memory.NewObjects()
	notes := service.NewNotesService(store, objects)

	note, err := notes.CreateNote(ctx, testTenant, "Roof repair", nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}

	capture := model.CaptureIndex{
		ID:          "cap_1",
		NoteID:      note.ID,
		UserID:      testTenant,
		Status:      model.StatusAppended,
		CreatedAt:   model.Now(),
		AudioKey:    fmt.Sprintf("tenants/%s/captures/cap_1/audio.webm", testTenant),
		RawKey:      fmt.Sprintf("tenants/%s/captures/cap_1/raw.txt", testTenant),
		RoutedKey:   fmt.Sprintf("tenants/%s/captures/cap_1/routed.txt", testTenant),
		CleanKey:    fmt.Sprintf("tenants/%s/captures/cap_1/clean.txt", testTenant),
		SegmentsKey: fmt.Sprintf("tenants/%s/captures/cap_1/segments.json", testTenant),
		PeaksKey:    fmt.Sprintf("tenants/%s/captures/cap_1/peaks.json", testTenant),
	}
	stored, err := store.PutCapture(ctx, capture)
	if err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	for _, key := range captureKeys(stored) {
		if err := objects.Put(ctx, key, []byte("x"), "application/octet-stream"); err != nil {
			t.Fatalf("Put(%s): %v", key, err)
		}
	}

	handler, err := New(notes, objects)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return &fixture{
		store: store, objects: objects, notes: notes,
		handler: handler, note: note, capture: stored,
	}
}

func captureKeys(c model.CaptureIndex) []string {
	return []string{c.AudioKey, c.RawKey, c.RoutedKey, c.CleanKey, c.SegmentsKey, c.PeaksKey}
}

// allKeys is every object the fixture's note and capture own together.
func (f *fixture) allKeys() []string {
	return append(captureKeys(f.capture), f.note.S3MarkdownKey, f.note.S3MetaKey)
}

func (f *fixture) surviving(t *testing.T) []string {
	t.Helper()
	var alive []string
	for _, key := range f.allKeys() {
		if _, err := f.objects.Get(context.Background(), key); err == nil {
			alive = append(alive, key)
		} else if !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("Get(%s): %v", key, err)
		}
	}
	sort.Strings(alive)
	return alive
}

// noteRemoveRecord is the stream record DynamoDB emits for the fixture's note.
// identity is what distinguishes an expiry from a delete somebody asked for.
func (f *fixture) noteRemoveRecord(identity *events.DynamoDBUserIdentity) events.DynamoDBEventRecord {
	return events.DynamoDBEventRecord{
		EventName:    "REMOVE",
		EventSource:  "aws:dynamodb",
		UserIdentity: identity,
		Change: events.DynamoDBStreamRecord{
			SequenceNumber: "000000000000000000001",
			StreamViewType: "NEW_AND_OLD_IMAGES",
			Keys: map[string]events.DynamoDBAttributeValue{
				"pk": events.NewStringAttribute(tenantKeyPrefix + testTenant),
				"sk": events.NewStringAttribute("NOTE#" + f.note.ID),
			},
			OldImage: map[string]events.DynamoDBAttributeValue{
				"pk":              events.NewStringAttribute(tenantKeyPrefix + testTenant),
				"sk":              events.NewStringAttribute("NOTE#" + f.note.ID),
				"type":            events.NewStringAttribute("note"),
				"note_id":         events.NewStringAttribute(f.note.ID),
				"title":           events.NewStringAttribute(f.note.Title),
				"s3_markdown_key": events.NewStringAttribute(f.note.S3MarkdownKey),
				"s3_meta_key":     events.NewStringAttribute(f.note.S3MetaKey),
				"deleted_at":      events.NewStringAttribute(model.Now()),
			},
		},
	}
}

// ttlIdentity is what DynamoDB stamps on a record its own expiry service
// produced.
func ttlIdentity() *events.DynamoDBUserIdentity {
	return &events.DynamoDBUserIdentity{Type: ttlIdentityType, PrincipalID: ttlPrincipalID}
}

// TestTTLExpiryUnlinksEveryObjectTheNoteOwned is the leak this package closes.
//
// Before it existed, TTL removed the index row and left all eight objects in
// the bucket: two for the note, six for its capture. Nothing referenced them
// and nothing could reach them, and the bill grew with every note the table
// collected.
func TestTTLExpiryUnlinksEveryObjectTheNoteOwned(t *testing.T) {
	f := newFixture(t)

	resp, err := f.handler.Handle(context.Background(), events.DynamoDBEvent{
		Records: []events.DynamoDBEventRecord{f.noteRemoveRecord(ttlIdentity())},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Fatalf("batch item failures = %v, want none", resp.BatchItemFailures)
	}

	if alive := f.surviving(t); len(alive) != 0 {
		t.Errorf("these objects outlived the expiry that removed their note: %v", alive)
	}

	// The capture row itself is not expired by TTL — only the note carries the
	// attribute — so a handler that unlinked objects and left the rows behind
	// would leave the note's captures listed forever against a note that no
	// longer exists.
	page, err := f.store.ListCapturesByNote(context.Background(), testTenant, f.note.ID, repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListCapturesByNote: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("captures still filed against the expired note: %d, want 0", len(page.Items))
	}
}

// TestAUserDeleteIsNotTreatedAsAnExpiry is the other direction, and it is the
// one that costs a user their recording if it regresses.
//
// Archiving a note is a PutItem, but permanently deleting one — and every
// cascade and chintanctl erase — is a DeleteItem, which produces a REMOVE
// record carrying the same old image as an expiry and no userIdentity at all.
// Acting on those would delete the audio of a note the user still has.
func TestAUserDeleteIsNotTreatedAsAnExpiry(t *testing.T) {
	cases := map[string]*events.DynamoDBUserIdentity{
		// The ordinary DeleteItem: no identity block on the record at all.
		"no user identity": nil,
		// A principal that is not the expiry service.
		"an assumed role": {Type: "AssumedRole", PrincipalID: "AROAEXAMPLE:chintan-api"},
		// The right principal under the wrong type, and vice versa: both halves
		// have to match or the filter is a substring check.
		"the right principal, the wrong type": {Type: "AssumedRole", PrincipalID: ttlPrincipalID},
		"the right type, the wrong principal": {Type: ttlIdentityType, PrincipalID: "s3.amazonaws.com"},
	}

	for name, identity := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			want := f.surviving(t)
			if len(want) != len(f.allKeys()) {
				t.Fatalf("fixture starts with %d objects, want %d", len(want), len(f.allKeys()))
			}

			resp, err := f.handler.Handle(context.Background(), events.DynamoDBEvent{
				Records: []events.DynamoDBEventRecord{f.noteRemoveRecord(identity)},
			})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if len(resp.BatchItemFailures) != 0 {
				t.Fatalf("batch item failures = %v, want none", resp.BatchItemFailures)
			}

			if got := f.surviving(t); len(got) != len(want) {
				t.Errorf("a delete this handler did not perform destroyed objects: %d of %d survived",
					len(got), len(want))
			}
		})
	}
}

// TestOnlyRemoveEventsAreActedOn guards the other half of the filter. An
// archive is a MODIFY carrying a complete old image, and unlinking on that
// would delete the audio of a note the user can still restore.
func TestOnlyRemoveEventsAreActedOn(t *testing.T) {
	for _, eventName := range []string{"INSERT", "MODIFY"} {
		t.Run(eventName, func(t *testing.T) {
			f := newFixture(t)
			record := f.noteRemoveRecord(ttlIdentity())
			record.EventName = eventName

			if IsTTLExpiry(record) {
				t.Fatalf("%s was read as a TTL expiry", eventName)
			}
			if _, err := f.handler.Handle(context.Background(), events.DynamoDBEvent{
				Records: []events.DynamoDBEventRecord{record},
			}); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if got := f.surviving(t); len(got) != len(f.allKeys()) {
				t.Errorf("a %s event destroyed objects: %d of %d survived",
					eventName, len(got), len(f.allKeys()))
			}
		})
	}
}

// TestARedeliveredRecordDoesNotFail is why the handler may report batch item
// failures at all. Streams are at-least-once, so the same expiry arrives twice
// whenever anything else in the batch is retried; a second pass finds every
// object already gone and must call that success, not a fault to redeliver
// until the record ages out of the stream.
func TestARedeliveredRecordDoesNotFail(t *testing.T) {
	f := newFixture(t)
	event := events.DynamoDBEvent{
		Records: []events.DynamoDBEventRecord{f.noteRemoveRecord(ttlIdentity())},
	}

	for attempt := 1; attempt <= 3; attempt++ {
		resp, err := f.handler.Handle(context.Background(), event)
		if err != nil {
			t.Fatalf("delivery %d: Handle: %v", attempt, err)
		}
		if len(resp.BatchItemFailures) != 0 {
			t.Fatalf("delivery %d reported %v; an already-unlinked record is not a failure",
				attempt, resp.BatchItemFailures)
		}
	}
}

// TestAnExpiredCaptureUnlinksItsSixArtefacts covers the other item type the
// stream can carry. No capture holds a ttl today, but the six keys are promoted
// to top-level attributes precisely so a cascade can read them off a projection
// — which is what makes them readable off a stream record with no table access
// at all.
func TestAnExpiredCaptureUnlinksItsSixArtefacts(t *testing.T) {
	f := newFixture(t)

	old := map[string]events.DynamoDBAttributeValue{
		"pk":         events.NewStringAttribute(tenantKeyPrefix + testTenant),
		"sk":         events.NewStringAttribute("CAPTURE#" + f.capture.ID),
		"type":       events.NewStringAttribute("capture"),
		"capture_id": events.NewStringAttribute(f.capture.ID),
	}
	for i, attr := range captureObjectAttributes {
		old[attr] = events.NewStringAttribute(captureKeys(f.capture)[i])
	}

	resp, err := f.handler.Handle(context.Background(), events.DynamoDBEvent{
		Records: []events.DynamoDBEventRecord{{
			EventName:    "REMOVE",
			UserIdentity: ttlIdentity(),
			Change: events.DynamoDBStreamRecord{
				SequenceNumber: "000000000000000000002",
				OldImage:       old,
			},
		}},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Fatalf("batch item failures = %v, want none", resp.BatchItemFailures)
	}

	for _, key := range captureKeys(f.capture) {
		if _, err := f.objects.Get(context.Background(), key); !errors.Is(err, repository.ErrNotFound) {
			t.Errorf("%s outlived the capture that named it (err = %v)", key, err)
		}
	}
	// The note's own objects were not named on this record and must be untouched.
	for _, key := range []string{f.note.S3MarkdownKey, f.note.S3MetaKey} {
		if _, err := f.objects.Get(context.Background(), key); err != nil {
			t.Errorf("%s was deleted by a record that did not name it: %v", key, err)
		}
	}
}

// TestAnExpiredRecordWithNoObjectsIsNotAnError covers the other items that
// carry the shared ttl attribute — idempotency records and WebAuthn challenges
// — which expire through the same stream and own nothing in S3.
func TestAnExpiredRecordWithNoObjectsIsNotAnError(t *testing.T) {
	f := newFixture(t)

	resp, err := f.handler.Handle(context.Background(), events.DynamoDBEvent{
		Records: []events.DynamoDBEventRecord{{
			EventName:    "REMOVE",
			UserIdentity: ttlIdentity(),
			Change: events.DynamoDBStreamRecord{
				SequenceNumber: "000000000000000000003",
				OldImage: map[string]events.DynamoDBAttributeValue{
					"pk":   events.NewStringAttribute("WACHAL#abc"),
					"sk":   events.NewStringAttribute("WACHAL#abc"),
					"type": events.NewStringAttribute("wachal"),
					"data": events.NewStringAttribute("{}"),
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Fatalf("batch item failures = %v, want none", resp.BatchItemFailures)
	}
	if got := f.surviving(t); len(got) != len(f.allKeys()) {
		t.Errorf("an unrelated expiry destroyed objects: %d of %d survived", len(got), len(f.allKeys()))
	}
}

// failingCascade fails every purge, so the batch-item-failure path can be
// exercised without inventing a broken object store.
type failingCascade struct {
	mu    sync.Mutex
	calls int
}

var errCascadeBroke = errors.New("induced cascade failure")

func (c *failingCascade) PurgeNoteArtifacts(context.Context, string, string, model.NoteIndex) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return errCascadeBroke
}

// TestAFailedUnlinkIsReportedForRedelivery proves the handler does not swallow
// a cascade failure. Swallowing it is how the leak this package closes would
// come back silently: the record is deleted from the stream, the objects stay,
// and nothing has recorded that they did.
func TestAFailedUnlinkIsReportedForRedelivery(t *testing.T) {
	f := newFixture(t)
	cascade := &failingCascade{}
	handler, err := New(cascade, f.objects)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	record := f.noteRemoveRecord(ttlIdentity())
	resp, err := handler.Handle(context.Background(), events.DynamoDBEvent{
		Records: []events.DynamoDBEventRecord{record},
	})
	if err != nil {
		// The batch is reported per record, not as an invocation error: an
		// error here would redeliver records that succeeded.
		t.Fatalf("Handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 1 {
		t.Fatalf("batch item failures = %v, want exactly one", resp.BatchItemFailures)
	}
	if got := resp.BatchItemFailures[0].ItemIdentifier; got != record.Change.SequenceNumber {
		t.Errorf("item identifier = %q, want the sequence number %q", got, record.Change.SequenceNumber)
	}
	if cascade.calls != 1 {
		t.Errorf("cascade calls = %d, want 1", cascade.calls)
	}
}

// TestNewRefusesAnIncompleteHandler keeps a half-built handler from starting.
// A nil cascade unlinks nothing and reports success, which is the leak wearing
// a green tick.
func TestNewRefusesAnIncompleteHandler(t *testing.T) {
	if _, err := New(nil, memory.NewObjects()); err == nil {
		t.Error("New accepted a nil note cascade")
	}
	if _, err := New(service.NewNotesService(memory.NewStore(), memory.NewObjects()), nil); err == nil {
		t.Error("New accepted a nil object store")
	}
}
