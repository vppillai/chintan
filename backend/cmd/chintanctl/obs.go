package main

// Shared wiring for the read-only observability subcommands (§11.4): `usage` and
// `audit`. Both need the same four things — a tenant, a repository, an audit record for
// the invocation, and a source description honest enough to appear in the report — and
// §11.2's lesson from passbook is that two front-ends duplicating that wiring is how the
// two drift. One implementation, two thin subcommands, two thin bash wrappers.
//
// # Both scripts write an audit record, and one of them reads the log it writes to
//
// §11.3: "Every invocation of a data script writes an audit record" (I13). That is
// unremarkable for `usage`, and recursive for `audit`: querying the audit log appends to
// the audit log. Handled deliberately rather than accidentally (see obsEnv.ownAuditID and
// runAudit): the record is written BEFORE the read, because audit.Record's contract is
// that the record precedes the access it attests to — and then that one record is
// excluded from the result set and the exclusion is disclosed in the output. Both halves
// matter. Writing after the read would leave the gap §2A.1 calls unrepairable if the
// process died mid-read; including it silently would make `audit --limit 1 --newest`
// return nothing but itself, and would make each invocation's record inflate the next
// invocation's answer.
//
// # Read-only, but not side-effect-free
//
// Neither subcommand mutates user data and neither has --apply (§11.3 is explicit that
// read-only scripts need none). They do write one audit record each, which is a mutation
// of the audit log — required by I13, and the one write §11.3 mandates rather than
// permits. If that write fails, the read does not happen: an access to user data with no
// record of it is precisely the state I13 exists to prevent (see audit.Record's contract).

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/vppillai/chintan/backend/internal/audit"
	"github.com/vppillai/chintan/backend/internal/awsclient"
	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/config"
	"github.com/vppillai/chintan/backend/internal/ids"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/systemid"
)

// Exit codes. §11.3 requires meaningful ones, and the caller here is as often a CI step
// or an agent as a person: 2 says "you invoked this wrongly", 1 says "what you asked for
// failed", and 3 and 4 say "the report was produced and it contains a finding". Collapsing
// the last two into 1 would make a cost overrun indistinguishable from a DynamoDB outage.
const (
	obsExitOK       = 0
	obsExitFailure  = 1
	obsExitUsage    = 2
	obsExitVariance = 3 // usage only: metered totals disagree with the supplied actuals
	obsExitBudget   = 4 // usage only: a month exceeds the §10.7 ceiling
)

// Action vocabulary for the invocation records (audit.actionRe: lowercase dotted verbs).
const (
	obsActionUsage = "usage.report"
	obsActionAudit = "audit.query"
)

// errObsUsage marks a caller mistake, so openObs's failures map to exit 2 rather than 1
// without every call site re-deciding which kind of failure it was looking at.
var errObsUsage = errors.New("invocation error")

// obsFlags are the flags every observability subcommand shares.
type obsFlags struct {
	tenant   string
	config   string
	fixtures string
	as       string
	asJSON   bool
}

func (f *obsFlags) register(fs *flag.FlagSet, defaultActor string) {
	fs.StringVar(&f.tenant, "tenant", "", "tenant id (REQUIRED — no data operation runs untenanted, I11)")
	fs.StringVar(&f.config, "config", "", "instance config file (§7.4); required unless --fixtures")
	fs.StringVar(&f.fixtures, "fixtures", "", "read records from a JSON fixture instead of DynamoDB (CI mode, §11.5)")
	fs.StringVar(&f.as, "as", defaultActor, "actor recorded in this invocation's audit record (I13); a user id, never an email (§9.2)")
	fs.BoolVar(&f.asJSON, "json", false, "machine-readable output")
}

// obsEnv is everything a subcommand needs after the tenant is resolved and the
// invocation has been recorded.
type obsEnv struct {
	tenant keys.TenantID
	repo   repository.Repository

	// cfg is nil when --fixtures is given without --config. It carries the pricing basis
	// the usage report cross-checks against (§7.1's cost_per_hour_usd) and the retention
	// windows, neither of which the read itself needs.
	cfg *config.Config

	clk clock.Clock

	// source describes where the records came from, and appears in the report. A report
	// built from fixtures that did not say so would be indistinguishable from a report of
	// a real tenant's spend, which is the kind of plausible-but-wrong answer §11.6 and the
	// audit package both refuse elsewhere.
	source string

	// ownAuditID is the sort key of THIS invocation's audit record. Known because the
	// generator handed to the Auditor remembers the last id it issued — see obsIDs.
	ownAuditID string
}

