package pipeline

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// functionLineCeiling is the most lines a non-test function declaration may
// span, from its func keyword to its closing brace, comments included. It is
// a ceiling, not a ratchet: a function may grow up to it, and nothing here
// notices one shrinking. Function literals (closures, the handlers in a var
// block such as cmd/worker's scheduled) are not counted, only declarations.
// Set just above the largest function at the time it was added (route, 193
// lines; CleanNote 167; transcribe 166), so the line holds and the next gate
// added to a stage function is written beside it, as transcriptOutcome and
// tickSafety were (review 2026-10-01, BE-6, PR9-6). Lower it as those
// shrink; raising it wants a reason in the commit.
const functionLineCeiling = 195

// functionLineAllowlist names the functions already past the ceiling, each
// with the length it had when listed, so growth past that is still caught.
// runReconcile is the chintanctl scan with its six classifications inline
// (321 lines, unchanged since 2026-09-30); a later round lifts them out.
var functionLineAllowlist = map[string]int{
	"cmd/chintanctl/reconcile.go:runReconcile": 321,
}

// TestNoFunctionOutgrowsTheCeiling reads every non-test Go file under
// backend/ and fails on a function longer than functionLineCeiling that is
// not on the allowlist, or an allowlisted one that grew. Nothing in CI
// noticed a function crossing 200 lines before this: backend/ has no
// .golangci.yml and the v2 defaults carry no funlen.
func TestNoFunctionOutgrowsTheCeiling(t *testing.T) {
	root := filepath.Join("..", "..")
	var over []string
	seen := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			lines := fset.Position(fn.End()).Line - fset.Position(fn.Pos()).Line + 1
			key := filepath.ToSlash(rel) + ":" + fn.Name.Name
			if listed, ok := functionLineAllowlist[key]; ok {
				seen[key] = true
				if lines > listed {
					over = append(over, key+" grew to "+strconv.Itoa(lines)+" lines, listed at "+strconv.Itoa(listed))
				}
				continue
			}
			if lines > functionLineCeiling {
				over = append(over, key+" is "+strconv.Itoa(lines)+" lines, ceiling "+strconv.Itoa(functionLineCeiling))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the sources: %v", err)
	}
	for key := range functionLineAllowlist {
		if !seen[key] {
			t.Errorf("%s is allowlisted but no longer exists; drop it from the list", key)
		}
	}
	sort.Strings(over)
	for _, line := range over {
		t.Errorf("%s: split the decision out of the I/O rather than raising the ceiling", line)
	}
}
