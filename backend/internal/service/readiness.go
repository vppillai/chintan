package service

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/vppillai/chintan/backend/internal/repository"
)

// readinessTimeout bounds the whole probe. A readiness check that hangs is
// worse than one that reports failure: the load balancer learns nothing and the
// caller waits.
const readinessTimeout = 2 * time.Second

// readinessProbeTenant is the partition the probes address. It is a reserved id
// no Cognito subject can take — subjects are UUIDs — so the probe cannot read
// or disturb a real tenant's data.
const readinessProbeTenant = "__readiness__"

// ReadinessCheck is one dependency's result.
type ReadinessCheck struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"-"` // logged, never serialised
}

// Readiness is the whole probe result.
type Readiness struct {
	Status string                    `json:"status"`
	Checks map[string]ReadinessCheck `json:"checks"`
}

// Readiness status values.
const (
	ReadinessOK       = "ok"
	ReadinessDegraded = "degraded"
)

// ReadinessService probes the dependencies the API cannot serve a request
// without.
//
// v1's health check returned a static {"status":"ok"}, which is a fine liveness
// answer and a misleading readiness one: it stayed green through a DynamoDB
// outage. These probes do a real round trip to each dependency, and a "not
// found" counts as success — reaching the service is the question, not finding
// the object.
type ReadinessService struct {
	store   repository.Store
	objects repository.Objects
}

// NewReadinessService builds the probe.
func NewReadinessService(store repository.Store, objects repository.Objects) *ReadinessService {
	return &ReadinessService{store: store, objects: objects}
}

// Check probes every dependency and reports the aggregate.
func (s *ReadinessService) Check(ctx context.Context) Readiness {
	ctx, cancel := context.WithTimeout(ctx, readinessTimeout)
	defer cancel()

	out := Readiness{Status: ReadinessOK, Checks: map[string]ReadinessCheck{}}

	out.Checks["dynamodb"] = probe(ctx, func(ctx context.Context) error {
		_, err := s.store.GetSettings(ctx, readinessProbeTenant)
		return err
	})
	out.Checks["s3"] = probe(ctx, func(ctx context.Context) error {
		_, err := s.objects.Get(ctx, "tenants/"+readinessProbeTenant+"/readiness")
		return err
	})

	for _, name := range sortedKeys(out.Checks) {
		if !out.Checks[name].OK {
			out.Status = ReadinessDegraded
		}
	}
	return out
}

func probe(ctx context.Context, fn func(context.Context) error) ReadinessCheck {
	start := time.Now()
	err := fn(ctx)
	check := ReadinessCheck{OK: true, LatencyMS: time.Since(start).Milliseconds()}
	// An absent item is a completed round trip, which is exactly what the probe
	// is asking about.
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		check.OK = false
		check.Error = err.Error()
	}
	return check
}

func sortedKeys(m map[string]ReadinessCheck) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
