package main

// Snapshot machinery shared by `chintanctl backup` and `chintanctl restore`
// (§11.4, Phase 0, "Backup and data protection").
//
// # What a snapshot is for, and what it is not for
//
// PITR is enabled on the table and versioning on the bucket (§6.3, §6.2), so this
// is not the only recovery mechanism and must not pretend to be. The division of
// labour, because confusing the two is how an operator reaches for the wrong one
// at 2am:
//
//	PITR / bucket versioning  in-place time travel inside one account, to any
//	                          second in the retention window, at no operator
//	                          effort. This is what you use for "the pipeline wrote
//	                          nonsense an hour ago".
//	a snapshot               a PORTABLE, self-describing copy that outlives the
//	                          account, the table, and the stack. This is what you
//	                          use for "move a tenant to a new instance", "keep a
//	                          copy off AWS before a risky migration", and "prove
//	                          the corpus can be reconstructed somewhere else".
//
// So this code deliberately does not implement point-in-time selection: asking it
// to duplicate PITR would produce a worse PITR. It reads the corpus as it is now,
// and its value is that the result is a directory of files with a manifest.
//
// # A snapshot carries no tenant, on purpose
//
// Restore's contract is "restore from a snapshot into a NAMED tenant" (§11.4), so
// the target tenant may differ from the source and re-keying is mandatory. I11 is
// explicit that it binds "admin and migration scripts" too, and the way re-keying
// goes wrong is not a deliberate cross-tenant write — it is a key copied verbatim
// out of an archive.
//
// So the format removes the possibility rather than guarding against it. Every
// DynamoDB record is stored as its SORT KEY only, and every object as its path
// RELATIVE to the per-tenant prefix. Both are tenant-free by construction (§6.3
// gives every entity the same partition key; §6.2 nests every object under the
// one tenant prefix), and the source tenant appears exactly once, in the manifest,
// as provenance that nothing reads back into a key. Restore therefore cannot
// produce a key without asking internal/keys for the TARGET tenant's partition
// key and object prefix — there is no stored key for it to reuse.
//
// Backup enforces the other half: every record it reads must sit in the source
// tenant's partition and every object under the source tenant's prefix, or it
// refuses. A stray record from another tenant is not silently copied.
//
// # A snapshot is user content outside the encrypted store
//
// §9.2: "Voice recordings are among the most sensitive content categories a
// product can hold. Treat the audio corpus as such regardless of current user
// count." A snapshot contains verbatim transcripts and raw audio.
//
// I8's at-rest encryption is a property of the bucket and the table (SSE-S3 and
// SSE-DynamoDB with AWS-managed keys in the personal phase) — it is NOT a property
// of these files. Once written to a filesystem they are plaintext, with no key to
// revoke and therefore no crypto-shredding path at all (§9.3). Files are created
// 0600 and directories 0700 so at least the default is not world-readable, the
// manifest carries a notice for whoever finds the directory a year later, and
// backup refuses to write a filesystem destination under --apply without an
// explicit acknowledgement flag. None of that makes a local snapshot safe; it
// makes the operator's acceptance of the risk explicit and recorded.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/vppillai/chintan/backend/internal/audit"
	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/ids"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/systemid"
	"github.com/vppillai/chintan/backend/internal/version"
)

// snapshotFormatVersion is the archive format this build reads and writes.
//
// Checked on restore and refused rather than partially interpreted, for the same
// reason config.SchemaVersion is: a format that grew a field this build does not
// know about is a format whose omissions this build cannot see.
const snapshotFormatVersion = 1

// File names inside a snapshot directory. Named constants because restore looks
// for exactly these and a typo would read as "not a snapshot".
const (
	snapshotManifestFile = "manifest.json"
	snapshotItemsFile    = "items.jsonl"
	snapshotObjectsDir   = "objects"
)

// Permissions for everything a snapshot writes. Restrictive because the contents
// are verbatim transcripts and raw audio outside the encrypted store (§9.2) — see
// the package note above.
const (
	snapshotFileMode = fs.FileMode(0o600)
	snapshotDirMode  = fs.FileMode(0o700)
)

// snapshotNotice is embedded in every manifest, for whoever finds the directory
// later without the context of having run the command.
const snapshotNotice = "Contains verbatim transcripts and raw audio for one tenant, in plaintext. " +
	"The at-rest encryption of the source store does not follow these files (I8, §9.2). " +
	"Keep on encrypted media; delete when no longer needed."

// ---------------------------------------------------------------------------
// Archive format
// ---------------------------------------------------------------------------

