package service

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// RecordingURL is one entry of a note's download manifest: a presigned GET for
// the capture's audio and the name the client should save it under.
type RecordingURL struct {
	CaptureID string
	Filename  string
	URL       string
	ExpiresAt time.Time
}

// MaxRecordingURLs bounds one manifest. Two hundred presigned URLs is a few
// hundred kilobytes of response and a few hundred HEADs behind it; a note with
// more recordings than that is not a note anyone zips in one go, and the cap
// keeps the request inside the gateway's ceiling.
const MaxRecordingURLs = 200

// recordingExistenceParallelism bounds the concurrent HEADs that confirm each
// audio object is still there. Retention expiry removes audio from under
// finished captures by design, and a manifest naming a file that is gone hands
// the client a 404 in the middle of its zip.
const recordingExistenceParallelism = 8

// RecordingURLs lists a presigned download for every recording of a note that
// still has its audio, oldest first — the order the note was dictated in — so
// a client can zip them itself without a round trip per recording.
//
// Filenames are `<note-title-slug>-<yyyymmdd-hhmm>.<ext>`, from the capture's
// creation instant in UTC and the audio object's extension; two recordings in
// the same minute get a `-2`, `-3` suffix so the archive holds both.
func (s *CaptureService) RecordingURLs(ctx context.Context, userID, noteID string) ([]RecordingURL, error) {
	note, err := s.store.GetNote(ctx, userID, noteID)
	if err != nil {
		return nil, fmt.Errorf("failed to get note: %w", err)
	}

	captures, err := repository.DrainPages(ctx, MaxRecordingURLs, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
		return s.store.ListCapturesByNote(ctx, userID, noteID, opts)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list captures: %w", err)
	}

	withAudio := make([]model.CaptureIndex, 0, len(captures))
	for _, c := range captures {
		if c.AudioKey != "" {
			withAudio = append(withAudio, c)
		}
	}
	present, err := s.audioPresent(ctx, withAudio)
	if err != nil {
		return nil, err
	}

	// Oldest first. CreatedAt has a fixed-width fraction, so the string order
	// is the chronological one; the id breaks ties.
	sort.Slice(present, func(i, j int) bool {
		if present[i].CreatedAt != present[j].CreatedAt {
			return present[i].CreatedAt < present[j].CreatedAt
		}
		return present[i].ID < present[j].ID
	})

	expires := time.Now().Add(DownloadTTL)
	slug := filenameSlug(note.Title)
	taken := make(map[string]int, len(present))
	out := make([]RecordingURL, 0, len(present))
	for _, c := range present {
		url, err := s.objects.PresignGet(ctx, c.AudioKey, DownloadTTL)
		if err != nil {
			return nil, fmt.Errorf("failed to presign audio: %w", err)
		}
		out = append(out, RecordingURL{
			CaptureID: c.ID,
			Filename:  uniqueFilename(taken, recordingFilename(slug, c)),
			URL:       url,
			ExpiresAt: expires,
		})
	}
	return out, nil
}

// audioPresent keeps the captures whose audio object still exists, asking the
// object store about several at once.
func (s *CaptureService) audioPresent(ctx context.Context, captures []model.CaptureIndex) ([]model.CaptureIndex, error) {
	exists := make([]bool, len(captures))
	errs := make([]error, len(captures))
	sem := make(chan struct{}, recordingExistenceParallelism)
	var wg sync.WaitGroup
	for i, c := range captures {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, key string) {
			defer wg.Done()
			defer func() { <-sem }()
			exists[i], errs[i] = s.objects.Exists(ctx, key)
		}(i, c.AudioKey)
	}
	wg.Wait()

	out := make([]model.CaptureIndex, 0, len(captures))
	for i, c := range captures {
		if errs[i] != nil {
			return nil, fmt.Errorf("failed to check audio for capture %s: %w", c.ID, errs[i])
		}
		if exists[i] {
			out = append(out, c)
		}
	}
	return out, nil
}

// recordingFilename is `<slug>-<yyyymmdd-hhmm>.<ext>` for one capture.
func recordingFilename(slug string, c model.CaptureIndex) string {
	stamp := "undated"
	if t, err := model.ParseTime(c.CreatedAt); err == nil {
		stamp = t.UTC().Format("20060102-1504")
	}
	ext := strings.TrimPrefix(path.Ext(c.AudioKey), ".")
	if ext == "" {
		ext = "bin"
	}
	return slug + "-" + stamp + "." + ext
}

// uniqueFilename returns name, or name with a "-2", "-3" ... before the
// extension when taken already holds it.
func uniqueFilename(taken map[string]int, name string) string {
	taken[name]++
	if taken[name] == 1 {
		return name
	}
	ext := path.Ext(name)
	return strings.TrimSuffix(name, ext) + "-" + strconv.Itoa(taken[name]) + ext
}

// maxFilenameSlugRunes bounds the title's share of a filename.
const maxFilenameSlugRunes = 60

// filenameSlug folds a note title into something every filesystem accepts:
// lowercase letters, digits and combining marks of any script — a vowel sign
// or virama is part of the syllable it follows, and dropping it mangles every
// Indic title — with runs of anything else collapsed to one hyphen, bounded,
// never empty.
func filenameSlug(title string) string {
	var b strings.Builder
	hyphen := true // suppress a leading one
	for _, r := range strings.ToLower(title) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r):
			b.WriteRune(r)
			hyphen = false
		case !hyphen:
			b.WriteByte('-')
			hyphen = true
		}
	}
	slug := strings.TrimRight(b.String(), "-")
	if runes := []rune(slug); len(runes) > maxFilenameSlugRunes {
		slug = strings.TrimRight(string(runes[:maxFilenameSlugRunes]), "-")
	}
	if slug == "" {
		return "recording"
	}
	return slug
}
