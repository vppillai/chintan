package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// stampFaults is the store with StampNoteAppend answering from a script:
// conflicts version conflicts first, then fail (when set) for every call
// after them, and the real stamp otherwise. calls counts every attempt.
type stampFaults struct {
	repository.Store
	mu        sync.Mutex
	conflicts int
	fail      error
	calls     int
}

func (s *stampFaults) StampNoteAppend(ctx context.Context, tenantID, noteID, captureID string, expectedVersion int64, at time.Time) (model.NoteIndex, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	if n <= s.conflicts {
		return model.NoteIndex{}, repository.ErrVersionConflict
	}
	if s.fail != nil {
		return model.NoteIndex{}, s.fail
	}
	return s.Store.StampNoteAppend(ctx, tenantID, noteID, captureID, expectedVersion, at)
}

// The exactly-once branches of append (R7-20), driven directly rather than
// through Run so each starts from the state it guards: a claim somebody else
// holds, an append an earlier attempt finished, a stamp that fails, and a
// stamp that loses the version race — and recovers, or gives up. In every
// refusal the body is not written, and a failure after the claim hands the
// claim back so the next attempt is not parked for the whole lease.
func TestAppendExactlyOnceBranches(t *testing.T) {
	ownToken := appendToken("capture1", appendCleanKey)
	cases := []struct {
		name string
		// stamp configures the store's stamp; nil leaves it real.
		stamp *stampFaults
		// before prepares the capture's claim.
		before func(t *testing.T, f *appendFixture)
		// wantErr, wantBody, wantToken and wantStampCalls are the outcome;
		// wantConceded names the one error that is not a failure.
		wantErr        bool
		wantConceded   bool
		wantBody       string
		wantToken      string
		wantStampCalls int
	}{
		{
			name: "a claim under another token concedes without writing",
			before: func(t *testing.T, f *appendFixture) {
				claim(t, f, "someone-else")
			},
			wantErr: true, wantConceded: true, wantToken: "someone-else",
		},
		{
			name: "an append an earlier attempt finished returns without writing",
			before: func(t *testing.T, f *appendFixture) {
				claim(t, f, ownToken)
				if _, err := f.store.CompleteCaptureAppend(context.Background(), "user1", "capture1", ownToken); err != nil {
					t.Fatalf("CompleteCaptureAppend: %v", err)
				}
			},
			wantToken: ownToken,
		},
		{
			name:    "a failed stamp releases the claim and writes nothing",
			stamp:   &stampFaults{fail: errors.New("dynamodb: 500 InternalServerError")},
			wantErr: true, wantStampCalls: 1,
		},
		{
			name:     "a stamp that loses the version race re-reads and stamps",
			stamp:    &stampFaults{conflicts: maxIndexRefreshAttempts - 1},
			wantBody: appendedText, wantToken: ownToken, wantStampCalls: maxIndexRefreshAttempts,
		},
		{
			name:    "a stamp that loses every race gives up and releases the claim",
			stamp:   &stampFaults{conflicts: 1 << 30},
			wantErr: true, wantStampCalls: maxIndexRefreshAttempts,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var wrap func(repository.Store) repository.Store
			if tc.stamp != nil {
				wrap = func(s repository.Store) repository.Store {
					tc.stamp.Store = s
					return tc.stamp
				}
			}
			f := newAppendFixture(t, memory.NewObjects(), wrap)
			if tc.before != nil {
				tc.before(t, f)
			}
			capture, err := f.store.GetCapture(ctx, "user1", "capture1")
			if err != nil {
				t.Fatal(err)
			}
			note := mustGetNote(t, f.store, "user1", "note1")

			_, err = f.h.pipeline.append(ctx, "user1", &capture, note, appendOptions{})
			if (err != nil) != tc.wantErr {
				t.Fatalf("append error = %v, want error %v", err, tc.wantErr)
			}
			if errors.Is(err, errDeliveryConceded) != tc.wantConceded {
				t.Fatalf("append error = %v, want conceded %v", err, tc.wantConceded)
			}
			if got := f.body(t); (tc.wantBody == "" && got != "") || (tc.wantBody != "" && strings.Count(got, tc.wantBody) != 1) {
				t.Errorf("body = %q, want %q exactly once", got, tc.wantBody)
			}
			stored, err := f.store.GetCapture(ctx, "user1", "capture1")
			if err != nil {
				t.Fatal(err)
			}
			if stored.AppendToken != tc.wantToken {
				t.Errorf("append token = %q, want %q (a failed attempt must hand the claim back)", stored.AppendToken, tc.wantToken)
			}
			if tc.stamp != nil && tc.stamp.calls != tc.wantStampCalls {
				t.Errorf("stamp attempts = %d, want %d", tc.stamp.calls, tc.wantStampCalls)
			}
			if tc.wantErr && mustGetNote(t, f.store, "user1", "note1").AppendingCapture != "" {
				t.Error("a failed append left the note stamped; editor saves would wait for it")
			}
		})
	}
}

func claim(t *testing.T, f *appendFixture, token string) {
	t.Helper()
	claimed, _, err := f.store.ClaimCaptureAppend(context.Background(), "user1", "capture1", token)
	if err != nil || !claimed {
		t.Fatalf("ClaimCaptureAppend(%q) = %v, %v", token, claimed, err)
	}
}

// The router may pick a note that was archived after the candidate list was
// read. The archive is the person's decision, so the dictation goes into a
// new note, and the archived note's body is not touched (route.go, the
// NoteIsActive case; R7-20).
func TestARoutedNoteThatIsArchivedGetsANewNote(t *testing.T) {
	f := newRoutingFixture(t, "the flashing is loose",
		provider.RouteDecision{Action: provider.RouteAppend, NoteID: "n1", Title: "Flashing", Confidence: 1, Content: "the flashing is loose"}, false)
	ctx := context.Background()
	n1 := mustGetNote(t, f.store, "user1", "n1")
	n1.DeletedAt = model.Now()
	if _, err := f.store.PutNote(ctx, "user1", n1); err != nil {
		t.Fatalf("archive n1: %v", err)
	}

	capture, err := f.run(ctx, "c_1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if capture.Status != model.StatusAppended || capture.NoteID == "n1" || capture.NoteID == "" {
		t.Fatalf("capture = %s in %q, want appended into a new note", capture.Status, capture.NoteID)
	}
	if titles := f.h.creator.createdTitles(); len(titles) != 1 {
		t.Errorf("created titles = %v, want one new note", titles)
	}
	if body, _ := f.objects.Get(ctx, "tenants/user1/notes/n1/note.md"); string(body) != "existing line" {
		t.Errorf("archived n1 body = %q, want it untouched", body)
	}
}
