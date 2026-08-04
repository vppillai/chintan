// Package handler holds the HTTP handlers for the sync API function.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/vppillai/chintan/backend/internal/config"
	"github.com/vppillai/chintan/backend/internal/version"
)

// HealthResponse is what GET /v1/health returns (§6.6, §0.6).
//
// This endpoint is genuinely unauthenticated and returns no user data of any
// kind, which is what keeps it compatible with I10. It exists for one specific
// job: the frontend and the backend deploy through separate workflows and can
// drift, so the app fetches this, compares it against its own build, and flags a
// mismatch. Without the endpoint that check cannot exist, and version drift gets
// diagnosed by guesswork instead (§0.6).
type HealthResponse struct {
	// Version is the API's display version — the bare git tag.
	Version string `json:"version"`
	// Commit is the short SHA. The frontend needs it because a tag alone cannot
	// distinguish two deploys of the same tag (the reasoning behind G-035).
	Commit string `json:"commit"`
	// BuildTime is when the artifact was built, RFC3339 UTC.
	BuildTime string `json:"build_time"`
	// Stamped is false when the build carries no real version information, so an
	// accidentally unstamped deploy is visible rather than being read as a
	// version literally named "unstamped" and ignored (G-036).
	Stamped bool `json:"stamped"`
	// ConfigVersion is the config schema version this build validated against.
	// Required by §Phase 0 acceptance: "GET /v1/health returns the deployed API
	// version and build SHA".
	ConfigVersion int `json:"config_version"`
	// Instance is dev or prod. Included because the most confusing deployment
	// failure is a frontend talking to the wrong instance's API, which otherwise
	// presents as data that is simply missing.
	Instance string `json:"instance"`
}

// Health serves GET /v1/health.
//
// No auth, no user data, no audit record — there is no user content accessed, so
// I13's "every access to user content writes an audit record" does not engage.
// It deliberately does not report dependency health: a health endpoint that
// probes DynamoDB and a provider on every call is a cost and a rate-limit
// surface, and nothing consumes a liveness signal here (there are no alarms by
// §10.1). It reports what this build *is*, which is what the drift check needs.
func Health(cfg *config.Config) http.HandlerFunc {
	// Built once rather than per request: the values cannot change during the
	// life of a function instance.
	body, err := json.Marshal(HealthResponse{
		Version:       version.Display(),
		Commit:        version.Commit,
		BuildTime:     version.BuildTime,
		Stamped:       version.Stamped(),
		ConfigVersion: cfg.Version,
		Instance:      cfg.Instance,
	})

	return func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			// Unreachable for a struct of scalars, handled so a future field
			// with a custom marshaller cannot turn this into a panic on the one
			// endpoint used to diagnose a broken deploy.
			http.Error(w, `{"error":"health_unavailable"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// No caching: a stale health response would defeat the drift check that
		// is this endpoint's only purpose.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}
