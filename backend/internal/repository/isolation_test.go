package repository_test

// The cross-tenant isolation test.
//
// §Phase 0 acceptance names this test specifically: "Two test tenants cannot read each
// other's data, verified by an integration test that attempts it directly against the
// data layer, not only through the API." §9.1 restates why — "Integration tests attempt
// cross-tenant reads directly against the data layer, bypassing the API. **Passing only
// at the API layer is insufficient.**" An authorizer is one component; I11 is a property
// of every stored record and every query, and the only place that property can be
// demonstrated is here, below the handlers.
//
// Read the failure messages, not this comment, if a test in this file fails: each one
// names the invariant that broke and what it means for a user, because whoever reads it
// will be reading it under pressure.
//
// # Why this is an external test package
//
// `package repository_test`, not `package repository`, because the test provisions its
// two tenants through the *service layer* — meter, audit, consent, idem, kmsref — and
// those packages import repository. Hand-built items would test the test: the point is
// that the code paths production actually writes through put records where this test
// expects, so that a future writer which forgets its tenant is caught here rather than
// by a customer.
//
// # No key literal appears in this file
//
// scripts/checks/check-tenant-keys.sh fails the build if a key-prefix literal appears
// outside backend/internal/keys, and this file is not exempt — deliberately. Every
// partition key, sort key and prefix used below is derived from a keys constructor, so
// the test cannot drift from the key scheme it is asserting on. skFamily is how a
// sort-key prefix is obtained without naming one.
//
// # Coverage of §6.3, row by row
//
// Every entity in §6.3's table is attacked. Where a row is asserted depends on who
// builds its key:
//
//	Tenant, User, Capture, Item, Thread, Segment, Session, Ingest, Rule, Metric
//	                    → keyedEntities: the key is fully caller-determined, so both
//	                      tenants hold the *same* entity id and the keys then differ
//	                      only by tenant. Attacked in TestCrossTenantGet*.
//	Usage, Audit        → written by meter and audit under a generated ULID, so the
//	                      test cannot predict the key and works backwards instead:
//	                      TestUsageAndAuditCannotBeReadAcrossTenants discovers tenant
//	                      B's identifier and rebuilds the key for tenant A through the
//	                      same constructor. Also covered by the forged-key sweep and by
//	                      the service readers meter.MonthTotal / audit.Query.
//	Telegram link       → the one documented exception: its partition key is the
//	                      Telegram sender id, not the tenant. §6.3 and
//	                      keys.TelegramLink explain why; TestTelegramLink* asserts the
//	                      exception is bounded rather than a hole.
//
// Two stored record types the code adds beyond §6.3's table are covered too:
// Idempotency (keyedEntities — and the collision case is the reason it matters: two
// tenants routinely send the same client-generated key) and the consent event log
// (through consent's own reader, since its sort key is assembled inside that package).
//
// # One tenant id is a strict prefix of the other
//
// The fixture's two tenants are "…a" and "…ab", so tenant A's partition key and S3 prefix
// are strict prefixes of tenant B's. That is the shape a *prefix* comparison leaks on
// while an exact one does not, and it is not exotic: S3 has no exact match at all, only
// prefixes, which is what §9.1's IAM key-prefix conditions are written in. A test using
// two unrelated ids passes against a prefix comparison and proves less than it reads.
//
// # What running against the Memory fake does and does not prove
//
// The fake is exact where DynamoDB is exact — an exact-match partition key, sort-key
// ordering, the conditional write — so every assertion here is meaningful against it.
// What it cannot prove is that the *service* enforces the same partition semantics: that
// a Query on a partition key never returns a neighbouring partition, and that a
// forged sort key under the wrong partition really misses. Set CHINTAN_ISOLATION_TABLE
// to run the identical table against a live DynamoDB table (see liveBackend); with no
// such table the divergence is stated in the package report rather than assumed away.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/audit"
	"github.com/vppillai/chintan/backend/internal/awsclient"
	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/consent"
	"github.com/vppillai/chintan/backend/internal/idem"
	"github.com/vppillai/chintan/backend/internal/ids"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/kmsref"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// The two entity ids every tenant writes under.
//
// **Both tenants use the same id.** That is the whole design of this test: with the same
// id in both partitions, the two records' keys differ *only* by the tenant component, so
// any read path that drops, ignores, or truncates that component hands tenant A tenant
// B's record instead of returning nothing. A test using different ids per tenant would
// pass against such a path, because the wrong key would simply not exist.
//
// bOnlyID exists only in tenant B, so an attempt by A to name it must find nothing at
// all — the other half of the assertion, and the one that catches a read which falls
// back to a table-wide lookup when a scoped one misses.
const (
	sharedID = "iso_shared"
	bOnlyID  = "iso_bonly"
)

// The window every record in this test is stamped with. Fixed, because a usage record's
// month and a metric's date are part of their sort key, and a test that straddles
// midnight UTC would otherwise fail once a day.
var isoNow = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

const (
	isoMonth = "2026-08"
	isoDate  = "2026-08-04"
)

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// tenantFixture is one tenant's identity and the values that make its records
// recognisable.
type tenantFixture struct {
	id keys.TenantID

	// marker is a string that appears only in this tenant's records, in whatever
	// content-bearing field each entity has — a capture label, an item body, an audit
	// resource, a consent version. It is what turns "a read returned something" into
	// "a read returned *the other tenant's* content": every assertion below scans the
	// returned item, recursively and including nested maps and lists, for the other
	// tenant's marker.
	//
	// The two markers are deliberately not prefixes of one another, so a substring
	// scan cannot report a false positive in either direction.
	marker string

	// kms is the tenant's kms_key_id (§6.3, I8). The two tenants are given references
	// of *different kinds* so that a leaked resolve is visible in behaviour and not
	// only in a string: A's is AWS-managed and cannot be crypto-shredded, B's is
	// customer-managed and can. An erasure report that read the wrong one would claim
	// a completeness it does not have (§9.3, G-021).
	kms string

	// costMicros is this tenant's entire metered spend. Distinct per tenant so that a
	// leaked sum is arithmetically visible: the spend breaker (§10.5.9) reads this
	// figure, so A inheriting B's spend is a cap that closes early and B inheriting
	// A's is a cap that never closes.
	costMicros int64

	// purpose is the one consent purpose this tenant granted. The tenants grant
	// *different* purposes, so a cross-tenant consent read resolves to "granted" for a
	// purpose the user never agreed to — which under I14 is content retained without
	// consent.
	purpose consent.Purpose

	// tgUser is this tenant's Telegram sender id, for the §6.3 exception.
	tgUser string
}

// fixture is one backend with two provisioned tenants.
type fixture struct {
	name string
	repo repository.Repository
	clk  clock.Fixed
	log  *slog.Logger

	meter   *meter.Meter
	auditor *audit.Auditor
	consent *consent.Resolver
	idem    *idem.Store
	kms     *kmsref.Resolver

	// fingerprint is the same for both tenants, on purpose: two tenants sending the
	// same request body with the same Idempotency-Key is the collision idem.Request
	// documents as the reason its key is tenant-scoped.
	fingerprint string

	a tenantFixture
	b tenantFixture
}

// backend is one Repository implementation to run the whole table against.
type backend struct {
	name string
	open func(t *testing.T) repository.Repository
}

// backends returns every implementation available in this environment.
//
// The Memory fake always runs. The DynamoDB adapter runs only when a table is named,
// because §11.5 requires the check suite to run in CI with no AWS credentials — a test
// that reached for a real account by default would either fail the credential-free job
// or, worse, pass by silently writing somewhere.
func backends() []backend {
	out := []backend{{
		name: "memory",
		open: func(*testing.T) repository.Repository { return repository.NewMemory() },
	}}
	if b, ok := liveBackend(); ok {
		out = append(out, b)
	}
	return out
}

// liveBackend builds the DynamoDB adapter against a real table when CHINTAN_ISOLATION_TABLE
// names one.
//
// This is the escape hatch requirement 6 of the brief asks for: the table of assertions
// below is written against the Repository interface, so pointing it at the adapter is a
// change of one value and not a second copy of the test.
//
// It refuses a production table by name. The test writes records, and writing synthetic
// tenants into the table that holds the user's captures is a data-integrity event, not a
// test run — the refusal is here rather than in a README because a README does not stop
// an exported variable from being wrong.
func liveBackend() (backend, bool) {
	table := strings.TrimSpace(os.Getenv("CHINTAN_ISOLATION_TABLE"))
	if table == "" {
		return backend{}, false
	}
	return backend{
		name: "dynamodb:" + table,
		open: func(t *testing.T) repository.Repository {
			t.Helper()
			if strings.HasSuffix(table, "-prod") {
				t.Fatalf("refusing to run the isolation test against %q: it writes records, and the "+
					"prod table holds real captures. Point CHINTAN_ISOLATION_TABLE at the dev instance (§6.3).", table)
			}
			cli, err := awsclient.NewDynamoDB(context.Background())
			if err != nil {
				t.Fatalf("building a DynamoDB client for %q: %v", table, err)
			}
			repo, err := repository.NewDynamo(cli, table)
			if err != nil {
				t.Fatalf("building the DynamoDB adapter for %q: %v", table, err)
			}
			return repo
		},
	}, true
}

// forEachBackend runs one data-dependent test against every available implementation,
// each with its own freshly provisioned pair of tenants.
func forEachBackend(t *testing.T, fn func(t *testing.T, f *fixture)) {
	t.Helper()
	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			fn(t, newFixture(t, b))
		})
	}
}