// snapshotManifest is <snapshot>/manifest.json — the whole of what makes an
// archive self-describing.
type snapshotManifest struct {
	FormatVersion int    `json:"format_version"`
	SnapshotID    string `json:"snapshot_id"`
	SystemID      string `json:"system_id"`
	Instance      string `json:"instance"`
	CreatedAt     string `json:"created_at"`
	ToolVersion   string `json:"tool_version"`

	// SourceTenant is provenance only. **Nothing constructs a key from it** — see
	// the package note. It is here so an operator can tell two snapshots apart and
	// so a restore can report what it is about to graft onto which tenant.
	SourceTenant string `json:"source_tenant"`

	Notice string `json:"notice"`

	// Excluded names anything deliberately absent, so a reader can tell a
	// considered omission from a truncated archive.
	Excluded []string `json:"excluded,omitempty"`

	Items   snapshotItemsSection  `json:"items"`
	Objects []snapshotObjectEntry `json:"objects"`
}

// snapshotItemsSection describes the DynamoDB side of the archive.
type snapshotItemsSection struct {
	Count int    `json:"count"`
	File  string `json:"file"`
	// SHA256 covers the whole items file. Restore verifies it before writing
	// anything: a half-written archive that restores silently is worse than one
	// that refuses, because the missing records are the ones nobody notices.
	SHA256 string `json:"sha256"`
}

// snapshotObjectEntry describes one stored object.
type snapshotObjectEntry struct {
	// Path is relative to the per-tenant prefix, never a full object key.
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	// SHA256 is what makes a restore both verifiable and idempotent: an object
	// already present at the target with this hash is provably the same bytes, so
	// skipping it is safe rather than assumed.
	SHA256 string `json:"sha256"`
	// L0 marks a raw-transcript object, classified at backup time from the key
	// layout (see snapshotIsL0Path). Recorded so restore's I1 protection does not
	// depend on re-deriving the layout from a different build.
	L0 bool `json:"l0"`
}

// snapshotItem is one line of items.jsonl.
//
// No partition key and no GSI1 partition key: both are the tenant's, and storing
// them would give restore a key to copy instead of one to construct (I11).
type snapshotItem struct {
	SK     string         `json:"sk"`
	Attrs  map[string]any `json:"attrs,omitempty"`
	GSI1SK string         `json:"gsi1sk,omitempty"`
	TTL    int64          `json:"ttl,omitempty"`
}

// ---------------------------------------------------------------------------
// Object store seam
// ---------------------------------------------------------------------------

// snapshotObjectRef is one object as a listing reports it.
type snapshotObjectRef struct {
	Key   string
	Bytes int64
}

// snapshotObjectStore is the narrow object surface backup and restore need.
//
// Four operations, locally declared, for the same reason repository.DynamoAPI is:
// the absence of a delete is a property of the type rather than of the current
// implementation. Neither of these commands may remove an object — deletion of
// stored content is the erasure operation's job, separately permissioned (§9.3),
// and L0 in particular has no delete path outside it (I1).
type snapshotObjectStore interface {
	// List returns every object under a prefix. The prefix is always a
	// keys-constructed per-tenant prefix, so there is no way to express "list the
	// whole bucket" (I11).
	List(ctx context.Context, prefix string) ([]snapshotObjectRef, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Create(ctx context.Context, key string, r io.Reader) error
	Exists(ctx context.Context, key string) (bool, error)
}

// snapshotFSObjects is a filesystem-backed object store rooted at a directory.
//
// Used for three things: the snapshot archive's own object tree, the local store
// tree the harness tests against (§11.5), and a filesystem backup destination.
type snapshotFSObjects struct{ root string }

// snapshotSafePath maps an object key to a path under root, refusing anything
// that could escape it.
//
// This is a security check, not tidiness. Restore reads object paths out of an
// archive that may have come from anywhere, and a path of "../../.ssh/authorized_keys"
// in a manifest would otherwise make restore an arbitrary-file-write primitive.
// The same check guards the destination side of a backup, where the keys come from
// the store rather than from an operator.
func snapshotSafePath(root, key string) (string, error) {
	if err := snapshotCheckKeyShape(key); err != nil {
		return "", err
	}
	full := filepath.Join(root, filepath.FromSlash(key))
	// Belt and braces: prove containment of the result rather than trusting the
	// shape checks to have been exhaustive.
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("snapshot: object key resolves outside the store root (length %d)", len(key))
	}
	return full, nil
}

