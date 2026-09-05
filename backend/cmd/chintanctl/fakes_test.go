package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// The test doubles live here, in a _test.go file, and nowhere else.
// TestProductionBinaryDoesNotLinkTestDoubles in internal/repository runs
// `go list -deps ./cmd/...` and fails if internal/repository/memory or
// internal/provider/fake is reachable from any main — including this one.

// fakePartition is an in-memory Partition: pk -> sk -> item.
type fakePartition struct {
	mu      sync.Mutex
	items   map[string]map[string]Item
	puts    int
	updates int
	deletes int
	scans   int
}

func newFakePartition() *fakePartition {
	return &fakePartition{items: map[string]map[string]Item{}}
}

func (f *fakePartition) Scan(ctx context.Context, pk, skPrefix string, fn func(Item) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.scans++
	part := f.items[pk]
	keys := make([]string, 0, len(part))
	for sk := range part {
		if strings.HasPrefix(sk, skPrefix) {
			keys = append(keys, sk)
		}
	}
	sort.Strings(keys)
	snapshot := make([]Item, 0, len(keys))
	for _, sk := range keys {
		snapshot = append(snapshot, cloneItem(part[sk]))
	}
	f.mu.Unlock()

	for _, it := range snapshot {
		if err := fn(it); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakePartition) Put(ctx context.Context, it Item) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	pk, sk := it.PK(), it.SK()
	if pk == "" || sk == "" {
		return errors.New("fake partition: item without pk/sk")
	}
	if f.items[pk] == nil {
		f.items[pk] = map[string]Item{}
	}
	f.items[pk][sk] = cloneItem(it)
	f.puts++
	return nil
}

func (f *fakePartition) Update(ctx context.Context, pk, sk string, set Item, expectVersion int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[pk][sk]
	if !ok || it.Num("version") != expectVersion {
		return ErrItemChanged
	}
	for name, v := range set {
		it[name] = v
	}
	f.items[pk][sk] = it
	f.updates++
	return nil
}

func (f *fakePartition) Get(ctx context.Context, pk, sk string) (Item, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[pk][sk]
	if !ok {
		return nil, false, nil
	}
	return cloneItem(it), true, nil
}

// ScanIndex answers a GSI query the way the real index would for an INCLUDE
// projection: every item whose pkAttr matches, ordered by the corresponding
// sort attribute (gsi1pk → gsi1sk), carrying its keys and index attributes.
func (f *fakePartition) ScanIndex(ctx context.Context, _ string, pkAttr, pk string, fn func(Item) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	skAttr := strings.Replace(pkAttr, "pk", "sk", 1)
	f.mu.Lock()
	var hits []Item
	for _, part := range f.items {
		for _, it := range part {
			if it.Str(pkAttr) == pk {
				hits = append(hits, Item{"pk": it["pk"], "sk": it["sk"], pkAttr: it[pkAttr], skAttr: it[skAttr]})
			}
		}
	}
	f.mu.Unlock()
	sort.Slice(hits, func(i, j int) bool { return hits[i].Str(skAttr) < hits[j].Str(skAttr) })
	for _, it := range hits {
		if err := fn(it); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakePartition) Delete(ctx context.Context, pk, sk string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items[pk], sk)
	f.deletes++
	return nil
}

func (f *fakePartition) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, part := range f.items {
		n += len(part)
	}
	return n
}

func cloneItem(it Item) Item {
	body, err := json.Marshal(it)
	if err != nil {
		panic(err)
	}
	var out Item
	if err := json.Unmarshal(body, &out); err != nil {
		panic(err)
	}
	return out
}

// fakeBlobs is an in-memory Blobs. The bodies live in the repository's own
// fake object store; this type adds the prefix listing that Blobs needs and
// repository.Objects does not have.
type fakeBlobs struct {
	mu      sync.Mutex
	store   *memory.Objects
	keys    map[string]bool
	opens   int
	puts    int
	deletes int
}

func newFakeBlobs() *fakeBlobs {
	return &fakeBlobs{store: memory.NewObjects(), keys: map[string]bool{}}
}

func (f *fakeBlobs) seed(t *testing.T, key, body, contentType string) {
	t.Helper()
	if err := f.Put(context.Background(), key, strings.NewReader(body), int64(len(body)), contentType); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
	f.mu.Lock()
	f.puts = 0
	f.mu.Unlock()
}

func (f *fakeBlobs) List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	keys := make([]string, 0, len(f.keys))
	for k := range f.keys {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	f.mu.Unlock()
	sort.Strings(keys)

	for _, k := range keys {
		body, etag, err := f.store.GetWithETag(ctx, k)
		if err != nil {
			return err
		}
		if err := fn(ObjectInfo{Key: k, Size: int64(len(body)), ETag: strings.Trim(etag, `"`)}); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeBlobs) Prefixes(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	for k := range f.keys {
		rest, ok := strings.CutPrefix(k, prefix)
		if !ok {
			continue
		}
		head, _, found := strings.Cut(rest, "/")
		if !found {
			continue
		}
		seen[prefix+head+"/"] = true
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeBlobs) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	body, err := f.store.Get(ctx, key)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, errObjectMissing
	}
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.opens++
	f.mu.Unlock()
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (f *fakeBlobs) Put(ctx context.Context, key string, body io.Reader, _ int64, contentType string) error {
	buf, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if err := f.store.Put(ctx, key, buf, contentType); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[key] = true
	f.puts++
	return nil
}

func (f *fakeBlobs) Delete(ctx context.Context, key string) error {
	err := f.store.Delete(ctx, key)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.keys, key)
	f.deletes++
	return nil
}

