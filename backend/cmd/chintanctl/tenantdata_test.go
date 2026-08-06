package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/audit"
	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/ids"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/kmsref"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Tests for §9.3 export and erasure.
//
// §11.5 sets the bar these are written against, and it is worth restating because the
// tests are otherwise easy to read as a formality:
//
//   - every mutating script has a test exercising BOTH --dry-run and --apply
//   - dry-run output is asserted to describe precisely what --apply then does
//   - destructive scripts are tested for refusal when --tenant is omitted
//   - no real AWS credentials anywhere
//
// The third of those is the one that decides whether a dry run is worth having at all: "a
// dry-run that lies is worse than no dry-run." So the assertions below compare the plan
// against what the stores actually contain afterwards, not the plan against itself.

// testTenant is the personal-phase tenant id. A plain slug: the point of these tests is the
// operation, not key validation, which keys_test covers.
const testTenant = keys.TenantID("personal")

// fixtureDir is the fixture set the bash harness also runs against.
//
// Shared deliberately. A Go test with its own private corpus and a harness test with
// another proves the operation twice over data neither shares, and the fixture format
// itself — which the harness depends on — would then be unexercised by anything that
// asserts on content.
const fixtureDir = "../../../scripts/test/fixtures/tenant-data"

// newTestTenantData builds a tenantData over an in-memory repository and a fixture object
// store, with no AWS anywhere.
func newTestTenantData(t *testing.T, repo repository.Repository, store objectStore) *tenantData {
	t.Helper()
	tenantKey, err := keys.Tenant(testTenant)
	if err != nil {
		t.Fatalf("keys.Tenant: %v", err)
	}
	prefix, err := keys.S3TenantPrefix(testTenant)
	if err != nil {
		t.Fatalf("keys.S3TenantPrefix: %v", err)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	gen := ids.NewGenerator(clock.System{})
	resolver, err := kmsref.New(repo, kmsref.Deployment{
		Bucket: kmsref.S3ServiceDefault(),
		Table:  kmsref.DynamoDBServiceDefault(),
	})
	if err != nil {
		t.Fatalf("kmsref.New: %v", err)
	}
	scope, err := resolver.ErasureScope(context.Background(), testTenant)
	if err != nil {
		t.Fatalf("kmsref ErasureScope: %v", err)
	}
	return &tenantData{
		tenant:   testTenant,
		pk:       tenantKey.PK,
		prefix:   prefix,
		table:    "voicenotes-test",
		repo:     repo,
		objects:  store,
		auditor:  audit.New(repo, clock.System{}, gen, log, 0),
		meter:    meter.New(repo, clock.System{}, gen, log, 0),
		scope:    scope,
		actor:    scriptActor(""),
		fixtures: true,
	}
}

// loadTestFixtures seeds both stores from the shared fixture set.
func loadTestFixtures(t *testing.T) (repository.Repository, objectStore) {
	t.Helper()
	repo, store, err := loadFixtures(fixtureDir, testTenant)
	if err != nil {
		t.Fatalf("loadFixtures: %v", err)
	}
	return repo, store
}

// captureReport redirects the report so a test can assert on what an operator reads, and
// restores it afterwards.
func captureReport(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := reportOut
	reportOut = buf
	t.Cleanup(func() { reportOut = prev })
	return buf
}

// decodeErase parses the --json document.
func decodeErase(t *testing.T, buf *bytes.Buffer) eraseResult {
	t.Helper()
	var res eraseResult
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("decoding erase result: %v\n%s", err, buf.String())
	}
	return res
}

func decodeExport(t *testing.T, buf *bytes.Buffer) exportResult {
	t.Helper()
	var res exportResult
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("decoding export result: %v\n%s", err, buf.String())
	}
	return res
}

// ---------------------------------------------------------------------------
// Fixture sanity
// ---------------------------------------------------------------------------

