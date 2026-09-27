package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
)

// recordingPusher stands in for the push service: it records every send and
// answers each subscription with the status the test set for it.
type recordingPusher struct {
	mu       sync.Mutex
	statuses map[string]int
	err      error
	sent     []sentPush
	// onSend runs during a send, where a test stands in for the person
	// acting while the worker is mid-loop.
	onSend func(sub model.PushSubscription)
}

type sentPush struct {
	SubscriptionID string
	Payload        pushPayload
}

func (r *recordingPusher) Send(_ context.Context, sub model.PushSubscription, payload []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var decoded pushPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return 0, err
	}
	r.sent = append(r.sent, sentPush{SubscriptionID: sub.ID, Payload: decoded})
	if r.onSend != nil {
		r.onSend(sub)
	}
	if r.err != nil {
		return 0, r.err
	}
	if status, ok := r.statuses[sub.ID]; ok {
		return status, nil
	}
	return 201, nil
}

func seedPushSubscription(t *testing.T, h *harness, id string) {
	t.Helper()
	if err := h.store.PutPushSubscription(context.Background(), "user1", model.PushSubscription{
		ID: id, Endpoint: "https://push.example/" + id, P256DH: "p", Auth: "a", CreatedAt: model.FormatTime(h.clock.Now()),
	}); err != nil {
		t.Fatalf("seed subscription %s: %v", id, err)
	}
}

// A device's recording that files sends one push per subscription, naming
// the note; the push service's verdict is written back to the row.
func TestAppendedDeviceCaptureIsPushedToEverySubscription(t *testing.T) {
	pusher := &recordingPusher{statuses: map[string]int{"laptop": 500}}
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Result: &provider.Transcription{Text: "buy milk", Language: "en", Duration: 2}},
		llm: &fake.LLM{Response: "Buy milk."},
	})
	h.pipeline.cfg.Pusher = pusher
	seedUploadedCapture(t, h, "note1")
	ctx := context.Background()
	capture, _ := h.store.GetCapture(ctx, "user1", "c_1")
	capture.Source = model.DeviceSource("dev_ring")
	capture.TargetSource = model.TargetSourceClient
	if _, err := h.store.PutCapture(ctx, capture); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	seedPushSubscription(t, h, "phone")
	seedPushSubscription(t, h, "laptop")

	final, err := h.pipeline.Run(ctx, "user1", "c_1")
	if err != nil || final.Status != model.StatusAppended {
		t.Fatalf("Run: %v, status %s", err, final.Status)
	}
	if len(pusher.sent) != 2 {
		t.Fatalf("%d pushes sent, want one per subscription: %+v", len(pusher.sent), pusher.sent)
	}
	for _, s := range pusher.sent {
		want := pushPayload{Type: "appended", CaptureID: "c_1", NoteID: "note1", Title: "Destination"}
		if s.Payload != want {
			t.Fatalf("payload = %+v, want %+v", s.Payload, want)
		}
	}
	subs, _ := h.store.ListPushSubscriptions(ctx, "user1")
	byID := map[string]model.PushSubscription{}
	for _, s := range subs {
		byID[s.ID] = s
	}
	if byID["phone"].LastSuccessAt == "" || byID["phone"].Failures != 0 {
		t.Fatalf("accepted send not recorded: %+v", byID["phone"])
	}
	if byID["laptop"].LastSuccessAt != "" || byID["laptop"].Failures != 1 {
		t.Fatalf("refused send not counted: %+v", byID["laptop"])
	}

	// A second delivery of the finished capture returns at the top of run
	// and sends nothing more.
	if _, err := h.pipeline.Run(ctx, "user1", "c_1"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(pusher.sent) != 2 {
		t.Fatalf("a retry of a finished capture sent again: %d", len(pusher.sent))
	}
}

// 404 and 410 from the push service are the browser's goodbye: the row is
// removed, and nothing is sent to it again.
func TestGoneSubscriptionIsPruned(t *testing.T) {
	pusher := &recordingPusher{statuses: map[string]int{"old-phone": 410, "reinstalled": 404}}
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Result: &provider.Transcription{Text: "buy milk", Language: "en", Duration: 2}},
		llm: &fake.LLM{Response: "Buy milk."},
	})
	h.pipeline.cfg.Pusher = pusher
	seedUploadedCapture(t, h, "note1")
	ctx := context.Background()
	capture, _ := h.store.GetCapture(ctx, "user1", "c_1")
	capture.Source = model.DeviceSource("dev_ring")
	if _, err := h.store.PutCapture(ctx, capture); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	seedPushSubscription(t, h, "old-phone")
	seedPushSubscription(t, h, "reinstalled")
	seedPushSubscription(t, h, "current")

	if _, err := h.pipeline.Run(ctx, "user1", "c_1"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	subs, _ := h.store.ListPushSubscriptions(ctx, "user1")
	if len(subs) != 1 || subs[0].ID != "current" {
		t.Fatalf("subscriptions after pruning = %+v, want only current", subs)
	}
}