// newFixture provisions two tenants with real-shaped data through the production
// writers.
func newFixture(t *testing.T, b backend) *fixture {
	t.Helper()

	repo := b.open(t)
	clk := clock.Fixed{T: isoNow}
	// Discarded rather than logging.New(): these packages log at debug on every write,
	// and a passing run must not bury the one message that matters when it fails.
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	gen := ids.NewGenerator(clk)

	kmsResolver, err := kmsref.New(repo, kmsref.Deployment{
		Bucket: kmsref.S3ServiceDefault(),
		Table:  kmsref.DynamoDBServiceDefault(),
	})
	if err != nil {
		t.Fatalf("kmsref.New: %v", err)
	}
	idemStore, err := idem.New(repo, clk, log, 24)
	if err != nil {
		t.Fatalf("idem.New: %v", err)
	}

	// Tenant ids where one is a **prefix of the other**.
	//
	// This is the adversarial choice in the fixture, and it is worth stating plainly: a
	// partition key or an S3 prefix compared with a prefix match rather than an exact
	// one lets a query for tenant A see tenant B. That is not hypothetical — S3 has no
	// exact match at all, only prefixes, which is why keys.S3TenantPrefix ends in a
	// separator (§9.1) and why TestS3PrefixesCannotOverlap exists. Unique per run so a
	// live table can be re-run without colliding with the previous run's records.
	base := keys.TenantID("t_iso_" + strings.ToLower(gen.NewID()))
	f := &fixture{
		name:        b.name,
		repo:        repo,
		clk:         clk,
		log:         log,
		meter:       meter.New(repo, clk, gen, log, 25),
		auditor:     audit.New(repo, clk, gen, log, 2555),
		consent:     consent.New(repo, clk, log),
		idem:        idemStore,
		kms:         kmsResolver,
		fingerprint: idem.Fingerprint("POST", "/v1/captures", []byte(`{"label":"isolation"}`)),
		a: tenantFixture{
			id:         base,
			marker:     "MRKA_" + gen.NewID(),
			kms:        kmsref.AWSManagedS3,
			costMicros: 1234,
			purpose:    consent.PurposeModelImprovement,
			tgUser:     "tg_iso_a",
		},
		b: tenantFixture{
			// A's id plus a character: B's partition key therefore has A's as a strict
			// prefix.
			id:         base + "b",
			marker:     "MRKB_" + gen.NewID(),
			kms:        "alias/chintan-iso-tenant-b",
			costMicros: 7654321,
			purpose:    consent.PurposeCorpusRetention,
			tgUser:     "tg_iso_b",
		},
	}

	ctx := context.Background()
	for _, tf := range []tenantFixture{f.a, f.b} {
		// Tenant first: consent and kmsref both read the tenant record, and consent
		// refuses to append to a log for a tenant that does not exist.
		for _, e := range keyedEntities() {
			if _, err := e.write(f, tf, sharedID); err != nil {
				t.Fatalf("provisioning %s for tenant %s: %v", e.name, tf.id, err)
			}
		}
		f.writeServiceRecords(t, tf)
		f.writeTelegramLink(t, tf)
	}
	// B-only records: an id tenant A has no record under at all.
	for _, e := range keyedEntities() {
		if !e.idInKey {
			continue
		}
		if _, err := e.write(f, f.b, bOnlyID); err != nil {
			t.Fatalf("provisioning B-only %s: %v", e.name, err)
		}
	}

	// Litter removal for a live table. Only the records this run wrote, found by
	// sweeping the four partitions it touched — synthetic tenants with no user content,
	// on a non-prod instance. Skipped for the fake, which is discarded with the test.
	if _, live := liveBackend(); live && f.name != "memory" {
		t.Cleanup(func() { f.cleanup(t) })
	}

	// A fixture that wrote nothing would make every "returns nothing" assertion below
	// pass vacuously, which is the failure mode a negative test has and a positive one
	// does not.
	for _, tf := range []tenantFixture{f.a, f.b} {
		items, err := f.repo.QueryPrefix(ctx, f.pk(t, tf), "", 0)
		if err != nil {
			t.Fatalf("sweeping tenant %s: %v", tf.id, err)
		}
		if len(items) < len(keyedEntities()) {
			t.Fatalf("fixture wrote only %d records for tenant %s but there are %d entity types; "+
				"every negative assertion in this file would pass vacuously against an empty partition",
				len(items), tf.id, len(keyedEntities()))
		}
	}
	return f
}

// pk returns a tenant's partition key, via the one constructor that builds one.
func (f *fixture) pk(t *testing.T, tf tenantFixture) string {
	t.Helper()
	k, err := keys.Tenant(tf.id)
	if err != nil {
		t.Fatalf("keys.Tenant(%q): %v", tf.id, err)
	}
	return k.PK
}

// other returns the tenant that is not tf — the one whose data must never be reachable.
func (f *fixture) other(tf tenantFixture) tenantFixture {
	if tf.id == f.a.id {
		return f.b
	}
	return f.a
}

