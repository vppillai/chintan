package repository_test

import (
	"os/exec"
	"strings"
	"testing"
)

// In v1 the in-memory store (435 lines) and the fake providers shipped inside
// the production package, one wiring mistake away from being used for real.
// They now live in packages of their own, and this asserts the property that
// makes that worth anything: the API binary does not link them.
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
