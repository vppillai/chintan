package handler_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/model"
)

// docs/api/openapi.yaml is normative: a frontend has been written against it.
// This file is the check that keeps it honest in both directions — every
// operation it declares must be routable, every route the router registers must
// be declared, and every status code it declares must be one this API can
// actually produce.
//
// It also checks the third place the surface is described: the API Gateway
// route table in infrastructure/template.yaml decides which routes reach Lambda
// without a JWT, and a route that is public in the document but authenticated at
// the gateway answers 401 with the gateway's own body, never reaching any of the
// code below.
//
// Without this, the document is a description of what somebody once intended.

const (
	openAPIPath  = "../../../docs/api/openapi.yaml"
	routesGoPath = "routes.go"
	templatePath = "../../../infrastructure/template.yaml"
)

// operation is one path+method from the document.
type operation struct {
	Method   string
	Path     string
	Statuses []int
	// Public records `security: []` on the operation, which overrides the
	// document's global cognitoBearer requirement.
	Public bool
}

func (o operation) key() string { return o.Method + " " + o.Path }

// ---------------------------------------------------------------- parsing

var (
	pathLine   = regexp.MustCompile(`^ {2}(/\S*):\s*$`)
	methodLine = regexp.MustCompile(`^ {4}(get|put|post|patch|delete|head|options):\s*$`)
	statusLine = regexp.MustCompile(`^ {8}'(\d{3})':`)
)

// parseOpenAPI reads the paths section.
//
// It is a purpose-built reader rather than a YAML dependency: the shape it
// needs is three nesting levels deep and fixed by the document's own layout,
// and a parser small enough to read is a parser that cannot quietly disagree
// with what the file says.
func parseOpenAPI(t *testing.T, path string) []operation {
	t.Helper()

	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	var (
		ops         []operation
		currentPath string
		current     *operation
		inPaths     bool
		inResponses bool
	)

	flush := func() {
		if current != nil {
			ops = append(ops, *current)
			current = nil
		}
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "paths:":
			inPaths = true
			continue
		case !inPaths:
			continue
		case len(line) > 0 && line[0] != ' ' && strings.HasSuffix(line, ":"):
			// A new top-level key ends the paths section.
			flush()
			inPaths = false
			continue
		}

		if m := pathLine.FindStringSubmatch(line); m != nil {
			flush()
			currentPath = m[1]
			inResponses = false
			continue
		}
		if m := methodLine.FindStringSubmatch(line); m != nil {
			flush()
			current = &operation{Method: strings.ToUpper(m[1]), Path: currentPath}
			inResponses = false
			continue
		}
		if strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") && strings.HasSuffix(line, ":") {
			// Another key at method depth, such as `parameters:`.
			flush()
			inResponses = false
			continue
		}
		if line == "      security: []" && current != nil {
			current.Public = true
			continue
		}
		if line == "      responses:" {
			inResponses = true
			continue
		}
		if inResponses && current != nil {
			if m := statusLine.FindStringSubmatch(line); m != nil {
				var code int
				if _, err := fmt.Sscanf(m[1], "%d", &code); err == nil {
					current.Statuses = append(current.Statuses, code)
				}
			}
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(ops) == 0 {
		t.Fatalf("no operations parsed from %s; the reader and the document have diverged", path)
	}
	return ops
}

// The parser is itself a thing that can be wrong, so it is checked against
// operations known to be in the document.
func TestOpenAPIReaderFindsTheDocument(t *testing.T) {
	ops := parseOpenAPI(t, openAPIPath)
	byKey := map[string]operation{}
	for _, op := range ops {
		byKey[op.key()] = op
	}

	for _, want := range []string{
		"GET /v1/health",
		"GET /v1/health/ready",
		"PUT /v1/settings",
		"POST /v1/notes",
		"PATCH /v1/notes/{noteId}",
		"DELETE /v1/notes/{noteId}/permanent",
		"GET /v1/search",
		"POST /v1/captures/{captureId}/retry",
		"GET /v1/export/{exportId}",
	} {
		if _, ok := byKey[want]; !ok {
			t.Errorf("the reader did not find %q; every other assertion here is unreliable", want)
		}
	}

	retry := byKey["POST /v1/captures/{captureId}/retry"]
	if !containsInt(retry.Statuses, 202) || !containsInt(retry.Statuses, 429) {
		t.Errorf("retry statuses = %v, want at least 202 and 429", retry.Statuses)
	}
	if len(byKey) < 20 {
		t.Errorf("parsed %d operations; the document declares many more", len(byKey))
	}
}

