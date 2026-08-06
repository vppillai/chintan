package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Fixture loading for the credential-free test mode (§11.5).
//
// §11.5 requires that the tests for a mutating data script "run against the fake-AWS
// harness in CI, with no real AWS credentials". The fake `aws` shim on PATH covers a bash
// script's own AWS calls; it cannot cover a compiled binary reaching DynamoDB and S3
// through the SDK. So the binary carries its own substitutable stores — repository.Memory
// and fixtureStore — and this file is how a bash test seeds them.
//
// **Every key in a fixture is built by the keys package** (I11). The format therefore
// names an entity KIND and its identifiers rather than a sort key, which is not merely
// tidiness: a fixture that carried literal sort keys would be a second, unchecked
// key-construction path, and a test loading it would prove the operation works on keys
// that no production code path can produce.

// fixtureSet is the on-disk fixture document.
type fixtureSet struct {
	Records []fixtureRecord `json:"records"`
	Objects []fixtureObject `json:"objects"`
}

// fixtureRecord is one DynamoDB item to seed.
type fixtureRecord struct {
	// Kind selects the keys constructor. Unknown kinds are an error rather than a skip:
	// a silently skipped record makes a completeness test pass having inspected less
	// than it thinks.
	Kind string `json:"kind"`

	// IDs are the constructor's arguments in declaration order.
	IDs []string `json:"ids,omitempty"`

	// Seq is the segment sequence number, for the one constructor that takes an int.
	Seq int `json:"seq,omitempty"`

	Attrs map[string]any `json:"attrs,omitempty"`
	TTL   int64          `json:"ttl,omitempty"`
}

// fixtureObject is one S3 object to seed, with as many versions as the test needs.
type fixtureObject struct {
	Kind string   `json:"kind"`
	IDs  []string `json:"ids,omitempty"`

	// Body is the current version's content. Earlier versions get a marker appended, so
	// their checksums differ and an export copying the wrong version is visible.
	Body string `json:"body"`

	// Versions is how many versions exist, default 1. Above 1 seeds noncurrent versions
	// — the copies a plain DeleteObject leaves behind (G-021), and the reason erasure
	// enumerates versions at all.
	Versions int `json:"versions,omitempty"`

	// DeleteMarker adds a tombstone as the current version, modelling a key that was
	// "deleted" without its data being destroyed. An erasure that reported this key as
	// gone would be repeating exactly the mistake G-021 describes.
	DeleteMarker bool `json:"delete_marker,omitempty"`
}

// loadFixtures reads a fixture set and returns the two seeded stores.
//
// path may be the JSON file itself or a directory containing tenant-data.json, so a test
// can keep a scenario's fake-CLI fixtures and its store fixture in one directory.
func loadFixtures(path string, tenant keys.TenantID) (repository.Repository, objectStore, error) {
	file := path
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		file = filepath.Join(path, "tenant-data.json")
	}
	raw, err := os.ReadFile(file) //nolint:gosec // an operator-supplied test fixture path
	if err != nil {
		return nil, nil, usageErrorf("reading fixture %s: %v", file, err)
	}
	var set fixtureSet
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Unknown fields are an error: a fixture with a mistyped field name would otherwise
	// seed something different from what its author wrote, and the test would still pass.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&set); err != nil {
		return nil, nil, usageErrorf("parsing fixture %s: %v", file, err)
	}

	repo := repository.NewMemory()
	for i, r := range set.Records {
		key, err := fixtureKey(tenant, r)
		if err != nil {
			return nil, nil, usageErrorf("fixture %s record %d: %v", file, i, err)
		}
		if err := repo.Put(context.Background(), repository.Item{Key: key, Attrs: r.Attrs, TTL: r.TTL}); err != nil {
			return nil, nil, usageErrorf("fixture %s record %d: %v", file, i, err)
		}
	}

	store := newFixtureStore(filepath.Base(path))
	for i, o := range set.Objects {
		key, err := fixtureObjectKey(tenant, o)
		if err != nil {
			return nil, nil, usageErrorf("fixture %s object %d: %v", file, i, err)
		}
		n := o.Versions
		if n < 1 {
			n = 1
		}
		for v := 1; v <= n; v++ {
			body := o.Body
			if v < n {
				body = fmt.Sprintf("%s\n[noncurrent version %d]", o.Body, v)
			}
			latest := v == n && !o.DeleteMarker
			store.add(objectVersion{
				Key:       key,
				VersionID: fmt.Sprintf("%s|v%d", key, v),
				IsLatest:  latest,
				Bytes:     int64(len(body)),
			}, []byte(body))
		}
		if o.DeleteMarker {
			store.add(objectVersion{
				Key:          key,
				VersionID:    fmt.Sprintf("%s|dm", key),
				IsLatest:     true,
				DeleteMarker: true,
			}, nil)
		}
	}
	return repo, store, nil
}

