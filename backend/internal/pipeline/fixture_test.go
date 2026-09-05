package pipeline

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/breaker"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// memCounter is an atomic accumulator matching the contract DynamoCounter
// implements against the real table.
type memCounter struct {
	mu     sync.Mutex
	totals map[string]int64
}

func newMemCounter() *memCounter { return &memCounter{totals: map[string]int64{}} }

func (m *memCounter) Add(_ context.Context, day string, delta int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.totals[day] += delta
	return m.totals[day], nil
}

// total is what every day together was charged.
func (m *memCounter) total() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sum int64
	for _, v := range m.totals {
		sum += v
	}
	return sum
}

// newBreaker builds a breaker with the given instance-wide cap in
// microdollars. A cap of 0 counts without enforcing, which is the default for
// a fresh install.
func newBreaker(capMicros int64) *breaker.Breaker {
	return breaker.New(newMemCounter(), meter.DefaultPrices, capMicros)
}

// testClock is advanced explicitly so a test can prove a pipeline consumed more
// than thirty seconds without spending thirty seconds doing it.
type testClock struct {
	mu sync.Mutex
	at time.Time
}

func newTestClock() *testClock {
	return &testClock{at: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// noteCreator stands in for service.NotesService.
type noteCreator struct {
	mu     sync.Mutex
	store  *memory.Store
	seq    int
	titles []string
}

func (f *noteCreator) CreateNote(ctx context.Context, userID, title string, aliases []string) (model.NoteIndex, error) {
	f.mu.Lock()
	f.seq++
	id := fmt.Sprintf("new_%d", f.seq)
	f.titles = append(f.titles, title)
	f.mu.Unlock()

	note := model.NoteIndex{
		ID:            id,
		Title:         title,
		Aliases:       aliases,
		UpdatedAt:     model.Now(),
		S3MarkdownKey: fmt.Sprintf("tenants/%s/notes/%s/note.md", userID, id),
		S3MetaKey:     fmt.Sprintf("tenants/%s/notes/%s/meta.json", userID, id),
	}
	stored, err := f.store.PutNote(ctx, userID, note)
	if err != nil {
		return model.NoteIndex{}, err
	}
	return stored, nil
}

func (f *noteCreator) createdTitles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.titles...)
}

// harness is a whole pipeline over in-memory storage and fake providers.
type harness struct {
	store    *memory.Store
	objects  repository.Objects
	stt      *fake.STT
	llm      *fake.LLM
	router   *fake.Router
	creator  *noteCreator
	pipeline *Pipeline
	clock    *testClock
}

type harnessOpts struct {
	objects repository.Objects
	store   repository.Store
	stt     *fake.STT
	llm     *fake.LLM
	router  *fake.Router
	// capMicros of 0 counts without enforcing.
	capMicros int64
	// noNotes builds the pipeline without a note creator, so an unroutable
	// capture parks at needs_target instead of inventing a note.
	noNotes bool
	// routeAttemptTimeout shortens each routing attempt; zero keeps the
	// pipeline's default. stageTimeout does the same for the transcription,
	// cleanup and clean-note deadlines together.
	routeAttemptTimeout time.Duration
	stageTimeout        time.Duration
	// counter, when set, is the spend counter the breaker adds to, so a test
	// can read back what the day was charged.
	counter *memCounter
}

func newHarness(t *testing.T, opts harnessOpts) *harness {
	t.Helper()

	store := memory.NewStore()
	objects := opts.objects
	if objects == nil {
		objects = memory.NewObjects()
	}
	stt := opts.stt
	if stt == nil {
		stt = &fake.STT{}
	}
	llm := opts.llm
	if llm == nil {
		llm = &fake.LLM{}
	}
	router := opts.router
	if router == nil {
		router = &fake.Router{}
	}
	creator := &noteCreator{store: store}
	clock := newTestClock()

	var seen repository.Store = store
	if opts.store != nil {
		seen = opts.store
	}
	counter := opts.counter
	if counter == nil {
		counter = newMemCounter()
	}

	cfg := Config{
		Store:               seen,
		Objects:             objects,
		STT:                 stt,
		LLM:                 llm,
		Router:              router,
		Breaker:             breaker.New(counter, meter.DefaultPrices, opts.capMicros),
		STTProvider:         "groq",
		STTModel:            "whisper-large-v3-turbo",
		LLMProvider:         "openai",
		LLMModel:            "test-model",
		RouteAttemptTimeout: opts.routeAttemptTimeout,
		TranscribeTimeout:   opts.stageTimeout,
		CleanupTimeout:      opts.stageTimeout,
		CleanNoteTimeout:    opts.stageTimeout,
		Now:                 clock.Now,
	}
	if !opts.noNotes {
		cfg.Notes = creator
	}

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &harness{
		store: store, objects: objects, stt: stt, llm: llm,
		router: router, creator: creator, pipeline: p, clock: clock,
	}
}

// wrapStore rebuilds the harness with a decorated store, so a test can induce a
// failure at a chosen step without reaching into the pipeline.
func newHarnessWrapping(t *testing.T, objects repository.Objects, wrap func(repository.Store) repository.Store, opts harnessOpts) *harness {
	t.Helper()
	h := newHarness(t, harnessOpts{objects: objects})

	cfg := Config{
		Store:       wrap(h.store),
		Objects:     h.objects,
		STT:         h.stt,
		LLM:         h.llm,
		Router:      h.router,
		Notes:       h.creator,
		Breaker:     newBreaker(opts.capMicros),
		STTProvider: "groq",
		STTModel:    "whisper-large-v3-turbo",
		LLMProvider: "openai",
		LLMModel:    "test-model",
		Now:         h.clock.Now,
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.pipeline = p
	return h
}

func mustGetNote(t *testing.T, store *memory.Store, userID, noteID string) model.NoteIndex {
	t.Helper()
	note, err := store.GetNote(context.Background(), userID, noteID)
	if err != nil {
		t.Fatalf("GetNote(%s): %v", noteID, err)
	}
	return note
}
