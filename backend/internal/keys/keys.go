// Package keys derives every S3 object key from the ids that own it. Every key
// is tenants/<tenant>/…, which is what isolates one tenant's objects from
// another's (README, "Tenancy"); an id that could escape that prefix is refused
// here, once, rather than checked at each caller.
package keys

import (
	"fmt"
	"regexp"
)

var idRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func check(id, label string) error {
	if !idRe.MatchString(id) {
		return fmt.Errorf("keys: invalid %s %q", label, id)
	}
	return nil
}

func NoteMarkdown(userID, noteID string) (string, error) {
	if err := check(userID, "userID"); err != nil {
		return "", err
	}
	if err := check(noteID, "noteID"); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/notes/%s/note.md", userID, noteID), nil
}

func NoteMeta(userID, noteID string) (string, error) {
	if err := check(userID, "userID"); err != nil {
		return "", err
	}
	if err := check(noteID, "noteID"); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/notes/%s/meta.json", userID, noteID), nil
}

func CaptureAudio(userID, captureID, ext string) (string, error) {
	if err := check(userID, "userID"); err != nil {
		return "", err
	}
	if err := check(captureID, "captureID"); err != nil {
		return "", err
	}
	if err := check(ext, "ext"); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/captures/%s/audio.%s", userID, captureID, ext), nil
}

func CaptureRaw(userID, captureID string) (string, error) {
	if err := check(userID, "userID"); err != nil {
		return "", err
	}
	if err := check(captureID, "captureID"); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/captures/%s/raw.txt", userID, captureID), nil
}

// CaptureRouted holds the transcript with any spoken routing instruction removed.
func CaptureRouted(userID, captureID string) (string, error) {
	if err := check(userID, "userID"); err != nil {
		return "", err
	}
	if err := check(captureID, "captureID"); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/captures/%s/routed.txt", userID, captureID), nil
}

func CaptureClean(userID, captureID string) (string, error) {
	if err := check(userID, "userID"); err != nil {
		return "", err
	}
	if err := check(captureID, "captureID"); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/captures/%s/clean.txt", userID, captureID), nil
}

// CaptureCleanPrevious holds what CaptureClean held before the last item
// extraction overwrote it: the items a checklist recording produced the time
// before, kept until the append that replaces them by their words has landed,
// so an append resumed after a failure still knows which lines are the
// recording's (pipeline.extractItems, replaceChecklistItems).
func CaptureCleanPrevious(userID, captureID string) (string, error) {
	if err := check(userID, "userID"); err != nil {
		return "", err
	}
	if err := check(captureID, "captureID"); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/captures/%s/clean.prev.txt", userID, captureID), nil
}

// CaptureSegments holds the timestamped raw transcript that drives tap-to-seek
// playback. Timestamps belong to the raw transcript: cleanup rewrites the text,
// so cleaned prose carries no reliable time mapping.
func CaptureSegments(userID, captureID string) (string, error) {
	if err := check(userID, "userID"); err != nil {
		return "", err
	}
	if err := check(captureID, "captureID"); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/captures/%s/segments.json", userID, captureID), nil
}

// CapturePeaks holds the amplitude envelope the client computes while recording.
// The browser already has the decoded signal from its AnalyserNode; deriving it
// server-side would mean shipping an Opus decoder into Lambda.
func CapturePeaks(userID, captureID string) (string, error) {
	if err := check(userID, "userID"); err != nil {
		return "", err
	}
	if err := check(captureID, "captureID"); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/captures/%s/peaks.json", userID, captureID), nil
}