// TestFixtureSetIsRepresentative guards the shared fixture, because every assertion below
// is only as strong as the corpus it runs over.
//
// The three properties matter individually: a noncurrent version is what makes the G-021
// distinction observable, a delete marker is what makes "the prefix looks empty but is not"
// observable, and several record classes are what makes a class-by-class report meaningful.
// A fixture that lost any of them would leave the corresponding assertion passing
// vacuously.
func TestFixtureSetIsRepresentative(t *testing.T) {
	repo, store := loadTestFixtures(t)
	td := newTestTenantData(t, repo, store)
	inv, err := takeInventory(context.Background(), td)
	if err != nil {
		t.Fatalf("takeInventory: %v", err)
	}
	if inv.NoncurrentVersions == 0 {
		t.Error("fixture has no noncurrent object version; the versioning behaviour G-021 is about would go untested")
	}
	if inv.DeleteMarkers == 0 {
		t.Error("fixture has no delete marker; the tombstone case would go untested")
	}
	if len(inv.RecordClasses) < 5 {
		t.Errorf("fixture has %d record classes, want at least 5", len(inv.RecordClasses))
	}
	if inv.CurrentObjects == 0 || len(inv.Records) == 0 {
		t.Fatal("fixture is empty")
	}
}

// ---------------------------------------------------------------------------
// Export
// ---------------------------------------------------------------------------

// TestExportDryRunWritesNothing is the §11.3 convention itself: the default invocation
// prints a plan and touches nothing.
func TestExportDryRunWritesNothing(t *testing.T) {
	repo, store := loadTestFixtures(t)
	td := newTestTenantData(t, repo, store)
	out := filepath.Join(t.TempDir(), "archive")
	buf := captureReport(t)

	if err := exportTenant(context.Background(), td, out, false, true); err != nil {
		t.Fatalf("export dry run: %v", err)
	}
	res := decodeExport(t, buf)
	if !res.DryRun {
		t.Error("dry_run is false on the default invocation")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("dry run created the destination directory %s", out)
	}
}

// TestExportPlanMatchesApply is §11.5's central assertion for export: the plan a dry run
// prints is precisely what --apply then acts on.
//
// Asserted by comparing the plan documents and then by checking that every file the plan
// promised exists on disk with the promised checksum. Comparing the two plans alone would
// pass even if --apply wrote nothing at all.
func TestExportPlanMatchesApply(t *testing.T) {
	ctx := context.Background()

	dryRepo, dryStore := loadTestFixtures(t)
	dryBuf := captureReport(t)
	out := filepath.Join(t.TempDir(), "archive")
	if err := exportTenant(ctx, newTestTenantData(t, dryRepo, dryStore), out, false, true); err != nil {
		t.Fatalf("export dry run: %v", err)
	}
	dry := decodeExport(t, dryBuf)

	// A fresh fixture load, so the apply run sees the same state the dry run described
	// rather than one the dry run's own audit and metering writes have changed.
	applyRepo, applyStore := loadTestFixtures(t)
	applyBuf := captureReport(t)
	if err := exportTenant(ctx, newTestTenantData(t, applyRepo, applyStore), out, true, true); err != nil {
		t.Fatalf("export apply: %v", err)
	}
	apply := decodeExport(t, applyBuf)

	if d, a := mustJSON(t, dry.Plan), mustJSON(t, apply.Plan); d != a {
		t.Errorf("plan differs between dry run and --apply\n--- dry\n%s\n--- apply\n%s", d, a)
	}
	if dry.Files != apply.Files {
		t.Errorf("dry run said it would write %d files; --apply wrote %d", dry.Files, apply.Files)
	}
	if dry.Bytes != apply.Plan.CurrentBytes {
		t.Errorf("dry run said %d bytes; the plan's current bytes are %d", dry.Bytes, apply.Plan.CurrentBytes)
	}

	var m manifest
	raw, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parsing manifest: %v", err)
	}

	// Every record in the plan is in the archive. This is the completeness claim §9.3
	// makes, checked rather than assumed.
	planned := map[string]bool{}
	for _, r := range apply.Plan.Records {
		planned[r.SK] = true
	}
	for _, e := range m.Records {
		if !planned[e.SK] {
			t.Errorf("archive contains record %s that the plan did not list", e.SK)
		}
		delete(planned, e.SK)
	}
	for sk := range planned {
		t.Errorf("record %s was in the plan but not in the archive", sk)
	}

	// Every current object, and only current objects. A noncurrent version in the archive
	// would be a superseded copy presented as the tenant's content.
	current := map[string]bool{}
	for _, v := range apply.Plan.Objects {
		if v.IsLatest && !v.DeleteMarker {
			current[v.Key] = true
		}
	}
	for _, e := range m.Objects {
		if !current[e.Key] {
			t.Errorf("archive contains object %s which is not a current version", e.Key)
		}
		delete(current, e.Key)
	}
	for k := range current {
		t.Errorf("current object %s was in the plan but not in the archive", k)
	}

	// The checksums are the archive's verification story, so they have to be right.
	for _, e := range append(append([]manifestEntry{}, m.Records...), m.Objects...) {
		body, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(e.Path)))
		if err != nil {
			t.Errorf("manifest lists %s which is not in the archive: %v", e.Path, err)
			continue
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != e.SHA256 {
			t.Errorf("%s: manifest sha256 %s, file hashes to %s", e.Path, e.SHA256, got)
		}
		if int64(len(body)) != e.Bytes {
			t.Errorf("%s: manifest says %d bytes, file is %d", e.Path, e.Bytes, len(body))
		}
	}
}

