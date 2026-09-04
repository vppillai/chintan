package pipeline

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
)

// newRetryFixture is newRoutingFixture with the routing attempt bounded to a
// few milliseconds and the spend counter exposed.
func newRetryFixture(t *testing.T, router *fake.Router) (*routingFixture, *memCounter) {
	t.Helper()
	counter := newMemCounter()
	f := newRoutingFixture(t, "add this to my roof repair note the gutter is also leaking",
		provider.RouteDecision{}, false)
	// Rebuild over the same store and objects with the router under test.
	h := newHarness(t, harnessOpts{
		objects:             f.objects,
		store:               f.store,
		stt:                 f.h.stt,
		router:              router,
		routeAttemptTimeout: 20 * time.Millisecond,
		counter:             counter,
	})
	// newHarness makes its own store; point the note creator and the fixture at
	// the one the capture and note were seeded into.
	h.store = f.store
	h.creator.store = f.store
	f.h = h
	f.router = router
	return f, counter
}

func runWithMetrics(t *testing.T, f *routingFixture) (model.CaptureIndex, []emfRecord) {
	t.Helper()
	var metrics bytes.Buffer
	restore := obs.SetMetricOutput(&metrics)
	capture, err := f.run(context.Background(), "c_1")
	restore()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return capture, decodeMetrics(t, metrics.Bytes())
}

func dimension(rec emfRecord, name string) string {
	v, _ := rec.Values[name].(string)
	return v
}

// A routing call stuck in the provider's queue is abandoned after the attempt
// timeout and asked once more on a fresh context. When the second answer
// arrives the capture is filed where the speaker asked, not in a fallback note.
func TestRoutingRetriesOnceAfterATimeout(t *testing.T) {
	router := &fake.Router{
		HangCalls: 1,
		Decision:  provider.RouteDecision{Action: provider.RouteAppend, NoteID: "n1", Confidence: 0.95, Content: "the gutter is also leaking"},
	}
	f, _ := newRetryFixture(t, router)

	capture, records := runWithMetrics(t, f)

	if n := router.CallCount(); n != 2 {
		t.Fatalf("router was called %d times, want 2 (one stall, one answer)", n)
	}
	if capture.Status != model.StatusAppended || capture.NoteID != "n1" {
		t.Fatalf("capture = status %q note %q, want appended to n1", capture.Status, capture.NoteID)
	}
	if titles := f.h.creator.createdTitles(); len(titles) != 0 {
		t.Errorf("a retry that succeeded still created %v", titles)
	}

	timedOut := findMetric(t, records, "RouterTimedOut")
	if got := dimension(timedOut, "Attempt"); got != "1" {
		t.Errorf("RouterTimedOut Attempt = %q, want 1", got)
	}
	retried := findMetric(t, records, "RouterRetried")
	if got := dimension(retried, "Reason"); got != routeRetryTimeout {
		t.Errorf("RouterRetried Reason = %q, want %q", got, routeRetryTimeout)
	}
}

// When the retry stalls too, the existing fallback takes over: the dictation is
// kept in a new, auto-titled note. Neither stalled attempt costs the day's
// budget anything — the breaker reserved for each and released each in full,
// so the day was charged exactly what a router that failed outright would have
// charged: transcription and cleanup only.
func TestRoutingFallsBackAfterTheRetryAlsoTimesOut(t *testing.T) {
	stalled := &fake.Router{HangCalls: 2}
	f, counter := newRetryFixture(t, stalled)
	capture, records := runWithMetrics(t, f)

	if n := stalled.CallCount(); n != 2 {
		t.Fatalf("router was called %d times, want exactly 2", n)
	}
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %q, want appended into the fallback note", capture.Status)
	}
	titles := f.h.creator.createdTitles()
	if len(titles) != 1 || !strings.HasPrefix(titles[0], "Voice note ") {
		t.Fatalf("created titles = %v, want one timestamped fallback note", titles)
	}
	body, _ := f.objects.Get(context.Background(), mustGetNote(t, f.store, f.userID, capture.NoteID).S3MarkdownKey)
	if !strings.Contains(string(body), "the gutter is also leaking") {
		t.Errorf("fallback note body = %q, want the dictation kept", body)
	}

	var timeouts int
	for _, rec := range records {
		for _, n := range rec.Names {
			if n == "RouterTimedOut" {
				timeouts++
			}
		}
	}
	if timeouts != 2 {
		t.Errorf("RouterTimedOut records = %d, want one per stalled attempt", timeouts)
	}
	if got := dimension(findMetric(t, records, "RouterRetried"), "Reason"); got != routeRetryTimeout {
		t.Errorf("RouterRetried Reason = %q, want %q", got, routeRetryTimeout)
	}

	// The control: the same capture through a router that fails immediately.
	failing := &fake.Router{ShouldFail: true}
	g, control := newRetryFixture(t, failing)
	if _, err := g.run(context.Background(), "c_1"); err != nil {
		t.Fatalf("control run: %v", err)
	}
	if counter.total() != control.total() {
		t.Errorf("two stalled attempts charged %d micros, an immediate failure %d; a timeout must cost nothing",
			counter.total(), control.total())
	}
	if counter.total() <= 0 {
		t.Errorf("day total = %d; transcription and cleanup should still have been charged", counter.total())
	}
}

