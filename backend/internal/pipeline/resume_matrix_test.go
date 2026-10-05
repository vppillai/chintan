package pipeline

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// stageLog records every stage status the pipeline writes to the row, in
// order: a stage announces itself with one setStatus write, so the sequence
// is the list of stages that ran.
type stageLog struct {
	repository.Store
	mu     sync.Mutex
	stages []model.CaptureStatus
}

func (s *stageLog) PutCapture(ctx context.Context, c model.CaptureIndex) (model.CaptureIndex, error) {
	switch c.Status {
	case model.StatusTranscribing, model.StatusRouting, model.StatusCleaning, model.StatusAppending:
		s.mu.Lock()
		s.stages = append(s.stages, c.Status)
		s.mu.Unlock()
	}
	return s.Store.PutCapture(ctx, c)
}

const (
	matrixTranscript = "the gutter over the back door is leaking again after the storm last night"
	matrixRawKey     = "tenants/user1/captures/c_1/raw.txt"
	matrixRoutedKey  = "tenants/user1/captures/c_1/routed.txt"
	matrixCleanKey   = "tenants/user1/captures/c_1/clean.txt"
)

// The resume matrix: for each set of artefacts a row can carry when a run
// picks it up — nothing, the transcript, the transcript and a destination,
// those and the cleaned text — which stages run and which provider calls
// are made. Every stage persists its artefact before the next one starts,
// so a retry resumes at the first stage whose artefact is missing and bills
// nothing twice; the rules are RawKey, NoteID and CleanKey, with the
// destination's language and the instruction strip hanging off the second.
// The matrix was implied across the hand-off, retranscribe and deadline
// tests; here it is in one place, so a change to the orchestration shows
// as a row.
func TestResumeMatrix(t *testing.T) {
	type counts struct{ stt, router, cleanups, items int }
	cases := []struct {
		name string
		// note is the destination seeded: "" for none, "note1" plain,
		// "list1" a checklist, "note_ml" a plain note in Malayalam.
		note string
		// row is the capture as the run finds it; the fields it leaves
		// zero are filled in (id, user, audio key, created at).
		row model.CaptureIndex
		// objects are the artefacts in the bucket, by key.
		objects map[string]string
		want    []model.CaptureStatus
		calls   counts
		final   model.CaptureStatus
	}{
		{
			name:  "nothing yet: every stage runs",
			note:  "note1",
			row:   model.CaptureIndex{Status: model.StatusUploaded},
			want:  []model.CaptureStatus{model.StatusTranscribing, model.StatusRouting, model.StatusCleaning, model.StatusAppending},
			calls: counts{stt: 1, router: 1, cleanups: 1}, final: model.StatusAppended,
		},
		{
			name:    "transcript: resumes at routing",
			note:    "note1",
			row:     model.CaptureIndex{Status: model.StatusTranscribing, RawKey: matrixRawKey, Language: "en"},
			objects: map[string]string{matrixRawKey: matrixTranscript},
			want:    []model.CaptureStatus{model.StatusRouting, model.StatusCleaning, model.StatusAppending},
			calls:   counts{router: 1, cleanups: 1}, final: model.StatusAppended,
		},
		{
			name:    "transcript and a routed destination: resumes at the cleanup",
			note:    "note1",
			row:     model.CaptureIndex{Status: model.StatusTranscribed, RawKey: matrixRawKey, RoutedKey: matrixRoutedKey, NoteID: "note1", Language: "en"},
			objects: map[string]string{matrixRawKey: matrixTranscript, matrixRoutedKey: matrixTranscript},
			want:    []model.CaptureStatus{model.StatusCleaning, model.StatusAppending},
			calls:   counts{cleanups: 1}, final: model.StatusAppended,
		},
		{
			name:    "transcript into a chosen note: the strip asks nothing without a cue, then the cleanup",
			note:    "note1",
			row:     model.CaptureIndex{Status: model.StatusTranscribed, RawKey: matrixRawKey, NoteID: "note1", Language: "en"},
			objects: map[string]string{matrixRawKey: matrixTranscript},
			want:    []model.CaptureStatus{model.StatusCleaning, model.StatusAppending},
			calls:   counts{cleanups: 1}, final: model.StatusAppended,
		},
		{
			name:    "transcript with an instruction into a chosen note: the strip is one routing call",
			note:    "note1",
			row:     model.CaptureIndex{Status: model.StatusTranscribed, RawKey: matrixRawKey, NoteID: "note1", Language: "en"},
			objects: map[string]string{matrixRawKey: "add this to my roof note " + matrixTranscript},
			want:    []model.CaptureStatus{model.StatusCleaning, model.StatusAppending},
			calls:   counts{router: 1, cleanups: 1}, final: model.StatusAppended,
		},
		{
			name:    "transcript in the default, note in another language: transcribed again, then the cleanup",
			note:    "note_ml",
			row:     model.CaptureIndex{Status: model.StatusTranscribed, RawKey: matrixRawKey, NoteID: "note_ml", Language: "en"},
			objects: map[string]string{matrixRawKey: matrixTranscript},
			want:    []model.CaptureStatus{model.StatusTranscribing, model.StatusCleaning, model.StatusAppending},
			calls:   counts{stt: 1, cleanups: 1}, final: model.StatusAppended,
		},
		{
			name:    "transcript already in the note's language: not transcribed a third time",
			note:    "note_ml",
			row:     model.CaptureIndex{Status: model.StatusTranscribed, RawKey: matrixRawKey, NoteID: "note_ml", Language: "ml"},
			objects: map[string]string{matrixRawKey: matrixTranscript},
			want:    []model.CaptureStatus{model.StatusCleaning, model.StatusAppending},
			calls:   counts{cleanups: 1}, final: model.StatusAppended,
		},
		{
			name:    "cleaned text: resumes at the append",
			note:    "note1",
			row:     model.CaptureIndex{Status: model.StatusCleaning, RawKey: matrixRawKey, RoutedKey: matrixRoutedKey, NoteID: "note1", CleanKey: matrixCleanKey, Language: "en"},
			objects: map[string]string{matrixRawKey: matrixTranscript, matrixRoutedKey: matrixTranscript, matrixCleanKey: "The gutter is leaking again."},
			want:    []model.CaptureStatus{model.StatusAppending},
			final:   model.StatusAppended,
		},
		{
			name:    "append claimed and handed back: the append alone, once",
			note:    "note1",
			row:     model.CaptureIndex{Status: model.StatusAppending, RawKey: matrixRawKey, RoutedKey: matrixRoutedKey, NoteID: "note1", CleanKey: matrixCleanKey, Language: "en"},
			objects: map[string]string{matrixRawKey: matrixTranscript, matrixRoutedKey: matrixTranscript, matrixCleanKey: "The gutter is leaking again."},
			want:    []model.CaptureStatus{model.StatusAppending},
			final:   model.StatusAppended,
		},
		{
			name:    "checklist, transcript: the items call, then the append",
			note:    "list1",
			row:     model.CaptureIndex{Status: model.StatusTranscribed, RawKey: matrixRawKey, NoteID: "list1", Language: "en"},
			objects: map[string]string{matrixRawKey: "milk and eggs"},
			want:    []model.CaptureStatus{model.StatusCleaning, model.StatusAppending},
			calls:   counts{items: 1}, final: model.StatusAppended,
		},
		{
			name:    "checklist, items extracted: the append alone",
			note:    "list1",
			row:     model.CaptureIndex{Status: model.StatusCleaned, RawKey: matrixRawKey, NoteID: "list1", CleanKey: matrixCleanKey, Language: "en"},
			objects: map[string]string{matrixRawKey: "milk and eggs", matrixCleanKey: "Milk\nEggs"},
			want:    []model.CaptureStatus{model.StatusAppending},
			final:   model.StatusAppended,
		},
		{
			name:    "parked at needs_target: nothing runs until a person chooses",
			note:    "note1",
			row:     model.CaptureIndex{Status: model.StatusNeedsTarget, RawKey: matrixRawKey, RoutedKey: matrixRoutedKey, Language: "en"},
			objects: map[string]string{matrixRawKey: matrixTranscript, matrixRoutedKey: matrixTranscript},
			final:   model.StatusNeedsTarget,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			log := &stageLog{}
			h := newHarnessWrapping(t, memory.NewObjects(), func(s repository.Store) repository.Store {
				log.Store = s
				return log
			}, harnessOpts{})
			h.stt.Response = matrixTranscript
			h.llm.Response = "The gutter is leaking again."
			h.llm.ItemsResponse = []cleanup.Item{{Text: "Milk"}, {Text: "Eggs"}}
			h.router.Decision = provider.RouteDecision{Action: provider.RouteAppend, NoteID: "note1", Content: matrixTranscript, Confidence: 0.95}
			switch tc.note {
			case "note1":
				seedNote(t, h.store, h.objects, "note1")
			case "list1":
				seedNoteWithBody(t, h, "list1", "", func(n *model.NoteIndex) { n.Kind = model.NoteKindChecklist })
			case "note_ml":
				seedNoteWithBody(t, h, "note_ml", "", func(n *model.NoteIndex) { n.Language = "ml" })
			}
			if err := h.objects.Put(ctx, "tenants/user1/captures/c_1/audio.webm", []byte("audio"), "audio/webm"); err != nil {
				t.Fatal(err)
			}
			for key, body := range tc.objects {
				if err := h.objects.Put(ctx, key, []byte(body), "text/plain"); err != nil {
					t.Fatal(err)
				}
			}
			row := tc.row
			row.ID, row.UserID, row.CreatedAt = "c_1", "user1", model.Now()
			row.AudioKey, row.DurationMS = "tenants/user1/captures/c_1/audio.webm", 12_000
			if _, err := h.store.PutCapture(ctx, row); err != nil {
				t.Fatal(err)
			}
			log.stages = nil

			final, err := h.pipeline.Run(ctx, "user1", "c_1")
			if err != nil || final.Status != tc.final {
				t.Fatalf("Run = %s, %v (%s), want %s", final.Status, err, final.Error, tc.final)
			}
			if !slices.Equal(log.stages, tc.want) {
				t.Errorf("stages run = %v, want %v", log.stages, tc.want)
			}
			got := counts{stt: h.stt.Calls(), router: h.router.CallCount(), cleanups: h.llm.Calls(), items: len(h.llm.ItemsCalls())}
			if got != tc.calls {
				t.Errorf("provider calls = %+v, want %+v", got, tc.calls)
			}
			if tc.final == model.StatusAppended {
				body, err := h.objects.Get(ctx, "tenants/user1/notes/"+tc.note+"/note.md")
				if err != nil || strings.Count(string(body), "chintan:capture:c_1") != 1 {
					t.Errorf("note body after the run: %q, %v; want the paragraph once", body, err)
				}
			}
		})
	}
}
