package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/llm"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// recordingItems is a checklist recording's items as its clean artefact holds
// them, one per line, nil for a plain note or a recording with none. It is
// what cutRecording goes by when the marker stands bare.
func (s *CaptureService) recordingItems(ctx context.Context, note model.NoteIndex, c model.CaptureIndex) ([]string, error) {
	if note.Kind != model.NoteKindChecklist || c.CleanKey == "" {
		return nil, nil
	}
	clean, err := s.objects.Get(ctx, c.CleanKey)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the recording's items: %w", err)
	}
	return strings.Split(string(clean), "\n"), nil
}

// cutRecording is CutCaptureParagraph for a recording that may be a
// checklist's. Every save from the Items tab carries the markers to the end
// (CarryCaptureMarkers finds no paragraph boundary in a list), so after the
// first tick a recording's marker stands bare and the paragraph under it is
// empty: delete and move cut the marker and left every item behind. When the
// paragraph holds no item line and items are given, the marker is cut and
// the recording's lines are found by their words, each once, as the worker's
// replaceChecklistItems finds them; text is those lines as they stand, ticks
// and indent kept. A found line with a line it did not take under it is a
// parent another recording or the person joined, and stays.
func cutRecording(body, captureID string, items []string) (rest, text string, found bool) {
	rest, text, found = CutCaptureParagraph(body, captureID)
	if !found || len(items) == 0 || hasItemLine(text) {
		return rest, text, found
	}
	wanted := map[string]int{}
	for _, item := range items {
		if t := llm.FoldWords(item); t != "" {
			wanted[t]++
		}
	}
	lines := strings.Split(rest, "\n")
	parsed := cleanup.ParseLines(lines)
	take := make([]bool, len(lines))
	for i, l := range parsed {
		if t := llm.FoldWords(l.Text); l.OK && wanted[t] > 0 {
			wanted[t]--
			take[i] = true
		}
	}
	for i := range lines {
		for j := i + 1; take[i] && j < len(lines) && parsed[j].OK && parsed[j].Depth > parsed[i].Depth; j++ {
			if !take[j] {
				take[i] = false
			}
		}
	}
	var kept, cut []string
	for i, line := range lines {
		if take[i] {
			cut = append(cut, line)
		} else {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n"), strings.Join(cut, "\n"), true
}

// hasItemLine reports whether text holds a task-list line.
func hasItemLine(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if _, _, _, ok := cleanup.ParseLine(line); ok {
			return true
		}
	}
	return false
}
