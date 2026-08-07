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

func CaptureClean(userID, captureID string) (string, error) {
	if err := check(userID, "userID"); err != nil {
		return "", err
	}
	if err := check(captureID, "captureID"); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/captures/%s/clean.txt", userID, captureID), nil
}

func CaptureMeta(userID, captureID string) (string, error) {
	if err := check(userID, "userID"); err != nil {
		return "", err
	}
	if err := check(captureID, "captureID"); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/captures/%s/meta.json", userID, captureID), nil
}