// snapshotCheckKeyShape refuses an object path that could escape a store root or
// forge a key.
//
// Separate from snapshotSafePath because restore validates a path from the archive
// *before* joining it to the target tenant's prefix — a traversal segment must never
// reach a key at all, not merely fail to escape a directory.
func snapshotCheckKeyShape(key string) error {
	if key == "" {
		return fmt.Errorf("snapshot: empty object key")
	}
	if strings.ContainsRune(key, '\\') {
		// Refused rather than normalised: a backslash is not a separator in an S3
		// key, so a key containing one either came from a Windows-flavoured
		// producer or is an attempt to confuse this check.
		return fmt.Errorf("snapshot: object key contains a backslash (length %d)", len(key))
	}
	if strings.ContainsFunc(key, unicode.IsControl) {
		return fmt.Errorf("snapshot: object key contains a control character (length %d)", len(key))
	}
	if path.IsAbs(key) || strings.HasPrefix(key, "/") {
		return fmt.Errorf("snapshot: object key is absolute (length %d)", len(key))
	}
	// path.Clean collapses "a/../b"; comparing against the original catches every
	// traversal and every redundant separator rather than only a leading "..".
	if path.Clean(key) != key {
		return fmt.Errorf("snapshot: object key is not in canonical form (length %d); refusing a path that could escape the store root", len(key))
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("snapshot: object key has an empty or relative path segment (length %d)", len(key))
		}
	}
	return nil
}