// obsIDs wraps the ULID generator so that the id embedded in this invocation's own audit
// record is knowable afterwards.
//
// audit.Auditor.Record returns only an error, by design: a handler has no business
// knowing the id of the record attesting its own work. `audit.sh` is the one caller that
// does need it, because it reads the very partition it just appended to and must be able
// to exclude exactly one record rather than guess with a timestamp-and-action heuristic.
// Wrapping the generator gets that without widening the audit package's API or writing
// the record twice.
type obsIDs struct {
	inner *ids.Generator
	last  string
}

func (g *obsIDs) NewID() string {
	g.last = g.inner.NewID()
	return g.last
}

// obsLogger is the diagnostics logger for the CLI.
//
// **Not logging.New(), which writes JSON to stdout for CloudWatch.** stdout here is the
// machine-readable channel — §11.3's --json exists so a caller parses structure instead
// of scraping text — and one ERROR line from the audit write interleaved into that stream
// makes it unparseable. Same rule as common.sh, whose info/warn/err all go to stderr for
// exactly this reason. The level and the JSON shape are otherwise deliberately the same,
// so a script invocation's diagnostics look like the Lambda's.
func obsLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// openObs resolves the tenant, opens the record source, and records the invocation.
//
// Order is load-bearing: tenant first (an unscoped read is the one bug this product
// cannot survive, I11), then the source, then the audit record, and only then does the
// caller read anything. action and resource describe the invocation for the record;
// resource names WHAT is being read, never a caller-supplied filter value — see
// audit.Access.Resource on why an arbitrary caller string must not enter the
// longest-retained store in the system.
func openObs(ctx context.Context, f *obsFlags, action, resource string) (*obsEnv, error) {
	// keys.Tenant rather than a local emptiness check: the key helper is the authority on
	// what a usable tenant is (I11), and a second opinion here could disagree with it.
	if _, err := keys.Tenant(keys.TenantID(f.tenant)); err != nil {
		if strings.TrimSpace(f.tenant) == "" {
			return nil, fmt.Errorf("%w: --tenant is required: no data operation runs untenanted (I11, §11.3)", errObsUsage)
		}
		return nil, fmt.Errorf("%w: %v", errObsUsage, err)
	}
	tenant := keys.TenantID(f.tenant)

	var cfg *config.Config
	if f.config != "" {
		loaded, err := config.Load(f.config)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errObsUsage, err)
		}
		cfg = loaded
	}

	clk := clock.System{}
	gen := &obsIDs{inner: ids.NewGenerator(clk)}

	var repo repository.Repository
	var source string
	switch {
	case f.fixtures != "":
		mem := repository.NewMemory()
		if err := obsLoadFixture(f.fixtures, mem); err != nil {
			return nil, fmt.Errorf("%w: %v", errObsUsage, err)
		}
		repo = mem
		source = "fixtures:" + f.fixtures
	default:
		if cfg == nil {
			return nil, fmt.Errorf("%w: --config is required (or --fixtures for the CI mode); the table name derives from the instance in the config (§6.3, §7.4)", errObsUsage)
		}
		// Checked here rather than left to the SDK: with no region the default chain
		// fails deep inside a request with a message about endpoint resolution, which
		// reads like a network fault rather than a missing flag. The wrappers export
		// AWS_REGION from --region, defaulting to the instance's configured region.
		if os.Getenv("AWS_REGION") == "" && os.Getenv("AWS_DEFAULT_REGION") == "" {
			return nil, fmt.Errorf("%w: no AWS region set; pass --region to the wrapper script, which exports AWS_REGION (§11.3)", errObsUsage)
		}
		api, err := awsclient.NewDynamoDB(ctx)
		if err != nil {
			return nil, err
		}
		table := systemid.TableName(cfg.Instance)
		dyn, err := repository.NewDynamo(api, table)
		if err != nil {
			return nil, err
		}
		repo = dyn
		source = "dynamodb:" + table
	}

	ttlDays := 0
	if cfg != nil && cfg.Retention.AuditDays != nil {
		ttlDays = *cfg.Retention.AuditDays
	}
	auditor := audit.New(repo, clk, gen, obsLogger(), ttlDays)

	// **Before the read, and fail closed.** audit.Record's contract: a crash between
	// serving data and recording the access produces the gap §2A.1 calls unrepairable, so
	// the record goes first; and "do not serve user content when this returns non-nil",
	// because an unrecorded access is exactly what I13 forbids. Nothing is lost by
	// refusing — the operator re-runs a read-only report.
	if err := auditor.Allowed(ctx, audit.Access{
		Tenant:   tenant,
		Actor:    f.as,
		Action:   action,
		Resource: resource,
		// No IP and no UA: there is no request. audit.Access documents empty as "no
		// address was available — a script invocation", which is distinct from the
		// "invalid" marker a bad claim gets, so the log can tell a script apart from a
		// forged header.
	}); err != nil {
		return nil, fmt.Errorf("refusing to read without an audit record (I13): %w", err)
	}

	ownID := gen.last
	ownKey, err := keys.Audit(tenant, ownID)
	if err != nil {
		// Unreachable unless the generator emitted something keys refuses — which
		// Record would already have failed on. Surfaced rather than ignored because a
		// wrong ownAuditID would silently exclude some OTHER tenant's-log record from
		// the answer to a compliance question.
		return nil, fmt.Errorf("resolving this invocation's own audit key: %w", err)
	}

	return &obsEnv{
		tenant:     tenant,
		repo:       repo,
		cfg:        cfg,
		clk:        clk,
		source:     source,
		ownAuditID: ownKey.SK,
	}, nil
}

