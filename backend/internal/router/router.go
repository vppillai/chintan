// Package router maps HTTP routes to handlers for the sync API function.
//
// §4 requires one Lambda per execution profile, internally routed — a
// "Lambdalith" — never one function per endpoint, which multiplies cold starts
// and duplicated init for no benefit. This package is that internal routing.
//
// I15 requires every endpoint to be versioned from the first commit, so the
// prefix is applied structurally here rather than repeated in each route
// literal: an unversioned public endpoint becomes a permanent compatibility
// obligation, and the way one gets added is a hand-written path that forgot the
// prefix.
package router

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/vppillai/chintan/backend/internal/config"
	"github.com/vppillai/chintan/backend/internal/handler"
)

// APIVersion is the only supported API version prefix (I15).
const APIVersion = "v1"

// Mux wraps http.ServeMux so that every registration goes through Handle below
// and therefore cannot omit the version prefix.
type Mux struct {
	mux *http.ServeMux
}

// New builds the router with every route this phase serves.
//
// Routes arrive by phase (§6.6). Phase 0 serves exactly one: GET /v1/health,
// which is what makes the frontend's version-drift check possible (§0.6). The
// rest are added in the phase that owns them; there are no stub routes here,
// because a route returning 501 is indistinguishable from a deploy failure to a
// client and invites building against something that does not work.
func New(cfg *config.Config) http.Handler {
	m := &Mux{mux: http.NewServeMux()}

	// Phase 0 — unauthenticated, no user data (§6.6).
	m.Handle(http.MethodGet, "/health", handler.Health(cfg))

	// A request to an unknown path gets 404 from ServeMux. A request to a
	// *versioned* path this build does not serve gets the same, deliberately:
	// §9.1 requires that an unknown resource returns 404 rather than anything
	// that discloses what exists.
	return m.root()
}

// Handle registers a handler under the version prefix.
//
// path must begin with "/" and must NOT contain the version — passing "/v1/..."
// panics, because a caller doing that has misunderstood who owns the prefix and
// the result would be "/v1/v1/...", a 404 that looks like a routing bug.
func (m *Mux) Handle(method, path string, h http.Handler) {
	if !strings.HasPrefix(path, "/") {
		panic(fmt.Sprintf("router: path %q must begin with /", path))
	}
	if strings.HasPrefix(path, "/"+APIVersion+"/") || path == "/"+APIVersion {
		panic(fmt.Sprintf("router: path %q must not include the version prefix; Handle applies it (I15)", path))
	}
	// Go 1.22+ ServeMux understands "METHOD /path", which gives per-method
	// routing without a third-party router — and 405 rather than 404 for a
	// known path with the wrong method.
	m.mux.Handle(method+" /"+APIVersion+path, h)
}

func (m *Mux) root() http.Handler { return m.mux }
