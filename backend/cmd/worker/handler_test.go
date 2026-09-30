package main

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/pipeline"
)

// taskNames reads every task name the backend declares: each string constant
// called Task or Task<Something> under internal/. Read from source rather
// than listed here, so a task added tomorrow is in this test without anyone
// remembering to add it.
func taskNames(t *testing.T) map[string]string {
	t.Helper()
	names := map[string]string{}
	root := filepath.Join("..", "..", "internal")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "Task") || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						return err
					}
					names[value] = file.Name.Name + "." + name.Name
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read task constants: %v", err)
	}
	// The six known today; fewer means the walk is broken, not the code.
	if len(names) < 6 {
		t.Fatalf("found only %d task constants: %v", len(names), names)
	}
	return names
}

// Every task the backend declares reaches exactly one handler. Before R7-18,
// Handler named the pipeline's tasks itself and logged-and-dropped any other,
// so a task added to Worker.Handle alone returned nil here: no retry, no dead
// letter, the work gone.
func TestHandlerDispatchesEveryTaskConstant(t *testing.T) {
	var ran []string
	prevScheduled, prevWork := scheduled, handleWork
	t.Cleanup(func() { scheduled, handleWork = prevScheduled, prevWork })

	stubs := map[string]func(context.Context) error{}
	for task := range prevScheduled {
		stubs[task] = func(context.Context) error {
			ran = append(ran, "scheduled:"+task)
			return nil
		}
	}
	scheduled = stubs
	handleWork = func(_ context.Context, raw json.RawMessage) error {
		var probe struct{ Task string }
		_ = json.Unmarshal(raw, &probe)
		ran = append(ran, "worker:"+probe.Task)
		return nil
	}

	real := pipeline.NewWorker(nil)
	for task, constant := range taskNames(t) {
		// A pipeline task must be one the real worker's switch serves, not
		// one it refuses. With a tenant and nothing else, each served task
		// discards the payload before it reaches the (nil) pipeline.
		if strings.HasPrefix(constant, "pipeline.") {
			raw, _ := json.Marshal(map[string]string{"task": task, "tenant_id": "u"})
			if err := real.Handle(context.Background(), raw); errors.Is(err, pipeline.ErrUnknownTask) {
				t.Errorf("%s (%q) is not in Worker.Handle's switch", constant, task)
			}
		}

		ran = nil
		raw, _ := json.Marshal(map[string]string{"task": task, "tenant_id": "u"})
		if err := Handler(context.Background(), raw); err != nil {
			t.Errorf("%s: Handler = %v", constant, err)
		}
		if len(ran) != 1 {
			t.Errorf("%s (%q) reached %d handlers %v, want exactly one", constant, task, len(ran), ran)
		}
	}
}

// A task nobody serves is refused with an error, which Lambda retries into
// the dead-letter queue and its alarm, rather than logged and dropped.
func TestHandlerRefusesAnUnknownTaskWithAnError(t *testing.T) {
	prevWork := handleWork
	t.Cleanup(func() { handleWork = prevWork })
	// The real worker: an unknown task is refused before the pipeline is touched.
	handleWork = pipeline.NewWorker(nil).Handle

	err := Handler(context.Background(), json.RawMessage(`{"task":"no-such-task","tenant_id":"u"}`))
	if !errors.Is(err, pipeline.ErrUnknownTask) {
		t.Fatalf("Handler = %v, want ErrUnknownTask", err)
	}
}
