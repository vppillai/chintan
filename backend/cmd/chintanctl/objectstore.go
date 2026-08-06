package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/vppillai/chintan/backend/internal/clock"
)

// objectVersion is one S3 object version, or one delete marker, under a tenant's prefix.
//
// **Versions are first-class here rather than an implementation detail, and that is the
// whole point of this type.** G-021: "S3 versioning, DynamoDB PITR, and backups retain
// copies after an object-level delete. Only destroying the encryption key makes data
// genuinely unrecoverable." The data bucket has versioning Enabled (infrastructure/
// template.yaml, on purpose — L0 immutability, I1) and **no NoncurrentVersionExpiration
// rule**, so a DeleteObject there does not remove bytes at all: it writes a delete marker
// and the previous version is retained indefinitely. §9.3's fallback — "object deletion
// plus waiting out the PITR retention window" — therefore has no window to wait out on
// the S3 side. Erasure must enumerate and delete versions explicitly, and a listing that
// returned only current objects would make it silently impossible to do so.
type objectVersion struct {
	Key       string `json:"key"`
	VersionID string `json:"version_id"`

	// IsLatest marks the version a plain GET would return. Export copies these;
	// erasure deletes every version regardless.
	IsLatest bool `json:"is_latest"`

	// DeleteMarker distinguishes a tombstone from data. It carries no bytes, but it is
	// still a record of a key that existed and it must be removed for the prefix to
	// come back genuinely empty.
	DeleteMarker bool `json:"delete_marker"`

	Bytes int64 `json:"bytes"`

	// LastModified is RFC3339 UTC, or empty when the store cannot report it.
	//
	// Present because kmsref.ErasureScope bounds a crypto-shredding claim *in time*:
	// objects written before the tenant was pointed at its current key survive that
	// key's destruction (see kmsref.TenantAttrKMSKeyIDSince). Reported so the erasure
	// report can say which side of the boundary an object falls on instead of
	// asserting coverage it cannot establish.
	LastModified string `json:"last_modified,omitempty"`
}

// objectStore is the S3 surface export and erasure need.
//
// Narrow and locally declared, in the same shape and for the same reason as
// repository.DynamoAPI: the tests that matter here run in the check suite that holds no
// AWS credentials (§0.5A, §11.5), so the production adapter has to be substitutable by a
// struct with three methods.
type objectStore interface {
	// ListVersions returns every version and delete marker under prefix, sorted by key
	// then version, so two runs of the same plan enumerate in the same order.
	ListVersions(ctx context.Context, prefix string) ([]objectVersion, error)

	// GetObject reads one specific version's bytes. The caller closes the reader.
	//
	// Version-qualified rather than "current", because an export taken while anything
	// is still writing would otherwise mix versions across objects, and because the
	// erasure report's byte counts must refer to the versions it enumerated.
	GetObject(ctx context.Context, key, versionID string) (io.ReadCloser, error)

	// DeleteVersions removes exactly the versions given — no prefix, no wildcard.
	//
	// Takes the enumerated list rather than a prefix so that --apply can only destroy
	// what --dry-run printed (§11.3: "a dry-run that lies is worse than no dry-run").
	// A prefix-delete API would make the plan advisory.
	//
	// Returns per-entry failures rather than stopping at the first: a partial erasure
	// that removed 999 of 1000 objects is better than one that removed none, and the
	// operation is idempotent, so a re-run converges. Failures are reported, and the
	// caller exits non-zero.
	DeleteVersions(ctx context.Context, vs []objectVersion) ([]deleteFailure, error)

	// Describe names the store for the report, so no reader can mistake a fixture run
	// for a live one.
	Describe() string

	// Requests reports the billable API calls made so far (I12). Counted rather than
	// estimated: it is the quantity the metering event carries.
	Requests() int
}

// deleteFailure is one version that could not be removed, and why.
type deleteFailure struct {
	Key       string `json:"key"`
	VersionID string `json:"version_id,omitempty"`
	Reason    string `json:"reason"`
}

// ---------------------------------------------------------------------------
// Live S3
// ---------------------------------------------------------------------------

