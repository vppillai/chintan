package repository_test

import (
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// deployable are the main packages scripts/build-lambda.sh packages for
// Lambda, read off its `package ./cmd/<name>` lines, plus the operator's CLI.
// cmd/local wires the doubles on purpose and must never join this list.
func deployable(t *testing.T) []string {
	t.Helper()
	script, err := os.ReadFile("../../../scripts/build-lambda.sh")
	if err != nil {
		t.Fatal(err)
	}
	var pkgs []string
	for _, m := range regexp.MustCompile(`(?m)^\s*package \./cmd/([a-z]+)\b`).FindAllStringSubmatch(string(script), -1) {
		pkgs = append(pkgs, m[1])
	}
	slices.Sort(pkgs)
	if want := []string{"api", "worker"}; !slices.Equal(pkgs, want) {
		t.Fatalf("scripts/build-lambda.sh packages %v, want exactly %v: cmd/local is the development path and never a Lambda", pkgs, want)
	}
	return append(pkgs, "chintanctl")
}

// The fake table, the in-memory objects and the fake providers live in packages
// of their own, not inside the production package where they would be one
// wiring mistake away from being used for real. This asserts the property that makes that worth
// anything: the deployed binaries and the operator CLI do not link them.
// cmd/local does, by design, and is kept out of the Lambda build by the
// list this reads.
//
// A Go binary contains exactly the packages reachable from main, so "not in the
// import graph" is "not in the binary".
func TestProductionBinaryDoesNotLinkTestDoubles(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}

	args := []string{"list", "-deps"}
	for _, pkg := range deployable(t) {
		args = append(args, "github.com/vppillai/chintan/backend/cmd/"+pkg)
	}
	out, err := exec.Command(goBin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	forbidden := []string{
		"github.com/vppillai/chintan/backend/internal/repository/dynamofake",
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
