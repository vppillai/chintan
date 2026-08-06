package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/vppillai/chintan/backend/internal/audit"
	"github.com/vppillai/chintan/backend/internal/awsclient"
	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/config"
	"github.com/vppillai/chintan/backend/internal/ids"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/kmsref"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/systemid"
)

// This file is the ONE implementation §9.3's export and erasure share, and the one
// §Phase 7 reuses.
//
// §Phase 7 acceptance: "Export and export-tenant.sh produce identical content for the
// same tenant — one implementation, not two." That is a statement about where the code
// lives, so the structure here is deliberate rather than incidental:
//
//   - Enumeration is one function (takeInventory) over one tenant partition and one S3
//     prefix. Export and erasure both act on its output, so the archive can never
//     describe a set of objects different from the set erasure would destroy.
//   - Nothing enumerates by ENTITY TYPE. Both operations read every item in the tenant's
//     partition and every object under its prefix, whatever kind they are. A
//     type-by-type export is an export that silently omits the entity type added next
//     phase — and Phase 0 is exactly the moment that omission would be invisible,
//     because most types do not exist yet. Completeness is by construction.
//   - The archive is written through archiveSink rather than to files directly, so
//     Phase 7's asynchronous export (POST /v1/exports) can point the same code at S3
//     instead of a local directory. I3 forbids audio bytes transiting a Lambda *payload*;
//     an object streamed S3-to-S3 through a sink is not a payload, and the seam is what
//     keeps that available.
//   - Neither operation constructs a key. Every key comes from the keys package (I11),
//     including the partition key both enumerations start from, so there is no read or
//     write path here that is not tenant-qualified — "including admin and migration
//     scripts" is the clause this file exists under.
//
// Fenced-scope note, because a future reader will wonder: this logic belongs in an
// internal package that both chintanctl and the Phase 7 handler import (§11.2's "a future
// admin UI then calls the same module"). It lives under cmd/chintanctl for now only
// because the pass that wrote it was not permitted to add an internal package; moving it
// is a file move plus an import, with no logic change, and the interfaces above are what
// keep it that way.

// dataOpts is the flag set every tenant-scoped data subcommand shares (§11.3).
type dataOpts struct {
	tenant   string
	instance string
	account  string
	region   string
	fixtures string
	config   string
	actor    string
	apply    bool
	asJSON   bool
}

// registerDataFlags binds the shared flags.
//
// --dry-run is not a flag. It is the DEFAULT, and --apply is what executes (§11.3: "a
// mistaken invocation prints a plan instead of causing damage"). A --dry-run flag would
// imply its absence means "apply", which is the inversion this convention exists to
// prevent.
func registerDataFlags(fs *flag.FlagSet, o *dataOpts) {
	fs.StringVar(&o.tenant, "tenant", "", "tenant id (required — no data operation runs untenanted, I11)")
	fs.StringVar(&o.instance, "instance", "", "instance name, e.g. dev or prod (names the table and bucket)")
	fs.StringVar(&o.account, "account", "", "AWS account id (part of the bucket name, §6.2)")
	fs.StringVar(&o.region, "region", "", "AWS region")
	fs.StringVar(&o.fixtures, "fixtures", "", "run against a fixture set instead of AWS (credential-free test mode, §11.5)")
	fs.StringVar(&o.config, "config", "", "instance config file, for retention.audit_days and retention.usage_months (§7.4)")
	fs.StringVar(&o.actor, "as", "", "who is running this, recorded in the audit record (default script:chintanctl); a user id, never an email (§9.2)")
	fs.BoolVar(&o.apply, "apply", false, "execute the plan; without it the plan is printed and nothing changes (§11.3)")
	fs.BoolVar(&o.asJSON, "json", false, "machine-readable output")
}

// tenantData is one tenant's stores, resolved and ready.
type tenantData struct {
	tenant  keys.TenantID
	pk      string
	prefix  string
	table   string
	repo    repository.Repository
	objects objectStore
	auditor *audit.Auditor
	meter   *meter.Meter
	scope   kmsref.ErasureScope

	// actor is what the audit record attributes the invocation to (I13).
	actor string

	// fixtures records that no AWS account was touched. Printed everywhere a count is
	// printed: a fixture run's "removed 412 objects" must not be mistakable for one.
	fixtures bool
}