// A switch turned off while the worker is between its list and its send
// stays off: the send's result is written to the row only if the row is
// still there, never as a whole row that would enrol the browser again.
func TestSendResultDoesNotRecreateARowRemovedMeanwhile(t *testing.T) {
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Result: &provider.Transcription{Text: "buy milk", Language: "en", Duration: 2}},
		llm: &fake.LLM{Response: "Buy milk."},
	})
	ctx := context.Background()
	pusher := &recordingPusher{onSend: func(sub model.PushSubscription) {
		if err := h.store.DeletePushSubscription(ctx, "user1", sub.ID); err != nil {
			t.Errorf("delete during send: %v", err)
		}
	}}
	h.pipeline.cfg.Pusher = pusher
	seedUploadedCapture(t, h, "note1")
	capture, _ := h.store.GetCapture(ctx, "user1", "c_1")
	capture.Source = model.DeviceSource("dev_ring")
	if _, err := h.store.PutCapture(ctx, capture); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	seedPushSubscription(t, h, "phone")

	if _, err := h.pipeline.Run(ctx, "user1", "c_1"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(pusher.sent) != 1 {
		t.Fatalf("%d pushes sent, want one", len(pusher.sent))
	}
	if subs, _ := h.store.ListPushSubscriptions(ctx, "user1"); len(subs) != 0 {
		t.Fatalf("a row removed during the send came back: %+v", subs)
	}
}

// The app's own recording into a chosen note is on screen while it files,
// so it is not announced; a recording that fails or parks is, and a capture
// that never had a note names none.
func TestPushFollowsHomesRuleForWhatAPersonIsNotWatching(t *testing.T) {
	pusher := &recordingPusher{}
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Result: &provider.Transcription{Text: "buy milk", Language: "en", Duration: 2}},
		llm: &fake.LLM{Response: "Buy milk."},
	})
	h.pipeline.cfg.Pusher = pusher
	seedUploadedCapture(t, h, "note1")
	ctx := context.Background()
	capture, _ := h.store.GetCapture(ctx, "user1", "c_1")
	capture.TargetSource = model.TargetSourceClient
	if _, err := h.store.PutCapture(ctx, capture); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	seedPushSubscription(t, h, "phone")
	if _, err := h.pipeline.Run(ctx, "user1", "c_1"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(pusher.sent) != 0 {
		t.Fatalf("a recording made into a note in the app was announced: %+v", pusher.sent)
	}

	// The same recording failing is worth hearing about even from the app:
	// the person may have put the phone down.
	failing := newHarness(t, harnessOpts{stt: &fake.STT{Err: errors.New("provider down")}})
	failing.pipeline.cfg.Pusher = pusher
	seedUploadedCapture(t, failing, "note1")
	seedPushSubscription(t, failing, "phone")
	final, err := failing.pipeline.Run(ctx, "user1", "c_1")
	if err != nil || final.Status != model.StatusFailed {
		t.Fatalf("failing Run: %v, status %s", err, final.Status)
	}
	if len(pusher.sent) != 1 || pusher.sent[0].Payload.Type != "failed" || pusher.sent[0].Payload.NoteID != "note1" || pusher.sent[0].Payload.Title != "Destination" {
		t.Fatalf("failed capture's push = %+v", pusher.sent)
	}

	// No pusher, no sends and no error: the dormant instance.
	dormant := newHarness(t, harnessOpts{
		stt: &fake.STT{Result: &provider.Transcription{Text: "buy milk", Language: "en", Duration: 2}},
		llm: &fake.LLM{Response: "Buy milk."},
	})
	seedUploadedCapture(t, dormant, "note1")
	seedPushSubscription(t, dormant, "phone")
	if final, err := dormant.pipeline.Run(ctx, "user1", "c_1"); err != nil || final.Status != model.StatusAppended {
		t.Fatalf("dormant Run: %v, %s", err, final.Status)
	}
}

// A routed recording the router could not place parks at needs_target and
// says so, with no note to name.
func TestNeedsTargetIsPushedWithoutANote(t *testing.T) {
	pusher := &recordingPusher{}
	h := newHarness(t, harnessOpts{
		stt:     &fake.STT{Result: &provider.Transcription{Text: "something unplaceable", Language: "en", Duration: 2}},
		llm:     &fake.LLM{Response: "Something unplaceable."},
		router:  &fake.Router{Decision: provider.RouteDecision{Action: provider.RouteNew, Title: "Unplaced", Content: "something unplaceable", Confidence: 0.9}},
		noNotes: true,
	})
	h.pipeline.cfg.Pusher = pusher
	seedUploadedCapture(t, h, "")
	seedPushSubscription(t, h, "phone")
	final, err := h.pipeline.Run(context.Background(), "user1", "c_1")
	if err != nil || final.Status != model.StatusNeedsTarget {
		t.Fatalf("Run: %v, status %s (%s)", err, final.Status, final.Error)
	}
	if len(pusher.sent) != 1 || pusher.sent[0].Payload != (pushPayload{Type: "needs_target", CaptureID: "c_1"}) {
		t.Fatalf("push = %+v", pusher.sent)
	}
}
