package match

import (
	"regexp"
	"sort"
	"strings"

	"github.com/vppillai/chintan/backend/internal/model"
)

const (
	minHighConfidenceScore = 0.72
	minScoreGap            = 0.18

	weightTitle   = 0.55
	weightAlias   = 0.30
	weightSnippet = 0.15
)

var nonLetters = regexp.MustCompile(`[^a-z]+`)

// Candidate is a ranked note match for a vague description query.
type Candidate struct {
	NoteID  string
	Title   string
	Score   float64
	Aliases []string
}

// Rank scores notes against query and returns the top limit matches, highest score first.
func Rank(query string, notes []model.NoteIndex, limit int) []Candidate {
	query = strings.TrimSpace(query)
	if query == "" || len(notes) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = len(notes)
	}

	candidates := make([]Candidate, 0, len(notes))
	for _, note := range notes {
		score := scoreNote(query, note)
		if score <= 0 {
			continue
		}
		aliases := note.Aliases
		if aliases == nil {
			aliases = []string{}
		}
		candidates = append(candidates, Candidate{
			NoteID:  note.ID,
			Title:   note.Title,
			Score:   score,
			Aliases: aliases,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].NoteID < candidates[j].NoteID
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

// HighConfidence reports whether the top ranked candidate is strong enough to auto-select.
// True when top score >= 0.72 and either there is no second candidate or top-second >= 0.18.
func HighConfidence(ranked []Candidate) bool {
	if len(ranked) == 0 || ranked[0].Score < minHighConfidenceScore {
		return false
	}
	if len(ranked) == 1 {
		return true
	}
	return ranked[0].Score-ranked[1].Score >= minScoreGap
}

func scoreNote(query string, note model.NoteIndex) float64 {
	qTokens := tokens(query)
	if len(qTokens) == 0 {
		return 0
	}

	titleScore := bestTextScore(query, qTokens, note.Title)
	aliasScore := 0.0
	for _, alias := range note.Aliases {
		if s := bestTextScore(query, qTokens, alias); s > aliasScore {
			aliasScore = s
		}
	}
	snippetScore := tokenOverlap(qTokens, tokens(note.Snippet))

	total := weightTitle*titleScore + weightAlias*aliasScore + weightSnippet*snippetScore
	if normalize(query) == normalize(note.Title) {
		total = max(total, 0.95)
	}
	if total > 1 {
		total = 1
	}
	return total
}

func bestTextScore(query string, qTokens []string, text string) float64 {
	return max(tokenOverlap(qTokens, tokens(text)), substringScore(query, text))
}

func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func tokens(s string) []string {
	s = normalize(s)
	parts := nonLetters.Split(s, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func tokenOverlap(queryTokens, textTokens []string) float64 {
	if len(queryTokens) == 0 {
		return 0
	}
	textSet := make(map[string]struct{}, len(textTokens))
	for _, t := range textTokens {
		textSet[t] = struct{}{}
	}
	hits := 0
	for _, q := range queryTokens {
		if _, ok := textSet[q]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(queryTokens))
}

func substringScore(query, text string) float64 {
	q := normalize(query)
	t := normalize(text)
	if q == "" || t == "" {
		return 0
	}
	if q == t {
		return 1
	}
	if strings.Contains(t, q) || strings.Contains(q, t) {
		shorter, longer := len(q), len(t)
		if shorter > longer {
			shorter, longer = longer, shorter
		}
		return 0.5 + 0.5*float64(shorter)/float64(longer)
	}
	return 0
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