// cleanup deletes every record in the partitions this run touched.
func (f *fixture) cleanup(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pks := []string{f.pk(t, f.a), f.pk(t, f.b)}
	for _, tf := range []tenantFixture{f.a, f.b} {
		k, err := keys.TelegramLink(tf.tgUser)
		if err != nil {
			t.Errorf("keys.TelegramLink: %v", err)
			continue
		}
		pks = append(pks, k.PK)
	}
	for _, pk := range pks {
		items, err := f.repo.QueryPrefix(ctx, pk, "", 0)
		if err != nil {
			t.Errorf("cleanup: sweeping %s: %v", pk, err)
			continue
		}
		for _, it := range items {
			if err := f.repo.Delete(ctx, it.Key); err != nil {
				t.Errorf("cleanup: deleting %s / %s: %v", it.Key.PK, it.Key.SK, err)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The entity table
// ---------------------------------------------------------------------------

// entity is one §6.3 row whose key this test can construct itself.
type entity struct {
	// name is the §6.3 entity name, used verbatim in failure messages so the row is
	// findable in the spec.
	name string

	// idInKey reports whether the entity id participates in the key. False only for
	// Tenant, whose sort key is a constant — so there is no "B-only" tenant record to
	// attempt, and the shared-id case is the whole of it.
	idInKey bool

	key   func(tenant keys.TenantID, id string) (keys.DynamoKey, error)
	write func(f *fixture, tf tenantFixture, id string) (keys.DynamoKey, error)
}

// shape describes the entity's key by example, for a failure message that has to be read
// next to §6.3's table.
//
// Built from the constructor with placeholder identifiers rather than written out as a
// string. Two reasons, and the first is the interesting one: a literal key prefix in this
// file would fail check-tenant-keys.sh, which does not exempt tests — deliberately, since
// a test is exactly where a hand-built key gets normalised. The second is that an example
// produced by the constructor cannot drift from what the constructor produces.
func (e entity) shape() string {
	k, err := e.key("tenant_id", "id")
	if err != nil {
		return "(key shape unavailable: " + err.Error() + ")"
	}
	return k.PK + " / " + k.SK
}

// keyedEntities is every §6.3 entity whose key is fully determined by its caller, plus
// the idempotency record.
//
// The attribute sets are §6.3's key attributes with the tenant's marker in the
// content-bearing field — a label, a body, a title. They are not the complete attribute
// set for each entity: that fidelity belongs to the package that owns each write, and
// what this test needs is a record shaped like the real thing whose provenance is
// visible in what comes back.
func keyedEntities() []entity {
	return []entity{
		{
			name: "Tenant", idInKey: false,
			key: func(tn keys.TenantID, _ string) (keys.DynamoKey, error) { return keys.Tenant(tn) },
			write: func(f *fixture, tf tenantFixture, _ string) (keys.DynamoKey, error) {
				key, err := keys.Tenant(tf.id)
				if err != nil {
					return key, err
				}
				// No `consent` attribute: consent.loadLog treats one as a tripwire for
				// out-of-band mutation (I16), and the append-only log is authoritative.
				// kms_key_id is never absent (§6.3, I8), and the attribute name comes
				// from kmsref so provisioning and the resolver cannot drift.
				return key, f.repo.Put(context.Background(), repository.Item{
					Key: key,
					Attrs: map[string]any{
						"plan":                          "personal",
						"region":                        "ca-central-1",
						kmsref.TenantAttrKMSKeyID:       tf.kms,
						kmsref.TenantAttrCreatedAt:      clock.RFC3339UTC(f.clk.Now()),
						kmsref.TenantAttrKMSKeyIDSince:  clock.RFC3339UTC(f.clk.Now()),
						"status":                        "active",
						"isolation_marker_for_the_test": tf.marker,
					},
				})
			},
		},
		{
			name: "User", idInKey: true,
			key: keys.User,
			write: func(f *fixture, tf tenantFixture, id string) (keys.DynamoKey, error) {
				key, err := keys.User(tf.id, id)
				if err != nil {
					return key, err
				}
				return key, f.repo.Put(context.Background(), repository.Item{
					Key: key,
					Attrs: map[string]any{
						// Not an email: §9.2 keeps PII out of long-retained records, and
						// the marker is what identifies the tenant here anyway.
						"role":       "owner",
						"created_at": clock.RFC3339UTC(f.clk.Now()),
						"settings":   map[string]any{"display_name": tf.marker},
					},
				})
			},
		},
		{
			name: "Capture", idInKey: true,
			key: keys.Capture,
			write: func(f *fixture, tf tenantFixture, id string) (keys.DynamoKey, error) {
				key, err := keys.Capture(tf.id, id)
				if err != nil {
					return key, err
				}
				now := clock.RFC3339UTC(f.clk.Now())
				gsiPK, gsiSK, err := keys.GSI1(tf.id, now)
				if err != nil {
					return key, err
				}
				prefix, err := keys.S3TenantPrefix(tf.id)
				if err != nil {
					return key, err
				}
				c := model.Capture{
					CaptureID: id, OwnerUserID: sharedID, SessionID: "s_" + id,
					Label: tf.marker, CreatedAt: now, UpdatedAt: now,
					S3Prefix: prefix, ActiveL0Run: "whisper-" + id,
					IngestSource: model.IngestApp,
				}
				return key, f.repo.Put(context.Background(), repository.Item{
					Key: key,
					Attrs: map[string]any{
						"owner_user_id": c.OwnerUserID,
						"session_id":    c.SessionID,
						"label":         c.Label,
						"created_at":    c.CreatedAt,
						"updated_at":    c.UpdatedAt,
						"s3_prefix":     c.S3Prefix,
						"active_l0_run": c.ActiveL0Run,
						"ingest_source": string(c.IngestSource),
					},
					// Capture and Thread are the only records that project into GSI1
					// (§6.3), and GSI1PK is itself tenant-scoped — so the index is not a
					// way around the partition. TestGSI1IsTenantPartitioned asserts it.
					GSI1PK: gsiPK, GSI1SK: gsiSK,
				})
			},
		},
		{
			name: "Item", idInKey: true,
			key: keys.Item,
			write: func(f *fixture, tf tenantFixture, id string) (keys.DynamoKey, error) {
				key, err := keys.Item(tf.id, id)
				if err != nil {
					return key, err
				}
				now := clock.RFC3339UTC(f.clk.Now())
				it := model.Item{
					ItemID: id, CaptureID: sharedID, Kind: model.KindAction,
					Text: tf.marker, SourceBlocks: []string{"t-0001"},
					Confidence: 0.91, Status: model.StatusInbox,
					CreatedAt: now, UpdatedAt: now,
				}
				return key, f.repo.Put(context.Background(), repository.Item{
					Key: key,
					Attrs: map[string]any{
						"capture_id":    it.CaptureID,
						"kind":          string(it.Kind),
						"text":          it.Text,
						"source_blocks": it.SourceBlocks,
						"confidence":    it.Confidence,
						"status":        string(it.Status),
						"created_at":    it.CreatedAt,
						"updated_at":    it.UpdatedAt,
					},
				})
			},
		},
		{
			name: "Thread", idInKey: true,
			key: keys.Thread,
			write: func(f *fixture, tf tenantFixture, id string) (keys.DynamoKey, error) {
				key, err := keys.Thread(tf.id, id)
				if err != nil {
					return key, err
				}
				now := clock.RFC3339UTC(f.clk.Now())
				gsiPK, gsiSK, err := keys.GSI1(tf.id, now)
				if err != nil {
					return key, err
				}
				th := model.Thread{
					ThreadID: id, Title: tf.marker, Summary: tf.marker,
					KindMix:   map[model.ItemKind]int{model.KindAction: 1},
					ItemCount: 1, CreatedAt: now, UpdatedAt: now,
				}
				return key, f.repo.Put(context.Background(), repository.Item{
					Key: key,
					Attrs: map[string]any{
						"title":      th.Title,
						"summary":    th.Summary,
						"kind_mix":   th.KindMix,
						"item_count": th.ItemCount,
						"created_at": th.CreatedAt,
						"updated_at": th.UpdatedAt,
					},
					GSI1PK: gsiPK, GSI1SK: gsiSK,
				})
			},
		},
		{
			name: "Segment", idInKey: true,
			key: func(tn keys.TenantID, id string) (keys.DynamoKey, error) { return keys.Segment(tn, id, 0) },
			write: func(f *fixture, tf tenantFixture, id string) (keys.DynamoKey, error) {
				key, err := keys.Segment(tf.id, id, 0)
				if err != nil {
					return key, err
				}
				audioKey, err := keys.S3AudioSegment(tf.id, id, "seg_000")
				if err != nil {
					return key, err
				}
				l0, err := keys.S3TranscriptL0(tf.id, id, "whisper-"+id, "seg_000")
				if err != nil {
					return key, err
				}
				seg := model.Segment{
					CaptureID: id, Seq: 0, BlockID: "t-0001", AudioKey: audioKey,
					WallStartMS: 4120, DurMS: 7660, GapBeforeMS: 0,
					// The tenant is in the S3 key, which is what makes this attribute
					// tenant-identifying without a marker: an L0 key from the wrong
					// partition would point a transcript reader at another tenant's
					// prefix, which IAM denies and which is the leak I11 prevents.
					L0Keys: map[string]string{"whisper-" + id: l0},
				}
				return key, f.repo.Put(context.Background(), repository.Item{
					Key: key,
					Attrs: map[string]any{
						"block_id":      seg.BlockID,
						"audio_key":     seg.AudioKey,
						"wall_start_ms": seg.WallStartMS,
						"dur_ms":        seg.DurMS,
						"l0_keys":       seg.L0Keys,
						"label":         tf.marker,
					},
				})
			},
		},
		{
			name: "Session", idInKey: true,
			key: func(tn keys.TenantID, id string) (keys.DynamoKey, error) {
				return keys.Session(tn, id, "s_"+id)
			},
			write: func(f *fixture, tf tenantFixture, id string) (keys.DynamoKey, error) {
				key, err := keys.Session(tf.id, id, "s_"+id)
				if err != nil {
					return key, err
				}
				now := clock.RFC3339UTC(f.clk.Now())
				s := model.Session{
					SessionID: "s_" + id, CaptureID: id,
					TriggerSource: model.TriggerUI, IngestSource: model.IngestApp,
					ContentHash: "0011deadbeef", ResolvedTS: now, StartedAt: now,
					Device: tf.marker, MicLabel: tf.marker,
				}
				return key, f.repo.Put(context.Background(), repository.Item{
					Key: key,
					Attrs: map[string]any{
						"trigger_source": string(s.TriggerSource),
						"ingest_source":  string(s.IngestSource),
						"content_hash":   s.ContentHash,
						"resolved_ts":    s.ResolvedTS,
						"ts_derived":     s.TSDerived,
						"started_at":     s.StartedAt,
						"device":         s.Device,
						"mic_label":      s.MicLabel,
					},
				})
			},
		},
		{
			name: "Ingest", idInKey: true,
			key: keys.Ingest,
			write: func(f *fixture, tf tenantFixture, id string) (keys.DynamoKey, error) {
				key, err := keys.Ingest(tf.id, id)
				if err != nil {
					return key, err
				}
				return key, f.repo.Put(context.Background(), repository.Item{
					Key: key,
					Attrs: map[string]any{
						"capture_ids": []string{tf.marker},
						"source":      string(model.IngestDeviceImport),
						"imported_at": clock.RFC3339UTC(f.clk.Now()),
						"bytes":       int64(4096),
					},
				})
			},
		},
		{
			name: "Rule", idInKey: true,
			key: keys.Rule,
			write: func(f *fixture, tf tenantFixture, id string) (keys.DynamoKey, error) {
				key, err := keys.Rule(tf.id, id)
				if err != nil {
					return key, err
				}
				return key, f.repo.Put(context.Background(), repository.Item{
					Key: key,
					Attrs: map[string]any{
						// A correction rule carries the user's own vocabulary, which is
						// user content: leaking a rule set leaks what someone talks about.
						"canonical": tf.marker,
						"variants":  []string{tf.marker},
						"hits":      int64(3),
						"last_seen": clock.RFC3339UTC(f.clk.Now()),
					},
				})
			},
		},
		{
			name: "Metric", idInKey: true,
			key: func(tn keys.TenantID, id string) (keys.DynamoKey, error) {
				return keys.Metric(tn, isoDate, id)
			},
			write: func(f *fixture, tf tenantFixture, id string) (keys.DynamoKey, error) {
				key, err := keys.Metric(tf.id, isoDate, id)
				if err != nil {
					return key, err
				}
				return key, f.repo.Put(context.Background(), repository.Item{
					Key: key,
					Attrs: map[string]any{
						"value":              float64(tf.costMicros),
						"n":                  int64(1),
						"unit":               "micros",
						"definition_version": "1",
						"release_tag":        tf.marker,
					},
				})
			},
		},
		{
			// Not a §6.3 row, but a stored record, and the one where a missing tenant
			// scope has a documented consequence: "an unscoped key would let one
			// tenant's retry return another tenant's resource identifier"
			// (idem.Request). Both tenants below use the same key and the same request
			// fingerprint, which is what a client sending "1" produces.
			name: "Idempotency", idInKey: true,
			key: keys.Idempotency,
			write: func(f *fixture, tf tenantFixture, id string) (keys.DynamoKey, error) {
				key, err := keys.Idempotency(tf.id, id)
				if err != nil {
					return key, err
				}
				req := idem.Request{Tenant: tf.id, Key: id, Fingerprint: f.fingerprint}
				res, err := f.idem.Begin(context.Background(), req)
				if err != nil {
					return key, fmt.Errorf("idem.Begin: %w", err)
				}
				if res.State != idem.StateNew {
					// Both tenants claiming the same key must both get StateNew. If the
					// second gets StateInFlight the keys collided, which is the leak.
					return key, fmt.Errorf("idem.Begin for tenant %s returned %s, not %s: the key is not tenant-scoped (I11)",
						tf.id, res.State, idem.StateNew)
				}
				err = f.idem.Complete(context.Background(), req, res.Token,
					idem.Outcome{Status: 201, Resource: tf.marker})
				return key, err
			},
		},
	}
}

// writeServiceRecords writes the entities whose keys are generated inside their own
// package: Usage (meter), Audit (audit), and the consent log.
func (f *fixture) writeServiceRecords(t *testing.T, tf tenantFixture) {
	t.Helper()
	ctx := context.Background()

	if err := f.meter.Record(ctx, meter.Event{
		Tenant: tf.id, Unit: model.UnitRequests, Quantity: 1,
		Provider: "isolation-" + tf.marker, Op: "isolation.probe",
		CostMicros: tf.costMicros,
	}); err != nil {
		t.Fatalf("meter.Record for %s: %v", tf.id, err)
	}

	if err := f.auditor.Allowed(ctx, audit.Access{
		Tenant: tf.id, Actor: "script:isolation-test",
		Action: "capture.read", Resource: tf.marker,
	}); err != nil {
		t.Fatalf("audit.Allowed for %s: %v", tf.id, err)
	}

	// Each tenant grants a *different* purpose, and the version is the marker: §Phase 4
	// selects records to purge by consent version, so a version read from the wrong
	// tenant's log would purge the wrong tenant's corpus.
	if _, err := f.consent.Grant(ctx, tf.id, tf.purpose, tf.marker); err != nil {
		t.Fatalf("consent.Grant(%s, %s): %v", tf.id, tf.purpose, err)
	}
}

// writeTelegramLink writes the §6.3 exception: the record whose partition key is the
// Telegram sender id rather than the tenant.
func (f *fixture) writeTelegramLink(t *testing.T, tf tenantFixture) {
	t.Helper()
	key, err := keys.TelegramLink(tf.tgUser)
	if err != nil {
		t.Fatalf("keys.TelegramLink(%q): %v", tf.tgUser, err)
	}
	// PutOnce, not Put. §6.3's link resolves *to* a tenant, so a second link under the
	// same sender pointing at a different tenant would make the resolution ambiguous —
	// and an ambiguous resolution is a Telegram message filed into someone else's
	// account. The conditional write is what makes "exactly one tenant" structural.
	if err := f.repo.PutOnce(context.Background(), repository.Item{
		Key: key,
		Attrs: map[string]any{
			// Exactly §6.3's three attributes and nothing else. This record lives
			// outside every tenant partition, so anything stored here is outside I11's
			// protection — see TestTelegramLinkHoldsNoUserContent.
			"tenant_id": string(tf.id),
			"user_id":   sharedID,
			"linked_at": clock.RFC3339UTC(f.clk.Now()),
		},
	}); err != nil {
		t.Fatalf("writing the Telegram link for %s: %v", tf.id, err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// skFamily returns the entity-family prefix of a sort key: everything up to and
// including the first delimiter.
//
// Derived rather than written down, because a literal prefix in this file would be a
// second copy of the key scheme (and check-tenant-keys.sh would fail the build for it).
// For a sort key that is a bare constant — the tenant record's, the link's — the whole
// key is its own family.
func skFamily(sk string) string {
	if i := strings.IndexByte(sk, '#'); i >= 0 {
		return sk[:i+1]
	}
	return sk
}

// mentions reports whether a marker appears anywhere in a value, however nested.
//
// Recursive on purpose. The two Repository implementations disagree about the Go type of
// a stored collection — DynamoDB returns []any and map[string]any where the fake may
// return the type that was written — so a scan that only looked at top-level strings
// would miss a leaked capture id inside l0_keys, which is exactly where an S3 key
// carrying another tenant's prefix would hide.
func mentions(v any, marker string) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return strings.Contains(t, marker)
	case map[string]any:
		for k, e := range t {
			if strings.Contains(k, marker) || mentions(e, marker) {
				return true
			}
		}
		return false
	case []any:
		for _, e := range t {
			if mentions(e, marker) {
				return true
			}
		}
		return false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return strings.Contains(rv.String(), marker)
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			if mentions(rv.Index(i).Interface(), marker) {
				return true
			}
		}
		return false
	case reflect.Map:
		for _, k := range rv.MapKeys() {
			if strings.Contains(fmt.Sprint(k.Interface()), marker) ||
				mentions(rv.MapIndex(k).Interface(), marker) {
				return true
			}
		}
		return false
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return false
		}
		return mentions(rv.Elem().Interface(), marker)
	}
	return strings.Contains(fmt.Sprint(v), marker)
}

// itemMentions scans a whole stored item — key, index attributes, and every attribute
// value — for a marker.
func itemMentions(it repository.Item, marker string) bool {
	if strings.Contains(it.Key.PK, marker) || strings.Contains(it.Key.SK, marker) ||
		strings.Contains(it.GSI1PK, marker) || strings.Contains(it.GSI1SK, marker) {
		return true
	}
	for k, v := range it.Attrs {
		if strings.Contains(k, marker) || mentions(v, marker) {
			return true
		}
	}
	return false
}

// assertNotOtherTenants fails with the message this file exists to print.
func assertNotOtherTenants(t *testing.T, f *fixture, reader tenantFixture, what string, it repository.Item) {
	t.Helper()
	victim := f.other(reader)
	ownPK := f.pk(t, reader)

	if it.Key.PK != ownPK {
		t.Fatalf("I11 VIOLATED (%s): reading %s as tenant %s returned a record from partition %q, "+
			"not %q. One tenant is reading another's records directly at the data layer. "+
			"Cross-tenant leakage is the one bug a multi-tenant product cannot survive (§3 I11, §9.1) — "+
			"treat this as a disclosure, not a red test.",
			f.name, what, reader.id, it.Key.PK, ownPK)
	}
	if itemMentions(it, victim.marker) {
		t.Fatalf("I11 VIOLATED (%s): reading %s as tenant %s returned content belonging to tenant %s "+
			"(its marker %q appears in the record, key %s / %s). The partition key looked right, so the "+
			"leak is in what was stored, not in what was queried — a writer put one tenant's content "+
			"into another's record (§3 I11, §6.3).",
			f.name, what, reader.id, victim.id, victim.marker, it.Key.PK, it.Key.SK)
	}
}

// TestMarkerScanDetectsContentWhereverItHides guards the detector, not the system.
//
// Every "did this read return the other tenant's data" assertion in this file rests on
// mentions/itemMentions returning true. A detector that silently returned false would
// make the most important test in the codebase pass unconditionally — the same vacuity
// §0.5A demands every check be demonstrated red against. The nested cases are the ones
// that matter: a leaked S3 key hides inside l0_keys, and the two Repository
// implementations disagree about the Go type a stored collection comes back as.
func TestMarkerScanDetectsContentWhereverItHides(t *testing.T) {
	const marker = "MRKB_LEAK"

	positives := map[string]any{
		"plain string":      "prefix " + marker + " suffix",
		"typed string":      model.ItemKind(marker),
		"[]any (adapter)":   []any{"a", marker},
		"[]string (fake)":   []string{"a", marker},
		"map[string]any":    map[string]any{"k": marker},
		"map value typed":   map[string]string{"k": marker},
		"map key":           map[string]any{marker: "v"},
		"nested two deep":   map[string]any{"outer": []any{map[string]any{"inner": marker}}},
		"pointer":           &[]string{marker}[0],
		"int-keyed map":     map[int]string{1: marker},
		"struct via Sprint": struct{ Field string }{Field: marker},
	}
	for name, v := range positives {
		if !mentions(v, marker) {
			t.Errorf("mentions missed the marker in a %s; every cross-tenant content assertion in "+
				"this file would pass vacuously against that shape", name)
		}
	}

	negatives := map[string]any{
		"nil":           nil,
		"other string":  "MRKA_SOMETHINGELSE",
		"empty map":     map[string]any{},
		"nil slice":     []string(nil),
		"number":        int64(42),
		"nested no hit": map[string]any{"outer": []any{map[string]any{"inner": "clean"}}},
	}
	for name, v := range negatives {
		if mentions(v, marker) {
			t.Errorf("mentions reported a marker in a %s that does not contain one; a detector that "+
				"fires on clean data makes a real leak indistinguishable from noise", name)
		}
	}

	// And at the item level: key, index attributes, and attribute names all count,
	// because a leaked identifier appears in a key before it appears in a body.
	for name, it := range map[string]repository.Item{
		"sort key":       {Key: keys.DynamoKey{PK: "p", SK: marker}},
		"GSI1SK":         {Key: keys.DynamoKey{PK: "p", SK: "s"}, GSI1SK: marker},
		"attribute name": {Key: keys.DynamoKey{PK: "p", SK: "s"}, Attrs: map[string]any{marker: 1}},
		"nested attr": {Key: keys.DynamoKey{PK: "p", SK: "s"},
			Attrs: map[string]any{"l0_keys": map[string]string{"run": "some/path/" + marker + "/x"}}},
	} {
		if !itemMentions(it, marker) {
			t.Errorf("itemMentions missed the marker in the %s of an item", name)
		}
	}
}

// ---------------------------------------------------------------------------
// 1. Keys: no unscoped key exists
// ---------------------------------------------------------------------------

// TestKeyConstructorsRefuseAnUnscopedTenant asserts every key constructor fails closed.
//
// This is the runtime half of check-tenant-keys.sh. The static check proves no key is
// built outside the helper; this proves the helper itself cannot be talked into an
// unscoped key. The consequence of a missing check is not a wrong record but a *shared*
// one: an empty tenant collapses every tenant into one partition key, and an empty S3
// tenant prefix is a prefix of every tenant's objects, which is precisely what the IAM
// conditions in §9.1 are written against.
func TestKeyConstructorsRefuseAnUnscopedTenant(t *testing.T) {
	unusable := map[string]keys.TenantID{
		"empty":      "",
		"whitespace": "   ",
		"delimiter":  "t#a",
		"path":       "t/a",
		"wildcard":   "*",
	}

	dynamo := map[string]func(keys.TenantID) (keys.DynamoKey, error){
		"Tenant":      keys.Tenant,
		"User":        func(tn keys.TenantID) (keys.DynamoKey, error) { return keys.User(tn, sharedID) },
		"Capture":     func(tn keys.TenantID) (keys.DynamoKey, error) { return keys.Capture(tn, sharedID) },
		"Item":        func(tn keys.TenantID) (keys.DynamoKey, error) { return keys.Item(tn, sharedID) },
		"Thread":      func(tn keys.TenantID) (keys.DynamoKey, error) { return keys.Thread(tn, sharedID) },
		"Segment":     func(tn keys.TenantID) (keys.DynamoKey, error) { return keys.Segment(tn, sharedID, 0) },
		"Session":     func(tn keys.TenantID) (keys.DynamoKey, error) { return keys.Session(tn, sharedID, sharedID) },
		"Ingest":      func(tn keys.TenantID) (keys.DynamoKey, error) { return keys.Ingest(tn, sharedID) },
		"Rule":        func(tn keys.TenantID) (keys.DynamoKey, error) { return keys.Rule(tn, sharedID) },
		"Usage":       func(tn keys.TenantID) (keys.DynamoKey, error) { return keys.Usage(tn, isoMonth, "requests", sharedID) },
		"Audit":       func(tn keys.TenantID) (keys.DynamoKey, error) { return keys.Audit(tn, sharedID) },
		"Metric":      func(tn keys.TenantID) (keys.DynamoKey, error) { return keys.Metric(tn, isoDate, sharedID) },
		"Idempotency": func(tn keys.TenantID) (keys.DynamoKey, error) { return keys.Idempotency(tn, sharedID) },
	}
	s3 := map[string]func(keys.TenantID) (string, error){
		"S3TenantPrefix":    keys.S3TenantPrefix,
		"S3AudioSegment":    func(tn keys.TenantID) (string, error) { return keys.S3AudioSegment(tn, sharedID, "seg_000") },
		"S3AudioContinuous": func(tn keys.TenantID) (string, error) { return keys.S3AudioContinuous(tn, sharedID, sharedID) },
		"S3CaptureContent":  func(tn keys.TenantID) (string, error) { return keys.S3CaptureContent(tn, sharedID) },
		"S3CaptureAlignment": func(tn keys.TenantID) (string, error) {
			return keys.S3CaptureAlignment(tn, sharedID)
		},
		"S3TranscriptL0": func(tn keys.TenantID) (string, error) {
			return keys.S3TranscriptL0(tn, sharedID, "whisper-x", "seg_000")
		},
		"S3TranscriptL1":     func(tn keys.TenantID) (string, error) { return keys.S3TranscriptL1(tn, sharedID, "1") },
		"S3ItemText":         func(tn keys.TenantID) (string, error) { return keys.S3ItemText(tn, sharedID) },
		"S3EmbeddingsMatrix": keys.S3EmbeddingsMatrix,
		"S3EmbeddingsMeta":   keys.S3EmbeddingsMeta,
	}

	for label, tn := range unusable {
		for name, ctor := range dynamo {
			if k, err := ctor(tn); err == nil {
				t.Errorf("I11 VIOLATED: keys.%s accepted a %s tenant and returned %q / %q. "+
					"Every stored record must be tenant-scoped; a key built without a usable tenant "+
					"puts records where any tenant's query can reach them (§3 I11).",
					name, label, k.PK, k.SK)
			}
		}
		for name, ctor := range s3 {
			if got, err := ctor(tn); err == nil {
				t.Errorf("I11 VIOLATED: keys.%s accepted a %s tenant and returned %q. "+
					"IAM conditions restrict S3 by this prefix (§9.1), so an unscoped prefix is a "+
					"policy that matches every tenant's objects.", name, label, got)
			}
		}
	}

	// GSI1 is a read path too, and a sparse index with an unscoped partition key would
	// list every tenant's captures in one page (§6.3).
	for label, tn := range unusable {
		if pk, sk, err := keys.GSI1(tn, clock.RFC3339UTC(isoNow)); err == nil {
			t.Errorf("I11 VIOLATED: keys.GSI1 accepted a %s tenant and returned %q / %q; the "+
				"time-ordered listing index must be tenant-partitioned (§6.3).", label, pk, sk)
		}
	}
}

// TestEveryEntityKeyIsInItsOwnTenantPartition asserts the mapping from tenant to
// partition is total and injective across every entity type.
//
// Injective is the load-bearing half: if two tenants could ever produce the same
// partition key, every other assertion in this file is unenforceable no matter how the
// repository behaves.
func TestEveryEntityKeyIsInItsOwnTenantPartition(t *testing.T) {
	a := keys.TenantID("t_iso_a")
	// b has a as a strict prefix — see newFixture.
	b := keys.TenantID("t_iso_ab")

	pkA, err := keys.Tenant(a)
	if err != nil {
		t.Fatalf("keys.Tenant: %v", err)
	}
	pkB, err := keys.Tenant(b)
	if err != nil {
		t.Fatalf("keys.Tenant: %v", err)
	}
	if pkA.PK == pkB.PK {
		t.Fatalf("I11 VIOLATED: tenants %q and %q share partition key %q; every record of both "+
			"tenants is then in one partition and no query can separate them (§6.3).", a, b, pkA.PK)
	}

	for _, e := range keyedEntities() {
		ka, err := e.key(a, sharedID)
		if err != nil {
			t.Fatalf("%s key for %s: %v", e.name, a, err)
		}
		kb, err := e.key(b, sharedID)
		if err != nil {
			t.Fatalf("%s key for %s: %v", e.name, b, err)
		}
		if ka.PK != pkA.PK || kb.PK != pkB.PK {
			t.Errorf("I11 VIOLATED: %s (%s) is not in its tenant's partition: got %q and %q, "+
				"want %q and %q. A record outside the tenant partition is a record no tenant-scoped "+
				"query filters (§6.3, I11).", e.name, e.shape(), ka.PK, kb.PK, pkA.PK, pkB.PK)
		}
		if ka == kb {
			t.Errorf("I11 VIOLATED: %s (%s) produces the identical key %q / %q for tenants %q and %q, "+
				"so one tenant's write overwrites the other's record and either tenant's read returns "+
				"whichever wrote last (§3 I11).", e.name, e.shape(), ka.PK, ka.SK, a, b)
		}
		// Same id, so the sort keys are expected to match exactly: the tenant is the
		// only thing separating these two records. If that ever stops being true the
		// test below is weaker than it reads, so assert it rather than assume it.
		if e.idInKey && ka.SK != kb.SK {
			t.Errorf("%s: sort keys differ between tenants (%q vs %q) for the same entity id; the "+
				"cross-tenant read test relies on the tenant being the only difference between them",
				e.name, ka.SK, kb.SK)
		}
	}
}

// TestGSI1IsTenantPartitioned asserts the sparse listing index cannot span tenants.
func TestGSI1IsTenantPartitioned(t *testing.T) {
	a, b := keys.TenantID("t_iso_a"), keys.TenantID("t_iso_ab")
	ts := clock.RFC3339UTC(isoNow)

	pkA, skA, err := keys.GSI1(a, ts)
	if err != nil {
		t.Fatalf("keys.GSI1: %v", err)
	}
	pkB, skB, err := keys.GSI1(b, ts)
	if err != nil {
		t.Fatalf("keys.GSI1: %v", err)
	}
	if pkA == pkB {
		t.Fatalf("I11 VIOLATED: GSI1PK is %q for both tenants; the capture and thread listing would "+
			"return both tenants' records in one page (§6.3).", pkA)
	}
	if skA != skB {
		t.Errorf("GSI1SK differs between tenants for the same timestamp (%q vs %q); the index sorts by "+
			"time within a tenant, so the tenant belongs in the partition key only (§6.3)", skA, skB)
	}
	tenantPK, err := keys.Tenant(a)
	if err != nil {
		t.Fatalf("keys.Tenant: %v", err)
	}
	if pkA != tenantPK.PK {
		t.Errorf("GSI1PK %q is not the tenant partition key %q; §6.3 defines GSI1PK as the tenant "+
			"partition, and a different shape means the index is scoped by something else", pkA, tenantPK.PK)
	}
}

// TestS3PrefixesCannotOverlap covers the other half of the data layer.
//
// §9.1 enforces per-tenant S3 isolation "in IAM policy conditions, not only in
// application logic", and an IAM StringLike condition on a prefix is a *prefix* match.
// With tenant ids where one is a prefix of the other — the pair this test uses — a
// missing trailing separator makes tenant A's policy match every one of tenant B's
// objects. That is a live misconfiguration, not a theoretical one: it is why
// S3TenantPrefix ends in a separator.
func TestS3PrefixesCannotOverlap(t *testing.T) {
	a, b := keys.TenantID("t_iso_a"), keys.TenantID("t_iso_ab")

	pa, err := keys.S3TenantPrefix(a)
	if err != nil {
		t.Fatalf("keys.S3TenantPrefix: %v", err)
	}
	pb, err := keys.S3TenantPrefix(b)
	if err != nil {
		t.Fatalf("keys.S3TenantPrefix: %v", err)
	}
	if strings.HasPrefix(pb, pa) || strings.HasPrefix(pa, pb) {
		t.Fatalf("I11 VIOLATED: tenant prefixes %q and %q are prefixes of one another. An IAM "+
			"condition granting one tenant its prefix then grants it the other tenant's objects — "+
			"audio and transcripts, the most sensitive content this system holds (§9.1, §9.2).", pa, pb)
	}

	objects := map[string]func(keys.TenantID) (string, error){
		"audio segment":    func(tn keys.TenantID) (string, error) { return keys.S3AudioSegment(tn, sharedID, "seg_000") },
		"audio continuous": func(tn keys.TenantID) (string, error) { return keys.S3AudioContinuous(tn, sharedID, sharedID) },
		"L2 content":       func(tn keys.TenantID) (string, error) { return keys.S3CaptureContent(tn, sharedID) },
		"alignment":        func(tn keys.TenantID) (string, error) { return keys.S3CaptureAlignment(tn, sharedID) },
		"L0 transcript": func(tn keys.TenantID) (string, error) {
			return keys.S3TranscriptL0(tn, sharedID, "whisper-x", "seg_000")
		},
		"L1 transcript":     func(tn keys.TenantID) (string, error) { return keys.S3TranscriptL1(tn, sharedID, "1") },
		"item overflow":     func(tn keys.TenantID) (string, error) { return keys.S3ItemText(tn, sharedID) },
		"embeddings matrix": keys.S3EmbeddingsMatrix,
		"embeddings meta":   keys.S3EmbeddingsMeta,
	}
	for name, ctor := range objects {
		ka, err := ctor(a)
		if err != nil {
			t.Fatalf("%s for %s: %v", name, a, err)
		}
		kb, err := ctor(b)
		if err != nil {
			t.Fatalf("%s for %s: %v", name, b, err)
		}
		if !strings.HasPrefix(ka, pa) || !strings.HasPrefix(kb, pb) {
			t.Errorf("I11 VIOLATED: the %s key is not under its tenant's prefix (%q under %q, %q under %q); "+
				"an object outside the prefix is outside the IAM condition that isolates it (§6.2, §9.1)",
				name, ka, pa, kb, pb)
		}
		if strings.HasPrefix(ka, pb) || strings.HasPrefix(kb, pa) {
			t.Errorf("I11 VIOLATED: the %s key of one tenant falls under the other's prefix (%q, %q); "+
				"the S3 half of tenant isolation is a prefix comparison, so this is a readable object "+
				"across the boundary (§9.1)", name, ka, kb)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Direct cross-tenant reads against the Repository
// ---------------------------------------------------------------------------

// TestCrossTenantGetReturnsOwnRecordOrNothing is the assertion §Phase 0 acceptance names.
//
// For every entity type it makes three attempts as tenant A:
//
//  1. the shared id, which exists in both partitions — must return A's own record,
//     never B's, even though the two keys differ only by tenant;
//  2. the B-only id — must return nothing, with no fallback to a wider lookup;
//  3. the same pair from B's side, because an asymmetric leak is still a leak (and the
//     tenant ids are chosen so that A's is a strict prefix of B's, which is the
//     direction a prefix comparison breaks).
func TestCrossTenantGetReturnsOwnRecordOrNothing(t *testing.T) {
	forEachBackend(t, func(t *testing.T, f *fixture) {
		ctx := context.Background()
		for _, e := range keyedEntities() {
			t.Run(e.name, func(t *testing.T) {
				for _, reader := range []tenantFixture{f.a, f.b} {
					key, err := e.key(reader.id, sharedID)
					if err != nil {
						t.Fatalf("%s key: %v", e.name, err)
					}
					got, err := f.repo.Get(ctx, key)
					if err != nil {
						t.Fatalf("%s (%s): reading tenant %s's own record failed: %v — a fixture that "+
							"stored nothing makes every negative assertion here vacuous",
							e.name, e.shape(), reader.id, err)
					}
					assertNotOtherTenants(t, f, reader, e.name+" by its own key", *got)
					if !itemMentions(*got, reader.marker) && e.name != "Idempotency" {
						t.Errorf("%s (%s): tenant %s's record carries neither tenant's marker, so this "+
							"test cannot tell whose record it is. Fix the fixture before trusting the "+
							"assertion.", e.name, e.shape(), reader.id)
					}
				}

				if !e.idInKey {
					return
				}
				// The B-only id, named from A's tenant. There is no argument A can pass
				// that reaches B's record: the id is the same, the tenant is not.
				key, err := e.key(f.a.id, bOnlyID)
				if err != nil {
					t.Fatalf("%s key: %v", e.name, err)
				}
				got, err := f.repo.Get(ctx, key)
				if !errors.Is(err, repository.ErrNotFound) {
					// The key of what came back, not its attributes: an error message is a
					// thing that gets logged, and against a live table the record on the
					// other side of a real leak is user content (§9.2).
					reached := "no record"
					if got != nil {
						reached = fmt.Sprintf("the record at %s / %s", got.Key.PK, got.Key.SK)
					}
					t.Fatalf("I11 VIOLATED (%s): %s (%s) exists only for tenant %s, yet a Get scoped to "+
						"tenant %s reached %s (err %v) instead of ErrNotFound. A scoped read that misses "+
						"must return nothing — §9.1 requires the handler above it to answer 404, and a "+
						"read that falls back to a wider lookup makes that impossible.",
						f.name, e.name, e.shape(), f.b.id, f.a.id, reached, err)
				}
				// And the record really is there for B, so the miss above was the tenant
				// scope and not a typo in the id.
				bKey, err := e.key(f.b.id, bOnlyID)
				if err != nil {
					t.Fatalf("%s key: %v", e.name, err)
				}
				if _, err := f.repo.Get(ctx, bKey); err != nil {
					t.Fatalf("%s: the B-only record is missing for tenant %s (%v), so the ErrNotFound "+
						"above proves nothing", e.name, f.b.id, err)
				}
			})
		}
	})
}

// TestUsageAndAuditCannotBeReadAcrossTenants completes the §6.3 inventory.
//
// These are the two rows whose keys this test cannot predict — meter and audit generate a
// ULID inside the write — so the typed attempt has to work backwards: discover the
// identifier of tenant B's record, then rebuild the key for tenant A through the *same
// constructor* and assert it reaches nothing. That is the realistic leak, too: a usage or
// audit identifier is the kind of value that travels in a report, a support ticket, or a
// URL, so "I have the id" is not a far-fetched premise.
//
// Both entities are write-once (§6.3) and the audit log is the longest-retained store in
// the system (§9.2), so a cross-tenant read here discloses one tenant's spend and one
// tenant's access history — the two records a compliance conversation is about.
func TestUsageAndAuditCannotBeReadAcrossTenants(t *testing.T) {
	forEachBackend(t, func(t *testing.T, f *fixture) {
		ctx := context.Background()

		// One partition — tenant B's — for both entity types; UsageMonthPrefix is simply
		// the only constructor that hands back a sort-key prefix directly.
		bPK, usagePrefix, err := keys.UsageMonthPrefix(f.b.id, isoMonth)
		if err != nil {
			t.Fatalf("keys.UsageMonthPrefix: %v", err)
		}
		auditSample, err := keys.Audit(f.b.id, "sample")
		if err != nil {
			t.Fatalf("keys.Audit: %v", err)
		}

		cases := []struct {
			name string
			// prefix selects tenant B's records of this type.
			prefix string
			// rebuild names the same record in tenant A, through the constructor.
			rebuild func(sk string) (keys.DynamoKey, error)
		}{
			{
				name: "Usage", prefix: usagePrefix,
				rebuild: func(sk string) (keys.DynamoKey, error) {
					// SK is prefix + month + unit + ulid; the trailing two fields are what
					// the constructor needs back. Split rather than a literal, so this
					// stays derived from what keys.Usage produced.
					parts := strings.Split(sk, "#")
					if len(parts) < 4 {
						return keys.DynamoKey{}, fmt.Errorf("usage sort key %q has %d fields, want 4", sk, len(parts))
					}
					return keys.Usage(f.a.id, isoMonth, parts[len(parts)-2], parts[len(parts)-1])
				},
			},
			{
				name: "Audit", prefix: skFamily(auditSample.SK),
				rebuild: func(sk string) (keys.DynamoKey, error) {
					parts := strings.Split(sk, "#")
					return keys.Audit(f.a.id, parts[len(parts)-1])
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				items, err := f.repo.QueryPrefix(ctx, bPK, tc.prefix, 0)
				if err != nil {
					t.Fatalf("sweeping tenant %s for %s records: %v", f.b.id, tc.name, err)
				}
				if len(items) == 0 {
					t.Fatalf("tenant %s holds no %s records, so this assertion is vacuous", f.b.id, tc.name)
				}
				for _, it := range items {
					// Tenant B's record is readable by tenant B — the premise.
					if _, err := f.repo.Get(ctx, it.Key); err != nil {
						t.Fatalf("%s: tenant %s cannot read its own record %s: %v", tc.name, f.b.id, it.Key.SK, err)
					}
					aKey, err := tc.rebuild(it.Key.SK)
					if err != nil {
						t.Fatalf("%s: rebuilding %q for tenant %s: %v", tc.name, it.Key.SK, f.a.id, err)
					}
					if aKey.SK != it.Key.SK {
						t.Fatalf("%s: rebuilding %q produced sort key %q; the two tenants' records must "+
							"differ only by partition for this attempt to mean anything",
							tc.name, it.Key.SK, aKey.SK)
					}
					if _, err := f.repo.Get(ctx, aKey); !errors.Is(err, repository.ErrNotFound) {
						t.Fatalf("I11 VIOLATED (%s): tenant %s reached tenant %s's %s record %s "+
							"(err %v) — holding the identifier was enough. Both entity types are "+
							"write-once and the audit log is the longest-retained store in the system, "+
							"so this discloses another tenant's spend and access history (§3 I11, §6.3, §9.2).",
							f.name, f.a.id, f.b.id, tc.name, it.Key.SK, err)
					}
				}
			})
		}
	})
}

// TestForgedKeyCannotReachAnotherTenant sweeps every record tenant B holds and attempts
// each one from tenant A's partition.
//
// This is the adversary's move, and deliberately the one production code is forbidden to
// make: assembling a keys.DynamoKey by hand out of A's partition key and a sort key
// observed elsewhere. check-tenant-keys.sh stops that being written in the backend; this
// test does it anyway, because "no key literal in the source" is a static property and
// the question here is what the *storage layer* does when the attempt is made.
//
// It also covers the two §6.3 entities whose keys this test cannot predict — Usage and
// Audit are written under a generated ULID — and the consent log, whose sort key is
// assembled inside the consent package. Sweeping needs no prediction.
func TestForgedKeyCannotReachAnotherTenant(t *testing.T) {
	forEachBackend(t, func(t *testing.T, f *fixture) {
		ctx := context.Background()
		for _, reader := range []tenantFixture{f.a, f.b} {
			victim := f.other(reader)
			victimItems, err := f.repo.QueryPrefix(ctx, f.pk(t, victim), "", 0)
			if err != nil {
				t.Fatalf("sweeping tenant %s: %v", victim.id, err)
			}
			if len(victimItems) == 0 {
				t.Fatalf("tenant %s holds no records, so forging keys against it proves nothing", victim.id)
			}
			for _, vi := range victimItems {
				forged := keys.DynamoKey{PK: f.pk(t, reader), SK: vi.Key.SK}
				got, err := f.repo.Get(ctx, forged)
				switch {
				case errors.Is(err, repository.ErrNotFound):
					// The sort key exists only in the victim's partition. Correct.
				case err != nil:
					t.Errorf("forging %q into tenant %s's partition failed with an unexpected error: %v",
						vi.Key.SK, reader.id, err)
				default:
					// A record came back. It must be the reader's own — both tenants hold
					// the same entity ids, so most sort keys exist in both partitions.
					assertNotOtherTenants(t, f, reader, fmt.Sprintf("a forged key %q", vi.Key.SK), *got)
				}
			}
		}
	})
}

// TestQueryPrefixCannotCrossPartitions covers requirement 4: the range read.
func TestQueryPrefixCannotCrossPartitions(t *testing.T) {
	forEachBackend(t, func(t *testing.T, f *fixture) {
		ctx := context.Background()

		for _, reader := range []tenantFixture{f.a, f.b} {
			ownPK := f.pk(t, reader)

			// The widest read the interface can express: one partition, no prefix.
			all, err := f.repo.QueryPrefix(ctx, ownPK, "", 0)
			if err != nil {
				t.Fatalf("sweeping tenant %s: %v", reader.id, err)
			}
			for _, it := range all {
				assertNotOtherTenants(t, f, reader, "the whole partition", it)
			}

			// Per entity family, derived from the reader's own keys so no prefix literal
			// appears here.
			for _, e := range keyedEntities() {
				k, err := e.key(reader.id, sharedID)
				if err != nil {
					t.Fatalf("%s key: %v", e.name, err)
				}
				fam := skFamily(k.SK)
				items, err := f.repo.QueryPrefix(ctx, ownPK, fam, 0)
				if err != nil {
					t.Fatalf("querying %s for tenant %s: %v", e.name, reader.id, err)
				}
				if len(items) == 0 {
					t.Fatalf("%s: querying prefix %q in tenant %s returned nothing, so the isolation "+
						"assertion below is vacuous", e.name, fam, reader.id)
				}
				for _, it := range items {
					assertNotOtherTenants(t, f, reader, e.name+" family query", it)
				}
			}

		}

		// A sort-key prefix that exists only in tenant B's partition returns nothing from
		// tenant A's — the query is bounded by the partition, not by what matches. Only
		// meaningful in this direction: bOnlyID was written for B alone.
		bOnly, err := keys.Capture(f.b.id, bOnlyID)
		if err != nil {
			t.Fatalf("keys.Capture: %v", err)
		}
		if items, err := f.repo.QueryPrefix(ctx, f.pk(t, f.a), bOnly.SK, 0); err != nil {
			t.Fatalf("querying: %v", err)
		} else if len(items) != 0 {
			t.Fatalf("I11 VIOLATED (%s): a query in tenant %s's partition for a sort key that "+
				"exists only in tenant %s's returned %d records. The partition key is not bounding "+
				"the read (§6.3, I11).", f.name, f.a.id, f.b.id, len(items))
		}
		// Non-vacuity: the same prefix in B's own partition does match, so the empty
		// result above is the tenant scope and not a prefix that matches nothing anywhere.
		if items, err := f.repo.QueryPrefix(ctx, f.pk(t, f.b), bOnly.SK, 0); err != nil {
			t.Fatalf("querying: %v", err)
		} else if len(items) == 0 {
			t.Fatalf("the B-only sort key %q matches nothing even in tenant %s's own partition, so "+
				"the empty result from tenant %s's proves nothing", bOnly.SK, f.b.id, f.a.id)
		}

		// **The partition key is an exact match, never a prefix.** Both tenants' partition
		// keys begin with the same family, and one tenant id is a strict prefix of the
		// other, so a partition key compared as a prefix would return everything.
		truncated := skFamily(f.pk(t, f.a))
		if items, err := f.repo.QueryPrefix(ctx, truncated, "", 0); err != nil {
			t.Fatalf("querying the truncated partition key: %v", err)
		} else if len(items) != 0 {
			t.Fatalf("I11 VIOLATED (%s): querying the truncated partition key %q returned %d records. "+
				"The partition key is being matched as a prefix, which means one query reads every "+
				"tenant in the table — the whole-table read I11 exists to make unexpressible (§6.3).",
				f.name, truncated, len(items))
		}
		// The same trap one character short of the longer tenant: A's partition key is a
		// strict prefix of B's.
		if items, err := f.repo.QueryPrefix(ctx, f.pk(t, f.a), "", 0); err != nil {
			t.Fatalf("querying tenant A: %v", err)
		} else {
			for _, it := range items {
				if it.Key.PK != f.pk(t, f.a) {
					t.Fatalf("I11 VIOLATED (%s): a query for partition %q returned partition %q — "+
						"tenant %q's partition key is a strict prefix of tenant %q's, and the "+
						"comparison is not exact (§6.3).",
						f.name, f.pk(t, f.a), it.Key.PK, f.a.id, f.b.id)
				}
			}
		}
	})
}

// TestRepositoryExposesNoCrossPartitionShape is the structural half of requirement 4:
// there is no argument shape that reaches across partitions at all.
//
// Reflection rather than review, because the way I11 dies is documented in
// repository/dynamodb.go: "not a deliberate table-wide read — it is a 'just for the admin
// script' Scan added eighteen months later." A new method on the interface fails this
// test, which is a conversation about I11 rather than a silent capability.
func TestRepositoryExposesNoCrossPartitionShape(t *testing.T) {
	iface := reflect.TypeOf((*repository.Repository)(nil)).Elem()

	want := map[string]bool{"Get": true, "Put": true, "PutOnce": true, "QueryPrefix": true, "Delete": true}
	got := map[string]bool{}
	for i := 0; i < iface.NumMethod(); i++ {
		got[iface.Method(i).Name] = true
	}
	for name := range got {
		if !want[name] {
			t.Errorf("I11 AT RISK: Repository has gained the method %q. Every method here must be "+
				"qualified by a tenant-scoped key or a single partition key; a new one is a new read "+
				"shape, and the shape that ends tenant isolation is a table-wide read added for an "+
				"admin script (§6.3, I11). If it is genuinely scoped, add it to this test's list "+
				"deliberately.", name)
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("Repository no longer has %q; this test's inventory is stale and its guarantees "+
				"no longer describe the interface", name)
		}
	}

	// A key-taking method must take keys.DynamoKey, whose only source is the keys
	// package — a string pair would let a caller assemble one.
	keyType := reflect.TypeOf(keys.DynamoKey{})
	for _, name := range []string{"Get", "Delete"} {
		m, ok := iface.MethodByName(name)
		if !ok {
			continue
		}
		if m.Type.NumIn() != 2 || m.Type.In(1) != keyType {
			t.Errorf("I11 AT RISK: Repository.%s does not take exactly one keys.DynamoKey; a key "+
				"assembled from loose strings bypasses the only component that enforces tenant "+
				"scoping (§6.3, I11)", name)
		}
	}

	// And the implementations expose nothing wider than the interface. A Scan on the
	// adapter is reachable by type assertion even if the interface never mentions it.
	for _, impl := range []reflect.Type{
		reflect.TypeOf((*repository.Dynamo)(nil)),
		reflect.TypeOf((*repository.Memory)(nil)),
	} {
		for i := 0; i < impl.NumMethod(); i++ {
			name := impl.Method(i).Name
			if strings.Contains(name, "Scan") || strings.Contains(name, "AllTenants") {
				t.Errorf("I11 VIOLATED: %s has the method %q. A table-wide read exists, so a read "+
					"path that is not qualified by tenant_id exists — reachable by type assertion "+
					"even though Repository does not mention it (§3 I11, §6.3).", impl, name)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 3. The scoped handle
// ---------------------------------------------------------------------------

// TestScopedHandleCannotBeCoercedToAnotherTenant covers requirement 3.
//
// repository.TenantScoped is documented as *not* a security boundary — the keys package
// is — and this test asserts the property it does provide: the tenant is bound once, at
// the point it was authenticated, and nothing downstream can change it. §9.1 requires
// tenant_id to come only from a validated JWT claim, so a handle that could be
// re-pointed would reintroduce exactly the "tenant from a path parameter" path that
// requirement forbids.
func TestScopedHandleCannotBeCoercedToAnotherTenant(t *testing.T) {
	forEachBackend(t, func(t *testing.T, f *fixture) {
		ctx := context.Background()

		// Fails closed on an unusable tenant, at the request boundary, rather than
		// producing a handle that reads some other partition.
		for label, tn := range map[string]keys.TenantID{"empty": "", "whitespace": "  ", "delimiter": "t#a"} {
			if h, err := repository.Scope(f.repo, tn); err == nil {
				t.Errorf("I11 VIOLATED: repository.Scope accepted a %s tenant and returned a handle "+
					"bound to %q. Auth failure must default to deny (I10) and every path must be "+
					"tenant-qualified (I11); a handle with no tenant is neither.", label, h.Tenant())
			}
		}

		handleA, err := repository.Scope(f.repo, f.a.id)
		if err != nil {
			t.Fatalf("repository.Scope(%s): %v", f.a.id, err)
		}
		handleB, err := repository.Scope(f.repo, f.b.id)
		if err != nil {
			t.Fatalf("repository.Scope(%s): %v", f.b.id, err)
		}
		if handleA.Tenant() != f.a.id || handleB.Tenant() != f.b.id {
			t.Fatalf("a scoped handle does not report the tenant it was bound to (%q, %q): the binding "+
				"is the whole of what this type provides", handleA.Tenant(), handleB.Tenant())
		}

		// The zero value is unusable rather than permissive: a handle nobody scoped
		// carries no tenant, and every key built from it is refused.
		var unbound repository.TenantScoped
		if unbound.Tenant() != "" {
			t.Errorf("the zero TenantScoped reports tenant %q; a handle nobody bound must carry no "+
				"tenant, or a struct literal becomes a way to forge one", unbound.Tenant())
		}
		if _, err := keys.Tenant(unbound.Tenant()); err == nil {
			t.Error("I11 VIOLATED: a key can be built from an unbound scoped handle, so a forged " +
				"handle reaches storage (§3 I11)")
		}

		// Structural: no exported field and no setter. A method taking a keys.TenantID
		// would be a way to re-point a bound handle after authentication.
		hType := reflect.TypeOf(repository.TenantScoped{})
		for i := 0; i < hType.NumField(); i++ {
			if hType.Field(i).IsExported() {
				t.Errorf("I11 AT RISK: TenantScoped.%s is exported, so a caller can re-point a bound "+
					"handle at another tenant by assignment. The tenant must be settable only at the "+
					"point it was authenticated (§9.1).", hType.Field(i).Name)
			}
		}
		ptr := reflect.TypeOf(&repository.TenantScoped{})
		tenantType := reflect.TypeOf(keys.TenantID(""))
		for i := 0; i < ptr.NumMethod(); i++ {
			m := ptr.Method(i)
			for j := 1; j < m.Type.NumIn(); j++ {
				if m.Type.In(j) == tenantType {
					t.Errorf("I11 AT RISK: TenantScoped.%s accepts a keys.TenantID, which makes a bound "+
						"handle re-pointable after the tenant was authenticated (§9.1, I11)", m.Name)
				}
			}
		}

		// Behavioural: every key a handle builds from its own tenant lands in its own
		// partition, and the entity ids are identical in both tenants — so the handle is
		// the only thing deciding which record is read.
		for _, e := range keyedEntities() {
			for _, h := range []*repository.TenantScoped{handleA, handleB} {
				reader := f.a
				if h.Tenant() == f.b.id {
					reader = f.b
				}
				key, err := e.key(h.Tenant(), sharedID)
				if err != nil {
					t.Fatalf("%s key: %v", e.name, err)
				}
				if key.PK != f.pk(t, reader) {
					t.Fatalf("I11 VIOLATED: %s built from a handle scoped to %q addresses partition %q; "+
						"the handle's tenant is not reaching the key helper (§6.3, I11)",
						e.name, h.Tenant(), key.PK)
				}
				got, err := h.Repo().Get(ctx, key)
				if err != nil {
					t.Fatalf("%s: reading through the handle for %s: %v", e.name, h.Tenant(), err)
				}
				assertNotOtherTenants(t, f, reader, e.name+" through a scoped handle", *got)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// 4. The service layer
// ---------------------------------------------------------------------------

// TestServiceLayerReadsAreTenantScoped attempts the same crossing through the readers
// production actually calls.
//
// These matter separately from the repository assertions because each one has its own
// consequence, and the consequence is not "a record was disclosed":
//
//   - meter feeds the daily spend breaker (§10.5.9), so a leaked sum changes whether
//     provider calls are allowed to happen at all;
//   - consent decides retention (I14), so a leaked grant retains content the user
//     refused;
//   - kmsref decides what erasure can claim (§9.3), so a leaked key reference makes a
//     completed erasure report untrue;
//   - idem answers a client's retry, so a leaked outcome hands one tenant another's
//     resource identifier.
func TestServiceLayerReadsAreTenantScoped(t *testing.T) {
	forEachBackend(t, func(t *testing.T, f *fixture) {
		ctx := context.Background()

		t.Run("meter", func(t *testing.T) {
			for _, reader := range []tenantFixture{f.a, f.b} {
				victim := f.other(reader)
				month, err := f.meter.MonthTotal(ctx, reader.id, isoMonth)
				if err != nil {
					t.Fatalf("MonthTotal(%s): %v", reader.id, err)
				}
				if month != reader.costMicros {
					t.Errorf("I11/I12 VIOLATED: tenant %s's month total is %d micros, want %d "+
						"(tenant %s spent %d). Per-tenant usage is the only source of truth for unit "+
						"economics (§6.4) and the input to the daily spend breaker (§10.5.9): a total "+
						"carrying another tenant's spend bills the wrong tenant and moves the wrong "+
						"tenant's cap.", reader.id, month, reader.costMicros, victim.id, victim.costMicros)
				}
				day, err := f.meter.DayTotal(ctx, reader.id, isoDate)
				if err != nil {
					t.Fatalf("DayTotal(%s): %v", reader.id, err)
				}
				if day != reader.costMicros {
					t.Errorf("I11/I12 VIOLATED: tenant %s's day total is %d micros, want %d; the spend "+
						"breaker reads this figure before every provider call (§10.5.9)",
						reader.id, day, reader.costMicros)
				}
			}
		})

		t.Run("audit", func(t *testing.T) {
			for _, reader := range []tenantFixture{f.a, f.b} {
				victim := f.other(reader)
				page, err := f.auditor.Query(ctx, audit.Query{Tenant: reader.id})
				if err != nil {
					t.Fatalf("audit.Query(%s): %v", reader.id, err)
				}
				if len(page.Records) == 0 {
					t.Fatalf("tenant %s has no audit records, so this assertion is vacuous", reader.id)
				}
				for _, rec := range page.Records {
					if strings.Contains(rec.Resource, victim.marker) || strings.Contains(rec.Actor, victim.marker) {
						t.Errorf("I11/I13 VIOLATED: tenant %s's audit log contains a record naming "+
							"tenant %s's resource (%q). The access log is the longest-retained store "+
							"in the system and the first thing any compliance conversation asks for "+
							"(§2A.1); one tenant reading another's is a disclosure of what the other "+
							"tenant did (§9.1).", reader.id, victim.id, rec.Resource)
					}
				}
			}
		})

		t.Run("consent", func(t *testing.T) {
			for _, reader := range []tenantFixture{f.a, f.b} {
				victim := f.other(reader)

				// The purpose this tenant granted resolves granted...
				if d := f.consent.Resolve(ctx, reader.id, reader.purpose); !d.Allowed() {
					t.Fatalf("tenant %s did not get its own grant for %s back (%s); the fixture is "+
						"broken and the refusal below proves nothing", reader.id, reader.purpose, d)
				}
				// ...and the purpose only the *other* tenant granted must not.
				if d := f.consent.Resolve(ctx, reader.id, victim.purpose); d.Allowed() {
					t.Errorf("I14 VIOLATED: tenant %s resolves %s as granted, but only tenant %s "+
						"granted it. Absence of consent is refusal, and a leaked grant means content "+
						"retained for a purpose this user never agreed to (§3 I14) — retroactive "+
						"consent across a user base is expensive and frequently unobtainable.",
						reader.id, victim.purpose, victim.id)
				}
				// The version is what §Phase 4's purge selects by, so a leaked version
				// list purges the wrong tenant's corpus.
				versions, err := f.consent.GrantedVersions(ctx, reader.id, reader.purpose)
				if err != nil {
					t.Fatalf("GrantedVersions(%s): %v", reader.id, err)
				}
				for _, v := range versions {
					if strings.Contains(v, victim.marker) {
						t.Errorf("I11/I14 VIOLATED: tenant %s's granted versions for %s include tenant "+
							"%s's version %q. §Phase 4's withdrawal purge selects records by this "+
							"value, so it would delete the wrong tenant's corpus.",
							reader.id, reader.purpose, victim.id, v)
					}
				}
				state, err := f.consent.State(ctx, reader.id)
				if err != nil {
					t.Fatalf("State(%s): %v", reader.id, err)
				}
				if d, ok := state[victim.purpose]; ok && d.Allowed() {
					t.Errorf("I14 VIOLATED: tenant %s's consent state reports %s as granted; only "+
						"tenant %s granted it (§3 I14)", reader.id, victim.purpose, victim.id)
				}
			}
		})

		t.Run("kmsref", func(t *testing.T) {
			for _, reader := range []tenantFixture{f.a, f.b} {
				victim := f.other(reader)
				ref, err := f.kms.Resolve(ctx, reader.id)
				if err != nil {
					t.Fatalf("kmsref.Resolve(%s): %v", reader.id, err)
				}
				if ref.ID() != reader.kms {
					t.Fatalf("I8/I11 VIOLATED: tenant %s resolves to key reference %q, want %q "+
						"(tenant %s uses %q). New objects would be encrypted under another tenant's "+
						"key, and destroying that key on erasure would shred this tenant's data or "+
						"leave it recoverable (§9.3, G-021).",
						reader.id, ref.ID(), reader.kms, victim.id, victim.kms)
				}
			}
			// The two references are of different kinds, so a leak is visible in what an
			// erasure report would claim and not only in a string comparison.
			aRef, err := f.kms.Resolve(ctx, f.a.id)
			if err != nil {
				t.Fatalf("kmsref.Resolve: %v", err)
			}
			if aRef.CryptoShreddable() {
				t.Errorf("§9.3 VIOLATED: tenant %s resolves to a crypto-shreddable key, but it is "+
					"provisioned with the AWS-managed key %q, which the account cannot delete. An "+
					"erasure report would claim a completeness it does not have (G-021).",
					f.a.id, f.a.kms)
			}
			bRef, err := f.kms.Resolve(ctx, f.b.id)
			if err != nil {
				t.Fatalf("kmsref.Resolve: %v", err)
			}
			if !bRef.CryptoShreddable() {
				t.Errorf("tenant %s is provisioned with the customer-managed reference %q but does "+
					"not resolve as crypto-shreddable; the two tenants then differ in no observable "+
					"way and this assertion cannot detect a swap", f.b.id, f.b.kms)
			}
		})

		t.Run("idem", func(t *testing.T) {
			// Both tenants already claimed and completed sharedID with the *same*
			// fingerprint. Each replay must return its own outcome.
			for _, reader := range []tenantFixture{f.a, f.b} {
				victim := f.other(reader)
				res, err := f.idem.Begin(ctx, idem.Request{
					Tenant: reader.id, Key: sharedID, Fingerprint: f.fingerprint,
				})
				if err != nil {
					t.Fatalf("idem.Begin(%s): %v", reader.id, err)
				}
				if res.State != idem.StateCompleted {
					t.Fatalf("tenant %s's replay reports %s, want %s; the fixture did not complete "+
						"the reservation and the assertion below is vacuous", reader.id, res.State, idem.StateCompleted)
				}
				if res.Outcome.Resource != reader.marker {
					t.Errorf("I11 VIOLATED: tenant %s's retry of idempotency key %q was answered with "+
						"resource %q, want %q (tenant %s recorded %q). idem.Request documents this "+
						"exact failure: 'an unscoped key would let one tenant's retry return another "+
						"tenant's resource identifier' — the client is then handed an id it can ask "+
						"for by name.",
						reader.id, sharedID, res.Outcome.Resource, reader.marker, victim.id, victim.marker)
				}
			}
		})
	})
}

// ---------------------------------------------------------------------------
// 5. The one documented exception
// ---------------------------------------------------------------------------

// TestTelegramLinkIsTheOnlyRecordOutsideATenantPartition covers requirement 5.
//
// §6.3 partitions the Telegram link by sender id rather than by tenant, and
// keys.TelegramLink explains why the exception is inherent: it is the lookup that
// *resolves* a tenant from an inbound sender, so it cannot be qualified by the value it
// exists to discover. That reasoning holds only while two things stay true, and both are
// asserted here rather than trusted:
//
//   - the record holds no user content, so it is "the gate in front of" tenant data
//     rather than a path to it;
//   - it resolves to exactly one tenant, because an ambiguous resolution files one
//     person's voice message into another person's account.
func TestTelegramLinkIsTheOnlyRecordOutsideATenantPartition(t *testing.T) {
	forEachBackend(t, func(t *testing.T, f *fixture) {
		ctx := context.Background()

		for _, tf := range []tenantFixture{f.a, f.b} {
			other := f.other(tf)
			key, err := keys.TelegramLink(tf.tgUser)
			if err != nil {
				t.Fatalf("keys.TelegramLink: %v", err)
			}

			// Outside every tenant partition, and not confusable with one.
			for _, pk := range []string{f.pk(t, f.a), f.pk(t, f.b)} {
				if key.PK == pk {
					t.Fatalf("the Telegram link shares partition %q with a tenant; §6.3 gives it its "+
						"own partition precisely so it is not reachable by a tenant-scoped query", pk)
				}
			}

			got, err := f.repo.Get(ctx, key)
			if err != nil {
				t.Fatalf("reading the Telegram link for %s: %v", tf.id, err)
			}

			// Holds no user content. Checked as an exact attribute set, not a substring
			// scan: this record sits outside I11's protection, so an attribute added
			// here later — a transcript preview for a reply, a username — is content
			// stored where no tenant scope applies.
			wantAttrs := map[string]bool{"tenant_id": true, "user_id": true, "linked_at": true}
			for name := range got.Attrs {
				if !wantAttrs[name] {
					t.Errorf("§6.3 VIOLATED: the Telegram link record carries the attribute %q. This "+
						"record is the one record outside every tenant partition, so anything stored "+
						"here is stored outside I11's protection — it may hold a tenant reference and "+
						"a user reference only.", name)
				}
			}
			for _, marker := range []string{f.a.marker, f.b.marker} {
				if itemMentions(*got, marker) {
					t.Errorf("I11 VIOLATED: the Telegram link for %s contains tenant content (marker "+
						"%q). §6.3's exception holds only because this record 'holds no user content — "+
						"only a tenant and user reference'; with content in it, the exception is a hole.",
						tf.id, marker)
				}
			}

			// Resolves to exactly one tenant: this one.
			resolved, ok := got.Attrs["tenant_id"].(string)
			if !ok || resolved == "" {
				t.Fatalf("the Telegram link for %s carries no usable tenant_id (%v); an inbound "+
					"message would then resolve to no tenant, and §6.6 requires an unmapped sender "+
					"to be rejected rather than guessed", tf.id, got.Attrs["tenant_id"])
			}
			if keys.TenantID(resolved) != tf.id {
				t.Fatalf("I11 VIOLATED: the Telegram link for sender %q resolves to tenant %q, not "+
					"%q. Every message from that sender would be filed into another person's "+
					"account (§6.3, §6.6).", tf.tgUser, resolved, tf.id)
			}
			if keys.TenantID(resolved) == other.id {
				t.Fatalf("I11 VIOLATED: sender %q resolves to tenant %q, which is the other tenant "+
					"(§6.3)", tf.tgUser, resolved)
			}

			// One partition, one link. Exactly one record, so "resolving it yields
			// exactly one tenant" is a property of the store and not of the reader.
			items, err := f.repo.QueryPrefix(ctx, key.PK, "", 0)
			if err != nil {
				t.Fatalf("sweeping the link partition: %v", err)
			}
			if len(items) != 1 {
				t.Fatalf("I11 VIOLATED: the link partition for sender %q holds %d records, want 1. "+
					"A sender that resolves to more than one tenant makes routing ambiguous, and an "+
					"ambiguous route delivers a voice message to the wrong account (§6.3).",
					tf.tgUser, len(items))
			}

			// A second link for the same sender pointing at the other tenant is refused
			// by the conditional write, so ambiguity cannot be created by a race or a
			// re-link.
			err = f.repo.PutOnce(ctx, repository.Item{
				Key: key,
				Attrs: map[string]any{
					"tenant_id": string(other.id),
					"user_id":   sharedID,
					"linked_at": clock.RFC3339UTC(f.clk.Now()),
				},
			})
			if !errors.Is(err, repository.ErrAlreadyExists) {
				t.Fatalf("I11 VIOLATED: a second Telegram link for sender %q pointing at tenant %q "+
					"was accepted (%v). One sender must resolve to exactly one tenant; a second "+
					"mapping re-routes an existing user's captures into another tenant (§6.3).",
					tf.tgUser, other.id, err)
			}
			// And the refusal left the original intact rather than half-written.
			after, err := f.repo.Get(ctx, key)
			if err != nil {
				t.Fatalf("re-reading the link after the refused write: %v", err)
			}
			if after.Attrs["tenant_id"] != string(tf.id) {
				t.Fatalf("I11 VIOLATED: the refused re-link changed the mapping for sender %q to %v; "+
					"a refused write must not mutate the record (§6.3)", tf.tgUser, after.Attrs["tenant_id"])
			}

			// Once resolved, reaching data still goes through the key helper with the
			// resolved tenant — the link grants a tenant, not access to a partition.
			capKey, err := keys.Capture(keys.TenantID(resolved), sharedID)
			if err != nil {
				t.Fatalf("keys.Capture from the resolved tenant: %v", err)
			}
			capture, err := f.repo.Get(ctx, capKey)
			if err != nil {
				t.Fatalf("reading the resolved tenant's capture: %v", err)
			}
			assertNotOtherTenants(t, f, tf, "a capture reached through a Telegram link", *capture)
		}

		// The links never appear in a tenant-scoped sweep, so no tenant-scoped read path
		// can reach them and no listing exposes another user's Telegram id.
		for _, tf := range []tenantFixture{f.a, f.b} {
			items, err := f.repo.QueryPrefix(ctx, f.pk(t, tf), "", 0)
			if err != nil {
				t.Fatalf("sweeping tenant %s: %v", tf.id, err)
			}
			for _, it := range items {
				for _, other := range []tenantFixture{f.a, f.b} {
					if strings.Contains(it.Key.SK, other.tgUser) || mentions(it.Attrs, other.tgUser) {
						t.Errorf("a tenant-scoped sweep of %s surfaced the Telegram sender id %q "+
							"(record %s / %s); the link record must stay in its own partition (§6.3)",
							tf.id, other.tgUser, it.Key.PK, it.Key.SK)
					}
				}
			}
		}

		// keys.TelegramLink is the exception, not a general unscoped constructor: it
		// still refuses an unusable sender id, so it cannot be called with nothing to
		// produce a partition that a later record shares.
		for label, id := range map[string]string{"empty": "", "whitespace": " ", "delimiter": "a#b", "path": "a/b"} {
			if k, err := keys.TelegramLink(id); err == nil {
				t.Errorf("keys.TelegramLink accepted a %s sender id and returned %q / %q; the one "+
					"record type outside a tenant partition must not also accept an unvalidated "+
					"partition key (§6.3)", label, k.PK, k.SK)
			}
		}
	})
}
