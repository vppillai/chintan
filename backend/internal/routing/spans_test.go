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
