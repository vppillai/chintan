package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/vppillai/chintan/backend/internal/config"
	"github.com/vppillai/chintan/backend/internal/version"
)

const configUsage = `chintanctl config validate <dir|file>

Validate instance configuration against the schema (§7.4).

This is the same validator the Lambda runs at cold start. Running one
implementation in both places is deliberate: a separate CI validator could
disagree with the runtime one, and the disagreement would surface as a
cold-start failure on a config that had already passed its gate (§11.2).

Read-only. No --apply, because it mutates nothing.

Examples:
  chintanctl config validate ../config/instances
  chintanctl config validate ../config/instances/dev.yaml --json
`

func runConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, configUsage)
		return 2
	}
	switch args[0] {
	case "validate":
		return runConfigValidate(args[1:])
	case "-h", "--help":
		fmt.Print(configUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "chintanctl config: unknown subcommand %q\n\n%s", args[0], configUsage)
		return 2
	}
}

// validateResult is the --json shape, so CI and the agent parse structured
// results rather than scraping human-formatted text (§11.3).
type validateResult struct {
	OK        bool             `json:"ok"`
	Instances []instanceResult `json:"instances"`
}

type instanceResult struct {
	Instance      string   `json:"instance"`
	Path          string   `json:"path"`
	OK            bool     `json:"ok"`
	Problems      []string `json:"problems,omitempty"`
	ActiveSTT     string   `json:"active_stt,omitempty"`
	PromptBiasing *bool    `json:"prompt_biasing,omitempty"`
}

func runConfigValidate(args []string) int {
	fs := newFlagSet("config validate", configUsage)
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, configUsage)
		return 2
	}
	target := fs.Arg(0)

	info, err := os.Stat(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "chintanctl config validate: %v\n", err)
		return 1
	}

	var result validateResult
	result.OK = true

	if info.IsDir() {
		cfgs, err := config.LoadDir(target)
		if err != nil {
			// LoadDir joins every problem across every file, so the operator
			// sees the whole list rather than discovering the next one after
			// each redeploy.
			if *asJSON {
				emitJSON(validateResult{OK: false, Instances: []instanceResult{{
					Path: target, OK: false, Problems: splitJoined(err),
				}}})
			} else {
				fmt.Fprintf(os.Stderr, "%v\n", err)
			}
			return 1
		}
		names := make([]string, 0, len(cfgs))
		for name := range cfgs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			result.Instances = append(result.Instances, describe(cfgs[name]))
		}
	} else {
		cfg, err := config.Load(target)
		if err != nil {
			if *asJSON {
				emitJSON(validateResult{OK: false, Instances: []instanceResult{{
					Path: target, OK: false, Problems: splitJoined(err),
				}}})
			} else {
				fmt.Fprintf(os.Stderr, "%v\n", err)
			}
			return 1
		}
		result.Instances = append(result.Instances, describe(cfg))
	}

	if *asJSON {
		emitJSON(result)
		return 0
	}

	for _, r := range result.Instances {
		fmt.Printf("ok   %-6s %s\n", r.Instance, r.Path)
		fmt.Printf("       active stt: %s", r.ActiveSTT)
		if r.PromptBiasing != nil && !*r.PromptBiasing {
			// Worth surfacing rather than leaving in the file: with biasing
			// absent, Phase 4's corrections must route to the LLM cleanup layer
			// instead of silently producing none (§7.1, G-042).
			fmt.Printf("  (no prompt biasing — corrections route to the LLM layer, §7.1)")
		}
		fmt.Println()
	}
	fmt.Printf("\n%d instance(s) valid.\n", len(result.Instances))
	return 0
}

func describe(cfg *config.Config) instanceResult {
	biasing := cfg.PromptBiasingAvailable()
	return instanceResult{
		Instance:      cfg.Instance,
		Path:          cfg.SourcePath(),
		OK:            true,
		ActiveSTT:     cfg.Providers.STT.Active,
		PromptBiasing: &biasing,
	}
}

// splitJoined turns a joined or multi-line error into a list, so --json output
// is a JSON array of problems rather than one string with embedded newlines.
func splitJoined(err error) []string {
	msg := err.Error()
	var out []string
	cur := ""
	for _, r := range msg {
		if r == '\n' {
			if cur != "" {
				out = append(out, trimBullet(cur))
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, trimBullet(cur))
	}
	return out
}

func trimBullet(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '-' || s[0] == '\t') {
		s = s[1:]
	}
	return s
}

func emitJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func runVersion(args []string) int {
	fs := newFlagSet("version", "chintanctl version\n\nPrint build version information (§0.6).\n")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *asJSON {
		emitJSON(map[string]any{
			"version":     version.Display(),
			"commit":      version.Commit,
			"build_time":  version.BuildTime,
			"stamped":     version.Stamped(),
			"cache_token": version.CacheToken(),
		})
		return 0
	}
	fmt.Printf("version:     %s\n", version.Display())
	fmt.Printf("commit:      %s\n", version.Commit)
	fmt.Printf("build time:  %s\n", version.BuildTime)
	fmt.Printf("cache token: %s\n", version.CacheToken())
	if !version.Stamped() {
		// G-036: a tag pushed after the deploy workflow ran does not reach the
		// artifact, so an unstamped build is a real and recurring situation
		// rather than a theoretical one.
		fmt.Println("\nwarning: this build carries no version information.")
		fmt.Println("         Build through scripts/build-lambda.sh, and tag before deploying (G-036).")
	}
	return 0
}