func (f *fakeBlobs) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keys[key]
}

// ---------------------------------------------------------------- fixtures

func newTestEnv(stdin io.Reader) (*env, *fakePartition, *fakeBlobs) {
	part := newFakePartition()
	blobs := newFakeBlobs()
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	return &env{
		Part:  part,
		Blobs: blobs,
		Target: target{
			Instance: "dev", Environment: "prod", Region: "us-east-1",
			Table: "chintan-dev-prod", Bucket: "chintan-content-dev-000000000000",
		},
		Stdin:  stdin,
		Stdout: io.Discard,
		Stderr: io.Discard,
	}, part, blobs
}

// noteItem builds the DynamoDB item repository writes for a note: the whole
// record in `data`, with the queried attributes promoted alongside it.
func noteItem(tenantID string, n model.NoteIndex) Item {
	blob, err := json.Marshal(n)
	if err != nil {
		panic(err)
	}
	it := Item{
		"pk":              StringAttr(tenantPK(tenantID)),
		"sk":              StringAttr("NOTE#" + n.ID),
		"type":            StringAttr("note"),
		"note_id":         StringAttr(n.ID),
		"title":           StringAttr(n.Title),
		"updated_at":      StringAttr(n.UpdatedAt),
		"s3_markdown_key": StringAttr(n.S3MarkdownKey),
		"s3_meta_key":     StringAttr(n.S3MetaKey),
		"version":         NumberAttr(n.Version),
		"data":            StringAttr(string(blob)),
	}
	if len(n.Aliases) > 0 {
		it["aliases"] = AttrValue{SS: n.Aliases}
	}
	if len(n.Tags) > 0 {
		it["tags"] = AttrValue{SS: n.Tags}
	}
	return it
}

// captureItem builds the item repository writes for a capture, GSI1 keys
// included: a row without them is the legacy shape reconcile reports, and a
// fixture that left them off would make every seeded capture a finding.
func captureItem(c model.CaptureIndex) Item {
	blob, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	return Item{
		"pk":         StringAttr(tenantPK(c.UserID)),
		"sk":         StringAttr("CAPTURE#" + c.ID),
		"type":       StringAttr("capture"),
		"capture_id": StringAttr(c.ID),
		"note_id":    StringAttr(c.NoteID),
		"status":     StringAttr(string(c.Status)),
		"created_at": StringAttr(c.CreatedAt),
		"version":    NumberAttr(c.Version),
		"gsi1pk":     StringAttr("TENANT#" + c.UserID + "#NOTE#" + c.NoteID),
		"gsi1sk":     StringAttr("CAPTURE#" + c.CreatedAt),
		"data":       StringAttr(string(blob)),
	}
}

func put(t *testing.T, part *fakePartition, it Item) {
	t.Helper()
	if err := part.Put(context.Background(), it); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	part.mu.Lock()
	part.puts = 0
	part.mu.Unlock()
}

// seedTenant lays down one tenant with one note, one capture and every
// per-capture artifact the pipeline writes.
func seedTenant(t *testing.T, part *fakePartition, blobs *fakeBlobs, tenantID string) {
	t.Helper()

	note := model.NoteIndex{
		ID:            "n1",
		Title:         "Kitchen: Rebuild Plan",
		Aliases:       []string{"kitchen", "rebuild"},
		Tags:          []string{"house", "todo"},
		Snippet:       "counter depth",
		UpdatedAt:     "2026-08-07T10:00:00.000000000Z",
		S3MarkdownKey: "tenants/" + tenantID + "/notes/n1/note.md",
		S3MetaKey:     "tenants/" + tenantID + "/notes/n1/meta.json",
		Version:       3,
	}
	put(t, part, noteItem(tenantID, note))

	capture := model.CaptureIndex{
		ID:          "c1",
		NoteID:      "n1",
		UserID:      tenantID,
		Status:      model.StatusAppended,
		Mode:        model.CleanupFaithful,
		AudioKey:    "tenants/" + tenantID + "/captures/c1/audio.webm",
		RawKey:      "tenants/" + tenantID + "/captures/c1/raw.txt",
		CleanKey:    "tenants/" + tenantID + "/captures/c1/clean.txt",
		SegmentsKey: "tenants/" + tenantID + "/captures/c1/segments.json",
		PeaksKey:    "tenants/" + tenantID + "/captures/c1/peaks.json",
		CreatedAt:   "2026-08-07T09:59:00.000000000Z",
		DurationMS:  42000,
		Version:     2,
	}
	put(t, part, captureItem(capture))

	put(t, part, Item{
		"pk":   StringAttr(tenantPK(tenantID)),
		"sk":   StringAttr("SETTINGS"),
		"type": StringAttr("settings"),
		"data": StringAttr(`{"cleanup_mode":"faithful","retention_days":0}`),
	})

	base := "tenants/" + tenantID
	blobs.seed(t, base+"/notes/n1/note.md", "# Kitchen\n\nCounter depth is 24 inches.\n", "text/markdown")
	blobs.seed(t, base+"/notes/n1/meta.json", `{"id":"n1"}`, "application/json")
	blobs.seed(t, base+"/captures/c1/audio.webm", "OPUSOPUSOPUS", "audio/webm")
	blobs.seed(t, base+"/captures/c1/raw.txt", "counter depth is twenty four inches", "text/plain")
	blobs.seed(t, base+"/captures/c1/clean.txt", "Counter depth is 24 inches.", "text/plain")
	blobs.seed(t, base+"/captures/c1/segments.json", `[{"start":0,"end":2}]`, "application/json")
	blobs.seed(t, base+"/captures/c1/peaks.json", `[0,1,0]`, "application/json")
}
