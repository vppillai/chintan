package pipeline

import (
	"context"
	"sync"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// forCapture is the pipeline as one capture's run sees it: the same
// configuration, with the capture's text artefacts and its destination row
// remembered between stages.
//
// Each stage used to read back from S3 the text the stage before it had just
// written — the transcript for routing, the routed text for the cleanup, the
// cleaned text for the append — and the destination row was read again
// straight after routing or the transcription's language check had read it.
// With 25–33 sequential AWS calls per capture that was a third of a second of
// the p50 spent outside the providers (R7-16a). The artefacts are still
// written as each stage finishes, so a retry resumes from S3 exactly as
// before; only this invocation's reads are served from memory.
func (p *Pipeline) forCapture(tenantID, captureID string) *Pipeline {
	texts := &captureTexts{Objects: p.cfg.Objects, bodies: map[string][]byte{}, keep: map[string]bool{}}
	for _, key := range []func(string, string) (string, error){keys.CaptureRaw, keys.CaptureRouted, keys.CaptureClean} {
		if k, err := key(tenantID, captureID); err == nil {
			texts.keep[k] = true
		}
	}
	cfg := p.cfg
	cfg.Objects = texts
	return &Pipeline{cfg: cfg, now: p.now, seen: &seenNote{}}
}

// captureTexts serves the capture's raw, routed and clean text from what this
// invocation last wrote or read. Only those three keys, which only the
// pipeline writes: a note body is edited by the person too and is always read
// from the bucket.
type captureTexts struct {
	repository.Objects
	mu     sync.Mutex
	bodies map[string][]byte
	keep   map[string]bool
}

// seenNote is the destination row this invocation last read, for the next
// stage that wants the same row. Only run's own check reads it (destination):
// the append stamp and the index refresh write the row under its version and
// always read it fresh.
type seenNote struct {
	note *model.NoteIndex
}

func (o *captureTexts) Get(ctx context.Context, key string) ([]byte, error) {
	if !o.keep[key] {
		return o.Objects.Get(ctx, key)
	}
	o.mu.Lock()
	body, ok := o.bodies[key]
	o.mu.Unlock()
	if ok {
		return body, nil
	}
	body, err := o.Objects.Get(ctx, key)
	if err == nil {
		o.remember(key, body)
	}
	return body, err
}

func (o *captureTexts) Put(ctx context.Context, key string, body []byte, contentType string) error {
	if err := o.Objects.Put(ctx, key, body, contentType); err != nil {
		return err
	}
	if o.keep[key] {
		o.remember(key, body)
	}
	return nil
}

func (o *captureTexts) remember(key string, body []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.bodies[key] = append([]byte(nil), body...)
}

// getNote reads a destination row and remembers it for destination.
func (p *Pipeline) getNote(ctx context.Context, tenantID, noteID string) (model.NoteIndex, error) {
	note, err := p.cfg.Store.GetNote(ctx, tenantID, noteID)
	if err == nil {
		p.rememberNote(note)
	}
	return note, err
}

// rememberNote keeps a row this run already holds, read or just created, for
// destination.
func (p *Pipeline) rememberNote(note model.NoteIndex) {
	if p.seen != nil {
		p.seen.note = &note
	}
}

// destination is the capture's destination row for run's checks: the one
// routing or the transcription's language check read moments ago when it is
// the same note, else a fresh read. The row routing read is milliseconds old;
// the language check's is as old as the transcription, and a note archived in
// those seconds is caught by nothing here, but the append stamp re-reads the
// row and a purged note fails it, and the retry then asks for a new
// destination.
func (p *Pipeline) destination(ctx context.Context, tenantID, noteID string) (model.NoteIndex, error) {
	if p.seen != nil && p.seen.note != nil && p.seen.note.ID == noteID {
		return *p.seen.note, nil
	}
	return p.getNote(ctx, tenantID, noteID)
}
