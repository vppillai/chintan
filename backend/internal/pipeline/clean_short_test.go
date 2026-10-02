package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
)

// A dictation under routing.ShortDictationWords words is tidied, not sent to the
// cleanup model; one of exactly that many words is sent (R7-15).
func TestAShortDictationSkipsTheCleanupModel(t *testing.T) {
	cases := []struct {
		name, transcript, want string
		calls                  int
	}{
		{"eleven words", "call  the roofer about the gutter over the back door tomorrow",
			"Call the roofer about the gutter over the back door tomorrow.", 0},
		{"twelve words", "call the roofer about the gutter over the back door tomorrow morning",
			"MODEL", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h := newHarness(t, harnessOpts{llm: &fake.LLM{Response: "MODEL"}})
			seedNote(t, h.store, h.objects, "note1")
			if err := h.objects.Put(ctx, "tenants/user1/captures/c_t/raw.txt", []byte(tc.transcript), "text/plain"); err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
				ID: "c_t", UserID: "user1", NoteID: "note1", Status: model.StatusTranscribed, CreatedAt: model.Now(),
				RawKey: "tenants/user1/captures/c_t/raw.txt", TargetSource: model.TargetSourceClient,
			}); err != nil {
				t.Fatal(err)
			}
			final, err := h.pipeline.Run(ctx, "user1", "c_t")
			if err != nil || final.Status != model.StatusAppended {
				t.Fatalf("Run = %s, %v", final.Status, err)
			}
			if got := h.llm.Calls(); got != tc.calls {
				t.Errorf("cleanup calls = %d, want %d", got, tc.calls)
			}
			clean, err := h.objects.Get(ctx, final.CleanKey)
			if err != nil || string(clean) != tc.want {
				t.Errorf("clean text = %q, %v; want %q", clean, err, tc.want)
			}
			// The receipt's excerpt is the filed line on both paths, the
			// tidy's as much as the model's, never the raw transcript.
			if final.Excerpt != tc.want {
				t.Errorf("excerpt = %q, want the clean text %q", final.Excerpt, tc.want)
			}
			body, _ := h.objects.Get(ctx, "tenants/user1/notes/note1/note.md")
			if !strings.Contains(string(body), tc.want) {
				t.Errorf("note body %q lacks %q", body, tc.want)
			}
		})
	}
}

func TestTidyDictation(t *testing.T) {
	cases := []struct{ in, want string }{
		{"buy milk", "Buy milk."},
		{"  buy \n milk\tand eggs ", "Buy milk and eggs."},
		{"Buy milk.", "Buy milk."},
		{"is the roofer coming?", "Is the roofer coming?"},
		{"stop!", "Stop!"},
		{"and then…", "And then…"},
		{`he said "stop."`, `He said "stop."`},
		{"(see the roofer)", "(See the roofer)."},
		// A first word that is not an ordinary lowercase word keeps its
		// letters; only the full stop is added.
		{"eBay order", "eBay order."},
		{"3 eggs", "3 eggs."},
		{"7pm call", "7pm call."},
		{"7pm", "7pm."},
		{"iPhone charger", "iPhone charger."},
		{"eBay", "eBay."},
		{"e.g. the roof", "e.g. the roof."},
		{"bob@example.com about it", "bob@example.com about it."},
		// A trailing URL gets no full stop; a leading one is not capitalised.
		{"https://x.com", "https://x.com"},
		{"see https://x.com/roof", "See https://x.com/roof"},
		{"look at www.example.com", "Look at www.example.com"},
		{"don't forget", "Don't forget."},
		{"well, maybe", "Well, maybe."},
		{"გამარჯობა მეგობარო", "გამარჯობა მეგობარო."},
		// Scripts without case are not capitalised; their own sentence
		// marks end a sentence.
		{"പാൽ വാങ്ങണം", "പാൽ വാങ്ങണം."},
		{"दूध लाना है।", "दूध लाना है।"},
		{"ÉCOLE demain", "ÉCOLE demain."},
		{"élan", "Élan."},
	}
	for _, tc := range cases {
		if got := tidyDictation(tc.in); got != tc.want {
			t.Errorf("tidyDictation(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsShortDictation(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{strings.Repeat("word ", 11), true},
		{strings.Repeat("word ", 12), false},
		{"", true},
		// A sentence in a script written without spaces is one field; it
		// is never judged short by the count.
		{"明日屋根屋さんに電話する", false},
	}
	for _, tc := range cases {
		if got := isShortDictation(tc.in); got != tc.want {
			t.Errorf("isShortDictation(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
