package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
)

// fakeNoteCreator stands in for NotesService.
type fakeNoteCreator struct {
	store  *mockStore
	seq    int
	titles []string
}

func (f *fakeNoteCreator) CreateNote(ctx context.Context, userID, title string, aliases []string) (model.NoteIndex, error) {
	f.seq++
	id := fmt.Sprintf("new_%d", f.seq)
	f.titles = append(f.titles, title)
	note := model.NoteIndex{
		ID:            id,
		Title:         title,
		S3MarkdownKey: fmt.Sprintf("tenants/%s/notes/%s/note.md", userID, id),
	}
	if err := f.store.PutNote(ctx, userID, note); err != nil {
		return model.NoteIndex{}, err
	}
	return note, nil
}

// routingFixture builds a service with one existing note and an unrouted capture.
type routingFixture struct {
	svc     *CaptureService
	store   *mockStore
	objects *mockObjects
	creator *fakeNoteCreator
	router  *provider.FakeRouter
	userID  string
}

func newRoutingFixture(t *testing.T, transcript string, decision provider.RouteDecision, routerFails bool) *routingFixture {
	t.Helper()

	store := newMockStore()
	objects := newMockObjects()
	creator := &fakeNoteCreator{store: store}
	router := &provider.FakeRouter{Decision: decision, ShouldFail: routerFails}

	svc := NewCaptureService(store, objects,
		&provider.FakeSTT{Response: transcript},
		&provider.FakeLLM{},
	).WithRouting(router, creator)

	ctx := context.Background()
	userID := "user1"

	if err := store.PutNote(ctx, userID, model.NoteIndex{
		ID:            "n1",
		Title:         "Roof repair",
		Aliases:       []string{"roof"},
		UpdatedAt:     "2026-08-01T00:00:00Z",
		S3MarkdownKey: "tenants/user1/notes/n1/note.md",
	}); err != nil {
		t.Fatalf("PutNote: %v", err)
	}
	if err := objects.Put(ctx, "tenants/user1/notes/n1/note.md", []byte("existing line"), "text/markdown"); err != nil {
		t.Fatalf("Put note body: %v", err)
	}
	if err := objects.Put(ctx, "tenants/user1/captures/c_1/audio.webm", []byte("audio"), "audio/webm"); err != nil {
		t.Fatalf("Put audio: %v", err)
	}
	// NoteID is deliberately empty: the destination comes from routing.
	if err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: userID, Status: model.StatusUploaded,
		Mode: model.CleanupFaithful, AudioKey: "tenants/user1/captures/c_1/audio.webm",
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	return &routingFixture{svc: svc, store: store, objects: objects, creator: creator, router: router, userID: userID}
}

func TestCompleteCaptureAppendsToSpokenNote(t *testing.T) {
	f := newRoutingFixture(t,
		"add this to my roof repair note the gutter is also leaking",
		provider.RouteDecision{
			Action:     provider.RouteAppend,
			NoteID:     "n1",
			Confidence: 0.95,
			Content:    "the gutter is also leaking",
		}, false)

	ctx := context.Background()
	capture, err := f.svc.CompleteCapture(ctx, f.userID, "c_1")
	if err != nil {
		t.Fatalf("CompleteCapture: %v", err)
	}

	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended", capture.Status)
	}
	if capture.NoteID != "n1" {
		t.Fatalf("note_id = %q, want n1", capture.NoteID)
	}
	if len(f.creator.titles) != 0 {
		t.Fatalf("created notes %v, want none", f.creator.titles)
	}

	body, err := f.objects.Get(ctx, "tenants/user1/notes/n1/note.md")
	if err != nil {
		t.Fatalf("Get note body: %v", err)
	}
	// FakeLLM in faithful mode lowercases whatever it is given, so the body shows
	// which text reached cleanup: the routing instruction must be gone.
	if !strings.Contains(string(body), "the gutter is also leaking") {
		t.Errorf("note body missing dictated text: %q", body)
	}
	if strings.Contains(string(body), "add this to my roof repair note") {
		t.Errorf("routing instruction leaked into note body: %q", body)
	}
	if !strings.Contains(string(body), "existing line") {
		t.Errorf("existing note content was lost: %q", body)
	}
}