// List walks the tree and reports every object whose key begins with prefix.
func (s snapshotFSObjects) List(_ context.Context, prefix string) ([]snapshotObjectRef, error) {
	var out []snapshotObjectRef
	err := filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// A store or archive with no objects yet is empty, not broken.
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, snapshotObjectRef{Key: key, Bytes: info.Size()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot: listing objects under %s: %w", s.root, err)
	}
	// Sorted so a plan is deterministic. A plan whose order varies between a
	// dry-run and an --apply cannot be diffed, which is the §11.5 assertion.
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Open returns one object's body. The caller closes it.
func (s snapshotFSObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	full, err := snapshotSafePath(s.root, key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, fmt.Errorf("snapshot: opening object: %w", err)
	}
	return f, nil
}

// Create writes one object. Refuses to replace an existing one.
//
// O_EXCL rather than a check followed by a write: restore's promise is that it
// never overwrites (see restore.go), and a check-then-write has a window in which
// two concurrent restores both believe the object is absent. Enforcing it at the
// syscall means the promise holds even then.
func (s snapshotFSObjects) Create(_ context.Context, key string, r io.Reader) error {
	full, err := snapshotSafePath(s.root, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), snapshotDirMode); err != nil {
		return fmt.Errorf("snapshot: creating object directory: %w", err)
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, snapshotFileMode)
	if err != nil {
		return fmt.Errorf("snapshot: creating object: %w", err)
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return fmt.Errorf("snapshot: writing object: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("snapshot: closing object: %w", err)
	}
	return nil
}

// Exists reports whether an object is present.
func (s snapshotFSObjects) Exists(_ context.Context, key string) (bool, error) {
	full, err := snapshotSafePath(s.root, key)
	if err != nil {
		return false, err
	}
	switch _, err := os.Stat(full); {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("snapshot: stat object: %w", err)
	}
}

// ---------------------------------------------------------------------------
// Corpus handle — the pair of stores a tenant's data lives in
// ---------------------------------------------------------------------------

// snapshotCorpus is the (records, objects) pair these commands read and write.
type snapshotCorpus struct {
	repo    repository.Repository
	objects snapshotObjectStore
	// label describes the corpus in operator-facing output, so a plan says which
	// store it was computed against.
	label string
	// flush persists a local store tree. Nil for a store that needs none.
	flush func() error
}

// snapshotLocalStoreItems is the file a local store tree keeps its records in.
const snapshotLocalStoreItems = "items.json"

// snapshotLocalRepo is a file-backed Repository over repository.Memory.
//
// Memory rather than a second fake: it already enforces what DynamoDB enforces —
// PutOnce's conditional write, the 400KB ceiling, sort-key ordering — and §11.5
// requires these scripts to be tested with no AWS credentials. A looser store here
// would let a test pass on code that fails in production, which repository/memory.go
// calls worse than having no fake at all.
type snapshotLocalRepo struct {
	*repository.Memory
	dir string
}

// snapshotStoredItem is one record in a local store tree's items.json. Full keys,
// unlike a snapshot archive: this models the table, which holds every tenant, and
// backup's tenant scoping is only observable against a store that has more than
// one tenant in it.
type snapshotStoredItem struct {
	PK     string         `json:"pk"`
	SK     string         `json:"sk"`
	Attrs  map[string]any `json:"attrs,omitempty"`
	GSI1PK string         `json:"gsi1pk,omitempty"`
	GSI1SK string         `json:"gsi1sk,omitempty"`
	TTL    int64          `json:"ttl,omitempty"`
}

// snapshotOpenLocalCorpus opens a local store tree: records in items.json, objects
// under objects/.
//
// This is the mode the harness runs (§11.5) and the mode a restore into a scratch
// tree uses. It is a real store, not a stub: writes are persisted, so a test can
// assert that --apply wrote exactly what the dry-run described, and that the audit
// record I13 requires was written.
func snapshotOpenLocalCorpus(dir string) (*snapshotCorpus, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("snapshot: resolving store directory: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("snapshot: opening store %s: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("snapshot: store %s is not a directory", abs)
	}

	mem := repository.NewMemory()
	raw, err := os.ReadFile(filepath.Join(abs, snapshotLocalStoreItems))
	switch {
	case err == nil:
		var stored []snapshotStoredItem
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		if err := dec.Decode(&stored); err != nil {
			return nil, fmt.Errorf("snapshot: reading store records: %w", err)
		}
		for _, s := range stored {
			attrs, err := snapshotNormalizeNumbers(s.Attrs)
			if err != nil {
				return nil, fmt.Errorf("snapshot: store record %s: %w", s.SK, err)
			}
			item := repository.Item{
				Key:    keys.DynamoKey{PK: s.PK, SK: s.SK},
				GSI1PK: s.GSI1PK,
				GSI1SK: s.GSI1SK,
				TTL:    s.TTL,
			}
			if m, ok := attrs.(map[string]any); ok {
				item.Attrs = m
			}
			if err := mem.Put(context.Background(), item); err != nil {
				return nil, fmt.Errorf("snapshot: loading store record: %w", err)
			}
		}
	case errors.Is(err, fs.ErrNotExist):
		// An empty store is the normal state of a fresh restore target.
	default:
		return nil, fmt.Errorf("snapshot: reading store records: %w", err)
	}

	repo := &snapshotLocalRepo{Memory: mem, dir: abs}
	return &snapshotCorpus{
		repo:    repo,
		objects: snapshotFSObjects{root: filepath.Join(abs, snapshotObjectsDir)},
		label:   "local store " + abs,
		flush:   repo.flush,
	}, nil
}

// flush writes every record back to items.json.
func (r *snapshotLocalRepo) flush() error {
	ctx := context.Background()
	all := r.Keys()
	out := make([]snapshotStoredItem, 0, len(all))
	for _, k := range all {
		item, err := r.Get(ctx, k)
		if err != nil {
			return fmt.Errorf("snapshot: reading back store record: %w", err)
		}
		out = append(out, snapshotStoredItem{
			PK: item.Key.PK, SK: item.Key.SK, Attrs: item.Attrs,
			GSI1PK: item.GSI1PK, GSI1SK: item.GSI1SK, TTL: item.TTL,
		})
	}
	return snapshotWriteJSON(filepath.Join(r.dir, snapshotLocalStoreItems), out)
}

// snapshotOpenLiveCorpus is the live-AWS path, and it refuses.
//
// Being explicit about why, because a silent partial snapshot is the failure this
// refusal exists to prevent: the record side is wired and works — awsclient.NewDynamoDB
// plus repository.NewDynamo is the sanctioned seam — but the object side needs LIST,
// PUT and HEAD, and internal/awsclient exposes GetObject only. Reaching for the AWS
// SDK from this command instead would put client construction in a second place, which
// is exactly what that package exists to prevent (its own doc names the symptom: a
// client built ad hoc picks up the ambient region, and the ambient region on a
// developer machine is not the deploy region).
//
// So this returns an actionable refusal rather than half a snapshot. A "full
// point-in-time snapshot" (§11.4) missing every audio object and every transcript
// would be worse than no snapshot at all, because it would be trusted.
func snapshotOpenLiveCorpus(instance string) (*snapshotCorpus, error) {
	return nil, fmt.Errorf(
		"the live path is not wired yet: the record side would read table %s, but the object side needs "+
			"List/Put/Head on the bucket and internal/awsclient exposes GetObject only. "+
			"Refusing rather than writing a snapshot with no audio and no transcripts in it. "+
			"Use --store <dir> to operate against a local store tree; the plan, the re-keying and the "+
			"I1 protections are the same code either way",
		systemid.TableName(instance))
}

// snapshotOpenCorpus resolves the store the caller asked for.
func snapshotOpenCorpus(storeDir, instance string) (*snapshotCorpus, error) {
	if storeDir != "" {
		return snapshotOpenLocalCorpus(storeDir)
	}
	return snapshotOpenLiveCorpus(instance)
}

// ---------------------------------------------------------------------------
// Key layout — everything tenant-shaped comes from internal/keys
// ---------------------------------------------------------------------------

// snapshotTenantPK returns a tenant's partition key.
//
// Via keys.Tenant rather than string concatenation: it is the only key constructor
// in the system (I11), it validates the tenant, and check-tenant-keys.sh fails the
// build if a key prefix literal appears outside it. An admin script assembling a
// partition key by hand is the exact case I11 calls out.
func snapshotTenantPK(t keys.TenantID) (string, error) {
	k, err := keys.Tenant(t)
	if err != nil {
		return "", err
	}
	return k.PK, nil
}

// snapshotL0Fragment returns the path fragment that identifies a raw-transcript
// object, DERIVED from the keys package rather than written here as a literal.
//
// Two reasons it is derived. First, a hand-written path fragment in this file is a
// second copy of the S3 layout, and the copy that goes stale is the one that decides
// whether restore protects L0 — a layout change would silently turn the I1 guard
// into a no-op. Second, this file must contain no key literal at all
// (check-tenant-keys.sh, I11), and a path fragment is part of a key.
//
// Fails rather than guessing: a derivation that cannot identify L0 objects means
// restore cannot prove it is not overwriting one, and the only safe response to that
// is to refuse the operation (see runRestore).
func snapshotL0Fragment() (string, error) {
	const probe = "probe"
	t := keys.TenantID(probe)
	runPrefix, err := keys.S3TranscriptL0RunPrefix(t, probe, probe)
	if err != nil {
		return "", fmt.Errorf("snapshot: deriving the raw-transcript path fragment: %w", err)
	}
	// The alignment sidecar is the nearest neighbour that sits directly in the
	// per-capture prefix, so trimming its file name yields that prefix without this
	// file naming any part of the layout.
	align, err := keys.S3CaptureAlignment(t, probe)
	if err != nil {
		return "", fmt.Errorf("snapshot: deriving the per-capture prefix: %w", err)
	}
	capturePrefix := align[:strings.LastIndex(align, "/")+1]
	if capturePrefix == "" || !strings.HasPrefix(runPrefix, capturePrefix) {
		return "", fmt.Errorf("snapshot: cannot derive the raw-transcript path fragment from the key layout")
	}
	frag := strings.TrimPrefix(runPrefix, capturePrefix)
	frag = strings.TrimSuffix(frag, probe+"/")
	if frag == "" || strings.Contains(frag, probe) || !strings.HasSuffix(frag, "/") {
		return "", fmt.Errorf("snapshot: derived raw-transcript path fragment %q is not usable", frag)
	}
	return frag, nil
}

// snapshotIsL0Path reports whether a tenant-relative object path is a raw
// transcript, and therefore immutable (I1).
func snapshotIsL0Path(relPath, l0Fragment string) bool {
	return strings.Contains(relPath, "/"+l0Fragment)
}

// ---------------------------------------------------------------------------
// Attribute fidelity
// ---------------------------------------------------------------------------

// snapshotCheckRepresentable refuses an attribute value the archive format cannot
// round-trip.
//
// The one real case is binary: JSON would carry a []byte through as a base64
// string, so it would come back as a DynamoDB S where it was a B. No entity in
// §6.3 stores binary — the embedding matrix is an S3 object precisely because it is
// too large for an item — so this is a fail-closed guard on a shape that should
// never appear, not a limitation being worked around. If one ever does appear the
// format needs a version bump, and refusing is what makes that a decision rather
// than a silent corruption.
func snapshotCheckRepresentable(field string, v any) error {
	switch t := v.(type) {
	case nil, string, bool, int, int32, int64, float32, float64, json.Number:
		return nil
	case []byte:
		return fmt.Errorf("attribute %q is binary (%d bytes); the snapshot format stores JSON and would restore it as text, "+
			"and no entity in the data model stores binary (§6.3) — refusing rather than corrupting it", field, len(t))
	case []any:
		for i, e := range t {
			if err := snapshotCheckRepresentable(field+"["+strconv.Itoa(i)+"]", e); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for k, e := range t {
			if err := snapshotCheckRepresentable(field+"."+k, e); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("attribute %q has type %T, which the snapshot format does not carry", field, v)
	}
}

// snapshotNormalizeNumbers converts decoded json.Number values back to the Go types
// the repository layer stores.
//
// A whole number becomes int64 and anything else float64 — which is exactly the
// fidelity DynamoDB itself offers. repository/dynamodb.go records the same
// divergence in its own doc ("a whole float64 comes back as int64, because DynamoDB
// has one number type"), so a round-trip through a snapshot loses nothing that a
// round-trip through the table would have kept. Without this, every int64 would
// return as a float64 and a restored TTL or cost_micros would be stored as a
// non-integer — meter.go is explicit that money must stay an exact integer.
func snapshotNormalizeNumbers(v any) (any, error) {
	switch t := v.(type) {
	case json.Number:
		if i, err := strconv.ParseInt(t.String(), 10, 64); err == nil {
			return i, nil
		}
		f, err := strconv.ParseFloat(t.String(), 64)
		if err != nil {
			return nil, fmt.Errorf("value %q is not a number the store can hold", t.String())
		}
		return f, nil
	case []any:
		for i, e := range t {
			n, err := snapshotNormalizeNumbers(e)
			if err != nil {
				return nil, err
			}
			t[i] = n
		}
		return t, nil
	case map[string]any:
		for k, e := range t {
			n, err := snapshotNormalizeNumbers(e)
			if err != nil {
				return nil, err
			}
			t[k] = n
		}
		return t, nil
	default:
		return v, nil
	}
}

// ---------------------------------------------------------------------------
// Plan — the one description both --dry-run and --apply print
// ---------------------------------------------------------------------------

// Plan actions. A refusal action is any that starts with "refuse".
const (
	snapActionCopy          = "copy"           // backup: read from the store, write into the archive
	snapActionPut           = "put"            // restore: write into the target store
	snapActionSkipIdentical = "skip-identical" // already present with the same bytes
	snapActionSkipExists    = "skip-exists"    // already present and different; left alone
	snapActionRefuseL0      = "refuse-l0"      // present, differs, and immutable (I1)
	snapActionRefuseExists  = "refuse-exists"  // present and differs, under the refuse policy
	snapActionRefuseRef     = "refuse-ref"     // carries a source-tenant reference we cannot re-key (I11)
	snapActionRefuseAttr    = "refuse-attr"    // holds an attribute the archive format cannot round-trip
)

// snapshotPlanEntry is one line of the plan.
//
// The plan is computed once and printed identically in both modes; --apply then
// executes precisely these entries and nothing else. §11.5's central requirement is
// that "dry-run output is asserted to describe precisely what --apply then does",
// and the way to make that testable rather than aspirational is for there to be one
// plan structure, emitted verbatim under --json, that the test can diff between the
// two runs.
type snapshotPlanEntry struct {
	Kind   string `json:"kind"` // "item" or "object"
	Action string `json:"action"`
	Ref    string `json:"ref"` // sort key, or object path relative to the tenant prefix
	Bytes  int64  `json:"bytes,omitempty"`
	Note   string `json:"note,omitempty"`
}

func snapshotIsRefusal(action string) bool { return strings.HasPrefix(action, "refuse") }

// snapshotSummary is the counted form of the plan.
type snapshotSummary struct {
	Items        int   `json:"items"`
	ItemsWrite   int   `json:"items_write"`
	ItemsSkip    int   `json:"items_skip"`
	Objects      int   `json:"objects"`
	ObjectsWrite int   `json:"objects_write"`
	ObjectsSkip  int   `json:"objects_skip"`
	Bytes        int64 `json:"bytes"`
	Refusals     int   `json:"refusals"`
}

func snapshotSummarize(plan []snapshotPlanEntry) snapshotSummary {
	var s snapshotSummary
	for _, e := range plan {
		write := e.Action == snapActionCopy || e.Action == snapActionPut
		switch e.Kind {
		case "item":
			s.Items++
			if write {
				s.ItemsWrite++
			} else if !snapshotIsRefusal(e.Action) {
				s.ItemsSkip++
			}
		case "object":
			s.Objects++
			if write {
				s.ObjectsWrite++
				s.Bytes += e.Bytes
			} else if !snapshotIsRefusal(e.Action) {
				s.ObjectsSkip++
			}
		}
		if snapshotIsRefusal(e.Action) {
			s.Refusals++
		}
	}
	return s
}

// snapshotSortPlan orders the plan deterministically.
//
// Required, not cosmetic: an unordered plan cannot be diffed between a dry-run and
// an --apply, so the §11.5 assertion would be untestable. Items before objects,
// then by reference.
func snapshotSortPlan(plan []snapshotPlanEntry) {
	sort.SliceStable(plan, func(i, j int) bool {
		if plan[i].Kind != plan[j].Kind {
			return plan[i].Kind < plan[j].Kind
		}
		return plan[i].Ref < plan[j].Ref
	})
}

// snapshotResult is the --json document both commands emit.
type snapshotResult struct {
	Operation    string              `json:"operation"`
	Mode         string              `json:"mode"`
	Store        string              `json:"store"`
	SnapshotID   string              `json:"snapshot_id,omitempty"`
	SnapshotPath string              `json:"snapshot_path,omitempty"`
	SourceTenant string              `json:"source_tenant,omitempty"`
	TargetTenant string              `json:"target_tenant,omitempty"`
	Plan         []snapshotPlanEntry `json:"plan"`
	Summary      snapshotSummary     `json:"summary"`
	Refused      bool                `json:"refused"`
	// Applied is the count of entries actually executed, present only under
	// --apply. A test asserts it equals the plan's write counts, which is the other
	// half of "the dry-run does not lie".
	Applied *snapshotSummary `json:"applied,omitempty"`
	Notices []string         `json:"notices,omitempty"`
}

// snapshotModeName names the mode in output. Spelled the same in both commands so a
// caller can branch on it.
func snapshotModeName(apply bool) string {
	if apply {
		return "apply"
	}
	return "dry-run"
}

// snapshotRenderPlan prints the human-readable plan.
//
// To stdout, because it is the command's output rather than a diagnostic; --json
// suppresses it entirely so that stdout stays parseable (§11.3).
func snapshotRenderPlan(res *snapshotResult) {
	fmt.Printf("PLAN %s  mode=%s  store=%s\n", res.Operation, res.Mode, res.Store)
	if res.SnapshotPath != "" {
		fmt.Printf("     snapshot=%s  id=%s\n", res.SnapshotPath, res.SnapshotID)
	}
	if res.SourceTenant != "" {
		fmt.Printf("     source_tenant=%s\n", res.SourceTenant)
	}
	if res.TargetTenant != "" {
		fmt.Printf("     target_tenant=%s\n", res.TargetTenant)
	}
	for _, e := range res.Plan {
		line := fmt.Sprintf("  %-6s %-14s %s", e.Kind, e.Action, e.Ref)
		if e.Bytes > 0 {
			line += fmt.Sprintf("  (%d B)", e.Bytes)
		}
		if e.Note != "" {
			line += "  " + e.Note
		}
		fmt.Println(line)
	}
	s := res.Summary
	fmt.Printf("SUMMARY items=%d (write %d, skip %d)  objects=%d (write %d, skip %d)  bytes=%d  refusals=%d\n",
		s.Items, s.ItemsWrite, s.ItemsSkip, s.Objects, s.ObjectsWrite, s.ObjectsSkip, s.Bytes, s.Refusals)
}

// ---------------------------------------------------------------------------
// Cost estimate (§11.3)
// ---------------------------------------------------------------------------

// snapshotPrintCostEstimate prints the cost estimate §11.3 requires before an
// --apply.
//
// It prints quantities and dimensions, not dollars, and that is a deliberate
// choice rather than an omission. No AWS unit price exists in config (§7.4), and a
// price compiled into this binary is a number that goes stale silently — the same
// reasoning I5 applies to model names. The quantities are what bound the spend, and
// they are exact rather than estimated because they come from the plan.
//
// The clause about the daily spend breaker (§10.5.9) does not apply here and saying
// so is part of the estimate: no provider call is made, so there is no provider
// spend to check against the cap. What this operation spends is AWS request and
// transfer money.
func snapshotPrintCostEstimate(w io.Writer, s snapshotSummary, toLocalFilesystem bool) {
	fmt.Fprintf(w, "COST ESTIMATE (§11.3)\n")
	fmt.Fprintf(w, "  no provider call is made — no STT, LLM or embedding spend, so the daily spend\n")
	fmt.Fprintf(w, "  breaker (§10.5.9) has nothing to check and is not consulted.\n")
	fmt.Fprintf(w, "  AWS spend is bounded by these exact quantities:\n")
	fmt.Fprintf(w, "    object reads/writes   %d\n", s.ObjectsWrite)
	fmt.Fprintf(w, "    record reads/writes   %d\n", s.ItemsWrite)
	fmt.Fprintf(w, "    bytes transferred     %d\n", s.Bytes)
	if toLocalFilesystem {
		fmt.Fprintf(w, "    egress                %d bytes leave the region to this machine\n", s.Bytes)
	}
	fmt.Fprintf(w, "  No per-unit price is printed: none is in config, and a price compiled into this\n")
	fmt.Fprintf(w, "  binary is a number that goes stale without anyone noticing (I5's reasoning).\n")
}

// ---------------------------------------------------------------------------
// Archive I/O
// ---------------------------------------------------------------------------

func snapshotWriteJSON(pathname string, v any) error {
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("snapshot: encoding %s: %w", filepath.Base(pathname), err)
	}
	buf = append(buf, '\n')
	if err := os.MkdirAll(filepath.Dir(pathname), snapshotDirMode); err != nil {
		return fmt.Errorf("snapshot: creating directory for %s: %w", filepath.Base(pathname), err)
	}
	if err := os.WriteFile(pathname, buf, snapshotFileMode); err != nil {
		return fmt.Errorf("snapshot: writing %s: %w", filepath.Base(pathname), err)
	}
	return nil
}

// snapshotReadManifest reads and validates a manifest.
func snapshotReadManifest(dir string) (*snapshotManifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, snapshotManifestFile))
	if err != nil {
		return nil, fmt.Errorf("snapshot: %s is not a snapshot directory: %w", dir, err)
	}
	var m snapshotManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("snapshot: reading manifest: %w", err)
	}
	if m.FormatVersion != snapshotFormatVersion {
		return nil, fmt.Errorf("snapshot: manifest declares format version %d, this build reads %d; "+
			"refusing rather than partially interpreting an archive whose omissions it cannot see",
			m.FormatVersion, snapshotFormatVersion)
	}
	if m.SystemID != systemid.ID {
		return nil, fmt.Errorf("snapshot: manifest declares system %q, this build is %q", m.SystemID, systemid.ID)
	}
	if m.SourceTenant == "" {
		return nil, fmt.Errorf("snapshot: manifest records no source tenant; provenance is not optional")
	}
	return &m, nil
}