// ------------------------------------------------------------ routability

// TestEveryDocumentedOperationIsRoutable asserts the router has a handler for
// every path and method the document declares.
//
// An unauthenticated request is enough: reaching authentication means the route
// exists. A 404 or 405 means the document promises something the router does
// not serve.
func TestEveryDocumentedOperationIsRoutable(t *testing.T) {
	h := newHarness(t)

	for _, op := range parseOpenAPI(t, openAPIPath) {
		t.Run(op.key(), func(t *testing.T) {
			path := concretePath(op.Path)
			if op.Method == http.MethodGet && op.Path == "/v1/search" {
				path += "?q=x"
			}
			if strings.HasSuffix(op.Path, "/download") {
				path += "?kind=audio"
			}

			var body any
			if op.Method == http.MethodPost || op.Method == http.MethodPut || op.Method == http.MethodPatch {
				body = map[string]any{}
			}
			w := h.do(t, op.Method, path, "", body)
			if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
				t.Fatalf("the document declares %s but the router answers %d", op.key(), w.Code)
			}
		})
	}
}

// --------------------------------------------------- the reverse direction

// registrationLine matches one row of the routes() table, which is written as
// rt.handle("METHOD "+p+"/rest/of/path", ...) without exception.
var registrationLine = regexp.MustCompile(`rt\.handle\("([A-Z]+) "\s*\+\s*p\s*\+\s*"([^"]*)"`)

// parseRegisteredRoutes reads routes.go and returns every pattern it registers.
//
// It reads the source rather than the built router because net/http's ServeMux
// does not enumerate its patterns. The table in routes.go is one regular line
// per route by construction, so a reader this small cannot silently disagree
// with it — and the sanity check below fails if the shape ever changes.
func parseRegisteredRoutes(t *testing.T, path string) []string {
	t.Helper()

	src, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}

	var routes []string
	for _, m := range registrationLine.FindAllStringSubmatch(string(src), -1) {
		routes = append(routes, m[1]+" "+handler.APIPrefix+m[2])
	}
	if len(routes) < 20 {
		t.Fatalf("parsed %d registrations from %s; the reader and the route table have diverged",
			len(routes), path)
	}
	return routes
}

// TestEveryRegisteredRouteIsDocumented is the direction
// TestEveryDocumentedOperationIsRoutable does not cover.
//
// Without it a route can be added to the router, shipped, and never appear in
// the document — so a generated client cannot reach it and nobody finds out,
// because every assertion in this file starts from the document.
func TestEveryRegisteredRouteIsDocumented(t *testing.T) {
	documented := map[string]bool{}
	for _, op := range parseOpenAPI(t, openAPIPath) {
		documented[op.key()] = true
	}

	for _, route := range parseRegisteredRoutes(t, routesGoPath) {
		if !documented[route] {
			t.Errorf("the router serves %s but %s does not declare it; document it or stop registering it",
				route, openAPIPath)
		}
	}
}

// ------------------------------------------------- gateway authorization

// gatewayRoute is one AWS::ApiGatewayV2::Route from the CloudFormation template.
type gatewayRoute struct {
	Resource string
	RouteKey string
	AuthType string
}

var (
	cfnResourceLine = regexp.MustCompile(`^ {2}([A-Za-z0-9]+):\s*$`)
	cfnTypeLine     = regexp.MustCompile(`^ {4}Type:\s*(\S+)\s*$`)
	cfnRouteKeyLine = regexp.MustCompile(`^ {6}RouteKey:\s*'?([^'\n]+?)'?\s*$`)
	cfnAuthTypeLine = regexp.MustCompile(`^ {6}AuthorizationType:\s*(\S+)\s*$`)
)