// openTenantData validates the arguments and binds both stores to one tenant.
//
// The tenant is resolved through keys.Tenant before anything else, so an empty or
// malformed one fails here rather than producing a key that reads across the table (I11).
func openTenantData(ctx context.Context, o dataOpts, log *slog.Logger) (*tenantData, error) {
	if strings.TrimSpace(o.tenant) == "" {
		return nil, usageErrorf("--tenant is required: no data operation runs untenanted (I11, §11.3)")
	}
	tenant := keys.TenantID(o.tenant)
	tenantKey, err := keys.Tenant(tenant)
	if err != nil {
		return nil, usageErrorf("%v", err)
	}
	prefix, err := keys.S3TenantPrefix(tenant)
	if err != nil {
		return nil, usageErrorf("%v", err)
	}

	auditDays, usageMonths, err := retentionFromConfig(o.config)
	if err != nil {
		return nil, err
	}

	td := &tenantData{tenant: tenant, pk: tenantKey.PK, prefix: prefix, actor: scriptActor(o.actor)}

	switch {
	case o.fixtures != "":
		if o.instance != "" || o.account != "" {
			// Refused rather than silently preferring one: an operator who passed both
			// meant to run against the account and would read the fixture run's output
			// as though it had.
			return nil, usageErrorf("--fixtures cannot be combined with --instance or --account: one run touches an account and the other does not")
		}
		repo, store, err := loadFixtures(o.fixtures, tenant)
		if err != nil {
			return nil, err
		}
		td.repo, td.objects, td.table, td.fixtures = repo, store, "fixtures:"+o.fixtures, true

	default:
		if o.instance == "" || o.account == "" || o.region == "" {
			return nil, usageErrorf("--instance, --account and --region are required for a live run (they name the table and the bucket, §6.2/§6.3); use --fixtures for the credential-free test mode")
		}
		api, err := awsclient.NewDynamoDB(ctx)
		if err != nil {
			return nil, err
		}
		td.table = systemid.TableName(o.instance)
		repo, err := repository.NewDynamo(api, td.table)
		if err != nil {
			return nil, err
		}
		// The bucket is REQUIRED, not optional. An export or erasure that ran with no
		// object store would report success over the DynamoDB records alone while every
		// byte of audio, every L0 transcript and every markdown document stayed where it
		// was — the completeness claim §9.3 forbids. Refusing is the only honest
		// alternative to implementing it.
		store, err := newLiveS3(ctx, systemid.BucketName(o.instance, o.account, o.region), o.region)
		if err != nil {
			return nil, err
		}
		td.repo, td.objects = repo, store
	}

	gen := ids.NewGenerator(clock.System{})
	td.auditor = audit.New(td.repo, clock.System{}, gen, log, auditDays)
	td.meter = meter.New(td.repo, clock.System{}, gen, log, usageMonths)

	// The key indirection is resolved BEFORE anything is read or destroyed, because what
	// it answers changes what the report may claim (§9.3, G-021). kmsref makes no AWS
	// call; it reads the tenant record through the repository.
	//
	// Deployment mirrors what infrastructure/template.yaml actually configures today —
	// AES256 on the bucket, SSEEnabled on the table — via kmsref's own constructors
	// rather than string literals, so the CMK flip stays a provisioning change (I8).
	resolver, err := kmsref.New(td.repo, kmsref.Deployment{
		Bucket: kmsref.S3ServiceDefault(),
		Table:  kmsref.DynamoDBServiceDefault(),
	})
	if err != nil {
		return nil, err
	}
	scope, err := resolver.ErasureScope(ctx, tenant)
	if err != nil {
		// Fail closed. An unresolvable key reference means the report cannot state what
		// erasure reaches, and §6.3 is explicit that kms_key_id "is never null and never
		// absent" — so this is a provisioning defect to fix, not a condition to proceed
		// past with a vaguer caveat.
		return nil, fmt.Errorf("resolving the tenant's key reference: %w", err)
	}
	td.scope = scope
	return td, nil
}

