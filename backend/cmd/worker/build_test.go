package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository/dynamofake"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// memCounter is the spend counter over a map, the breaker's one dependency.
type memCounter struct {
	mu     sync.Mutex
	totals map[string]int64
}

func (m *memCounter) Add(_ context.Context, day string, delta int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.totals[day] += delta
	return m.totals[day], nil
}

// The composition root over fakes: the same build main runs, with the fake
// table, the in-memory bucket and the fake providers in place of AWS and the
// models, sets every handler the dispatch reaches, and each scheduled task
// runs to its no-op end on an empty instance. Nothing here reads the
// environment; a wiring build loses fails here rather than at the next
// deploy's smoke.
func TestBuildWiresTheWorkerOverFakes(t *testing.T) {
	prevWork, prevSweeper, prevCosts, prevSnapshots, prevReaper := handleWork, sweeper, costs, snapshots, reaper
	t.Cleanup(func() {
		handleWork, sweeper, costs, snapshots, reaper = prevWork, prevSweeper, prevCosts, prevSnapshots, prevReaper
	})

	err := build(deps{
		store:    dynamofake.NewStore(),
		objects:  memory.NewObjects(),
		stt:      &fake.STT{},
		llm:      &fake.LLM{},
		router:   &fake.Router{},
		sttModel: "whisper-large-v3-turbo",
		llmModel: "MiniMax-M3",
		counter:  &memCounter{totals: map[string]int64{}},
		usage:    memory.NewUsage(),
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if handleWork == nil || sweeper == nil || costs == nil || snapshots == nil || reaper == nil {
		t.Fatal("build left a handler unset")
	}
	for task := range scheduled {
		raw, _ := json.Marshal(map[string]string{"task": task})
		if err := Handler(context.Background(), raw); err != nil {
			t.Errorf("Handler(%s) over an empty instance = %v", task, err)
		}
	}
}
