package provider

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// providerBounds is the table bounds.go is held to: every constant it
// declares, with the source text it must hold. The text is repeated here on
// purpose, as routing's rule table repeats its literals: changing a bound is
// two edits, the number and its row, which is the review this test forces.
var providerBounds = map[string]string{
	"ProviderRetryAttempts":    "3",
	"ProviderRetryAttemptsAsk": "2",
	"ProviderRetryBaseWait":    "1 * time.Second",
	"ProviderRetryMaxWait":     "4 * time.Second",
}

// TestProviderBoundsAreRegistered holds bounds.go to the table both ways: a
// constant added without a row fails, and so does a value changed in one
// place.
func TestProviderBoundsAreRegistered(t *testing.T) {
	src, err := os.ReadFile("bounds.go")
	if err != nil {
		t.Fatalf("read bounds.go: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bounds.go", src, 0)
	if err != nil {
		t.Fatalf("parse bounds.go: %v", err)
	}
	declared := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					t.Errorf("%s has no value of its own; a bound is written once, in full", name.Name)
					continue
				}
				v := vs.Values[i]
				declared[name.Name] = string(src[fset.Position(v.Pos()).Offset:fset.Position(v.End()).Offset])
			}
		}
	}
	for name, want := range providerBounds {
		if got, ok := declared[name]; !ok {
			t.Errorf("%s is in the table but bounds.go does not declare it", name)
		} else if got != want {
			t.Errorf("%s = %s in bounds.go, %s in the table; change both or neither", name, got, want)
		}
	}
	var unregistered []string
	for name := range declared {
		if _, ok := providerBounds[name]; !ok {
			unregistered = append(unregistered, name)
		}
	}
	sort.Strings(unregistered)
	if len(unregistered) > 0 {
		t.Errorf("bounds.go declares %s with no row in the table", strings.Join(unregistered, ", "))
	}
	// The waits must fit the shortest attempt a call runs under; the
	// pipeline's test holds the sum against its own deadlines.
	if ProviderRetryAttemptsAsk >= ProviderRetryAttempts {
		t.Errorf("Ask gets %d attempts, the rest %d; the interactive call is meant to get fewer", ProviderRetryAttemptsAsk, ProviderRetryAttempts)
	}
}
