package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// exportSink is where an export writes. Two implementations: a directory,
// which is resumable, and a gzipped tar, which is not.
//
// Every write goes through an io.Writer and every source through an io.Reader.
// Nothing here ever holds an object body.
type exportSink interface {
	// Create opens a destination for the relative path rel. size is the exact
	// byte count that will be written, because a tar header needs it before
	// the first byte of content.
	Create(rel string, size int64) (io.WriteCloser, error)
	// Existing reports the size of a destination already present from an
	// earlier run, so an unchanged object need not be fetched again.
	Existing(rel string) (int64, bool)
	// Resumable reports whether Existing can ever be true.
	Resumable() bool
	Close() error
}

// safeRelPath makes an untrusted key safe to use as a path under the output
// root. An S3 key may legally contain "../"; joining one straight onto a
// directory would write outside the export.
func safeRelPath(rel string) (string, error) {
	rel = strings.ReplaceAll(rel, `\`, "_")
	// The check is on the raw segments, not on the cleaned path. path.Clean
	// would absorb "../" into a contained path, which is safe but silently
	// collapses two distinct keys onto one file. Refusing is the honest
	// answer: a key like that did not come from internal/keys.
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", fmt.Errorf("refusing to write path escaping the output root: %q", rel)
		}
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+rel), "/")
	if cleaned == "" || cleaned == "." {
		return "", fmt.Errorf("refusing to write empty path from %q", rel)
	}
	return cleaned, nil
}

// dirSink writes an export into a directory tree.
type dirSink struct {
	root string
}

func newDirSink(root string) (*dirSink, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}
	return &dirSink{root: root}, nil
}

func (d *dirSink) Create(rel string, _ int64) (io.WriteCloser, error) {
	clean, err := safeRelPath(rel)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(d.root, filepath.FromSlash(clean))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, fmt.Errorf("create directory for %s: %w", clean, err)
	}
	f, err := os.Create(full)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", clean, err)
	}
	return f, nil
}

func (d *dirSink) Existing(rel string) (int64, bool) {
	clean, err := safeRelPath(rel)
	if err != nil {
		return 0, false
	}
	info, err := os.Stat(filepath.Join(d.root, filepath.FromSlash(clean)))
	if err != nil || info.IsDir() {
		return 0, false
	}
	return info.Size(), true
}

func (d *dirSink) Resumable() bool { return true }
func (d *dirSink) Close() error    { return nil }

// tarSink streams an export into a .tar.gz.
//
// It writes each entry directly into the archive as the object is read, so a
// twenty-gigabyte corpus costs one buffer, not twenty gigabytes.
type tarSink struct {
	file *os.File
	gz   *gzip.Writer
	tw   *tar.Writer
	now  time.Time
	open bool
}

func newTarSink(path string) (*tarSink, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create output directory: %w", err)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	gz := gzip.NewWriter(f)
	return &tarSink{file: f, gz: gz, tw: tar.NewWriter(gz), now: time.Now().UTC()}, nil
}

func (t *tarSink) Create(rel string, size int64) (io.WriteCloser, error) {
	if t.open {
		return nil, errors.New("tar sink: previous entry still open")
	}
	clean, err := safeRelPath(rel)
	if err != nil {
		return nil, err
	}
	if size < 0 {
		return nil, fmt.Errorf("tar sink: %s needs a known size", clean)
	}
	hdr := &tar.Header{
		Name:    clean,
		Mode:    0o644,
		Size:    size,
		ModTime: t.now,
		Format:  tar.FormatPAX,
	}
	if err := t.tw.WriteHeader(hdr); err != nil {
		return nil, fmt.Errorf("tar header %s: %w", clean, err)
	}
	t.open = true
	return &tarEntry{sink: t}, nil
}

func (t *tarSink) Existing(string) (int64, bool) { return 0, false }
func (t *tarSink) Resumable() bool               { return false }

func (t *tarSink) Close() error {
	if err := t.tw.Close(); err != nil {
		_ = t.gz.Close()
		_ = t.file.Close()
		return fmt.Errorf("close tar: %w", err)
	}
	if err := t.gz.Close(); err != nil {
		_ = t.file.Close()
		return fmt.Errorf("close gzip: %w", err)
	}
	return t.file.Close()
}

type tarEntry struct {
	sink *tarSink
}

func (e *tarEntry) Write(p []byte) (int, error) { return e.sink.tw.Write(p) }

func (e *tarEntry) Close() error {
	e.sink.open = false
	return nil
}

// isArchivePath reports whether an --out value asks for a gzipped tar rather
// than a directory.
func isArchivePath(p string) bool {
	lower := strings.ToLower(p)
	return strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz")
}