// retentionFromConfig reads the two retention windows the audit and usage records this
// invocation writes will carry.
//
// Optional, and defaulted by the audit and meter packages when absent — but worth wiring,
// because a CLI-written audit record with a different TTL from every Lambda-written one is
// a store whose retention window depends on which component wrote the row.
func retentionFromConfig(path string) (auditDays, usageMonths int, err error) {
	if path == "" {
		return 0, 0, nil
	}
	cfg, err := config.Load(path)
	if err != nil {
		return 0, 0, fmt.Errorf("loading %s: %w", path, err)
	}
	if cfg.Retention.AuditDays != nil {
		auditDays = *cfg.Retention.AuditDays
	}
	if cfg.Retention.UsageMonths != nil {
		usageMonths = *cfg.Retention.UsageMonths
	}
	return auditDays, usageMonths, nil
}

// ---------------------------------------------------------------------------
// Inventory
// ---------------------------------------------------------------------------

// recordRef is one DynamoDB record in the plan.
//
// **Attributes are deliberately absent.** This struct is serialised into --json output and
// into the human-readable plan, and §9.2 keeps transcript content, audio and PII out of
// logs and messages. A sort key is an identifier; an attribute map is content. The
// exporter reads the attributes from the items list, which is never printed.
type recordRef struct {
	SK    string `json:"sk"`
	Class string `json:"class"`
	TTL   int64  `json:"ttl,omitempty"`
}

// classCount is one entity class and how many records of it the tenant has.
type classCount struct {
	Class string `json:"class"`
	Count int    `json:"count"`
}

// inventory is everything one tenant has, enumerated once.
//
// Shared by export and erasure so the archive and the deletion plan cannot disagree about
// what exists.
type inventory struct {
	Tenant       string `json:"tenant"`
	PartitionKey string `json:"partition_key"`
	Table        string `json:"table"`
	ObjectPrefix string `json:"object_prefix"`
	ObjectStore  string `json:"object_store"`

	Records       []recordRef  `json:"records"`
	RecordClasses []classCount `json:"record_classes"`

	// Objects is every version and delete marker, not only the current ones. Export uses
	// the current ones; erasure needs all of them (see objectVersion).
	Objects []objectVersion `json:"objects"`

	CurrentObjects     int   `json:"current_objects"`
	NoncurrentVersions int   `json:"noncurrent_versions"`
	DeleteMarkers      int   `json:"delete_markers"`
	CurrentBytes       int64 `json:"current_bytes"`
	TotalBytes         int64 `json:"total_bytes"`

	// APIRequests is the billable call count the enumeration itself cost (I12).
	APIRequests int `json:"api_requests"`

	// items carries the attribute maps the exporter writes. Unexported so it cannot be
	// marshalled into output by accident — that would put content in a log (§9.2).
	items []repository.Item
}

// takeInventory enumerates one tenant's records and objects.
//
// Two reads, both tenant-qualified by construction: a Query on the tenant partition and a
// versioned listing under the tenant prefix. Neither can be widened — repository has no
// Scan, and an empty S3 prefix is refused by both object stores.
func takeInventory(ctx context.Context, td *tenantData) (inventory, error) {
	inv := inventory{
		Tenant:       string(td.tenant),
		PartitionKey: td.pk,
		Table:        td.table,
		ObjectPrefix: td.prefix,
		ObjectStore:  td.objects.Describe(),
	}

	// An empty sort-key prefix, deliberately: it means "every record in this tenant's
	// partition", which is what completeness requires. It is not an unscoped read — the
	// partition key is the tenant, and repository.QueryPrefix refuses an empty one.
	items, err := td.repo.QueryPrefix(ctx, td.pk, "", 0)
	if err != nil {
		return inventory{}, fmt.Errorf("reading the tenant partition: %w", err)
	}
	inv.items = items

	counts := map[string]int{}
	for _, it := range items {
		class := recordClass(it.Key.SK)
		counts[class]++
		inv.Records = append(inv.Records, recordRef{SK: it.Key.SK, Class: class, TTL: it.TTL})
	}
	sort.Slice(inv.Records, func(i, j int) bool { return inv.Records[i].SK < inv.Records[j].SK })
	for class, n := range counts {
		inv.RecordClasses = append(inv.RecordClasses, classCount{Class: class, Count: n})
	}
	sort.Slice(inv.RecordClasses, func(i, j int) bool { return inv.RecordClasses[i].Class < inv.RecordClasses[j].Class })

	versions, err := td.objects.ListVersions(ctx, td.prefix)
	if err != nil {
		return inventory{}, fmt.Errorf("listing the tenant's objects: %w", err)
	}
	inv.Objects = versions
	for _, v := range versions {
		inv.TotalBytes += v.Bytes
		switch {
		case v.DeleteMarker:
			inv.DeleteMarkers++
		case v.IsLatest:
			inv.CurrentObjects++
			inv.CurrentBytes += v.Bytes
		default:
			inv.NoncurrentVersions++
		}
	}
	inv.APIRequests = td.objects.Requests()
	return inv, nil
}