func TestCompleteCaptureAsksWhenRoutingIsUncertain(t *testing.T) {
	f := newRoutingFixture(t,
		"the gutter is also leaking",
		provider.RouteDecision{
			Action:     provider.RouteAppend,
			NoteID:     "n1",
			Confidence: 0.4,
			Content:    "the gutter is also leaking",
		}, false)

	ctx := context.Background()
	capture, err := f.svc.CompleteCapture(ctx, f.userID, "c_1")
	if err != nil {
		t.Fatalf("CompleteCapture: %v", err)
	}

	if capture.Status != model.StatusNeedsTarget {
		t.Fatalf("status = %s, want needs_target", capture.Status)
	}
	if capture.NoteID != "" {
		t.Errorf("note_id = %q, want empty until confirmed", capture.NoteID)
	}
	if capture.SuggestedNoteID != "n1" {
		t.Errorf("suggested_note_id = %q, want n1", capture.SuggestedNoteID)
	}

	body, _ := f.objects.Get(ctx, "tenants/user1/notes/n1/note.md")
	if string(body) != "existing line" {
		t.Errorf("note was modified before confirmation: %q", body)
	}

	// Completing again must keep waiting rather than guess.
	again, err := f.svc.CompleteCapture(ctx, f.userID, "c_1")
	if err != nil {
		t.Fatalf("second CompleteCapture: %v", err)
	}
	if again.Status != model.StatusNeedsTarget {
		t.Errorf("status = %s, want needs_target", again.Status)
	}
}

func TestSetCaptureTargetConfirmsSuggestion(t *testing.T) {
	f := newRoutingFixture(t,
		"the gutter is also leaking",
		provider.RouteDecision{
			Action:     provider.RouteAppend,
			NoteID:     "n1",
			Confidence: 0.4,
			Content:    "the gutter is also leaking",
		}, false)

	ctx := context.Background()
	if _, err := f.svc.CompleteCapture(ctx, f.userID, "c_1"); err != nil {
		t.Fatalf("CompleteCapture: %v", err)
	}

	capture, err := f.svc.SetCaptureTarget(ctx, f.userID, "c_1", "n1", "")
	if err != nil {
		t.Fatalf("SetCaptureTarget: %v", err)
	}
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended", capture.Status)
	}
	if capture.SuggestedNoteID != "" {
		t.Errorf("suggested_note_id = %q, want cleared", capture.SuggestedNoteID)
	}

	body, _ := f.objects.Get(ctx, "tenants/user1/notes/n1/note.md")
	if !strings.Contains(string(body), "the gutter is also leaking") {
		t.Errorf("note body missing dictated text: %q", body)
	}
}

func TestSetCaptureTargetCanCreateNoteInstead(t *testing.T) {
	f := newRoutingFixture(t,
		"the gutter is also leaking",
		provider.RouteDecision{
			Action:     provider.RouteAppend,
			NoteID:     "n1",
			Confidence: 0.4,
			Content:    "the gutter is also leaking",
		}, false)

	ctx := context.Background()
	if _, err := f.svc.CompleteCapture(ctx, f.userID, "c_1"); err != nil {
		t.Fatalf("CompleteCapture: %v", err)
	}

	capture, err := f.svc.SetCaptureTarget(ctx, f.userID, "c_1", "", "Gutter leak")
	if err != nil {
		t.Fatalf("SetCaptureTarget: %v", err)
	}
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended", capture.Status)
	}
	if len(f.creator.titles) != 1 || f.creator.titles[0] != "Gutter leak" {
		t.Fatalf("created titles = %v, want [Gutter leak]", f.creator.titles)
	}

	untouched, _ := f.objects.Get(ctx, "tenants/user1/notes/n1/note.md")
	if string(untouched) != "existing line" {
		t.Errorf("suggested note was modified: %q", untouched)
	}
}