// TestExportArchiveIsPortable asserts the parts that make an archive usable by someone who
// does not have this source code — which is what §9.3's "complete enough to migrate off the
// product entirely" requires beyond the bytes themselves.
func TestExportArchiveIsPortable(t *testing.T) {
	repo, store := loadTestFixtures(t)
	td := newTestTenantData(t, repo, store)
	out := filepath.Join(t.TempDir(), "archive")
	captureReport(t)
	if err := exportTenant(context.Background(), td, out, true, true); err != nil {
		t.Fatalf("export apply: %v", err)
	}

	readme, err := os.ReadFile(filepath.Join(out, "README.md"))
	if err != nil {
		t.Fatalf("archive has no README: %v", err)
	}
	for _, want := range []string{"manifest.json", "records/", "objects/", "block-id"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("README does not explain %q", want)
		}
	}

	var m manifest
	raw, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parsing manifest: %v", err)
	}
	if m.Format != archiveFormat {
		t.Errorf("manifest format %q, want %q", m.Format, archiveFormat)
	}
	if m.KMSKeyID == "" {
		t.Error("manifest does not record the key the source data was encrypted under (§9.3)")
	}
	if m.CryptoShreddable {
		t.Error("manifest claims the AWS-managed personal-phase key is crypto-shreddable (I8, G-021)")
	}
	// The archive must state its own limits: the caveat that exists only in the terminal
	// output of the run that produced it is a caveat nobody sees.
	joined := strings.Join(m.Notes, " ")
	if !strings.Contains(joined, "NOT encrypted") {
		t.Error("manifest notes do not warn that the archive is unencrypted (I8)")
	}

	// The archive is plaintext user content of the most sensitive kind (§9.2), so it must
	// not be group- or world-readable.
	info, err := os.Stat(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("archive file mode is %o; must not be group- or world-readable (§9.2)", perm)
	}
}

// TestExportRefusesToWriteIntoSomeoneElsesDirectory covers the mistake an operator makes
// once: exporting into a directory that already holds their own files.
func TestExportRefusesToWriteIntoSomeoneElsesDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "thesis.md"), []byte("mine"), 0o600); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if _, err := newDirSink(dir); err == nil {
		t.Fatal("newDirSink accepted a non-empty directory holding unrelated files")
	}
	// ...but re-exporting into a previous archive is allowed, because §Phase 7 requires
	// export to be idempotent and re-runnable.
	archive := t.TempDir()
	if err := os.WriteFile(filepath.Join(archive, "manifest.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if _, err := newDirSink(archive); err != nil {
		t.Errorf("newDirSink refused a re-export into a previous archive: %v", err)
	}
}