// recordClass labels a record by the class segment of its sort key.
//
// Derived from the key rather than matched against a list of entity types, for two
// reasons that point the same way. The keys package holds a monopoly on key literals
// (I11, enforced by check-tenant-keys.sh), so a table of prefixes here would be a second
// copy of that vocabulary; and a fixed list would label the entity type added next phase
// as "unknown" — or worse, omit it from a count an operator reads as complete.
func recordClass(sk string) string {
	if i := strings.Index(sk, "#"); i > 0 {
		return sk[:i]
	}
	// A sort key with no class segment is a singleton record such as the tenant metadata
	// row. Its whole key is its class.
	return sk
}

// currentObjects returns the versions an export copies: the live version of each key,
// excluding tombstones.
func (inv inventory) currentObjects() []objectVersion {
	var out []objectVersion
	for _, v := range inv.Objects {
		if v.IsLatest && !v.DeleteMarker {
			out = append(out, v)
		}
	}
	return out
}

// empty reports a tenant with nothing left to export or erase.
func (inv inventory) empty() bool { return len(inv.Records) == 0 && len(inv.Objects) == 0 }

// ---------------------------------------------------------------------------
// Audit and metering
// ---------------------------------------------------------------------------

// recordAccess writes the audit record for this invocation (I13, §11.3: "every invocation
// of a data script writes an audit record").
//
// **Called before the operation, never after** — §9.3 requires exactly that for erasure,
// and audit.Record's own contract makes it the general rule: a crash between acting and
// recording produces the gap §2A.1 calls unrepairable, so the record goes first and an
// operation that then fails leaves a record of an access that did nothing.
//
// A failure here aborts the operation. That is audit.Record's documented contract for
// content access ("do not serve user content when this returns non-nil") and it is
// sharper for erasure: executing the most destructive operation in the system with no
// record that it was authorised or attempted is precisely the state I13 exists to make
// impossible.
//
// The resource is the tenant partition key. Both operations act on a whole partition
// rather than one entity, and an audit record with no resource cannot answer which tenant
// was touched.
func recordAccess(ctx context.Context, td *tenantData, action string) error {
	if err := td.auditor.Allowed(ctx, audit.Access{
		Tenant:   td.tenant,
		Actor:    td.actor,
		Action:   action,
		Resource: td.pk,
		// No IP and no UA: this is a script invocation, not a request. audit.Access
		// documents empty as "no address was available", which is the truth here —
		// inventing one would launder a guess into the log as fact.
	}); err != nil {
		return fmt.Errorf("writing the audit record: %w (I13 — nothing was changed)", err)
	}
	return nil
}

// scriptActor identifies the invoking front-end, per audit.Access's "script:{name}"
// convention.
//
// Supplied by the caller rather than fixed, so the record names WHICH front-end ran: §11.2
// puts one implementation behind several thin wrappers, and an audit log that attributed
// every invocation to the binary could not distinguish export-tenant.sh from erase-tenant.sh
// — or from a future admin UI calling the same code.
//
// The value is not validated here. The audit package refuses an email-shaped actor, an
// over-long one, and one carrying control characters, and duplicating those rules would let
// the two disagree about what an actor is (§9.2).
func scriptActor(requested string) string {
	if a := strings.TrimSpace(requested); a != "" {
		return a
	}
	return "script:chintanctl"
}

