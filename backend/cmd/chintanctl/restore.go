package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/vppillai/chintan/backend/internal/obs"
)

type restoreResult struct {
	Target          target `json:"target"`
	In              string `json:"in"`
	Apply           bool   `json:"apply"`
	SourceInstance  string `json:"source_instance"`
	SourceTable     string `json:"source_table"`
	SourceBucket    string `json:"source_bucket"`
	CreatedAt       string `json:"created_at"`
	ItemsPlanned    int    `json:"items_planned"`
	ObjectsPlanned  int    `json:"objects_planned"`
	BytesPlanned    int64  `json:"bytes_planned"`
	ItemsRestored   int    `json:"items_restored"`
	ObjectsRestored int    `json:"objects_restored"`
	BytesRestored   int64  `json:"bytes_restored"`
	// Mismatches names every object whose bytes on disk disagree with the
	// manifest. A non-empty list means nothing was restored.
	Mismatches []string `json:"mismatches,omitempty"`
}

func (r *restoreResult) human(w *lineWriter) {
	w.printf("restore %s -> %s (%s)\n", r.In, r.Target.Instance, r.Target.Environment)
	w.printf("  backup taken %s from instance %q, table %q, bucket %q\n",
		backupAge(r.CreatedAt), r.SourceInstance, r.SourceTable, r.SourceBucket)
	if r.SourceTable != r.Target.Table || r.SourceBucket != r.Target.Bucket {
		w.printf("  note: restoring into a different table/bucket than the backup came from\n")
	}
	if len(r.Mismatches) > 0 {
		w.printf("  REFUSED: %d object(s) do not match the manifest hash:\n", len(r.Mismatches))
		for _, m := range r.Mismatches {
			w.printf("    %s\n", m)
		}
		return
	}
	w.printf("  verified %d objects (%s) against the manifest\n", r.ObjectsPlanned, humanBytes(r.BytesPlanned))
	if r.Apply {
		w.printf("  restored %d index items and %d objects (%s)\n",
			r.ItemsRestored, r.ObjectsRestored, humanBytes(r.BytesRestored))
		return
	}
	w.printf("  would restore %d index items and %d objects (%s)\n",
		r.ItemsPlanned, r.ObjectsPlanned, humanBytes(r.BytesPlanned))
}

func cmdRestore(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	var g globalFlags
	var in string
	fs := newFlagSet("restore", stderr)
	g.register(fs, false, true)
	fs.StringVar(&in, "in", "", "directory written by chintanctl backup (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if in == "" {
		return fmt.Errorf("--in is required")
	}
	e, err := dial(ctx, g, stdout, stderr, stdin)
	if err != nil {
		return err
	}
	res, resErr := runRestore(ctx, e, in, g.apply)
	if res != nil {
		if err := report(stdout, g.jsonOut, res); err != nil {
			return err
		}
		if resErr == nil {
			if err := dryRunBanner(stdout, g.apply,
				fmt.Sprintf("write %d index items and %d objects into %s",
					res.ItemsPlanned, res.ObjectsPlanned, e.Target.Instance)); err != nil {
				return err
			}
		}
	}
	return resErr
}

// errManifestMismatch is what restore fails with when the bytes on disk are
// not the bytes the backup recorded.
var errManifestMismatch = errors.New("chintanctl: backup does not match its manifest")