// An overloaded provider (MiniMax answers 529) gets one more try before the
// fallback, so a single bad moment does not mis-file the recording.
func TestRoutingRetriesOnceAfterAServerError(t *testing.T) {
	router := &fake.Router{
		ErrCalls: []error{&provider.StatusError{Op: "llm request failed", StatusCode: 529}},
		Decision: provider.RouteDecision{Action: provider.RouteAppend, NoteID: "n1", Confidence: 0.95, Content: "the gutter is also leaking"},
	}
	f, _ := newRetryFixture(t, router)
	capture, records := runWithMetrics(t, f)

	if n := router.CallCount(); n != 2 {
		t.Fatalf("router was called %d times, want 2", n)
	}
	if capture.NoteID != "n1" {
		t.Fatalf("note_id = %q, want n1 from the second attempt", capture.NoteID)
	}
	if got := dimension(findMetric(t, records, "RouterRetried"), "Reason"); got != routeRetryServerError {
		t.Errorf("RouterRetried Reason = %q, want %q", got, routeRetryServerError)
	}
	if hasMetric(records, "RouterTimedOut") {
		t.Error("a 5xx is not a timeout and must not count as one")
	}
}

// A 4xx is our request and will fail identically; retrying it would only delay
// the fallback. One call, then the dictation goes into a new note.
func TestRoutingDoesNotRetryAClientError(t *testing.T) {
	router := &fake.Router{
		ErrCalls: []error{&provider.StatusError{Op: "llm request failed", StatusCode: 400}},
		Decision: provider.RouteDecision{Action: provider.RouteAppend, NoteID: "n1", Confidence: 0.95},
	}
	f, _ := newRetryFixture(t, router)
	capture, records := runWithMetrics(t, f)

	if n := router.CallCount(); n != 1 {
		t.Fatalf("router was called %d times, want 1", n)
	}
	if titles := f.h.creator.createdTitles(); len(titles) != 1 {
		t.Fatalf("created titles = %v, want the fallback note", titles)
	}
	if capture.NoteID == "n1" {
		t.Error("a 4xx must not be retried into the routed note")
	}
	if hasMetric(records, "RouterRetried") {
		t.Error("RouterRetried was emitted for a request that was not retried")
	}
}

// The spend cap is enforced per attempt and is never retried around: a second
// attempt would be a second reservation against a day that is already over
// budget.
func TestRoutingDoesNotRetryPastTheSpendCap(t *testing.T) {
	router := &fake.Router{Decision: provider.RouteDecision{Action: provider.RouteAppend, NoteID: "n1", Confidence: 0.95}}
	f := newRoutingFixture(t, "add this to my roof repair note the gutter is also leaking",
		provider.RouteDecision{}, false)
	ctx := context.Background()

	// Already transcribed, so routing is the first paid call and the one the
	// cap refuses.
	rawKey := "tenants/user1/captures/c_1/raw.txt"
	if err := f.objects.Put(ctx, rawKey, []byte("add this to my roof repair note the gutter is also leaking"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	capture, err := f.store.GetCapture(ctx, f.userID, "c_1")
	if err != nil {
		t.Fatal(err)
	}
	capture.RawKey, capture.Status = rawKey, model.StatusTranscribed
	if _, err := f.store.PutCapture(ctx, capture); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, harnessOpts{
		objects: f.objects, store: f.store, stt: f.h.stt, router: router,
		routeAttemptTimeout: 20 * time.Millisecond, counter: newMemCounter(),
		capMicros: 1,
	})
	h.store, h.creator.store = f.store, f.store
	f.h = h

	final, err := f.run(ctx, "c_1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := router.CallCount(); n != 0 {
		t.Fatalf("router was called %d times past the cap, want 0", n)
	}
	if final.Status != model.StatusSpendCapped {
		t.Errorf("status = %q, want %q", final.Status, model.StatusSpendCapped)
	}
}