func TestSetCaptureTargetRejectsAlreadyRoutedCapture(t *testing.T) {
	f := newRoutingFixture(t, "hello",
		provider.RouteDecision{Action: provider.RouteNew, Title: "Hello", Confidence: 1, Content: "hello"}, false)

	ctx := context.Background()
	if _, err := f.svc.CompleteCapture(ctx, f.userID, "c_1"); err != nil {
		t.Fatalf("CompleteCapture: %v", err)
	}

	if _, err := f.svc.SetCaptureTarget(ctx, f.userID, "c_1", "n1", ""); err == nil {
		t.Fatal("expected error when retargeting a routed capture")
	}
}

func TestCompleteCaptureCreatesNoteWithSpokenTitle(t *testing.T) {
	f := newRoutingFixture(t,
		"remind me to book the dentist on tuesday",
		provider.RouteDecision{
			Action:     provider.RouteNew,
			Title:      "Dentist appointment reminder",
			Confidence: 0.9,
			Content:    "remind me to book the dentist on tuesday",
		}, false)

	ctx := context.Background()
	capture, err := f.svc.CompleteCapture(ctx, f.userID, "c_1")
	if err != nil {
		t.Fatalf("CompleteCapture: %v", err)
	}

	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended", capture.Status)
	}
	if len(f.creator.titles) != 1 || f.creator.titles[0] != "Dentist appointment reminder" {
		t.Fatalf("created titles = %v, want [Dentist appointment reminder]", f.creator.titles)
	}
	if capture.NoteID == "" {
		t.Error("capture has no note_id after creating a note")
	}
}

func TestCompleteCaptureSavesNoteWhenRouterFails(t *testing.T) {
	f := newRoutingFixture(t, "some dictated words", provider.RouteDecision{}, true)

	ctx := context.Background()
	capture, err := f.svc.CompleteCapture(ctx, f.userID, "c_1")
	if err != nil {
		t.Fatalf("CompleteCapture: %v", err)
	}

	// A router outage must not cost the user their recording.
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended", capture.Status)
	}
	if len(f.creator.titles) != 1 {
		t.Fatalf("created titles = %v, want one fallback note", f.creator.titles)
	}
	if !strings.HasPrefix(f.creator.titles[0], "Voice note ") {
		t.Errorf("fallback title = %q, want a timestamped voice note", f.creator.titles[0])
	}

	body, _ := f.objects.Get(ctx, f.store.notes[f.userID+"/"+capture.NoteID].S3MarkdownKey)
	if !strings.Contains(string(body), "some dictated words") {
		t.Errorf("note body missing dictated text: %q", body)
	}
}

func TestCompleteCaptureIgnoresRoutingForExplicitTarget(t *testing.T) {
	f := newRoutingFixture(t, "words",
		provider.RouteDecision{Action: provider.RouteNew, Title: "Should not be used", Confidence: 1}, false)

	ctx := context.Background()
	// A capture created against a note keeps that note; routing is skipped.
	if err := f.store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_2", UserID: f.userID, NoteID: "n1", Status: model.StatusUploaded,
		Mode: model.CleanupFaithful, AudioKey: "tenants/user1/captures/c_1/audio.webm",
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	capture, err := f.svc.CompleteCapture(ctx, f.userID, "c_2")
	if err != nil {
		t.Fatalf("CompleteCapture: %v", err)
	}
	if capture.NoteID != "n1" {
		t.Errorf("note_id = %q, want n1", capture.NoteID)
	}
	if len(f.router.Calls) != 0 {
		t.Errorf("router was consulted %d times, want 0", len(f.router.Calls))
	}
}
