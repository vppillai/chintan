// Command chintanctl is the compiled admin binary (§11.2).
//
// §11.2 splits operations by weight: lifecycle and infrastructure work stays in
// bash with the AWS CLI, and anything carrying real logic — pipeline stages,
// embedding math, rule scoring, config validation — is a subcommand here,
// importing the backend's own service layer. The reason is stated in the spec
// and worth keeping in view: that logic belongs in tested application code
// rather than bash, and a future admin UI then calls the same module instead of
// reimplementing it.
//
// The bash wrappers in scripts/ own only argument parsing, confirmation
// prompts, and output formatting. No business logic in bash.
//
// Conventions (§11.3) are honoured by every subcommand: --help, --json, and
// --dry-run as the DEFAULT for anything destructive or costly, with --apply to
// execute. Read-only subcommands have no --apply and need none.
package main

import (
	"flag"
	"fmt"
	"os"
)

// usage lists subcommands. Kept as one string so `chintanctl` with no arguments
// is genuinely useful rather than an error code.
const usage = `chintanctl — Chintan administrative operations

Usage:
  chintanctl <command> [subcommand] [flags]

Commands:
  config validate <dir|file>   Validate instance configuration (§7.4)
  version                      Print build version information

Every command supports --help. Commands that mutate state or spend provider
money default to --dry-run and require --apply to execute (§11.3).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	// Exit code 2 for usage errors, 1 for operational failures — so a caller can
	// tell "I invoked this wrongly" from "the thing I asked for failed", which
	// matters when the caller is a CI step rather than a person.
	switch os.Args[1] {
	case "config":
		os.Exit(runConfig(os.Args[2:]))
	case "version":
		os.Exit(runVersion(os.Args[2:]))
	case "-h", "--help", "help":
		fmt.Print(usage)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "chintanctl: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func newFlagSet(name, help string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, help)
		fmt.Fprintln(os.Stderr, "\nFlags:")
		fs.PrintDefaults()
	}
	return fs
}
