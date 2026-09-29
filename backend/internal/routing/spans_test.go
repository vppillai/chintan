package routing

import (
	"errors"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/llm"
)

func TestNumberWords(t *testing.T) {
	t.Parallel()
	got := NumberWords(Words("Add this  to\nmy note."))
	if got != "0:Add 1:this 2:to 3:my 4:note." {
		t.Errorf("NumberWords = %q", got)
	}
	if NumberWords(nil) != "" {
		t.Error("no words should render as nothing")
	}
}

func TestRemoveSpans(t *testing.T) {
	t.Parallel()

	transcript := "add this to my roof repair note the gutter is leaking again"
	tests := []struct {
		name    string
		spans   []Span
		want    string
		wantErr error
	}{
		{name: "leading instruction", spans: []Span{{0, 7}}, want: "the gutter is leaking again"},
		{name: "trailing instruction", spans: []Span{{7, 12}}, want: "add this to my roof repair note"},
		{name: "middle", spans: []Span{{3, 7}}, want: "add this to the gutter is leaking again"},
		{name: "two spans", spans: []Span{{0, 2}, {10, 12}}, want: "to my roof repair note the gutter is"},
		{name: "overlapping spans are a union", spans: []Span{{0, 5}, {3, 7}}, want: "the gutter is leaking again"},
		{name: "every word", spans: []Span{{0, 12}}, want: ""},
		{name: "end past the transcript", spans: []Span{{7, 13}}, wantErr: ErrSpanMalformed},
		{name: "negative start", spans: []Span{{-1, 3}}, wantErr: ErrSpanMalformed},
		{name: "empty span", spans: []Span{{4, 4}}, wantErr: ErrSpanMalformed},
		{name: "reversed span", spans: []Span{{7, 3}}, wantErr: ErrSpanMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := RemoveSpans(transcript, tt.spans)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("RemoveSpans: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			// The property the old verifier checked after the fact now holds by
			// construction; this pins that it does.
			if !llm.VerifySubsequence(got, transcript) {
				t.Errorf("%q is not the transcript with words deleted", got)
			}
		})
	}
}

// Without spans the transcript comes back untouched — not re-joined — so a
// recording with no instruction in it is stored exactly as transcribed.
func TestRemoveSpansWithoutSpansIsIdentity(t *testing.T) {
	t.Parallel()
	transcript := "line one\n\nline  two.  "
	for _, spans := range [][]Span{nil, {}} {
		got, err := RemoveSpans(transcript, spans)
		if err != nil {
			t.Fatal(err)
		}
		if got != transcript {
			t.Errorf("got %q, want the transcript byte for byte", got)
		}
	}
}

func TestRemoveSpansRefusesToRemoveDictation(t *testing.T) {
	t.Parallel()
	transcript := strings.TrimSpace(strings.Repeat("word ", MaxInstructionWords+10))

	if _, err := RemoveSpans(transcript, []Span{{0, MaxInstructionWords + 1}}); !errors.Is(err, ErrSpansTooLong) {
		t.Errorf("err = %v, want ErrSpansTooLong", err)
	}
	// The cap is on words removed, not on span count or position.
	if _, err := RemoveSpans(transcript, []Span{{0, MaxInstructionWords / 2}, {20, 20 + MaxInstructionWords/2 + 1}}); !errors.Is(err, ErrSpansTooLong) {
		t.Errorf("err = %v, want ErrSpansTooLong across two spans", err)
	}
	if _, err := RemoveSpans(transcript, []Span{{0, MaxInstructionWords}}); err != nil {
		t.Errorf("a span at the limit should be accepted: %v", err)
	}
}

// The cue check decides whether a capture recorded into a note is worth a
// routing call at all. It must catch the instructions the router honours as
// people say them, and must not fire on plain dictation.
func TestMentionsInstruction(t *testing.T) {
	for transcript, want := range map[string]bool{
		"Create a note with the title Staging Smoke. The gutter on the north side is leaking again.": true,
		"Add this to my roof repair note: the flashing needs checking.":                              true,
		"FILE THIS under taxes.":                                                                 true,
		"Title this dentist, the appointment moved to Tuesday.":                                  true,
		"Put it in the garden note please":                                                       true,
		"the gutter on the north side is leaking again and the roofer should check the flashing": false,
		"we noted the additional cost and the filing deadline":                                   false,
		"": false,
	} {
		if got := MentionsInstruction(transcript); got != want {
			t.Errorf("MentionsInstruction(%q) = %v, want %v", transcript, got, want)
		}
	}
}