// snapshotHashFile returns a file's SHA-256, hex encoded.
func snapshotHashFile(pathname string) (string, int64, error) {
	f, err := os.Open(pathname)
	if err != nil {
		return "", 0, fmt.Errorf("snapshot: hashing %s: %w", filepath.Base(pathname), err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("snapshot: hashing %s: %w", filepath.Base(pathname), err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// snapshotHashReader hashes a stream, returning the hash and the byte count.
func snapshotHashReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// ---------------------------------------------------------------------------
// Audit (I13)
// ---------------------------------------------------------------------------

// snapshotAudit writes the audit record §11.3 requires of every data-script
// invocation, and returns an error if it could not.
//
// Three properties worth stating because each is a decision:
//
//   - **Written before the corpus is read, in BOTH modes.** audit.Record's contract
//     is "call it before the access, not after", and a dry-run is an access: it
//     lists objects and reads their bytes to compute the plan. §11.3 says every
//     invocation writes a record, not every mutating invocation. So a dry-run's
//     single write is this record, and the plan output says so.
//   - **A failure aborts the operation.** I13 does not permit this to be
//     best-effort; the same fail-closed rule the handlers follow.
//   - **The mode is carried in the action name**, because audit.Access is §6.3's
//     entity exactly and gaining a field for one caller's benefit would make the
//     record a shape audit.sh cannot query.
//
// It returns the keys it wrote, because backup must be able to exclude its own
// record from the archive — see the call site.
func snapshotAudit(ctx context.Context, corpus *snapshotCorpus, tenant keys.TenantID, action, actor, resource string) ([]keys.DynamoKey, error) {
	clk := clock.System{}
	// Diagnostics to stderr, never stdout: --json output on stdout must stay
	// parseable, which is the same reason every helper in scripts/lib/common.sh
	// writes to stderr.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// Zero ttlDays: the auditor applies retention.audit_days' documented default.
	// This command does not load an instance config — it holds no provider or model
	// decision, so there is nothing here for I5 to govern — and a wrong retention
	// on an audit record is not something to guess at from a flag.
	rec := &snapshotRecordingRepo{Repository: corpus.repo}
	auditor := audit.New(rec, clk, ids.NewGenerator(clk), logger, 0)
	if err := auditor.Allowed(ctx, audit.Access{
		Tenant:   tenant,
		Actor:    actor,
		Action:   action,
		Resource: resource,
	}); err != nil {
		return nil, fmt.Errorf("audit record could not be written, so the operation must not proceed (I13): %w", err)
	}
	if corpus.flush != nil {
		// Persist immediately. An audit record that only reaches disk if the whole
		// operation succeeds is not a record of an attempt, which is precisely what
		// I13 wants recorded — audit.Record's own doc prefers over-reporting an access
		// to missing one.
		if err := corpus.flush(); err != nil {
			return nil, fmt.Errorf("audit record could not be persisted (I13): %w", err)
		}
	}
	return rec.written, nil
}

// snapshotRecordingRepo reports which keys were written through it.
//
// Used for exactly one thing: learning the key of the audit record this invocation
// wrote, which audit.Auditor deliberately does not return (its API is "record an
// access", not "hand me a key"). Backup needs it in order to exclude its own record
// from the archive, and reading it back out of the partition afterwards would be a
// guess — the newest audit record is not necessarily ours.
type snapshotRecordingRepo struct {
	repository.Repository
	written []keys.DynamoKey
}

func (r *snapshotRecordingRepo) Put(ctx context.Context, item repository.Item) error {
	if err := r.Repository.Put(ctx, item); err != nil {
		return err
	}
	r.written = append(r.written, item.Key)
	return nil
}

func (r *snapshotRecordingRepo) PutOnce(ctx context.Context, item repository.Item) error {
	if err := r.Repository.PutOnce(ctx, item); err != nil {
		return err
	}
	r.written = append(r.written, item.Key)
	return nil
}

// snapshotActionName appends the dry-run marker to an action.
//
// A distinct action rather than a field, so audit.sh can filter planned from
// executed runs — and so a planned run is never mistaken for one that wrote data.
func snapshotActionName(base string, apply bool) string {
	if apply {
		return base
	}
	return base + ".plan"
}

// snapshotToolVersion records which build produced an archive, so a restore
// refusing on format grounds can say what wrote it.
func snapshotToolVersion() string { return version.Display() }