// fixtureKey builds one record's key through the keys package.
//
// The switch is the fixture format's vocabulary. It is deliberately not exhaustive over
// every entity type — it covers what the tests need — and an unlisted kind is refused with
// the list, so extending it is a one-line change with an obvious place to make it.
func fixtureKey(t keys.TenantID, r fixtureRecord) (keys.DynamoKey, error) {
	need := func(n int) error {
		if len(r.IDs) != n {
			return fmt.Errorf("kind %q needs %d id(s), got %d", r.Kind, n, len(r.IDs))
		}
		return nil
	}
	switch r.Kind {
	case "tenant":
		return keys.Tenant(t)
	case "user":
		if err := need(1); err != nil {
			return keys.DynamoKey{}, err
		}
		return keys.User(t, r.IDs[0])
	case "capture":
		if err := need(1); err != nil {
			return keys.DynamoKey{}, err
		}
		return keys.Capture(t, r.IDs[0])
	case "session":
		if err := need(2); err != nil {
			return keys.DynamoKey{}, err
		}
		return keys.Session(t, r.IDs[0], r.IDs[1])
	case "segment":
		if err := need(1); err != nil {
			return keys.DynamoKey{}, err
		}
		return keys.Segment(t, r.IDs[0], r.Seq)
	case "item":
		if err := need(1); err != nil {
			return keys.DynamoKey{}, err
		}
		return keys.Item(t, r.IDs[0])
	case "thread":
		if err := need(1); err != nil {
			return keys.DynamoKey{}, err
		}
		return keys.Thread(t, r.IDs[0])
	case "rule":
		if err := need(1); err != nil {
			return keys.DynamoKey{}, err
		}
		return keys.Rule(t, r.IDs[0])
	case "ingest":
		if err := need(1); err != nil {
			return keys.DynamoKey{}, err
		}
		return keys.Ingest(t, r.IDs[0])
	case "usage":
		if err := need(3); err != nil {
			return keys.DynamoKey{}, err
		}
		return keys.Usage(t, r.IDs[0], r.IDs[1], r.IDs[2])
	case "audit":
		if err := need(1); err != nil {
			return keys.DynamoKey{}, err
		}
		return keys.Audit(t, r.IDs[0])
	case "idempotency":
		if err := need(1); err != nil {
			return keys.DynamoKey{}, err
		}
		return keys.Idempotency(t, r.IDs[0])
	default:
		return keys.DynamoKey{}, fmt.Errorf("unknown record kind %q (known: tenant, user, capture, session, segment, item, thread, rule, ingest, usage, audit, idempotency)", r.Kind)
	}
}

// fixtureObjectKey builds one object's key through the keys package.
func fixtureObjectKey(t keys.TenantID, o fixtureObject) (string, error) {
	need := func(n int) error {
		if len(o.IDs) != n {
			return fmt.Errorf("kind %q needs %d id(s), got %d", o.Kind, n, len(o.IDs))
		}
		return nil
	}
	switch o.Kind {
	case "audio_segment":
		if err := need(2); err != nil {
			return "", err
		}
		return keys.S3AudioSegment(t, o.IDs[0], o.IDs[1])
	case "audio_continuous":
		if err := need(2); err != nil {
			return "", err
		}
		return keys.S3AudioContinuous(t, o.IDs[0], o.IDs[1])
	case "capture_content":
		if err := need(1); err != nil {
			return "", err
		}
		return keys.S3CaptureContent(t, o.IDs[0])
	case "capture_alignment":
		if err := need(1); err != nil {
			return "", err
		}
		return keys.S3CaptureAlignment(t, o.IDs[0])
	case "transcript_l0":
		if err := need(3); err != nil {
			return "", err
		}
		return keys.S3TranscriptL0(t, o.IDs[0], o.IDs[1], o.IDs[2])
	case "transcript_l1":
		if err := need(2); err != nil {
			return "", err
		}
		return keys.S3TranscriptL1(t, o.IDs[0], o.IDs[1])
	case "item_text":
		if err := need(1); err != nil {
			return "", err
		}
		return keys.S3ItemText(t, o.IDs[0])
	case "embeddings_matrix":
		return keys.S3EmbeddingsMatrix(t)
	case "embeddings_meta":
		return keys.S3EmbeddingsMeta(t)
	default:
		return "", fmt.Errorf("unknown object kind %q (known: audio_segment, audio_continuous, capture_content, capture_alignment, transcript_l0, transcript_l1, item_text, embeddings_matrix, embeddings_meta)", o.Kind)
	}
}