// s3API is the S3 surface the live adapter uses. Satisfied by *s3.Client.
type s3API interface {
	ListObjectVersions(ctx context.Context, in *s3.ListObjectVersionsInput, opts ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, opts ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

// liveS3 is the production objectStore.
//
// **Layering exception, stated rather than hidden.** internal/awsclient says no other
// package imports the AWS SDK directly, and it is right to. This adapter breaks that rule
// because awsclient exposes GetObject only — there is no version-aware listing, no
// version-qualified read and no batch delete — and those three calls are the mechanism
// §9.3's erasure fallback consists of. The alternative was an erasure that never looked at
// S3, which would report success having destroyed nothing under the tenant prefix. That is
// the G-021 failure verbatim: "a completed erasure request leaves recoverable data,
// discovered during audit, not during testing."
//
// The precedent is repository.Dynamo, which imports the SDK for the same kind of reason
// and says so. This belongs in awsclient the moment that package can be extended, and the
// interface above is what makes that a move rather than a rewrite.
type liveS3 struct {
	api      s3API
	bucket   string
	requests int
}

// newLiveS3 builds the live adapter for one bucket.
func newLiveS3(ctx context.Context, bucket, region string) (*liveS3, error) {
	if bucket == "" {
		return nil, fmt.Errorf("no bucket name; export and erasure must not run against an unnamed bucket")
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return &liveS3{api: s3.NewFromConfig(cfg), bucket: bucket}, nil
}

// Describe names the bucket.
func (s *liveS3) Describe() string { return "s3://" + s.bucket }

// Requests reports the API calls made.
func (s *liveS3) Requests() int { return s.requests }

// ListVersions enumerates every version and delete marker under prefix.
//
// Paginates to exhaustion. A truncated listing is the failure that matters most here: it
// makes an export silently incomplete and an erasure silently partial, and both look like
// success. So the loop has no page cap, and a listing that cannot be completed is an
// error rather than a short result.
func (s *liveS3) ListVersions(ctx context.Context, prefix string) ([]objectVersion, error) {
	if prefix == "" {
		// Refused rather than treated as "the whole bucket". An empty prefix is the one
		// shape this call could have that would reach another tenant's objects (I11),
		// and it means the caller bypassed the keys package.
		return nil, fmt.Errorf("empty S3 prefix; every listing must be tenant-scoped and the prefix must come from the keys package (I11)")
	}
	var (
		out       []objectVersion
		keyMarker *string
		verMarker *string
	)
	for {
		s.requests++
		page, err := s.api.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
			Bucket:          aws.String(s.bucket),
			Prefix:          aws.String(prefix),
			KeyMarker:       keyMarker,
			VersionIdMarker: verMarker,
		})
		if err != nil {
			return nil, fmt.Errorf("listing s3://%s/%s: %w", s.bucket, prefix, err)
		}
		for _, v := range page.Versions {
			out = append(out, objectVersion{
				Key:          aws.ToString(v.Key),
				VersionID:    aws.ToString(v.VersionId),
				IsLatest:     aws.ToBool(v.IsLatest),
				Bytes:        aws.ToInt64(v.Size),
				LastModified: formatModified(v.LastModified),
			})
		}
		for _, m := range page.DeleteMarkers {
			out = append(out, objectVersion{
				Key:          aws.ToString(m.Key),
				VersionID:    aws.ToString(m.VersionId),
				IsLatest:     aws.ToBool(m.IsLatest),
				DeleteMarker: true,
				LastModified: formatModified(m.LastModified),
			})
		}
		if !aws.ToBool(page.IsTruncated) {
			break
		}
		keyMarker, verMarker = page.NextKeyMarker, page.NextVersionIdMarker
		if keyMarker == nil && verMarker == nil {
			// Truncated with no continuation token is a contradiction. Looping again
			// would repeat the first page forever; treating it as the end would silently
			// truncate the inventory. Neither is acceptable for a listing an erasure
			// claim rests on.
			return nil, fmt.Errorf("listing s3://%s/%s: truncated with no continuation marker, so the inventory cannot be proven complete", s.bucket, prefix)
		}
	}
	sortVersions(out)
	return out, nil
}

// formatModified renders an S3 timestamp in the one representation this system stores.
// Empty when absent, because "unknown" must not read as the epoch — an epoch timestamp
// would place the object before every key-repoint boundary and change what the erasure
// report claims (kmsref.TenantAttrKMSKeyIDSince).
func formatModified(t *time.Time) string {
	if t == nil {
		return ""
	}
	return clock.RFC3339UTC(*t)
}

// GetObject reads one version.
func (s *liveS3) GetObject(ctx context.Context, key, versionID string) (io.ReadCloser, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}
	if versionID != "" {
		in.VersionId = aws.String(versionID)
	}
	s.requests++
	res, err := s.api.GetObject(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("get s3://%s/%s: %w", s.bucket, key, err)
	}
	return res.Body, nil
}

// deleteBatchMax is the maximum number of keys DeleteObjects accepts in one call.
const deleteBatchMax = 1000