// runRestore verifies before it writes.
//
// Verification is a complete pass over the local files with no network at all:
// the two manifests are hashed and compared to the header, then every object
// is re-hashed and compared to its record. Only if all of that agrees does
// anything get written. Hashing while uploading would be cheaper and useless —
// by the time the mismatch was known the corruption would already be in S3.
func runRestore(ctx context.Context, e *env, in string, apply bool) (*restoreResult, error) {
	headerBody, err := os.ReadFile(filepath.Join(in, backupHeaderName))
	if err != nil {
		return nil, fmt.Errorf("read %s (an interrupted backup has no header and cannot be restored): %w",
			backupHeaderName, err)
	}
	var header backupHeader
	if err := json.Unmarshal(headerBody, &header); err != nil {
		return nil, fmt.Errorf("decode %s: %w", backupHeaderName, err)
	}
	if header.Format != backupFormat {
		return nil, fmt.Errorf("backup format %d is not supported by this build (expected %d)",
			header.Format, backupFormat)
	}

	res := &restoreResult{
		Target:         e.Target,
		In:             in,
		Apply:          apply,
		SourceInstance: header.Instance,
		SourceTable:    header.Table,
		SourceBucket:   header.Bucket,
		CreatedAt:      header.CreatedAt,
	}

	itemsPath := filepath.Join(in, backupItemsName)
	objectsPath := filepath.Join(in, backupObjectsName)
	if err := verifyFileHash(itemsPath, header.ItemsSHA256); err != nil {
		res.Mismatches = append(res.Mismatches, err.Error())
	}
	if err := verifyFileHash(objectsPath, header.ObjectsSHA256); err != nil {
		res.Mismatches = append(res.Mismatches, err.Error())
	}
	if len(res.Mismatches) > 0 {
		return res, errManifestMismatch
	}

	err = forEachObjectRecord(objectsPath, func(rec objectRecord) error {
		res.ObjectsPlanned++
		body := filepath.Join(in, backupObjectsDir, filepath.FromSlash(mustRel(rec.Key)))
		info, err := os.Stat(body)
		switch {
		case err != nil:
			res.Mismatches = append(res.Mismatches, fmt.Sprintf("%s: %v", rec.Key, err))
			return nil
		case info.Size() != rec.Size:
			res.Mismatches = append(res.Mismatches,
				fmt.Sprintf("%s: size %d, manifest says %d", rec.Key, info.Size(), rec.Size))
			return nil
		}
		sum, err := hashFile(body)
		if err != nil {
			res.Mismatches = append(res.Mismatches, fmt.Sprintf("%s: %v", rec.Key, err))
			return nil
		}
		if sum != rec.SHA256 {
			res.Mismatches = append(res.Mismatches,
				fmt.Sprintf("%s: sha256 %s, manifest says %s", rec.Key, sum, rec.SHA256))
			return nil
		}
		res.BytesPlanned += rec.Size
		return nil
	})
	if err != nil {
		return res, err
	}

	if err := forEachItem(itemsPath, func(Item) error { res.ItemsPlanned++; return nil }); err != nil {
		return res, err
	}

	if len(res.Mismatches) > 0 {
		return res, errManifestMismatch
	}
	if !apply {
		return res, nil
	}

	err = forEachItem(itemsPath, func(it Item) error {
		if err := e.Part.Put(ctx, it); err != nil {
			return err
		}
		res.ItemsRestored++
		return nil
	})
	if err != nil {
		return res, err
	}
	err = forEachObjectRecord(objectsPath, func(rec objectRecord) error {
		body := filepath.Join(in, backupObjectsDir, filepath.FromSlash(mustRel(rec.Key)))
		f, err := os.Open(body)
		if err != nil {
			return err
		}
		err = e.Blobs.Put(ctx, rec.Key, f, rec.Size, contentTypeFor(rec.Key))
		_ = f.Close()
		if err != nil {
			return err
		}
		res.ObjectsRestored++
		res.BytesRestored += rec.Size
		return nil
	})
	if err != nil {
		return res, err
	}

	obs.Log(ctx).Info("restore complete",
		slog.Int("items", res.ItemsRestored),
		slog.Int("objects", res.ObjectsRestored),
		slog.Int64("bytes", res.BytesRestored),
	)
	return res, nil
}

func mustRel(key string) string {
	rel, err := safeRelPath(key)
	if err != nil {
		return "__unsafe__"
	}
	return rel
}

func verifyFileHash(path, want string) error {
	got, err := hashFile(path)
	if err != nil {
		return fmt.Errorf("%s: %v", filepath.Base(path), err)
	}
	if got != want {
		return fmt.Errorf("%s: sha256 %s, header says %s", filepath.Base(path), got, want)
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	sum := sha256.New()
	if _, err := io.Copy(sum, bufio.NewReader(f)); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// forEachLine streams a JSONL manifest. The manifests are read twice — once to
// verify, once to apply — rather than held, so a large instance costs one line
// of memory, not a whole inventory.
func forEachLine[T any](path string, fn func(T) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
		}
		if err := fn(v); err != nil {
			return err
		}
	}
	return sc.Err()
}

func forEachObjectRecord(path string, fn func(objectRecord) error) error {
	return forEachLine(path, fn)
}

func forEachItem(path string, fn func(Item) error) error {
	return forEachLine(path, fn)
}

// contentTypeFor restores the content type from the key, because the layout
// gives every artifact a fixed extension. It is metadata, not content.
func contentTypeFor(key string) string {
	switch {
	case hasSuffix(key, ".md"):
		return "text/markdown; charset=utf-8"
	case hasSuffix(key, ".txt"):
		return "text/plain; charset=utf-8"
	case hasSuffix(key, ".json"):
		return "application/json"
	case hasSuffix(key, ".webm"):
		return "audio/webm"
	case hasSuffix(key, ".m4a"), hasSuffix(key, ".mp4"):
		return "audio/mp4"
	case hasSuffix(key, ".mp3"):
		return "audio/mpeg"
	case hasSuffix(key, ".ogg"), hasSuffix(key, ".opus"):
		return "audio/ogg"
	case hasSuffix(key, ".wav"):
		return "audio/wav"
	default:
		return "application/octet-stream"
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