// ---------------------------------------------------------------------------
// Money
// ---------------------------------------------------------------------------
//
// Integer micros throughout, never float. §Phase 0 requires summed cost_micros to match
// a provider's reported cost within 5%, and a tolerance is meaningless if the arithmetic
// under it drifts: float money accumulates binary rounding error across thousands of
// records and then the reconciliation is measuring the adder, not the provider. The only
// float that appears anywhere near money is a price in a config file (§7.1's
// cost_per_hour_usd), converted to micros exactly once at that boundary.

const obsMicrosPerUSD = 1_000_000

// obsFormatUSD renders micros as a fixed six-decimal USD figure.
//
// All six decimals, always. Trimming to cents would hide the precision the 5%
// reconciliation is computed at, and at this scale the interesting figures — a single
// STT request at $0.000111 — are entirely below the cent.
func obsFormatUSD(micros int64) string {
	sign := ""
	if micros < 0 {
		sign = "-"
		// Negation before the split so the fractional part is not negative. math.MinInt64
		// has no positive counterpart, so it is clamped rather than silently wrapped to
		// itself — a negative cost is already a defect upstream (meter refuses one).
		if micros == math.MinInt64 {
			micros = math.MaxInt64
		} else {
			micros = -micros
		}
	}
	return fmt.Sprintf("%s%d.%06d", sign, micros/obsMicrosPerUSD, micros%obsMicrosPerUSD)
}

// obsParseUSD converts a decimal USD string to exact micros.
//
// Hand-parsed rather than strconv.ParseFloat, and that is the point: ParseFloat("0.07")
// yields a value that is not 0.07, so 0.07 * 1e6 is 69999.999... and a bill entered as
// 0.07 would reconcile against a metered 70000 with a one-micro discrepancy that has no
// cause in the data. Splitting on the decimal point and reading two integers cannot do
// that.
//
// Sub-micro precision is refused rather than rounded: no bill is denominated below a
// micro-dollar, so more than six decimal places means the value came from something other
// than an invoice — a float printed at full precision, most likely — and rounding it would
// launder that into a figure that looks authoritative.
func obsParseUSD(s string) (int64, error) {
	in := strings.TrimSpace(s)
	in = strings.TrimPrefix(in, "$")
	if in == "" {
		return 0, fmt.Errorf("empty amount; expected a decimal USD figure such as 0.40")
	}
	// Bounded before anything quotes it. Every message below reports the value, and a
	// refusal that echoes an unbounded argument is the mistake internal/audit made twice
	// (§9.2): a caller passing the wrong variable gets the whole of it back in an error that
	// something then logs. No billed figure needs 32 bytes.
	if len(in) > 32 {
		return 0, fmt.Errorf("amount is %d bytes, far longer than any billed figure; expected something like 0.40", len(in))
	}
	neg := false
	if rest, ok := strings.CutPrefix(in, "-"); ok {
		neg, in = true, rest
	}
	whole, frac, hasFrac := strings.Cut(in, ".")
	if whole == "" {
		return 0, fmt.Errorf("amount %q has no digits before the decimal point; write 0.40 rather than .40", s)
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("amount %q is not a plain decimal figure (no exponents, no separators): %w", s, err)
	}
	if w < 0 {
		// The sign was already consumed above, so a negative here means a second one:
		// "--1" would otherwise negate twice and read as +1.00, turning a typo into a
		// figure with the wrong sign and no complaint.
		return 0, fmt.Errorf("amount %q carries more than one sign", s)
	}
	if w > math.MaxInt64/obsMicrosPerUSD {
		return 0, fmt.Errorf("amount %q is too large to express in micros", s)
	}
	micros := w * obsMicrosPerUSD
	if hasFrac {
		if frac == "" || len(frac) > 6 {
			return 0, fmt.Errorf("amount %q has %d decimal places; USD is expressed to at most 6 (micro-dollars), and a longer value is a float printed at full precision rather than a billed figure", s, len(frac))
		}
		f, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("amount %q has a non-numeric fractional part: %w", s, err)
		}
		if f < 0 {
			return 0, fmt.Errorf("amount %q has a signed fractional part", s)
		}
		// Left-justify to micros: "4" is 0.4 USD = 400000 micros, not 4.
		for i := len(frac); i < 6; i++ {
			f *= 10
		}
		micros += f
	}
	if neg {
		micros = -micros
	}
	return micros, nil
}

