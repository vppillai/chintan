package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// Both dependencies answering is the only thing that may report ok. A static
// {"status":"ok"} stays green through a DynamoDB outage, which is a fine
// liveness answer and a lie about readiness.
func TestReadinessReportsOKWhenEveryDependencyAnswers(t *testing.T) {
	svc := NewReadinessService(memory.NewStore(), memory.NewObjects())

	got := svc.Check(context.Background())

	if got.Status != ReadinessOK {
		t.Fatalf("status = %q, want %q with both dependencies answering (checks=%+v)", got.Status, ReadinessOK, got.Checks)
	}
	for _, name := range []string{"dynamodb", "s3"} {
		check, ok := got.Checks[name]
		if !ok {
			t.Fatalf("no %q check was reported; checks = %+v", name, got.Checks)
		}
		if !check.OK {
			t.Errorf("%s check = %+v, want ok against a working dependency", name, check)
		}
	}
}

// The S3 probe reads a key nothing ever writes, so a not-found is the normal
// answer. Counting it as failure would report degraded forever; the probe asks
// whether the service was reached, not whether the object was there.
func TestReadinessCountsAMissingObjectAsAReachedDependency(t *testing.T) {
	objects := memory.NewObjects()
	svc := NewReadinessService(memory.NewStore(), objects)

	got := svc.Check(context.Background())

	if !got.Checks["s3"].OK {
		t.Fatalf("s3 check = %+v, want ok: nothing is stored at the probe key, so ErrNotFound is the expected answer", got.Checks["s3"])
	}
	if got.Status != ReadinessOK {
		t.Fatalf("status = %q, want %q", got.Status, ReadinessOK)
	}
}

// A wrapped not-found must count as success too, or one fmt.Errorf added inside
// a store turns a healthy instance into a permanently degraded one.
func TestReadinessCountsAWrappedNotFoundAsSuccess(t *testing.T) {
	store := errSettingsStore{
		Store: memory.NewStore(),
		err:   errors.New("dynamodb: GetItem TENANT#__readiness__: " + repository.ErrNotFound.Error()),
	}
	// The string above is deliberately not a wrap; this is the control. The wrap
	// is asserted below.
	if svc := NewReadinessService(store, memory.NewObjects()); svc.Check(context.Background()).Status != ReadinessDegraded {
		t.Fatal("a store error that only looks like a not-found must still be degraded")
	}

	wrapped := errSettingsStore{
		Store: memory.NewStore(),
		err:   errors.Join(errors.New("dynamodb: GetItem TENANT#__readiness__"), repository.ErrNotFound),
	}
	got := NewReadinessService(wrapped, memory.NewObjects()).Check(context.Background())
	if !got.Checks["dynamodb"].OK {
		t.Fatalf("dynamodb check = %+v, want ok: the error wraps repository.ErrNotFound", got.Checks["dynamodb"])
	}
	if got.Status != ReadinessOK {
		t.Fatalf("status = %q, want %q", got.Status, ReadinessOK)
	}
}

// One failing dependency degrades the whole answer. A load balancer that is told
// "ok" while DynamoDB is refusing connections keeps sending traffic to an
// instance that cannot serve it.
func TestReadinessReportsDegradedWhenADependencyFails(t *testing.T) {
	store := errSettingsStore{
		Store: memory.NewStore(),
		err:   errors.New("dynamodb: dial tcp: connection refused"),
	}
	svc := NewReadinessService(store, memory.NewObjects())

	got := svc.Check(context.Background())

	if got.Status != ReadinessDegraded {
		t.Fatalf("status = %q, want %q while DynamoDB is refusing connections (checks=%+v)",
			got.Status, ReadinessDegraded, got.Checks)
	}
	if got.Checks["dynamodb"].OK {
		t.Errorf("dynamodb check = %+v, want not ok", got.Checks["dynamodb"])
	}
	if got.Checks["dynamodb"].Error == "" {
		t.Error("the failing check recorded no error text, so the logs will not say what broke")
	}
	// The other probe still ran: a readiness report that stops at the first
	// failure cannot tell an operator whether one dependency is down or both.
	if !got.Checks["s3"].OK {
		t.Errorf("s3 check = %+v, want ok: only DynamoDB was made to fail", got.Checks["s3"])
	}
}

func TestReadinessReportsDegradedWhenObjectStorageFails(t *testing.T) {
	objects := errGetObjects{
		Objects: memory.NewObjects(),
		err:     errors.New("s3: AccessDenied on chintan-content-bucket"),
	}
	svc := NewReadinessService(memory.NewStore(), objects)

	got := svc.Check(context.Background())

	if got.Status != ReadinessDegraded {
		t.Fatalf("status = %q, want %q while S3 is denying reads (checks=%+v)",
			got.Status, ReadinessDegraded, got.Checks)
	}
	if got.Checks["s3"].OK {
		t.Errorf("s3 check = %+v, want not ok", got.Checks["s3"])
	}
}

// The probe's error text names buckets, tables and hostnames. It is logged and
// must not reach an unauthenticated readiness response.
func TestReadinessDoesNotSerialiseTheProbeError(t *testing.T) {
	store := errSettingsStore{
		Store: memory.NewStore(),
		err:   errors.New("dynamodb: dial tcp 10.0.3.14:8000: connection refused"),
	}
	got := NewReadinessService(store, memory.NewObjects()).Check(context.Background())

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal readiness: %v", err)
	}
	if strings.Contains(string(body), "10.0.3.14") {
		t.Fatalf("readiness body leaks the probe's error detail: %s", body)
	}
	if !strings.Contains(string(body), ReadinessDegraded) {
		t.Fatalf("readiness body does not report the degraded status: %s", body)
	}
}

// The probes address a reserved tenant id no Cognito subject can take, so a
// readiness check on a live instance cannot read or disturb a real tenant's
// partition.
func TestReadinessProbesAReservedTenantPartition(t *testing.T) {
	objects := &recordingObjects{Objects: memory.NewObjects()}
	NewReadinessService(memory.NewStore(), objects).Check(context.Background())

	if len(objects.reads) != 1 {
		t.Fatalf("object reads = %v, want exactly one probe read", objects.reads)
	}
	if !strings.HasPrefix(objects.reads[0], "tenants/"+readinessProbeTenant+"/") {
		t.Fatalf("probe read %q, want a key under the reserved tenant %q", objects.reads[0], readinessProbeTenant)
	}
}
