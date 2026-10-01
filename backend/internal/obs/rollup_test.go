package obs

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

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
	raw, err := os.ReadFile("../../../infrastructure/template.yaml")
	if err != nil {
		t.Fatalf("read the template: %v", err)
	}
	alarmed := regexp.MustCompile(`(?m)^\s+Namespace: Chintan\n\s+MetricName: (\w+)`).FindAllStringSubmatch(string(raw), -1)
	if len(alarmed) == 0 {
		t.Fatal("found no Chintan-namespace alarm in the template; the pattern is stale")
	}

	var rolled, plain strings.Builder
	err = filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(src), "\n") {
			switch {
			case strings.Contains(line, "WithRollup("):
				rolled.WriteString(line + "\n")
			case strings.Contains(line, "obs.Count(") || strings.Contains(line, "obs.Emit("):
				plain.WriteString(line + "\n")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the sources: %v", err)
	}

	for _, m := range alarmed {
		if !strings.Contains(rolled.String(), `"`+m[1]+`"`) {
			t.Errorf("an alarm reads Chintan/%s with no dimensions, but nothing emits it through CountWithRollup", m[1])
		}
		if strings.Contains(plain.String(), `"`+m[1]+`"`) {
			t.Errorf("Chintan/%s is alarmed, but at least one site still emits it through Count or Emit, which the alarm's dimensionless sum never sees", m[1])
		}
	}
}