// DeleteVersions removes each enumerated version, in batches.
//
// Quiet is deliberately false. The quiet form returns only errors, which is convenient
// until the count matters: the report states how many versions were destroyed, and
// deriving that from "everything I asked for, minus the errors I was told about" trusts
// the request rather than the response. §9.3 requires erasure to report what it removed,
// not what it attempted.
func (s *liveS3) DeleteVersions(ctx context.Context, vs []objectVersion) ([]deleteFailure, error) {
	var failures []deleteFailure
	for start := 0; start < len(vs); start += deleteBatchMax {
		end := start + deleteBatchMax
		if end > len(vs) {
			end = len(vs)
		}
		batch := make([]types.ObjectIdentifier, 0, end-start)
		for _, v := range vs[start:end] {
			id := types.ObjectIdentifier{Key: aws.String(v.Key)}
			if v.VersionID != "" {
				id.VersionId = aws.String(v.VersionID)
			}
			batch = append(batch, id)
		}
		s.requests++
		res, err := s.api.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{Objects: batch, Quiet: aws.Bool(false)},
		})
		if err != nil {
			// A whole-batch failure is reported per entry rather than as one error, so
			// the caller's count of what survived is right without having to know the
			// batch size. AccessDenied here is the expected outcome for a principal that
			// is not permitted to erase (§9.3), and it must not read as "nothing was
			// there".
			for _, v := range vs[start:end] {
				failures = append(failures, deleteFailure{Key: v.Key, VersionID: v.VersionID, Reason: err.Error()})
			}
			continue
		}
		for _, e := range res.Errors {
			failures = append(failures, deleteFailure{
				Key:       aws.ToString(e.Key),
				VersionID: aws.ToString(e.VersionId),
				Reason:    aws.ToString(e.Code) + ": " + aws.ToString(e.Message),
			})
		}
	}
	return failures, nil
}

// ---------------------------------------------------------------------------
// Sorting
// ---------------------------------------------------------------------------

// sortVersions orders an inventory deterministically.
//
// Determinism is not cosmetic here: §11.5 requires the dry-run to describe precisely what
// --apply then does, and a plan whose order varies between two enumerations of the same
// data cannot be compared against the outcome. Latest-first within a key so a report
// truncated for display shows the live object rather than an arbitrary old version.
func sortVersions(vs []objectVersion) {
	sort.SliceStable(vs, func(i, j int) bool {
		if vs[i].Key != vs[j].Key {
			return vs[i].Key < vs[j].Key
		}
		if vs[i].IsLatest != vs[j].IsLatest {
			return vs[i].IsLatest
		}
		return vs[i].VersionID < vs[j].VersionID
	})
}

// ---------------------------------------------------------------------------
// Fixture store
// ---------------------------------------------------------------------------

// fixtureStore is an in-memory objectStore for the credential-free harness (§11.5).
//
// It is strict where S3 is strict, for the reason repository.Memory states: a fake more
// permissive than the real thing lets a test pass on code that would fail in production.
// In particular a delete removes only the exact version asked for, so a test cannot pass
// by "deleting the key" the way a non-versioned bucket would.
type fixtureStore struct {
	name     string
	objects  map[string]objectVersion // version id -> version
	bodies   map[string][]byte        // version id -> bytes
	requests int
}

func newFixtureStore(name string) *fixtureStore {
	return &fixtureStore{
		name:    name,
		objects: make(map[string]objectVersion),
		bodies:  make(map[string][]byte),
	}
}

// add registers one version. Body may be nil for a delete marker.
func (f *fixtureStore) add(v objectVersion, body []byte) {
	f.objects[v.VersionID] = v
	if body != nil {
		f.bodies[v.VersionID] = body
	}
}

// Describe labels the store as a fixture. **Load-bearing, not decoration:** the report
// prints it, so an operator cannot mistake a fixture run's "erased 412 objects" for
// something that happened in an account.
func (f *fixtureStore) Describe() string { return "fixtures:" + f.name }

// Requests reports the calls a live store would have billed for.
func (f *fixtureStore) Requests() int { return f.requests }

// ListVersions returns the versions under prefix.
func (f *fixtureStore) ListVersions(_ context.Context, prefix string) ([]objectVersion, error) {
	if prefix == "" {
		return nil, fmt.Errorf("empty S3 prefix; every listing must be tenant-scoped (I11)")
	}
	f.requests++
	var out []objectVersion
	for _, v := range f.objects {
		if strings.HasPrefix(v.Key, prefix) {
			out = append(out, v)
		}
	}
	sortVersions(out)
	return out, nil
}

// GetObject returns one version's bytes.
func (f *fixtureStore) GetObject(_ context.Context, key, versionID string) (io.ReadCloser, error) {
	f.requests++
	body, ok := f.bodies[versionID]
	if !ok {
		return nil, fmt.Errorf("fixture has no body for %s version %q", key, versionID)
	}
	return io.NopCloser(strings.NewReader(string(body))), nil
}

// DeleteVersions removes exactly the versions given.
func (f *fixtureStore) DeleteVersions(_ context.Context, vs []objectVersion) ([]deleteFailure, error) {
	f.requests++
	var failures []deleteFailure
	for _, v := range vs {
		if _, ok := f.objects[v.VersionID]; !ok {
			// Absent is not a failure: S3 delete is idempotent, and erasure is required
			// to be (§9.3). A fake that errored here would make the re-run that proves
			// convergence fail.
			continue
		}
		delete(f.objects, v.VersionID)
		delete(f.bodies, v.VersionID)
	}
	return failures, nil
}