// obsShortArg quotes a rejected argument only while it is short enough to be one.
//
// The same rule internal/audit's describeRejected follows, for the same reason: an error
// message is a thing that gets logged, wrapped, or printed, so it must describe an
// unexpected value rather than reproduce it. A month is 7 bytes; anything past 32 is a
// different variable.
func obsShortArg(v string) string {
	if len(v) > 32 {
		return fmt.Sprintf("(%d bytes)", len(v))
	}
	return strconv.Quote(v)
}

// obsVarianceBP expresses (metered - actual) as basis points of actual.
//
// Basis points and not a percentage float, for the same reason the totals are micros:
// §Phase 0's tolerance is 5%, which is 500 bp exactly, and an integer comparison has no
// boundary ambiguity. Returns ok=false when actual is zero — a variance against nothing
// is undefined, and reporting it as 0% or as infinity would both be wrong. The caller
// treats that case as a finding in its own right: actual spend with no metering records
// is an unmetered provider path (I12).
func obsVarianceBP(metered, actual int64) (int64, bool) {
	if actual == 0 {
		return 0, false
	}
	diff := metered - actual
	neg := diff < 0
	if neg {
		diff = -diff
	}
	abs := actual
	if abs < 0 {
		abs = -abs
	}
	// Rounded rather than truncated so a reported figure and the tolerance test below
	// cannot disagree about a value sitting on the boundary.
	bp := (diff*10000 + abs/2) / abs
	if neg {
		return -bp, true
	}
	return bp, true
}

// obsWithinTolerance answers the §Phase 0 question directly, in integers.
//
// Compared as a cross-multiplication rather than against the rounded basis-point figure:
// rounding first makes a variance of 5.004% pass because it rounds to 500 bp, and a
// tolerance whose boundary depends on a display rounding is not a tolerance.
func obsWithinTolerance(metered, actual, toleranceBP int64) bool {
	if actual == 0 {
		return metered == 0
	}
	diff := metered - actual
	if diff < 0 {
		diff = -diff
	}
	abs := actual
	if abs < 0 {
		abs = -abs
	}
	return diff*10000 <= toleranceBP*abs
}

// ---------------------------------------------------------------------------
// Fixtures (§11.5)
// ---------------------------------------------------------------------------

// obsFixture is the CI record source: usage and audit records, each carrying its own
// tenant.
//
// Records carry a tenant individually rather than the file declaring one, so a fixture can
// hold two tenants and a test can assert that a report for one contains nothing of the
// other's — §Phase 0 acceptance requires that cross-tenant test "directly against the data
// layer, not only through the API", and this is the data layer the scripts read.
type obsFixture struct {
	// Comment carries the fixture's own explanation of what it is for. A named field
	// because unknown fields are refused below, and a fixture that cannot say why its
	// numbers are what they are is a fixture someone will "correct" (see records.json,
	// whose figures are chosen to make a per-request billing minimum observable).
	Comment []string `json:"_comment"`

	Usage []obsFixtureUsage `json:"usage"`
	Audit []obsFixtureAudit `json:"audit"`
}

