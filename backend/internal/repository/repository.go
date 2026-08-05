// Package repository is the storage seam.
//
// One interface, two implementations: DynamoDB in production, an in-memory fake for
// tests. §11.5 requires admin scripts to be testable without AWS credentials, and
// §Phase 0 acceptance requires an integration test that attempts a cross-tenant read
// **directly against the data layer, not only through the API** — "passing only at the
// API layer is insufficient." Both need a data layer that exists independently of AWS.
//
// The interface is deliberately narrow and key-oriented rather than entity-oriented.
// Every method takes a keys.DynamoKey or a key prefix, which means:
//
//   - There is no way to reach storage without having gone through the key helper, so
//     I11's tenant scoping is enforced by the type system rather than by review. A
//     caller cannot ask for "all captures" because there is no method that would
//     express it.
//   - The write-once entities have no update or delete method at all. I13 says audit
//     records are "append-only, never mutated" and §6.3 says audit and usage are
//     write-once — so PutOnce is the only way to write one, and it fails if the key
//     exists rather than silently overwriting.
package repository

import (
	"context"
	"errors"

	"github.com/vppillai/chintan/backend/internal/keys"
)

// Sentinel errors. Compared with errors.Is, so an implementation may wrap them with
// provider detail without breaking a caller's branch.
var (
	// ErrNotFound is returned for a missing item.
	//
	// Callers must translate this to 404 and never to 403, including for a
	// cross-tenant access attempt: a 403 confirms the resource exists (§9.1). The
	// distinction is invisible here because a tenant-scoped key for another tenant's
	// resource simply does not exist in this tenant's partition — which is the point
	// of the key design.
	ErrNotFound = errors.New("repository: item not found")

	// ErrAlreadyExists is returned by PutOnce when the key is taken. This is the
	// mechanism behind both write-once records and idempotency keys.
	ErrAlreadyExists = errors.New("repository: item already exists")

	// ErrImmutable is returned when a caller attempts to modify a record type that
	// has no mutation path. Distinct from a permission error because it is a
	// programming defect, not a policy outcome.
	ErrImmutable = errors.New("repository: record type is write-once")
)

// Item is one stored record: its key, and its attributes as a marshalled map.
//
// Attributes are `map[string]any` rather than a typed struct because one table holds
// every entity (§6.3) and the marshalling belongs to the caller that knows the type.
type Item struct {
	Key   keys.DynamoKey
	Attrs map[string]any

	// GSI1PK and GSI1SK participate in the sparse time-ordered index. Empty for the
	// entity types that must not project into it — Segment, Usage, Audit, and Metric
	// are high-volume, and projecting them makes the index a second copy of the table
	// (§6.3), which at on-demand pricing means paying twice for every write.
	GSI1PK string
	GSI1SK string

	// TTL is an absolute epoch second, or zero for no expiry. Usage expires at 25
	// months and audit at ~7 years (§6.3).
	TTL int64
}

// Repository is the storage contract.
type Repository interface {
	// Get returns one item, or ErrNotFound.
	Get(ctx context.Context, key keys.DynamoKey) (*Item, error)

	// Put writes or replaces one item. For mutable entities only.
	Put(ctx context.Context, item Item) error

	// PutOnce writes one item and fails with ErrAlreadyExists if the key is taken.
	//
	// This is the only write path for audit and usage records, and the mechanism
	// behind idempotency keys. A conditional write rather than a read-then-write:
	// read-then-write has a race that, for an idempotency key, means two concurrent
	// retries both create a capture — the exact data-integrity bug idempotency exists
	// to prevent (§2A.1).
	PutOnce(ctx context.Context, item Item) error

	// QueryPrefix returns every item in one partition whose sort key begins with the
	// given prefix, in sort-key order.
	//
	// Takes a partition key and a prefix rather than a free-form expression, so there
	// is no way to express a query that is not tenant-scoped (I11).
	QueryPrefix(ctx context.Context, pk string, skPrefix string, limit int) ([]Item, error)

	// Delete removes one item. Present for the mutable entities and for the erasure
	// path (§9.3); the write-once types are protected by having no code that calls it
	// on them, and by verify.sh asserting their presence (§11.6).
	Delete(ctx context.Context, key keys.DynamoKey) error
}

// TenantScoped narrows a Repository to one tenant.
//
// Not a security boundary — the key helper is that. It exists so a handler that has
// resolved a tenant from a validated JWT claim cannot then accidentally pass a
// different one further down: the tenant is bound once, at the point it was
// authenticated, rather than threaded through every call as an argument that could be
// transposed.
type TenantScoped struct {
	repo   Repository
	tenant keys.TenantID
}

// Scope binds a repository to a tenant. Returns an error rather than panicking on an
// empty tenant, so the failure surfaces at the request boundary where it can become a
// 401 instead of a crash.
func Scope(repo Repository, tenant keys.TenantID) (*TenantScoped, error) {
	// Validate by building a key: the helper is the authority on what a usable tenant
	// is, and duplicating its rules here would let the two disagree.
	if _, err := keys.Tenant(tenant); err != nil {
		return nil, err
	}
	return &TenantScoped{repo: repo, tenant: tenant}, nil
}

// Tenant reports the bound tenant.
func (t *TenantScoped) Tenant() keys.TenantID { return t.tenant }

// Repo exposes the underlying repository for the key-building call sites that need it.
func (t *TenantScoped) Repo() Repository { return t.repo }