// parseGatewayRoutes reads the API Gateway route table out of the template.
//
// An absent AuthorizationType is reported as NONE, which is what API Gateway
// itself does with one — so a public route added without the property is still
// seen here rather than passing unnoticed.
func parseGatewayRoutes(t *testing.T, path string) []gatewayRoute {
	t.Helper()

	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	var (
		routes  []gatewayRoute
		current *gatewayRoute
		isRoute bool
	)
	flush := func() {
		if current != nil && isRoute {
			if current.AuthType == "" {
				current.AuthType = "NONE"
			}
			routes = append(routes, *current)
		}
		current, isRoute = nil, false
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if m := cfnResourceLine.FindStringSubmatch(line); m != nil {
			flush()
			current = &gatewayRoute{Resource: m[1]}
			continue
		}
		if current == nil {
			continue
		}
		if m := cfnTypeLine.FindStringSubmatch(line); m != nil {
			isRoute = m[1] == "AWS::ApiGatewayV2::Route"
			continue
		}
		if m := cfnRouteKeyLine.FindStringSubmatch(line); m != nil {
			current.RouteKey = m[1]
			continue
		}
		if m := cfnAuthTypeLine.FindStringSubmatch(line); m != nil {
			current.AuthType = m[1]
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(routes) < 4 {
		t.Fatalf("parsed %d gateway routes from %s; the reader and the template have diverged",
			len(routes), path)
	}
	return routes
}

// TestPublicRoutesMatchTheGatewayRouteTable asserts that the set of routes the
// gateway lets through without a JWT is exactly the set the document declares
// with `security: []`.
//
// These two drifted once already: /v1/health/ready was documented public, coded
// public, and authenticated at the gateway, so it answered API Gateway's
// 401 {"message":"Unauthorized"} — not this API's problem+json, and never
// reaching the handler at all. Nothing in the Go tests could see it, because
// they exercise the router directly and the gateway is a YAML file.
func TestPublicRoutesMatchTheGatewayRouteTable(t *testing.T) {
	documented := map[string]bool{}
	for _, op := range parseOpenAPI(t, openAPIPath) {
		if op.Public {
			documented[op.key()] = true
		}
	}

	gateway := map[string]bool{}
	for _, route := range parseGatewayRoutes(t, templatePath) {
		if route.AuthType != "NONE" {
			continue
		}
		// $default carries the authorizer and is the catch-all, and the CORS
		// preflight is not an operation any document declares: the JWT
		// authorizer does not answer OPTIONS, so it has to stay open.
		if route.RouteKey == "$default" || strings.HasPrefix(route.RouteKey, "OPTIONS ") {
			continue
		}
		gateway[route.RouteKey] = true
	}

	for key := range documented {
		if !gateway[key] {
			t.Errorf("%s declares %s public but the template has no AuthorizationType: NONE route for it, "+
				"so the gateway answers 401 before Lambda is reached", openAPIPath, key)
		}
	}
	for key := range gateway {
		if !documented[key] {
			t.Errorf("the template exposes %s without a JWT but %s does not declare it `security: []`",
				key, openAPIPath)
		}
	}
}

// concretePath substitutes a sample identifier for every path template.
func concretePath(p string) string {
	r := strings.NewReplacer(
		"{noteId}", "sample-note",
		"{captureId}", "sample-capture",
		"{exportId}", "sample-export",
		"{askId}", "sample-ask",
	)
	return r.Replace(p)
}

// ------------------------------------------------------- status reachability

// scenario produces one documented status.
type scenario func(t *testing.T) int

// TestEveryDocumentedStatusIsReachable runs a scenario for each declared
// (path, method, status) and asserts it produces that status.
//
// A declared status with no scenario fails the test. That is the point: it is
// how a status somebody added to the document but never implemented — or
// implemented and then lost — gets noticed.
func TestEveryDocumentedStatusIsReachable(t *testing.T) {
	scenarios := statusScenarios()

	for _, op := range parseOpenAPI(t, openAPIPath) {
		for _, status := range op.Statuses {
			key := fmt.Sprintf("%s %s -> %d", op.Method, op.Path, status)
			run, ok := scenarios[key]
			if !ok {
				t.Errorf("no scenario proves %s is reachable; add one or stop declaring it", key)
				continue
			}
			t.Run(key, func(t *testing.T) {
				if got := run(t); got != status {
					t.Fatalf("scenario produced %d, want the documented %d", got, status)
				}
			})
			delete(scenarios, key)
		}
	}

	// A scenario for something the document does not declare means the two have
	// drifted the other way.
	if len(scenarios) > 0 {
		keys := make([]string, 0, len(scenarios))
		for k := range scenarios {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Errorf("scenarios exist for undocumented outcomes: %v", keys)
	}
}

// would hide the thing worth seeing, which is the whole contract at once.
//
//nolint:funlen // One table, one entry per documented outcome; splitting it
func statusScenarios() map[string]scenario {
	// Each scenario builds its own harness, so one cannot leave state behind
	// that changes another's answer.
	get := func(path, user string) scenario {
		return func(t *testing.T) int {
			return newHarness(t).do(t, http.MethodGet, path, user, nil).Code
		}
	}
	send := func(method, path, user string, body any) scenario {
		return func(t *testing.T) int {
			return newHarness(t).do(t, method, path, user, body).Code
		}
	}

	return map[string]scenario{
		// ---- health
		"GET /v1/health -> 200":       get("/v1/health", ""),
		"GET /v1/health/ready -> 200": get("/v1/health/ready", ""),
		"GET /v1/health/ready -> 503": func(t *testing.T) int {
			return newHarness(t, withBrokenStore()).do(t, http.MethodGet, "/v1/health/ready", "", nil).Code
		},

		// ---- settings
		"GET /v1/settings -> 200": get("/v1/settings", "user1"),
		"GET /v1/settings -> 401": get("/v1/settings", ""),
		"PUT /v1/settings -> 200": send(http.MethodPut, "/v1/settings", "user1", map[string]any{"theme": "nocturne"}),
		"PUT /v1/settings -> 400": send(http.MethodPut, "/v1/settings", "user1", map[string]any{"theme": "puce"}),
		"PUT /v1/settings -> 401": send(http.MethodPut, "/v1/settings", "", map[string]any{}),

		// ---- notes
		"GET /v1/notes -> 200":  get("/v1/notes", "user1"),
		"GET /v1/notes -> 401":  get("/v1/notes", ""),
		"POST /v1/notes -> 201": send(http.MethodPost, "/v1/notes", "user1", map[string]any{"title": "A note"}),
		"POST /v1/notes -> 400": send(http.MethodPost, "/v1/notes", "user1", map[string]any{"title": ""}),
		"POST /v1/notes -> 401": send(http.MethodPost, "/v1/notes", "", map[string]any{"title": "x"}),
		"POST /v1/notes -> 409": func(t *testing.T) int {
			h := newHarness(t)
			key := [2]string{"Idempotency-Key", "conflict-key-1"}
			h.do(t, http.MethodPost, "/v1/notes", "user1", map[string]any{"title": "First"}, key)
			return h.do(t, http.MethodPost, "/v1/notes", "user1", map[string]any{"title": "Second"}, key).Code
		},
		"POST /v1/notes/purge -> 200": func(t *testing.T) int {
			h := newHarness(t)
			// A note that does not exist is a legitimate 200: the batch reports
			// not_found per note rather than failing the request.
			return h.do(t, http.MethodPost, "/v1/notes/purge", "user1",
				map[string]any{"note_ids": []string{"note_missing"}}).Code
		},
		"POST /v1/notes/purge -> 400": send(http.MethodPost, "/v1/notes/purge", "user1",
			map[string]any{"note_ids": []string{}}),
		"POST /v1/notes/purge -> 401": send(http.MethodPost, "/v1/notes/purge", "",
			map[string]any{"note_ids": []string{"note_a"}}),
		"POST /v1/notes/purge -> 409": func(t *testing.T) int {
			h := newHarness(t)
			key := [2]string{"Idempotency-Key", "purge-conflict-1"}
			h.do(t, http.MethodPost, "/v1/notes/purge", "user1",
				map[string]any{"note_ids": []string{"note_a"}}, key)
			// The same key with a different batch. Replaying someone else's
			// answer would be worse than refusing.
			return h.do(t, http.MethodPost, "/v1/notes/purge", "user1",
				map[string]any{"note_ids": []string{"note_b"}}, key).Code
		},
		"POST /v1/notes/purge -> 413": func(t *testing.T) int {
			h := newHarness(t)
			ids := make([]string, 40000)
			for i := range ids {
				ids[i] = strings.Repeat("x", 40)
			}
			return h.do(t, http.MethodPost, "/v1/notes/purge", "user1",
				map[string]any{"note_ids": ids}).Code
		},
		"POST /v1/notes -> 413": func(t *testing.T) int {
			h := newHarness(t)
			return h.do(t, http.MethodPost, "/v1/notes", "user1", map[string]any{
				"title": "Big", "body": strings.Repeat("x", 1<<20+1),
			}).Code
		},

		"GET /v1/notes/{noteId} -> 200": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Readable", nil)
			return h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "user1", nil).Code
		},
		"GET /v1/notes/{noteId} -> 401": get("/v1/notes/whatever", ""),
		"GET /v1/notes/{noteId} -> 404": get("/v1/notes/missing", "user1"),

		"PATCH /v1/notes/{noteId} -> 200": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Editable", nil)
			return h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1",
				map[string]any{"version": note.Version, "title": "Edited"}).Code
		},
		"PATCH /v1/notes/{noteId} -> 400": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Editable", nil)
			return h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1",
				map[string]any{"title": "no version"}).Code
		},
		"PATCH /v1/notes/{noteId} -> 401": send(http.MethodPatch, "/v1/notes/x", "", map[string]any{"version": 1}),
		"PATCH /v1/notes/{noteId} -> 404": send(http.MethodPatch, "/v1/notes/missing", "user1", map[string]any{"version": 1}),
		"PATCH /v1/notes/{noteId} -> 409": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Contended", nil)
			h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1",
				map[string]any{"version": note.Version, "title": "Winner"})
			return h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1",
				map[string]any{"version": note.Version, "title": "Loser"}).Code
		},
		"PATCH /v1/notes/{noteId} -> 413": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Big", nil)
			return h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1", map[string]any{
				"version": note.Version, "body": strings.Repeat("x", 1<<20+1),
			}).Code
		},

		"DELETE /v1/notes/{noteId} -> 204": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Archivable", nil)
			return h.do(t, http.MethodDelete, "/v1/notes/"+note.ID, "user1", nil).Code
		},
		"DELETE /v1/notes/{noteId} -> 401": send(http.MethodDelete, "/v1/notes/x", "", nil),
		"DELETE /v1/notes/{noteId} -> 404": send(http.MethodDelete, "/v1/notes/missing", "user1", nil),

		"POST /v1/notes/{noteId}/restore -> 200": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Restorable", nil)
			h.do(t, http.MethodDelete, "/v1/notes/"+note.ID, "user1", nil)
			return h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/restore", "user1", nil).Code
		},
		"POST /v1/notes/{noteId}/restore -> 401": send(http.MethodPost, "/v1/notes/x/restore", "", nil),
		"POST /v1/notes/{noteId}/restore -> 404": send(http.MethodPost, "/v1/notes/missing/restore", "user1", nil),

		"DELETE /v1/notes/{noteId}/permanent -> 204": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Purgeable", nil)
			h.do(t, http.MethodDelete, "/v1/notes/"+note.ID, "user1", nil)
			return h.do(t, http.MethodDelete, "/v1/notes/"+note.ID+"/permanent", "user1", nil).Code
		},
		"DELETE /v1/notes/{noteId}/permanent -> 401": send(http.MethodDelete, "/v1/notes/x/permanent", "", nil),
		"DELETE /v1/notes/{noteId}/permanent -> 404": send(http.MethodDelete, "/v1/notes/missing/permanent", "user1", nil),
		// A cascade that cannot finish fails loudly and leaves the note in
		// place, rather than reporting success over orphaned audio.
		"DELETE /v1/notes/{noteId}/permanent -> 500": func(t *testing.T) int {
			h := newHarness(t, withBrokenObjects())
			note := h.createNote(t, "user1", "Undeletable", nil)
			h.do(t, http.MethodDelete, "/v1/notes/"+note.ID, "user1", nil)
			return h.do(t, http.MethodDelete, "/v1/notes/"+note.ID+"/permanent", "user1", nil).Code
		},

		"POST /v1/notes/match -> 200": func(t *testing.T) int {
			h := newHarness(t)
			h.createNote(t, "user1", "Matchable", nil)
			return h.do(t, http.MethodPost, "/v1/notes/match", "user1", map[string]any{"query": "matchable"}).Code
		},
		"POST /v1/notes/match -> 400": send(http.MethodPost, "/v1/notes/match", "user1", map[string]any{"query": ""}),
		"POST /v1/notes/match -> 401": send(http.MethodPost, "/v1/notes/match", "", map[string]any{"query": "x"}),

		"GET /v1/notes/{noteId}/recordings/urls -> 200": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Kitchen", nil)
			h.seedAppended(t, "user1", note, "c_1", model.Now(), "Dictated.")
			return h.do(t, http.MethodGet, "/v1/notes/"+note.ID+"/recordings/urls", "user1", nil).Code
		},
		"GET /v1/notes/{noteId}/recordings/urls -> 401": get("/v1/notes/n1/recordings/urls", ""),
		"GET /v1/notes/{noteId}/recordings/urls -> 404": get("/v1/notes/missing/recordings/urls", "user1"),

		"POST /v1/notes/{noteId}/clean -> 202": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Cleanable", map[string]any{"body": "the gutter leaks"})
			return h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", nil).Code
		},
		"POST /v1/notes/{noteId}/clean -> 400": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Cleanable", nil)
			return h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", map[string]any{"mode": "faithful"}).Code
		},
		"POST /v1/notes/{noteId}/clean -> 401": send(http.MethodPost, "/v1/notes/x/clean", "", nil),
		"POST /v1/notes/{noteId}/clean -> 404": send(http.MethodPost, "/v1/notes/missing/clean", "user1", nil),
		"POST /v1/notes/{noteId}/clean -> 409": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Archived", nil)
			h.do(t, http.MethodDelete, "/v1/notes/"+note.ID, "user1", nil)
			return h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", nil).Code
		},
		"POST /v1/notes/{noteId}/clean -> 429": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Capped", nil)
			h.spend.capped = true
			return h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", nil).Code
		},

		// ---- tags and search
		"GET /v1/tags -> 200":   get("/v1/tags", "user1"),
		"GET /v1/tags -> 401":   get("/v1/tags", ""),
		"GET /v1/search -> 200": get("/v1/search?q=anything", "user1"),
		"GET /v1/search -> 400": get("/v1/search", "user1"),
		"GET /v1/search -> 401": get("/v1/search?q=anything", ""),

		// ---- usage
		"GET /v1/usage -> 200": get("/v1/usage?month=2026-01", "user1"),
		"GET /v1/usage -> 400": get("/v1/usage?month=January", "user1"),
		"GET /v1/usage -> 401": get("/v1/usage", ""),

		// ---- ask
		"POST /v1/ask -> 202": send(http.MethodPost, "/v1/ask", "user1", map[string]any{"question": "what did I decide about the roof?"}),
		"POST /v1/ask -> 400": send(http.MethodPost, "/v1/ask", "user1", map[string]any{"question": "   "}),
		"POST /v1/ask -> 401": send(http.MethodPost, "/v1/ask", "", map[string]any{"question": "x?"}),
		"POST /v1/ask -> 409": func(t *testing.T) int {
			h := newHarness(t)
			key := [2]string{"Idempotency-Key", "ask-conflict-1"}
			h.do(t, http.MethodPost, "/v1/ask", "user1", map[string]any{"question": "roof?"}, key)
			return h.do(t, http.MethodPost, "/v1/ask", "user1", map[string]any{"question": "garden?"}, key).Code
		},
		"POST /v1/ask -> 413": func(t *testing.T) int {
			h := newHarness(t)
			history := make([]map[string]string, 6)
			for i := range history {
				history[i] = map[string]string{"question": "q", "answer": strings.Repeat("a", 3500)}
			}
			return h.do(t, http.MethodPost, "/v1/ask", "user1", map[string]any{"question": "x?", "history": history}).Code
		},
		"POST /v1/ask -> 429": func(t *testing.T) int {
			h := newHarness(t)
			h.spend.capped = true
			return h.do(t, http.MethodPost, "/v1/ask", "user1", map[string]any{"question": "roof?"}).Code
		},
		"POST /v1/ask -> 503": func(t *testing.T) int {
			return newHarness(t, withoutAsk()).do(t, http.MethodPost, "/v1/ask", "user1", map[string]any{"question": "roof?"}).Code
		},
		"GET /v1/ask/{askId} -> 200": func(t *testing.T) int {
			h := newHarness(t)
			w := h.do(t, http.MethodPost, "/v1/ask", "user1", map[string]any{"question": "roof?"})
			var a struct {
				ID string `json:"id"`
			}
			decodeInto(t, w, &a)
			return h.do(t, http.MethodGet, "/v1/ask/"+a.ID, "user1", nil).Code
		},
		"GET /v1/ask/{askId} -> 401": get("/v1/ask/ask_1", ""),
		"GET /v1/ask/{askId} -> 404": get("/v1/ask/ask_never", "user1"),

		// ---- captures
		"GET /v1/captures -> 200":  get("/v1/captures", "user1"),
		"GET /v1/captures -> 401":  get("/v1/captures", ""),
		"POST /v1/captures -> 201": send(http.MethodPost, "/v1/captures", "user1", map[string]any{"content_type": "audio/webm"}),
		"POST /v1/captures -> 400": send(http.MethodPost, "/v1/captures", "user1", map[string]any{}),
		"POST /v1/captures -> 401": send(http.MethodPost, "/v1/captures", "", map[string]any{"content_type": "audio/webm"}),
		"POST /v1/captures -> 429": func(t *testing.T) int {
			h := newHarness(t)
			h.spend.capped = true
			return h.do(t, http.MethodPost, "/v1/captures", "user1", map[string]any{"content_type": "audio/webm"}).Code
		},

		"GET /v1/captures/{captureId} -> 200": func(t *testing.T) int {
			h := newHarness(t)
			c := h.putCapture(t, model.CaptureIndex{ID: "c_r", UserID: "user1", Status: model.StatusUploaded, CreatedAt: model.Now()})
			return h.do(t, http.MethodGet, "/v1/captures/"+c.ID, "user1", nil).Code
		},
		"GET /v1/captures/{captureId} -> 401": get("/v1/captures/c_1", ""),
		"GET /v1/captures/{captureId} -> 404": get("/v1/captures/missing", "user1"),

		"POST /v1/captures/{captureId}/target -> 200": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Destination", nil)
			c := h.putCapture(t, model.CaptureIndex{ID: "c_t", UserID: "user1", Status: model.StatusNeedsTarget, CreatedAt: model.Now()})
			return h.do(t, http.MethodPost, "/v1/captures/"+c.ID+"/target", "user1",
				map[string]any{"note_id": note.ID}).Code
		},
		"POST /v1/captures/{captureId}/target -> 400": func(t *testing.T) int {
			h := newHarness(t)
			c := h.putCapture(t, model.CaptureIndex{ID: "c_t", UserID: "user1", Status: model.StatusNeedsTarget, CreatedAt: model.Now()})
			return h.do(t, http.MethodPost, "/v1/captures/"+c.ID+"/target", "user1", map[string]any{}).Code
		},
		"POST /v1/captures/{captureId}/target -> 401": send(http.MethodPost, "/v1/captures/c_1/target", "", map[string]any{}),
		"POST /v1/captures/{captureId}/target -> 404": send(http.MethodPost, "/v1/captures/missing/target", "user1", map[string]any{"note_id": "n1"}),
		"POST /v1/captures/{captureId}/target -> 409": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Taken", nil)
			c := h.putCapture(t, model.CaptureIndex{
				ID: "c_t", UserID: "user1", NoteID: note.ID, Status: model.StatusCleaned, CreatedAt: model.Now(),
			})
			return h.do(t, http.MethodPost, "/v1/captures/"+c.ID+"/target", "user1",
				map[string]any{"note_id": note.ID}).Code
		},

		"POST /v1/captures/{captureId}/retry -> 202": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Retryable", nil)
			c := h.putCapture(t, model.CaptureIndex{
				ID: "c_retry", UserID: "user1", NoteID: note.ID,
				Status: model.StatusFailed, CreatedAt: model.Now(),
			})
			return h.do(t, http.MethodPost, "/v1/captures/"+c.ID+"/retry", "user1", nil).Code
		},
		"POST /v1/captures/{captureId}/retry -> 401": send(http.MethodPost, "/v1/captures/c_1/retry", "", nil),
		"POST /v1/captures/{captureId}/retry -> 404": send(http.MethodPost, "/v1/captures/missing/retry", "user1", nil),
		"POST /v1/captures/{captureId}/retry -> 409": func(t *testing.T) int {
			h := newHarness(t)
			c := h.putCapture(t, model.CaptureIndex{
				ID: "c_done", UserID: "user1", Status: model.StatusAppended, CreatedAt: model.Now(),
			})
			return h.do(t, http.MethodPost, "/v1/captures/"+c.ID+"/retry", "user1", nil).Code
		},
		"POST /v1/captures/{captureId}/retry -> 429": func(t *testing.T) int {
			h := newHarness(t)
			h.spend.capped = true
			return h.do(t, http.MethodPost, "/v1/captures/c_1/retry", "user1", nil).Code
		},

		"GET /v1/captures/{captureId}/download -> 200": func(t *testing.T) int {
			h := newHarness(t)
			c := h.putCapture(t, model.CaptureIndex{
				ID: "c_dl", UserID: "user1", Status: model.StatusAppended, CreatedAt: model.Now(),
				AudioKey: "tenants/user1/captures/c_dl/audio.webm",
			})
			return h.do(t, http.MethodGet, "/v1/captures/"+c.ID+"/download?kind=audio", "user1", nil).Code
		},
		"GET /v1/captures/{captureId}/download -> 401": get("/v1/captures/c_1/download?kind=audio", ""),
		"GET /v1/captures/{captureId}/download -> 404": get("/v1/captures/missing/download?kind=audio", "user1"),
		"DELETE /v1/captures/{captureId} -> 204": func(t *testing.T) int {
			h := newHarness(t)
			note := h.createNote(t, "user1", "Kitchen", nil)
			h.seedAppended(t, "user1", note, "c_1", model.Now(), "Dictated.")
			return h.do(t, http.MethodDelete, "/v1/captures/c_1", "user1", nil).Code
		},
		"DELETE /v1/captures/{captureId} -> 401": send(http.MethodDelete, "/v1/captures/c_1", "", nil),
		"DELETE /v1/captures/{captureId} -> 404": send(http.MethodDelete, "/v1/captures/missing", "user1", nil),
		"DELETE /v1/captures/{captureId} -> 409": func(t *testing.T) int {
			h := newHarness(t)
			h.putCapture(t, model.CaptureIndex{ID: "c_1", UserID: "user1", Status: model.StatusTranscribing, CreatedAt: model.Now()})
			return h.do(t, http.MethodDelete, "/v1/captures/c_1", "user1", nil).Code
		},
		"POST /v1/captures/{captureId}/move -> 200": func(t *testing.T) int {
			h := newHarness(t)
			source := h.createNote(t, "user1", "Source", nil)
			target := h.createNote(t, "user1", "Target", nil)
			h.seedAppended(t, "user1", source, "c_1", model.Now(), "Dictated.")
			return h.do(t, http.MethodPost, "/v1/captures/c_1/move", "user1", map[string]any{"note_id": target.ID}).Code
		},
		"POST /v1/captures/{captureId}/move -> 204": func(t *testing.T) int {
			h := newHarness(t)
			source := h.createNote(t, "user1", "Source", nil)
			h.seedAppended(t, "user1", source, "c_1", model.Now(), "Dictated.")
			return h.do(t, http.MethodPost, "/v1/captures/c_1/move", "user1", map[string]any{"note_id": source.ID}).Code
		},
		"POST /v1/captures/{captureId}/move -> 400": send(http.MethodPost, "/v1/captures/c_1/move", "user1", map[string]any{}),
		"POST /v1/captures/{captureId}/move -> 401": send(http.MethodPost, "/v1/captures/c_1/move", "", map[string]any{"note_id": "n1"}),
		"POST /v1/captures/{captureId}/move -> 404": send(http.MethodPost, "/v1/captures/missing/move", "user1", map[string]any{"note_id": "n1"}),
		"POST /v1/captures/{captureId}/move -> 409": func(t *testing.T) int {
			h := newHarness(t)
			source := h.createNote(t, "user1", "Source", nil)
			archived := h.createNote(t, "user1", "Archived", nil)
			h.do(t, http.MethodDelete, "/v1/notes/"+archived.ID, "user1", nil)
			h.seedAppended(t, "user1", source, "c_1", model.Now(), "Dictated.")
			return h.do(t, http.MethodPost, "/v1/captures/c_1/move", "user1", map[string]any{"note_id": archived.ID}).Code
		},
		"POST /v1/captures/{captureId}/move -> 503": func(t *testing.T) int {
			var failKey string
			h := newHarness(t, withFailingBodyWrite(&failKey))
			source := h.createNote(t, "user1", "Source", nil)
			target := h.createNote(t, "user1", "Target", nil)
			h.seedAppended(t, "user1", source, "c_1", model.Now(), "Dictated.")
			stored, err := h.store.GetNote(context.Background(), "user1", target.ID)
			if err != nil {
				t.Fatalf("GetNote: %v", err)
			}
			failKey = stored.S3MarkdownKey
			return h.do(t, http.MethodPost, "/v1/captures/c_1/move", "user1", map[string]any{"note_id": target.ID}).Code
		},

		// ---- export
		"POST /v1/export -> 202":           send(http.MethodPost, "/v1/export", "user1", nil),
		"POST /v1/export -> 401":           send(http.MethodPost, "/v1/export", "", nil),
		"GET /v1/export/{exportId} -> 401": get("/v1/export/e_1", ""),
		"GET /v1/export/{exportId} -> 404": get("/v1/export/e_neverissued", "user1"),
		"GET /v1/export/{exportId} -> 200": func(t *testing.T) int {
			h := newHarness(t)
			w := h.do(t, http.MethodPost, "/v1/export", "user1", nil)
			var job struct {
				ID string `json:"id"`
			}
			decodeInto(t, w, &job)
			return h.do(t, http.MethodGet, "/v1/export/"+job.ID, "user1", nil).Code
		},
	}
}

func containsInt(values []int, want int) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
