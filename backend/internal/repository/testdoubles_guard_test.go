package repository_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The in-memory store and the fake providers live in packages of their own, not
// inside the production package where they would be one wiring mistake away
// from being used for real. This asserts the property that makes that worth
// anything: the API binary does not link them.
//
// A Go binary contains exactly the packages reachable from main, so "not in the
// import graph" is "not in the binary".
func TestProductionBinaryDoesNotLinkTestDoubles(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}

	out, err := exec.Command(goBin, "list", "-deps", "github.com/vppillai/chintan/backend/cmd/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	forbidden := []string{
		"github.com/vppillai/chintan/backend/internal/repository/memory",
		"github.com/vppillai/chintan/backend/internal/provider/fake",
	}
	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, dep := range deps {
		for _, bad := range forbidden {
			if strings.TrimSpace(dep) == bad {
				t.Errorf("test double %s is reachable from cmd/ and therefore ships in the production binary", bad)
			}
		}
	}
}