// TestArchiveObjectPathRefusesEscape covers the path-traversal boundary. An S3 key is not
// trustworthy input to a filesystem path: the archive is written with the operator's own
// credentials, so a key containing ".." would overwrite whatever it resolved to.
func TestArchiveObjectPathRefusesEscape(t *testing.T) {
	prefix := "tenants/personal/"
	for _, key := range []string{
		"tenants/personal/../../etc/passwd",
		"tenants/personal/a/../../../b",
		"tenants/other/audio/x.opus",
		"tenants/personal/",
		"tenants/personal/a\x00b",
	} {
		if got, err := archiveObjectPath(prefix, key); err == nil {
			t.Errorf("archiveObjectPath(%q) = %q, want refusal", key, got)
		}
	}
	got, err := archiveObjectPath(prefix, "tenants/personal/audio/c/segments/s.opus")
	if err != nil {
		t.Fatalf("archiveObjectPath refused a legitimate key: %v", err)
	}
	if got != "objects/audio/c/segments/s.opus" {
		t.Errorf("archiveObjectPath = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Erasure
// ---------------------------------------------------------------------------

// TestEraseDryRunDestroysNothing is the convention that matters most for agent safety
// (§11.3), on the most destructive operation in the system.
func TestEraseDryRunDestroysNothing(t *testing.T) {
	repo, store := loadTestFixtures(t)
	td := newTestTenantData(t, repo, store)
	buf := captureReport(t)

	before, err := takeInventory(context.Background(), td)
	if err != nil {
		t.Fatalf("takeInventory: %v", err)
	}
	if err := eraseTenant(context.Background(), td, false, true); err != nil {
		t.Fatalf("erase dry run: %v", err)
	}
	res := decodeErase(t, buf)
	if !res.DryRun {
		t.Error("dry_run is false on the default invocation")
	}
	if res.Removed != (removedCounts{}) {
		t.Errorf("dry run reported removals: %+v", res.Removed)
	}
	after, err := takeInventory(context.Background(), td)
	if err != nil {
		t.Fatalf("takeInventory: %v", err)
	}
	if len(after.Objects) != len(before.Objects) {
		t.Errorf("dry run changed the object store: %d versions before, %d after", len(before.Objects), len(after.Objects))
	}
	// The records grew by exactly the audit record and the metering record this invocation
	// wrote. Any other change means the dry run mutated user data.
	if got, want := len(after.Records), len(before.Records)+2; got != want {
		t.Errorf("records after a dry run = %d, want %d (the audit record and the metering record it writes)", got, want)
	}
}

// TestEraseAppliesExactlyThePlan is §11.5's assertion on the destructive path: --apply
// destroys the plan, the whole plan, and nothing beyond it.
func TestEraseAppliesExactlyThePlan(t *testing.T) {
	ctx := context.Background()

	dryRepo, dryStore := loadTestFixtures(t)
	dryBuf := captureReport(t)
	if err := eraseTenant(ctx, newTestTenantData(t, dryRepo, dryStore), false, true); err != nil {
		t.Fatalf("erase dry run: %v", err)
	}
	dry := decodeErase(t, dryBuf)

	applyRepo, applyStore := loadTestFixtures(t)
	td := newTestTenantData(t, applyRepo, applyStore)
	applyBuf := captureReport(t)
	if err := eraseTenant(ctx, td, true, true); err != nil {
		t.Fatalf("erase apply: %v", err)
	}
	apply := decodeErase(t, applyBuf)

	if d, a := mustJSON(t, dry.Plan), mustJSON(t, apply.Plan); d != a {
		t.Errorf("plan differs between dry run and --apply\n--- dry\n%s\n--- apply\n%s", d, a)
	}
	if apply.Removed.Records != len(apply.Plan.Records) {
		t.Errorf("removed %d records, plan listed %d", apply.Removed.Records, len(apply.Plan.Records))
	}
	if got, want := apply.Removed.ObjectVersions+apply.Removed.DeleteMarkers, len(apply.Plan.Objects); got != want {
		t.Errorf("removed %d object versions, plan listed %d", got, want)
	}
	if len(apply.Failures) != 0 {
		t.Errorf("failures: %+v", apply.Failures)
	}

	// What is actually left. Every planned record is gone; what remains is only what this
	// operation wrote after taking its inventory (I12, I13) — which the report is required
	// to state, and which is why erasure converges rather than emptying the partition.
	after, err := takeInventory(ctx, td)
	if err != nil {
		t.Fatalf("takeInventory: %v", err)
	}
	if len(after.Objects) != 0 {
		t.Errorf("%d object version(s) survived --apply: %+v", len(after.Objects), after.Objects)
	}
	plannedSKs := map[string]bool{}
	for _, r := range apply.Plan.Records {
		plannedSKs[r.SK] = true
	}
	for _, r := range after.Records {
		if plannedSKs[r.SK] {
			t.Errorf("record %s was in the plan and survived --apply", r.SK)
		}
	}
	if len(after.Records) != 2 {
		t.Errorf("%d record(s) remain, want exactly 2 (this run's audit record and metering record): %+v", len(after.Records), after.Records)
	}
}

// TestEraseIsIdempotent covers §9.3's "erasure is idempotent and reports what it removed".
//
// It also pins the fixed point, which is the part a reader would otherwise get wrong: the
// partition never becomes empty, because each run writes an attestation after taking its
// inventory. Converged means "the only thing left is the last run's own records", not
// "empty".
func TestEraseIsIdempotent(t *testing.T) {
	ctx := context.Background()
	repo, store := loadTestFixtures(t)
	td := newTestTenantData(t, repo, store)

	buf := captureReport(t)
	if err := eraseTenant(ctx, td, true, true); err != nil {
		t.Fatalf("first erase: %v", err)
	}
	first := decodeErase(t, buf)
	if first.Removed.Records == 0 {
		t.Fatal("first erase removed no records")
	}

	buf.Reset()
	if err := eraseTenant(ctx, td, true, true); err != nil {
		t.Fatalf("second erase: %v", err)
	}
	second := decodeErase(t, buf)
	if len(second.Plan.Objects) != 0 {
		t.Errorf("second run found %d object version(s); the first should have removed them all", len(second.Plan.Objects))
	}
	if got := len(second.Plan.Records); got != 2 {
		t.Errorf("second run planned %d records, want the 2 the first run wrote after its inventory", got)
	}

	buf.Reset()
	if err := eraseTenant(ctx, td, true, true); err != nil {
		t.Fatalf("third erase: %v", err)
	}
	third := decodeErase(t, buf)
	if got := len(third.Plan.Records); got != 2 {
		t.Errorf("third run planned %d records; the fixed point is 2 (the previous run's attestation and metering record)", got)
	}
}

// TestEraseWritesTheAuditRecordBeforeDeleting is the §9.3 ordering requirement, asserted the
// only way it can be: by making the audit write fail and checking that nothing was
// destroyed.
//
// A test that merely observed an audit record afterwards could not tell "before" from
// "after".
func TestEraseWritesTheAuditRecordBeforeDeleting(t *testing.T) {
	ctx := context.Background()
	base, store := loadTestFixtures(t)
	repo := &auditRefusingRepo{Repository: base}
	td := newTestTenantData(t, repo, store)
	captureReport(t)

	err := eraseTenant(ctx, td, true, true)
	if err == nil {
		t.Fatal("erase succeeded despite being unable to write its audit record (I13)")
	}
	if !strings.Contains(err.Error(), "audit") {
		t.Errorf("error does not name the audit failure: %v", err)
	}

	// Nothing may have been destroyed. This is what makes the audit record a precondition
	// rather than a formality.
	//
	// Read straight from the two stores rather than through takeInventory, so the assertion
	// still reports what it is about when the ordering regresses: an inventory needs the
	// tenant record, and a run that deleted before auditing has already removed it — which
	// would surface as an unresolvable key reference rather than as "data was destroyed".
	pk, err := keys.Tenant(testTenant)
	if err != nil {
		t.Fatalf("keys.Tenant: %v", err)
	}
	items, err := base.QueryPrefix(ctx, pk.PK, "", 0)
	if err != nil {
		t.Fatalf("reading the partition: %v", err)
	}
	prefix, err := keys.S3TenantPrefix(testTenant)
	if err != nil {
		t.Fatalf("keys.S3TenantPrefix: %v", err)
	}
	versions, err := store.ListVersions(ctx, prefix)
	if err != nil {
		t.Fatalf("listing objects: %v", err)
	}
	if len(items) == 0 {
		t.Error("every record was deleted even though the audit record could not be written (§9.3 requires the record BEFORE executing)")
	}
	if len(versions) == 0 {
		t.Error("every object version was deleted even though the audit record could not be written (§9.3)")
	}
}

// auditRefusingRepo fails the write-once path, which is how audit and usage records are
// written (repository.PutOnce). Everything else behaves normally.
type auditRefusingRepo struct {
	repository.Repository
}

func (r *auditRefusingRepo) PutOnce(context.Context, repository.Item) error {
	return errors.New("simulated write-once failure")
}

// TestEraseReportsPartialFailure covers the state an operator most needs to be told about:
// some deletions succeeded and some did not, so the erasure is incomplete and a re-run is
// required.
func TestEraseReportsPartialFailure(t *testing.T) {
	ctx := context.Background()
	base, store := loadTestFixtures(t)
	repo := &deleteRefusingRepo{Repository: base}
	td := newTestTenantData(t, repo, store)
	buf := captureReport(t)

	err := eraseTenant(ctx, td, true, true)
	if err == nil {
		t.Fatal("erase reported success with every record deletion failing")
	}
	res := decodeErase(t, buf)
	if res.OK {
		t.Error("ok is true in a run whose deletions failed")
	}
	if len(res.Failures) == 0 {
		t.Error("failures are not reported")
	}
	if res.Removed.Records != 0 {
		t.Errorf("reported %d records removed, but every deletion failed", res.Removed.Records)
	}
	// The objects went first, deliberately (§9.2 — audio is the most sensitive content), so
	// they are reported as removed even though the record pass failed.
	if res.Removed.ObjectVersions == 0 {
		t.Error("object versions are not reported as removed even though their deletion succeeded")
	}
}

// deleteRefusingRepo fails every Delete, modelling a principal without permission to erase
// — which is the expected outcome for the agent principal (§9.3, I17).
type deleteRefusingRepo struct {
	repository.Repository
}

func (r *deleteRefusingRepo) Delete(context.Context, keys.DynamoKey) error {
	return errors.New("AccessDeniedException: simulated")
}

// TestEraseReportDoesNotOverclaim is the honesty requirement (G-021), and the assertion
// this whole file exists for.
//
// In the personal phase there is no customer-managed key (I8), so crypto-shredding is
// unavailable and the report must say so — and must state what survives rather than
// reporting a completed erasure. The failure this guards against is not a missing feature;
// it is an accurate-sounding sentence.
func TestEraseReportDoesNotOverclaim(t *testing.T) {
	repo, store := loadTestFixtures(t)
	td := newTestTenantData(t, repo, store)
	buf := captureReport(t)

	if err := eraseTenant(context.Background(), td, false, true); err != nil {
		t.Fatalf("erase dry run: %v", err)
	}
	res := decodeErase(t, buf)

	if res.Shredding.Available {
		t.Error("report claims crypto-shredding is available under an AWS-managed key (I8, §9.3)")
	}
	if res.Shredding.KeyDestroyedByThis {
		t.Error("report claims this operation destroys the key; it does not call ScheduleKeyDeletion")
	}
	if res.Shredding.Caveat == "" {
		t.Error("report carries no erasure caveat; kmsref guarantees a non-empty one for every key kind")
	}
	// Quoted from kmsref rather than composed, so the claim and the classification cannot
	// drift apart.
	if want := td.scope.Caveat(); res.Shredding.Caveat != want {
		t.Errorf("caveat is not kmsref's:\n got %q\nwant %q", res.Shredding.Caveat, want)
	}

	survives := strings.Join(res.Survives, "\n")
	for _, want := range []string{
		"point-in-time recovery", // the DynamoDB window nothing here shortens
		"multipart",              // parts this operation does not enumerate
		"Telegram",               // a record no tenant-qualified query can reach
		"Cognito",                // the identity that outlives the data
		"idempotent",             // why re-running is required and safe
	} {
		if !strings.Contains(survives, want) {
			t.Errorf("the survival list does not mention %q — the report would overclaim by omission (G-021)", want)
		}
	}
}

// TestEraseHumanReportShowsWhatSurvives checks the operator-facing rendering, not just the
// JSON. The dry run is what an erasure decision is made from, so the survival section has
// to be in front of the person making it.
func TestEraseHumanReportShowsWhatSurvives(t *testing.T) {
	repo, store := loadTestFixtures(t)
	td := newTestTenantData(t, repo, store)
	buf := captureReport(t)

	if err := eraseTenant(context.Background(), td, false, false); err != nil {
		t.Fatalf("erase dry run: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"DRY RUN", "WOULD DESTROY", "WHAT SURVIVES THIS OPERATION", "--apply --confirm personal"} {
		if !strings.Contains(out, want) {
			t.Errorf("the human dry-run report does not contain %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "DESTROYED\n") {
		t.Error("the dry-run report uses the past tense for something it did not do")
	}
}

// ---------------------------------------------------------------------------
// Argument refusals (§11.3, I11)
// ---------------------------------------------------------------------------

// TestRefusesWithoutTenant is §11.5's "destructive scripts are tested for refusal when
// --tenant is omitted", asserted in the binary as well as in the wrapper: the wrapper is
// ergonomics, and a gate that lives only there is one that calling the binary walks past.
func TestRefusesWithoutTenant(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"erase", func() error { return erase(ctx, dataOpts{fixtures: fixtureDir}, "") }},
		{"export", func() error { return export(ctx, dataOpts{fixtures: fixtureDir}, "/tmp/does-not-matter") }},
	} {
		err := tc.run()
		if err == nil {
			t.Fatalf("%s ran with no --tenant (I11)", tc.name)
		}
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%s: error is %T, want a usage error so the exit code is 2", tc.name, err)
		}
		if !strings.Contains(err.Error(), "tenant") {
			t.Errorf("%s: refusal does not name --tenant: %v", tc.name, err)
		}
	}
}

// TestEraseApplyRequiresConfirmation covers the separate permissioning §9.3 requires, in the
// place it cannot be bypassed.
func TestEraseApplyRequiresConfirmation(t *testing.T) {
	ctx := context.Background()
	base := dataOpts{tenant: string(testTenant), fixtures: fixtureDir, apply: true, asJSON: true}
	captureReport(t)

	for _, confirm := range []string{"", "wrong", "PERSONAL"} {
		err := erase(ctx, base, confirm)
		if err == nil {
			t.Fatalf("erase --apply ran with --confirm %q", confirm)
		}
		var re *refusedError
		if !errors.As(err, &re) {
			t.Errorf("--confirm %q: error is %T, want a refusal so the exit code is 3", confirm, err)
		}
	}
	// The refusal must happen before anything is read, so a mistyped confirmation cannot
	// have side effects. Proven by passing a fixture path that does not exist: if the
	// stores were opened first, the error would be about the missing fixture.
	err := erase(ctx, dataOpts{tenant: "x", fixtures: "/nonexistent", apply: true}, "")
	if err == nil || !strings.Contains(err.Error(), "confirm") {
		t.Errorf("the confirmation is checked after the stores are opened: %v", err)
	}
}

// TestTenantWithoutKeyReferenceIsRefused covers the fail-closed path §6.3 requires: a tenant
// whose kms_key_id is absent cannot be erased or exported, because the report could not
// state what erasure reaches.
func TestTenantWithoutKeyReferenceIsRefused(t *testing.T) {
	ctx := context.Background()
	opts := dataOpts{tenant: string(testTenant), fixtures: "../../../scripts/test/fixtures/tenant-data-unprovisioned"}
	if err := erase(ctx, opts, ""); err == nil {
		t.Error("erase ran for a tenant with no kms_key_id (§6.3)")
	} else if !errors.Is(err, kmsref.ErrAbsent) {
		t.Errorf("error does not carry kmsref.ErrAbsent, so a repair script cannot branch on it: %v", err)
	}
	if err := export(ctx, opts, filepath.Join(t.TempDir(), "a")); err == nil {
		t.Error("export ran for a tenant with no kms_key_id (§6.3)")
	}
}

// TestFixturesAndLiveArgumentsAreMutuallyExclusive covers the mistake whose output is
// indistinguishable from a real run: passing both, and reading the fixture run's counts as
// though an account had been touched.
func TestFixturesAndLiveArgumentsAreMutuallyExclusive(t *testing.T) {
	err := erase(context.Background(), dataOpts{
		tenant:   string(testTenant),
		fixtures: fixtureDir,
		instance: "prod",
	}, "")
	if err == nil {
		t.Fatal("--fixtures and --instance were accepted together")
	}
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Errorf("error is %T, want a usage error", err)
	}
}

// ---------------------------------------------------------------------------
// Inventory
// ---------------------------------------------------------------------------

// TestRecordClassIsDerivedNotEnumerated pins the reason the class label is computed from the
// key: a fixed list of entity types would have to be edited in this file every phase, and
// the phase that forgot would produce a report that undercounts silently.
func TestRecordClassIsDerivedNotEnumerated(t *testing.T) {
	cases := map[string]string{
		"META":                  "META",
		"CAPTURE#c-1":           "CAPTURE",
		"CAPTURE#c-1#SEG#00001": "CAPTURE",
		"FUTUREKIND#x":          "FUTUREKIND",
	}
	for sk, want := range cases {
		if got := recordClass(sk); got != want {
			t.Errorf("recordClass(%q) = %q, want %q", sk, got, want)
		}
	}
}

// TestInventoryIsTenantScoped is the I11 assertion at the data layer, which §Phase 0
// acceptance requires directly and not only through the API: another tenant's records and
// objects must be invisible to this tenant's inventory.
func TestInventoryIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	repo, store := loadTestFixtures(t)

	// A second tenant, seeded through the same key helper.
	otherRepo, otherStore, err := loadFixtures(fixtureDir, keys.TenantID("someone-else"))
	if err != nil {
		t.Fatalf("loadFixtures: %v", err)
	}
	otherInv, err := takeInventory(ctx, newOtherTenantData(t, otherRepo, otherStore))
	if err != nil {
		t.Fatalf("takeInventory: %v", err)
	}
	// Merge the other tenant's records into this tenant's repository, so both partitions
	// live in one table exactly as they do in production (§6.3).
	for _, it := range otherInv.items {
		if err := repo.Put(ctx, it); err != nil {
			t.Fatalf("seeding the other tenant: %v", err)
		}
	}
	fs, ok := store.(*fixtureStore)
	if !ok {
		t.Fatalf("store is %T", store)
	}
	for _, v := range otherInv.Objects {
		fs.add(v, []byte("other tenant's bytes"))
	}

	inv, err := takeInventory(ctx, newTestTenantData(t, repo, store))
	if err != nil {
		t.Fatalf("takeInventory: %v", err)
	}
	if !strings.Contains(inv.PartitionKey, string(testTenant)) {
		t.Fatalf("partition key %q is not this tenant's", inv.PartitionKey)
	}
	if got := len(inv.Records); got != len(otherInv.Records) {
		t.Errorf("inventory has %d records; the other tenant's %d records leaked in or were lost", got, len(otherInv.Records))
	}
	for _, v := range inv.Objects {
		if !strings.HasPrefix(v.Key, inv.ObjectPrefix) {
			t.Errorf("object %q is outside this tenant's prefix %q (I11)", v.Key, inv.ObjectPrefix)
		}
	}
}

// newOtherTenantData builds a tenantData for a second tenant, for the cross-tenant test.
func newOtherTenantData(t *testing.T, repo repository.Repository, store objectStore) *tenantData {
	t.Helper()
	other := keys.TenantID("someone-else")
	key, err := keys.Tenant(other)
	if err != nil {
		t.Fatalf("keys.Tenant: %v", err)
	}
	prefix, err := keys.S3TenantPrefix(other)
	if err != nil {
		t.Fatalf("keys.S3TenantPrefix: %v", err)
	}
	return &tenantData{tenant: other, pk: key.PK, prefix: prefix, repo: repo, objects: store, fixtures: true}
}

// mustJSON renders a value for comparison and for a readable failure message.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	return string(b)
}
