package obs

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// emitCalls returns every obs metric call in the non-test sources under
// backend (internal and cmd), one string per call, joined across lines: the rollup
// calls, the plain ones (Count, Emit, Duration) and the metric names each
// names. A call that spans lines — purge's Emit lists its two metrics one
// per line — used to be invisible to a line-by-line scan, which is how an
// alarmed metric could hide behind a plain Emit.
func emitCalls(t *testing.T) (rolled, plain []string, names []string) {
	t.Helper()
	call := regexp.MustCompile(`obs\.(Count|CountWithRollup|Duration|Emit|EmitWithRollup)\(`)
	name := regexp.MustCompile(`(?:obs\.(?:Count|CountWithRollup|Duration)\(ctx, |Name:\s*)"(\w+)"`)
	seen := map[string]bool{}
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		for _, loc := range call.FindAllStringIndex(src, -1) {
			// The call runs from its opening parenthesis to the matching one.
			depth, end := 0, loc[1]-1
			for i := loc[1] - 1; i < len(src); i++ {
				if src[i] == '(' {
					depth++
				} else if src[i] == ')' {
					if depth--; depth == 0 {
						end = i
						break
					}
				}
			}
			text := src[loc[0] : end+1]
			if strings.Contains(text, "WithRollup(") {
				rolled = append(rolled, text)
			} else {
				plain = append(plain, text)
			}
			for _, m := range name.FindAllStringSubmatch(text, -1) {
				seen[m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the sources: %v", err)
	}
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return rolled, plain, names
}

// alarmedMetrics is every Chintan-namespace metric an alarm in the template
// reads with no Dimensions, as a set of exact names: the alarms that read the
// dimensionless identity only the rollup publishes. An alarm with Dimensions
// reads one dimensioned identity, which plain Count publishes; it is held
// instead by TestEveryEmittedMetricIsAlarmedOrListed to a metrics.md row.
func alarmedMetrics(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("../../../infrastructure/template.yaml")
	if err != nil {
		t.Fatalf("read the template: %v", err)
	}
	out := map[string]bool{}
	// The template writes an alarm's Dimensions, when it has them, on the
	// line after MetricName; a Dimensions block placed anywhere else would
	// read here as a dimensionless alarm and be held to the rollup, which
	// is the strict direction to be wrong in.
	for _, m := range regexp.MustCompile(`(?m)^\s+Namespace: Chintan\n\s+MetricName: (\w+)\n(\s+Dimensions:)?`).FindAllStringSubmatch(string(raw), -1) {
		if m[2] != "" {
			continue
		}
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatal("found no Chintan-namespace alarm in the template; the pattern is stale")
	}
	return out
}

// TestEveryAlarmedMetricIsRolledUp ties the template's Chintan-namespace
// alarms to the code that emits their metrics.
//
// Every such alarm is a plain metric alarm with no Dimensions, so it reads only
// the dimensionless identity, which obs.CountWithRollup publishes and obs.Count
// does not. A counter emitted through Count with any dimension leaves the alarm
// watching a metric that never exists — green forever, on exactly the failure
// it was added for.
//
// The second half is the same trap at a metric with several emission sites:
// one site switched to the rollup and the others left on Count gives the alarm
// a metric that exists and under-counts, which is harder to notice than one
// that is missing. So no alarmed metric may be emitted through plain Count
// anywhere.
func TestEveryAlarmedMetricIsRolledUp(t *testing.T) {
	rolled, plain, _ := emitCalls(t)
	for m := range alarmedMetrics(t) {
		if !strings.Contains(strings.Join(rolled, "\n"), `"`+m+`"`) {
			t.Errorf("an alarm reads Chintan/%s with no dimensions, but nothing emits it through CountWithRollup", m)
		}
		if strings.Contains(strings.Join(plain, "\n"), `"`+m+`"`) {
			t.Errorf("Chintan/%s is alarmed, but at least one site still emits it through Count, Duration or Emit, which the alarm's dimensionless sum never sees", m)
		}
	}
}

// TestEveryEmittedMetricIsAlarmedOrListed is the reverse direction: every
// metric name the code emits is either alarmed in the template or listed,
// with its reader, in docs/ops/metrics.md. Rounds 6–8 each added counters
// nothing read — write-only EMF that cost a metric identity apiece and told
// nobody anything (review 2026-10-01, BE-3; decision D6). A new counter
// therefore arrives with its alarm or with the sentence that says who reads
// it, or does not arrive.
func TestEveryEmittedMetricIsAlarmedOrListed(t *testing.T) {
	_, _, names := emitCalls(t)
	if len(names) < 20 {
		t.Fatalf("found only %d metric names in the sources; the pattern is stale", len(names))
	}
	alarmed := alarmedMetrics(t)
	page, err := os.ReadFile("../../../docs/ops/metrics.md")
	if err != nil {
		t.Fatalf("read docs/ops/metrics.md: %v", err)
	}
	for _, n := range names {
		// A table row, not a mention: the page's prose names the alarmed
		// metrics too, and a sentence is not a listing with a reader.
		listed := regexp.MustCompile("(?m)^\\| `" + n + "` \\|").Match(page)
		if !alarmed[n] && !listed {
			t.Errorf("Chintan/%s is emitted but no alarm reads it and docs/ops/metrics.md does not list it: alarm it, list it with its reader, or delete it (D6)", n)
		}
	}
}