// meterRequests emits the metering event for the AWS calls this invocation made (I12).
//
// I12 admits no best-effort exception, and meter's own doc names "an operational script's
// usage" as a direct caller. Two decisions inside it are worth stating rather than
// leaving to be inferred:
//
//   - The unit is requests and the quantity is the actual call count, not an estimate.
//     An export of a year's corpus is a real, per-tenant egress bill, and it is
//     attributable — unlike the shared table and function, whose cost §6.4 assigns to the
//     deployment tag set.
//   - cost_micros is 0, and that is a refusal to invent a number rather than a claim that
//     the operation is free. AWS request and transfer rates are not in config; the only
//     per-unit costs config carries are the providers' (§7.1). Writing a hardcoded rate
//     here would put a price in code that I5 keeps out of it everywhere else, and it
//     would drift. The dry-run prints the request and byte counts so the operator can
//     price them against the current rate card, which is the honest form of the same
//     information.
func meterRequests(ctx context.Context, td *tenantData, op string, requests int) error {
	if err := td.meter.Record(ctx, meter.Event{
		Tenant:     td.tenant,
		Unit:       model.UnitRequests,
		Quantity:   float64(requests),
		Provider:   "aws",
		Op:         op,
		CostMicros: 0,
	}); err != nil {
		return fmt.Errorf("writing the metering record: %w (I12)", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Archive sink
// ---------------------------------------------------------------------------

// archiveSink receives one archive's files.
//
// The seam that makes §Phase 7 a reuse rather than a reimplementation: the local
// directory below is one implementation, and an S3 sink writing into the export bucket is
// another, with the same enumeration and the same manifest behind both.
type archiveSink interface {
	// Put writes one file and returns its size and SHA-256, which the manifest records
	// so the archive is verifiable by whoever receives it.
	Put(path string, r io.Reader) (int64, string, error)

	// Describe names the destination for the report.
	Describe() string
}

// reportOut is where a subcommand's report is written.
//
// A variable rather than os.Stdout directly, so a test can read the report it asserts on.
// That matters more here than convenience: §11.5's requirement is that "dry-run output is
// asserted to describe precisely what --apply then does", and output that cannot be
// captured cannot be asserted — the test would end up checking the struct the renderer was
// given instead of what an operator actually reads.
var reportOut io.Writer = os.Stdout

// emitReportJSON writes the machine-readable document (§11.3).
func emitReportJSON(v any) {
	enc := json.NewEncoder(reportOut)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// ---------------------------------------------------------------------------
// Errors and exit codes
// ---------------------------------------------------------------------------

// usageError marks an argument mistake, so main can exit 2 for "you invoked this wrongly"
// and 1 for "what you asked for failed" (see main.go). A caller in CI needs that
// distinction; a person reading the message does not.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usageErrorf(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// refusedError marks a precondition that must stop the operation before it starts — a
// principal that may not erase, an unconfirmed destructive run.
//
// Its own exit code (3) because "nothing happened, and deliberately" calls for a
// different next action from "something half-happened", which is the distinction
// cleanup-aws.sh draws for the same reason.
type refusedError struct{ msg string }

func (e *refusedError) Error() string { return e.msg }

func refusedErrorf(format string, args ...any) error {
	return &refusedError{msg: fmt.Sprintf(format, args...)}
}

// exitCode maps an error to this binary's exit vocabulary.
func exitCode(err error) int {
	var ue *usageError
	var re *refusedError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		return 2
	case errors.As(err, &re):
		return 3
	default:
		return 1
	}
}

// fail prints an error and returns its exit code, in the one form both subcommands use.
func fail(err error, asJSON bool) int {
	if asJSON {
		// Machine-readable on the failure path too. A caller that parses stdout on
		// success and scrapes stderr on failure has to implement two readers, and the
		// second one is never tested (§11.3).
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"ok": false, "error": err.Error()})
	}
	fmt.Fprintf(os.Stderr, "chintanctl: %v\n", err)
	return exitCode(err)
}

// newCLILogger builds the logger the audit and meter packages log through.
//
// **Not logging.New(), and the reason is not style.** That handler writes JSON to stdout,
// which is correct for a Lambda whose stdout is CloudWatch and wrong here: stdout is the
// --json channel, and a single WARN from audit.Record would make the document a caller
// parses invalid. §11.3 puts diagnostics on stderr precisely so a caller parses structure
// rather than scraping text.
//
// Warn and above: the paths that matter — a rejected audit record, a failed write — are
// logged at WARN and ERROR by their own packages, and an INFO stream would bury them.
func newCLILogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})).
		With(slog.String("component", "chintanctl"))
}