// The router stops a span one word early in two shapes the production battery
// showed three of three (2026-09-29, rows 13 and 2): before the spoken title,
// and before the "note" that closes a filing phrase. Both are closed from the
// words alone; anything else is left as the router said.
func TestExtendSpansClosesTheGapBeforeTheTitleAndTheTrailingNote(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		transcript string
		spans      []Span
		title      string
		want       []Span
	}{
		{
			name:       "a naming span stopping before the title grows over it",
			transcript: "title this staging smoke and then the deploy pipeline is green",
			spans:      []Span{{0, 2}}, title: "Staging smoke",
			want: []Span{{0, 4}},
		},
		{
			name:       "a filing span stopping before note grows over it",
			transcript: "the gutter is leaking again put that in my roof note",
			spans:      []Span{{5, 10}},
			want:       []Span{{5, 11}},
		},
		{
			name:       "the title and then note, in one span",
			transcript: "create a note titled roof repair note the gutter leaks",
			spans:      []Span{{0, 4}}, title: "roof repair",
			want: []Span{{0, 7}},
		},
		{
			// Row 13's actual shape, three of three: the span took the title's
			// first word and left "smoke" to open the body.
			name:       "a naming span stopping inside the title grows to its end",
			transcript: "title this staging smoke and then the actual content of the note is that the deploy pipeline is green",
			spans:      []Span{{0, 3}}, title: "staging smoke",
			want: []Span{{0, 4}},
		},
		{
			// Row 9 run 3: on an append the title is the destination's, and the
			// span that stopped before it grows over it, so nothing is left to
			// append.
			name:       "a filing span stopping before the destination's name grows over it",
			transcript: "Create a new note and add it to Pebble Ring Test",
			spans:      []Span{{0, 8}}, title: "Pebble Ring Test",
			want: []Span{{0, 11}},
		},
		{
			name:       "a filing span stopping inside the destination's name grows to its end",
			transcript: "Create a new note and add it to Pebble Ring Test",
			spans:      []Span{{0, 9}}, title: "Pebble Ring Test",
			want: []Span{{0, 11}},
		},
		{
			name:       "the overlap never reaches back before the span",
			transcript: "staging smoke is the title",
			spans:      []Span{{1, 2}}, title: "staging smoke",
			want: []Span{{1, 2}},
		},
		{
			name:       "the next word is neither",
			transcript: "add this to my roof repair note the gutter leaks",
			spans:      []Span{{0, 7}}, title: "",
			want: []Span{{0, 7}},
		},
		{
			// Rows 23, 24 and 26 of the same battery: the span already covers
			// the name, or the name does not abut it, and nothing grows.
			name:       "a name-first append whose span covers the name is left alone",
			transcript: "App feedback checklist move seems to be good where I can drag items up and down",
			spans:      []Span{{0, 2}}, title: "App feedback",
			want: []Span{{0, 2}},
		},
		{
			name:       "a name-first append with words after the name is left alone",
			transcript: "Business ideas by Priyanka seated pool for dogs",
			spans:      []Span{{0, 2}}, title: "Business ideas",
			want: []Span{{0, 2}},
		},
		{
			name:       "a filing phrase whose span ends after note is left alone",
			transcript: "Add to the app feedback note and the push to talk icon does not look good",
			spans:      []Span{{0, 6}}, title: "App feedback",
			want: []Span{{0, 6}},
		},
		{
			name:       "the title elsewhere than right after the span does not count",
			transcript: "title this and staging smoke follows later",
			spans:      []Span{{0, 2}}, title: "staging smoke",
			want: []Span{{0, 2}},
		},
		{
			name:       "a span at the end has nothing to grow over",
			transcript: "the gutter leaks put that in my roof note",
			spans:      []Span{{3, 9}}, title: "",
			want: []Span{{3, 9}},
		},
		{
			name:       "a span that does not fit is left for RemoveSpans to refuse",
			transcript: "title this staging smoke",
			spans:      []Span{{-1, 2}, {3, 3}}, title: "staging smoke",
			want: []Span{{-1, 2}, {3, 3}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtendSpans(Words(tc.transcript), tc.spans, tc.title)
			if len(got) != len(tc.want) {
				t.Fatalf("spans = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("span %d = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestNormalizeSpeechKeepsWordsOnly(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"  Roof   Repair. ":          "roof repair",
		"App Feedback: the split-up": "app feedback the split up",
		"don't":                      "don't",
		"test 1,2,3":                 "test 1 2 3",
		"":                           "",
	} {
		if got := NormalizeSpeech(in); got != want {
			t.Errorf("NormalizeSpeech(%q) = %q, want %q", in, got, want)
		}
	}
}
