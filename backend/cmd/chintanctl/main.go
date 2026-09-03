// Command chintanctl is the operator CLI for a Chintan instance.
//
// It exists because DynamoDB has point-in-time recovery and S3 has nothing:
// before this command there was no backup, no export and no recovery path for
// the notes, transcripts and audio that are the product. That is the audit
// finding it closes (docs/audit/2026-08-07-production-readiness-audit.md §5)
// and the success criterion it satisfies — "every note, transcript, and audio
// file is recoverable through chintanctl export without console access".
//
//	chintanctl export    --instance <name> --out <dir|tar.gz>
//	chintanctl backup    --instance <name> --out <dir>
//	chintanctl restore   --instance <name> --in <dir> [--apply]
//	chintanctl reconcile --instance <name> [--apply]
//	chintanctl reindex   --instance <name> [--apply]
//	chintanctl erase     --instance <name> --tenant <id> [--apply]
//
// Three conventions are load-bearing and shared with scripts/:
//
//   - Dry run is the default. Nothing mutates without --apply.
//   - Results go to stdout, diagnostics to stderr, so --json stays parseable.
//   - No user content is ever logged. Keys, counts and sizes are shape, not
//     content; transcripts and note bodies are streamed to files and never
//     into a log line.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/vppillai/chintan/backend/internal/repository"
)

const usageText = `chintanctl — operator CLI for a Chintan instance

Usage:
  chintanctl <command> [flags]

Commands:
  export     Write every note as markdown with front matter, plus each
             capture's audio and text artifacts, in an Obsidian-friendly layout.
  backup     Full-fidelity copy: DynamoDB items verbatim alongside the S3
             objects, with a content hash per object.
  restore    Inverse of backup. Verifies every hash before it writes anything.
  reindex    Write the gsi2 key attributes onto notes that lack them. Adding a
             global secondary index does not index the rows already in the
             table, so run this once after the deploy that adds one or the
             notes list comes back empty. Idempotent.
  reconcile  Report orphans in both directions: objects with no index row, and
             index rows whose objects are gone.
  erase      Irreversibly delete one tenant everywhere, and report what went.

Run "chintanctl <command> --help" for the flags of one command.
`

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.Stdin); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// globalFlags are the flags every subcommand shares.
type globalFlags struct {
	instance    string
	environment string
	region      string
	table       string
	bucket      string
	account     string
	tenants     stringList
	jsonOut     bool
	verbose     bool
	apply       bool
}

// stringList collects a repeatable flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("empty value")
	}
	*s = append(*s, v)
	return nil
}

func (g *globalFlags) register(fs *flag.FlagSet, withTenants, withApply bool) {
	fs.StringVar(&g.instance, "instance", "", "instance name, as in config/instances/<name>.yaml (required)")
	fs.StringVar(&g.environment, "environment", "prod", "stack environment: prod, staging or dev")
	fs.StringVar(&g.region, "region", "", "AWS region (defaults to the ambient configuration)")
	fs.StringVar(&g.table, "table", "", "override the derived DynamoDB table name")
	fs.StringVar(&g.bucket, "bucket", "", "override the derived content bucket name")
	fs.StringVar(&g.account, "account", "", "AWS account id, when STS may not be called")
	fs.BoolVar(&g.jsonOut, "json", false, "emit the result as JSON on stdout")
	fs.BoolVar(&g.verbose, "verbose", false, "log at debug level on stderr")
	if withTenants {
		fs.Var(&g.tenants, "tenant", "limit to this tenant id; repeatable. Defaults to every tenant found in the bucket")
	}
	if withApply {
		fs.BoolVar(&g.apply, "apply", false, "perform the changes. Without it, the command reports what it would do")
	}
}

func (g *globalFlags) validate() error {
	if g.instance == "" {
		return errors.New("--instance is required")
	}
	switch g.environment {
	case "prod", "staging", "dev":
	default:
		return fmt.Errorf("--environment %q must be one of prod, staging, dev", g.environment)
	}
	return nil
}

// env is everything a subcommand needs. Tests construct it directly with
// in-memory ports; main wires the AWS ones.
type env struct {
	Part  Partition
	Blobs Blobs
	// Notes is the note-index maintenance the repository owns. It is a
	// narrow interface rather than the Partition port because reindexing has
	// to be a conditional attribute update: Partition.Put overwrites an item
	// verbatim, so a Scan-then-Put would silently discard an edit made
	// between the two — and this tool is run against a live instance while
	// somebody is using it.
	Notes  NoteIndexMaintainer
	Target target
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	// Usage printed alongside an error is discarded deliberately: the error
	// being returned already says what went wrong, and replacing it with "could
	// not write to stderr" would hide the thing the operator needs to read.
	// The --help path below is different — there the write is the whole result.
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usageText)
		return errors.New("no command given")
	}
	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "-h", "--help", "help":
		_, err := fmt.Fprint(stdout, usageText)
		return err
	case "export":
		return cmdExport(ctx, rest, stdout, stderr, stdin)
	case "backup":
		return cmdBackup(ctx, rest, stdout, stderr, stdin)
	case "restore":
		return cmdRestore(ctx, rest, stdout, stderr, stdin)
	case "reconcile":
		return cmdReconcile(ctx, rest, stdout, stderr, stdin)
	case "reindex":
		return cmdReindex(ctx, rest, stdout, stderr, stdin)
	case "erase":
		return cmdErase(ctx, rest, stdout, stderr, stdin)
	default:
		_, _ = fmt.Fprint(stderr, usageText)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// NoteIndexMaintainer repairs the notes index. The store implements it; the
// command below only decides which tenants to run it for.
type NoteIndexMaintainer interface {
	ReindexNotes(ctx context.Context, tenantID string) (int, error)
}

// dial resolves the instance and returns a live env. Every subcommand goes
// through it, so there is one place where credentials are picked up.
func dial(ctx context.Context, g globalFlags, stdout, stderr io.Writer, stdin io.Reader) (*env, error) {
	if err := g.validate(); err != nil {
		return nil, err
	}
	setupLogging(g.verbose)
	t, dyn, s3c, err := resolveTarget(ctx, g)
	if err != nil {
		return nil, err
	}
	return &env{
		Part:   &dynamoPartition{client: dyn, table: t.Table},
		Blobs:  &s3Blobs{client: s3c, bucket: t.Bucket},
		Notes:  repository.NewDynamoStore(dyn, t.Table),
		Target: t,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	}, nil
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("chintanctl "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}
