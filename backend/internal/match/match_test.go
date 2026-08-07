package match_test

import (
	"testing"

	"github.com/vppillai/chintan/backend/internal/match"
	"github.com/vppillai/chintan/backend/internal/model"
)

func sampleNotes() []model.NoteIndex {
	return []model.NoteIndex{
		{ID: "n1", Title: "Project Alpha", Aliases: []string{"alpha project"}},
		{ID: "n2", Title: "Beta Notes", Aliases: []string{"beta"}},
		{ID: "n3", Title: "Shopping List", Aliases: []string{"groceries"}, Snippet: "milk eggs bread"},
	}
}

func TestExactTitleMatchRanksFirst(t *testing.T) {
	notes := []model.NoteIndex{
		{ID: "n1", Title: "Meeting Notes"},
		{ID: "n2", Title: "Shopping List"},
	}
	ranked := match.Rank("Meeting Notes", notes, 5)
	if len(ranked) == 0 {
		t.Fatal("expected at least one candidate")
	}
	if ranked[0].NoteID != "n1" {
		t.Fatalf("got top NoteID %q, want n1", ranked[0].NoteID)
	}
	if ranked[0].Title != "Meeting Notes" {
		t.Fatalf("got top title %q, want Meeting Notes", ranked[0].Title)
	}
	if ranked[0].Score < 0.72 {
		t.Fatalf("exact title score %v, want >= 0.72", ranked[0].Score)
	}
}

func TestAliasSubstringMatch(t *testing.T) {
	notes := []model.NoteIndex{
		{ID: "n1", Title: "Untitled", Aliases: []string{"vacation planning hawaii"}},
		{ID: "n2", Title: "Work Tasks"},
	}
	ranked := match.Rank("hawaii vacation", notes, 5)
	if len(ranked) == 0 {
		t.Fatal("expected at least one candidate")
	}
	if ranked[0].NoteID != "n1" {
		t.Fatalf("got top NoteID %q, want n1 (alias match)", ranked[0].NoteID)
	}
	if ranked[0].Score <= 0 {
		t.Fatalf("alias match score %v, want > 0", ranked[0].Score)
	}
}

func TestEmptyQueryReturnsEmpty(t *testing.T) {
	for _, q := range []string{"", "   ", "\t"} {
		ranked := match.Rank(q, sampleNotes(), 5)
		if len(ranked) != 0 {
			t.Fatalf("query %q: got %d candidates, want 0", q, len(ranked))
		}
	}
}

func TestEmptyNoteListReturnsEmpty(t *testing.T) {
	for _, notes := range [][]model.NoteIndex{nil, {}} {
		ranked := match.Rank("anything", notes, 5)
		if len(ranked) != 0 {
			t.Fatalf("got %d candidates for empty note list, want 0", len(ranked))
		}
	}
}

func TestHighConfidence(t *testing.T) {
	cases := []struct {
		name string
		in   []match.Candidate
		want bool
	}{
		{"single strong match", []match.Candidate{{Score: 0.85}}, true},
		{"top above threshold with gap", []match.Candidate{{Score: 0.80}, {Score: 0.55}}, true},
		{"top below threshold", []match.Candidate{{Score: 0.70}, {Score: 0.10}}, false},
		{"gap too small", []match.Candidate{{Score: 0.75}, {Score: 0.60}}, false},
		{"empty ranked", nil, false},
		{"zero score", []match.Candidate{{Score: 0}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := match.HighConfidence(tc.in)
			if got != tc.want {
				t.Fatalf("HighConfidence() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRankRespectsLimit(t *testing.T) {
	ranked := match.Rank("notes", sampleNotes(), 2)
	if len(ranked) > 2 {
		t.Fatalf("got %d candidates, want at most 2", len(ranked))
	}
}

func TestRankIntegrationHighConfidence(t *testing.T) {
	notes := []model.NoteIndex{
		{ID: "n1", Title: "Quarterly Review"},
		{ID: "n2", Title: "Random Thoughts"},
	}
	ranked := match.Rank("Quarterly Review", notes, 5)
	if !match.HighConfidence(ranked) {
		t.Fatal("exact title match should yield high confidence")
	}
}
