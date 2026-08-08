package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// setupLogging installs a structured logger on stderr.
//
// It does not call obs.Setup, which writes to stdout. That is right for a
// Lambda, where stdout is CloudWatch, and wrong for a CLI, where stdout is the
// result: scripts/lib/common.sh already fixed this convention — "diagnostics go
// to stderr so --json output on stdout stays parseable" — and a log line
// interleaved into a JSON document would break every caller that pipes this
// into jq.
//
// obs.Log still works: with obs.Setup uncalled it falls back to
// slog.Default(), so every log line still carries the correlation id and
// tenant that the context holds.
func setupLogging(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

// result is what a subcommand produces. Human output and JSON output are the
// same data, so --json can never drift from what the operator was told.
type result interface {
	human(w io.Writer)
}

func report(w io.Writer, asJSON bool, r result) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	r.human(w)
	return nil
}

// dryRunBanner is the last line of every destructive command that did not run
// with --apply. It matches the wording the shell scripts use, because an
// operator should not have to learn two vocabularies for the same idea.
func dryRunBanner(w io.Writer, apply bool, action string) {
	if apply {
		return
	}
	fmt.Fprintf(w, "\nDRY RUN — nothing was changed.\n")
	fmt.Fprintf(w, "  Would: %s\n", action)
	fmt.Fprintf(w, "  Re-run with --apply to execute.\n")
}

// confirmTyped demands that the operator type an exact string before an
// irreversible action proceeds.
//
// A y/n prompt is muscle memory. Typing the tenant id is not: it requires
// having read which tenant is about to be destroyed. --confirm carries the
// same string for unattended use, and is checked just as strictly, so
// automation cannot skip the check by omitting it.
func confirmTyped(in io.Reader, out io.Writer, supplied, want, action string) error {
	if supplied != "" {
		if supplied != want {
			return fmt.Errorf("--confirm %q does not match %q; refusing to %s", supplied, want, action)
		}
		return nil
	}
	fmt.Fprintf(out, "\nAbout to %s. This cannot be undone.\n", action)
	fmt.Fprintf(out, "Type %q to continue: ", want)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !strings.HasSuffix(line, "\n") && line == "" {
		return fmt.Errorf("confirmation not supplied: %w", err)
	}
	if strings.TrimSpace(line) != want {
		return fmt.Errorf("confirmation did not match %q; refusing to %s", want, action)
	}
	return nil
}

// humanBytes renders a size for a summary line. Sizes are shape, not content,
// so they are always safe to print.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