type obsFixtureUsage struct {
	Tenant string `json:"tenant"`
	// Month is optional: derived from TS when absent, because a fixture whose month and
	// timestamp disagree is a trap rather than a test.
	Month      string  `json:"month"`
	ID         string  `json:"id"`
	Unit       string  `json:"unit"`
	Quantity   float64 `json:"quantity"`
	Provider   string  `json:"provider"`
	CostMicros int64   `json:"cost_micros"`
	Op         string  `json:"op"`
	TS         string  `json:"ts"`
}

type obsFixtureAudit struct {
	Tenant   string `json:"tenant"`
	ID       string `json:"id"`
	Actor    string `json:"actor"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
	IP       string `json:"ip"`
	UA       string `json:"ua"`
	Result   string `json:"result"`
	TS       string `json:"ts"`
}

// obsLoadFixture seeds an in-memory repository from a JSON fixture.
//
// **Takes *repository.Memory, not repository.Repository, and that signature is the
// control.** This is the one place in the observability tooling that writes records other
// than the invocation's own audit entry, and writing a usage or audit record outside
// meter/audit would be an I12/I13 hole if it could ever reach a real table. Taking the
// concrete fake makes that structurally impossible rather than a matter of remembering:
// there is no call site that could pass the DynamoDB adapter in.
//
// The attribute maps mirror what meter.Record and audit.Record write, deliberately
// including the Go types — cost_micros as int64, quantity as float64 — so a reader that
// asserts a type directly instead of going through repository.AsInt64/AsFloat64 fails here
// the way it would fail in production (G-074).
func obsLoadFixture(path string, into *repository.Memory) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading fixture: %w", err)
	}
	var fx obsFixture
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	// Unknown fields are refused: a fixture with a mistyped key that silently seeds
	// nothing would make a test assert against an empty store and pass.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fx); err != nil {
		return fmt.Errorf("parsing fixture %s: %w", path, err)
	}

	ctx := context.Background()
	for i, u := range fx.Usage {
		month := u.Month
		if month == "" {
			if len(u.TS) < 7 {
				return fmt.Errorf("fixture usage[%d]: neither month nor a usable ts", i)
			}
			month = u.TS[:7]
		}
		key, err := keys.Usage(keys.TenantID(u.Tenant), month, u.Unit, u.ID)
		if err != nil {
			return fmt.Errorf("fixture usage[%d]: %w", i, err)
		}
		if err := into.PutOnce(ctx, repository.Item{
			Key: key,
			Attrs: map[string]any{
				"unit":        u.Unit,
				"quantity":    u.Quantity,
				"provider":    u.Provider,
				"cost_micros": u.CostMicros,
				"op":          u.Op,
				"ts":          u.TS,
			},
		}); err != nil {
			return fmt.Errorf("fixture usage[%d]: %w", i, err)
		}
	}
	for i, a := range fx.Audit {
		key, err := keys.Audit(keys.TenantID(a.Tenant), a.ID)
		if err != nil {
			return fmt.Errorf("fixture audit[%d]: %w", i, err)
		}
		if err := into.PutOnce(ctx, repository.Item{
			Key: key,
			Attrs: map[string]any{
				"actor":    a.Actor,
				"action":   a.Action,
				"resource": a.Resource,
				"ip":       a.IP,
				"ua":       a.UA,
				"result":   a.Result,
				"ts":       a.TS,
			},
		}); err != nil {
			return fmt.Errorf("fixture audit[%d]: %w", i, err)
		}
	}
	return nil
}

// obsEmitJSON writes one JSON document to the given writer.
//
// Takes an io.Writer rather than assuming stdout so the report builders stay testable
// without capturing a process-wide stream — and so a caller cannot accidentally send a
// report to stderr, where a --json consumer would not see it.
func obsEmitJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// obsFail prints one diagnostic to stderr and maps the error to an exit code.
//
// stderr, so --json output on stdout stays parseable even on the failure path — the
// convention common.sh follows for the same reason.
func obsFail(name string, err error) int {
	fmt.Fprintf(os.Stderr, "chintanctl %s: %v\n", name, err)
	if errors.Is(err, errObsUsage) {
		return obsExitUsage
	}
	return obsExitFailure
}
