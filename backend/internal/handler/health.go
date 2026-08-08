package handler

import (
	"log/slog"
	"net/http"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/service"
)

// health is liveness: the process is running and can answer.
//
// It deliberately probes nothing. A liveness check that fails when a dependency
// is down invites an orchestrator to restart a healthy process, which fixes
// nothing and loses whatever was in flight.
func (rt *router) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ready probes the dependencies a request cannot be served without.
//
// This is the check v1 did not have. Its health endpoint returned a static
// {"status":"ok"} that stayed green through a DynamoDB outage — fine as
// liveness, misleading as readiness, and the reason an alarm on it would never
// have fired.
func (rt *router) ready(w http.ResponseWriter, r *http.Request) {
	if rt.Readiness == nil {
		// Nothing to probe means nothing can be claimed. Reporting ok here would
		// reintroduce exactly the static answer this endpoint replaces.
		httperr.ServiceUnavailable(w, r, "readiness probing is not configured on this instance")
		return
	}

	result := rt.Readiness.Check(r.Context())
	for name, check := range result.Checks {
		if check.OK {
			continue
		}
		// The dependency's error text is logged, never serialised: it carries
		// table and bucket names.
		obs.Log(r.Context()).Error("readiness probe failed",
			slog.String("dependency", name),
			slog.String("error", check.Error))
	}

	if result.Status != service.ReadinessOK {
		p := httperr.New(http.StatusServiceUnavailable, "a dependency is not answering")
		httperr.Write(w, r, p)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
