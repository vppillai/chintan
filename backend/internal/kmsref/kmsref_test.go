package kmsref

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// The negative cases are the ones that matter. A resolver that returns the right
// key for a well-provisioned tenant but *also* returns something for a tenant with
// no kms_key_id is how the indirection stops being exercised (§6.3), and a
// classifier that answers "shreddable" optimistically is how an erasure report
// overclaims (G-021). Refusal and pessimism are the behaviour under test.

const (
	testKeyUUID   = "1234abcd-12ab-34cd-56ef-1234567890ab"
	testKeyARN    = "arn:aws:kms:eu-west-1:111122223333:key/" + testKeyUUID
	testCMKAlias  = "alias/voicenotes-prod"
	testCMKAlias2 = "alias/voicenotes-dev"
)

func TestParseClassifiesReferences(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want Kind
		// why records the consequence of getting this row wrong, so a future
		// edit that "simplifies" a row has to argue with the reason.
		why string
	}{
		"aws managed s3 alias": {
			raw: AWSManagedS3, want: KindAWSManaged,
			why: "§6.3's personal-phase value; misclassifying it as customer-managed would promise crypto-shredding",
		},
		"aws managed dynamodb alias": {
			raw: AWSManagedDynamoDB, want: KindAWSManaged,
		},
		"aws managed alias in arn form": {
			raw: "arn:aws:kms:eu-west-1:111122223333:" + AWSManagedS3, want: KindAWSManaged,
			why: "the ARN form of an AWS-managed alias must not read as customer-managed — the account cannot delete that key",
		},
		"customer alias": {
			raw: testCMKAlias, want: KindCustomerManaged,
		},
		"customer alias in arn form": {
			raw: "arn:aws:kms:eu-west-1:111122223333:" + testCMKAlias, want: KindCustomerManaged,
		},
		"customer alias in a non-commercial partition": {
			raw: "arn:aws-us-gov:kms:us-gov-west-1:111122223333:" + testCMKAlias, want: KindCustomerManaged,
			why: "region and partition are a deployment choice (§2A.1), not a code change",
		},
		"key arn is unverifiable offline": {
			raw: testKeyARN, want: KindUnverified,
			why: "an AWS-managed key has an identically shaped key ARN; only DescribeKey's KeyManager separates them",
		},
		"bare key id is unverifiable offline": {
			raw: testKeyUUID, want: KindUnverified,
		},
		"multi region key id": {
			raw: "mrk-" + strings.Repeat("a", 32), want: KindUnverified,
		},
		"key arn with uppercase hex": {
			raw: "arn:aws:kms:eu-west-1:111122223333:key/1234ABCD-12AB-34CD-56EF-1234567890AB", want: KindUnverified,
			why: "a UUID's hex carries no case meaning and operators paste this between consoles; refusing it fails every write for the tenant with a message saying the ARN does not end in a key ID while it visibly does",
		},
		"bare key id with uppercase hex": {
			raw: strings.ToUpper(testKeyUUID), want: KindUnverified,
		},
		"customer alias that merely starts with aws": {
			raw: "alias/awsome-key", want: KindCustomerManaged,
			why: "the reserved-neighbourhood refusal must key on alias/aws and alias/aws/, not on the letters a-w-s, or a legitimate alias is refused forever",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ref, err := Parse(c.raw)
			if err != nil {
				t.Fatalf("Parse(%q) failed: %v (%s)", c.raw, err, c.why)
			}
			if ref.Kind() != c.want {
				t.Errorf("Parse(%q).Kind() = %q, want %q (%s)", c.raw, ref.Kind(), c.want, c.why)
			}
			if ref.ID() != c.raw {
				t.Errorf("Parse(%q).ID() = %q; the reference must survive verbatim, because it is what an encryption call passes to AWS", c.raw, ref.ID())
			}
		})
	}
}

func TestParseRejectsUnusableReferences(t *testing.T) {
	cases := map[string]struct {
		raw     string
		wantErr error
		// wantMsg is asserted so a value rejected for the wrong reason cannot
		// pass this test while leaving the real constraint unverified.
		wantMsg string
	}{
		"empty": {
			raw: "", wantErr: ErrAbsent, wantMsg: "never absent",
		},
		"whitespace only": {
			raw: "  \t ", wantErr: ErrAbsent, wantMsg: "never absent",
		},
		"reserved prefix naming no service": {
			raw: "alias/aws/", wantErr: ErrMalformed, wantMsg: "not a service segment",
		},
		// The truncations and case variants of alias/aws/s3. Each of these once
		// classified as customer-managed, so CryptoShreddable() answered true for a
		// personal-phase tenant with no CMK and the §9.3 report printed
		// "crypto-shredding is available" while ScheduleKeyDeletion had nothing to
		// schedule (G-021: found during audit, not during testing).
		"reserved prefix truncated at the last slash": {
			raw: "alias/aws", wantErr: ErrMalformed, wantMsg: "truncated or differently-cased",
		},
		"reserved prefix with the service uppercased": {
			raw: "alias/AWS/s3", wantErr: ErrMalformed, wantMsg: "truncated or differently-cased",
		},
		"reserved prefix with an uppercase service segment": {
			raw: "alias/aws/S3", wantErr: ErrMalformed, wantMsg: "not a service segment",
		},
		"reserved alias with an extra segment": {
			raw: "alias/aws/s3/extra", wantErr: ErrMalformed, wantMsg: "not a service segment",
		},
		"alias prefix uppercased": {
			raw: "Alias/voicenotes-prod", wantErr: ErrMalformed, wantMsg: "case-sensitive",
		},
		"alias with no name": {
			raw: "alias/", wantErr: ErrMalformed, wantMsg: "not a valid KMS alias",
		},
		"alias name that is only a slash": {
			// "alias//" is a legal alias name to KMS, and in practice it is a
			// truncated or double-joined path.
			raw: "alias//", wantErr: ErrMalformed, wantMsg: "not a valid KMS alias",
		},
		"alias name starting with a slash": {
			raw: "alias//voicenotes-prod", wantErr: ErrMalformed, wantMsg: "not a valid KMS alias",
		},
		"alias containing whitespace": {
			raw: "alias/my key", wantErr: ErrMalformed, wantMsg: "not a valid KMS alias",
		},
		"arn for another service": {
			raw: "arn:aws:s3:::voicenotes-dev-media", wantErr: ErrMalformed, wantMsg: "not a KMS ARN",
		},
		"arn missing region": {
			raw: "arn:aws:kms::111122223333:" + testCMKAlias, wantErr: ErrMalformed, wantMsg: "not a KMS ARN",
		},
		"arn with a short account id": {
			raw: "arn:aws:kms:eu-west-1:1122:" + testCMKAlias, wantErr: ErrMalformed, wantMsg: "not a KMS ARN",
		},
		"key arn with a mangled key id": {
			raw: "arn:aws:kms:eu-west-1:111122223333:key/1234abcd", wantErr: ErrMalformed, wantMsg: "does not end in a KMS key ID",
		},
		"the sse algorithm mistaken for a key reference": {
			// A plausible provisioning error: §6.2 says the bucket uses AES256,
			// so "AES256" looks like the answer to "what key is this tenant on".
			raw: SSES3, wantErr: ErrMalformed, wantMsg: "not an alias",
		},
		"prose": {
			raw: "aws managed", wantErr: ErrMalformed, wantMsg: "not an alias",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ref, err := Parse(c.raw)
			if err == nil {
				t.Fatalf("Parse(%q) was accepted as %q; an unrecognised value must be a defect to fix, not an input to interpret", c.raw, ref.Kind())
			}
			if !errors.Is(err, c.wantErr) {
				t.Errorf("Parse(%q) error = %v, want errors.Is(_, %v)", c.raw, err, c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("Parse(%q) rejection did not mention %q: %v", c.raw, c.wantMsg, err)
			}
			if !ref.IsZero() {
				t.Errorf("Parse(%q) returned a usable Ref alongside an error; a caller ignoring the error would encrypt under it", c.raw)
			}
			// The specific consequence for the reserved-prefix near-misses: a
			// caller that ignored the error would ask §9.3's question and be told
			// the data can be shredded.
			if ref.CryptoShreddable() {
				t.Errorf("Parse(%q) rejected the value but the returned Ref claims to be crypto-shreddable", c.raw)
			}
		})
	}
}

// §9.3 depends on this distinction, and every wrong answer here is an erasure
// report that claims more than it delivered (G-021).
func TestCryptoShreddabilityIsAnsweredPessimistically(t *testing.T) {
	cases := map[string]struct {
		raw       string
		want      bool
		caveatHas string
	}{
		"aws managed cannot be destroyed by the account": {
			raw: AWSManagedS3, want: false, caveatHas: "PITR",
		},
		"unverified key arn must not claim shreddability": {
			raw: testKeyARN, want: false, caveatHas: "DescribeKey",
		},
		"customer managed alias can be shredded": {
			raw: testCMKAlias, want: true, caveatHas: "7 days",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ref, err := Parse(c.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := ref.CryptoShreddable(); got != c.want {
				t.Errorf("CryptoShreddable(%q) = %v, want %v", c.raw, got, c.want)
			}
			caveat := ref.ErasureCaveat()
			if caveat == "" {
				t.Fatal("ErasureCaveat is empty; a caveat that is not printed is how a completed-erasure claim overclaims (G-021)")
			}
			if !strings.Contains(caveat, c.caveatHas) {
				t.Errorf("ErasureCaveat(%q) does not mention %q: %s", c.raw, c.caveatHas, caveat)
			}
		})
	}

	// Even the shreddable case carries a caveat: KMS will not destroy a key
	// before its pending-deletion window expires, so "erased" is not yet true at
	// the moment the operation returns.
	ref, err := Parse(testCMKAlias)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ref.ErasureCaveat(), "pending-deletion") {
		t.Errorf("the customer-managed caveat omits the pending-deletion window: %s", ref.ErasureCaveat())
	}

	// An unresolved Ref must state that it can guarantee nothing rather than
	// returning an empty string a report would render as "no caveats".
	if (Ref{}).ErasureCaveat() == "" {
		t.Error("the zero Ref's caveat is empty; an unresolved key reference must not read as an unqualified guarantee")
	}
	if (Ref{}).CryptoShreddable() {
		t.Error("the zero Ref claims to be shreddable")
	}
}

// The over-claim itself. A Ref is a pure value: it cannot know when the tenant was
// pointed at this key, and it cannot know what the DynamoDB table is encrypted
// with, so it must not assert coverage of either.
func TestCustomerManagedCaveatDoesNotClaimCompleteness(t *testing.T) {
	ref, err := Parse(testCMKAlias)
	if err != nil {
		t.Fatal(err)
	}
	caveat := ref.ErasureCaveat()

	// The exact phrasing this replaced. It was quoted verbatim into the §9.3
	// report, and it was false for every object written before the repoint.
	if strings.Contains(caveat, "renders the tenant's data unrecoverable") {
		t.Errorf("the caveat still claims the tenant's data — not merely what is encrypted under this key — becomes unrecoverable: %s", caveat)
	}
	for _, want := range []string{
		// The time bound: pre-repoint objects survive.
		"before this tenant was pointed at this key",
		"survives this key's destruction",
		// The store bound: DynamoDB is encrypted at table level.
		"table level",
		// Where a complete answer comes from.
		TenantAttrKMSKeyIDSince,
	} {
		if !strings.Contains(caveat, want) {
			t.Errorf("the customer-managed caveat omits %q, so a report quoting it over-claims (G-021): %s", want, caveat)
		}
	}
}

// S3Encryption is the only value in this package that reaches the wire, so it gets
// the same zero-value refusal Ref has. An empty x-amz-server-side-encryption header
// either 403s the PUT with a signature complaint that never mentions encryption, or
// is dropped by a normalising signer and the object silently takes the bucket
// default — the outcome this type exists to prevent (G-020).
func TestS3EncryptionRefusesUnusableParameters(t *testing.T) {
	cases := map[string]struct {
		enc     S3Encryption
		wantErr error
		wantMsg string
	}{
		"the zero value": {
			enc: S3Encryption{}, wantErr: ErrAbsent, wantMsg: "nothing to sign",
		},
		"sse-kms with no key id": {
			enc: S3Encryption{sse: SSEKMS}, wantErr: ErrAbsent, wantMsg: "bucket or account default",
		},
		"aes256 carrying a key id": {
			enc: S3Encryption{sse: SSES3, kmsKeyID: testCMKAlias}, wantErr: ErrKeyMismatch, wantMsg: "bill per object",
		},
		"an unrecognised algorithm": {
			enc: S3Encryption{sse: "sse-c"}, wantErr: ErrAbsent, wantMsg: "nothing to sign",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h, err := c.enc.Headers()
			if err == nil {
				t.Fatalf("Headers() returned %v with no error; a presigner would sign it", h)
			}
			if !errors.Is(err, c.wantErr) {
				t.Errorf("error = %v, want errors.Is(_, %v)", err, c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("refusal did not mention %q: %v", c.wantMsg, err)
			}
			if h != nil {
				t.Errorf("Headers() = %v alongside an error; a caller discarding the error must get nothing to sign", h)
			}
		})
	}

	// And the value an ignored ForS3Put error leaves behind is that zero value,
	// not something that presigns.
	enc, err := (Ref{}).ForS3Put(S3ServiceDefault())
	if err == nil {
		t.Fatal("an unresolved Ref produced encryption parameters")
	}
	if !enc.IsZero() {
		t.Errorf("ForS3Put returned %+v alongside an error", enc)
	}
	if _, err := enc.Headers(); !errors.Is(err, ErrAbsent) {
		t.Errorf("the parameters left behind by a failed ForS3Put presign as %v, want ErrAbsent", err)
	}
}

func TestForS3PutOnAWSManagedUsesFreeSSES3(t *testing.T) {
	ref := S3ServiceDefault()
	enc, err := ref.ForS3Put(S3ServiceDefault())
	if err != nil {
		t.Fatalf("the personal-phase path must work: %v", err)
	}
	if enc.SSE() != SSES3 {
		t.Errorf("SSE = %q, want %q (§6.2)", enc.SSE(), SSES3)
	}
	// The load-bearing assertion: passing alias/aws/s3 as an SSE-KMS key ID is
	// accepted by S3 and silently converts free SSE-S3 into billed SSE-KMS on
	// every object written (G-020, I8's "free").
	if enc.KMSKeyID() != "" {
		t.Errorf("KMSKeyID = %q; an AWS-managed alias must not be sent as an SSE-KMS key ID, which would bill per object for identical protection", enc.KMSKeyID())
	}
	h, err := enc.Headers()
	if err != nil {
		t.Fatalf("Headers() refused the personal-phase parameters: %v", err)
	}
	if got := h[HeaderSSE]; got != SSES3 {
		t.Errorf("Headers()[%s] = %q, want %q; a presigned PUT must sign the header the client sends (I3)", HeaderSSE, got, SSES3)
	}
	if _, ok := h[HeaderSSEKeyID]; ok {
		t.Errorf("Headers() carries %s with no key to name", HeaderSSEKeyID)
	}
}

func TestForS3PutOnCustomerManagedNamesTheKey(t *testing.T) {
	tenant, err := Parse(testCMKAlias)
	if err != nil {
		t.Fatal(err)
	}
	// Bucket default differing from the tenant's key is the intended
	// commercial-phase arrangement: per-object SSE overrides it.
	bucket, err := Parse(testCMKAlias2)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := tenant.ForS3Put(bucket)
	if err != nil {
		t.Fatalf("a per-tenant CMK differing from the bucket default is not a fault: %v", err)
	}
	if enc.SSE() != SSEKMS || enc.KMSKeyID() != testCMKAlias {
		t.Fatalf("got SSE=%q key=%q, want SSE=%q key=%q", enc.SSE(), enc.KMSKeyID(), SSEKMS, testCMKAlias)
	}
	h, err := enc.Headers()
	if err != nil {
		t.Fatalf("Headers() refused a resolved CMK: %v", err)
	}
	if h[HeaderSSE] != SSEKMS || h[HeaderSSEKeyID] != testCMKAlias {
		t.Errorf("headers = %v; the presigner and the client must derive both from one source (I3)", h)
	}
}

// The commercial-phase branch, executed today: this is the silent failure the S3
// side of the indirection exists to catch.
func TestForS3PutRefusesADowngradeAwayFromACMK(t *testing.T) {
	bucket, err := Parse(testCMKAlias)
	if err != nil {
		t.Fatal(err)
	}
	_, err = S3ServiceDefault().ForS3Put(bucket)
	if err == nil {
		t.Fatal("an AES256 write into a CMK-defaulted bucket was allowed; that object would silently survive crypto-shredding")
	}
	if !errors.Is(err, ErrKeyMismatch) {
		t.Errorf("error = %v, want errors.Is(_, ErrKeyMismatch)", err)
	}
	if !strings.Contains(err.Error(), "crypto-shredding") {
		t.Errorf("the refusal does not name the consequence: %v", err)
	}
}

func TestForS3PutRefusesUnresolvedReferences(t *testing.T) {
	if _, err := (Ref{}).ForS3Put(S3ServiceDefault()); !errors.Is(err, ErrAbsent) {
		t.Errorf("an unresolved tenant reference produced %v; it must not fall back to the service default", err)
	}
	if _, err := S3ServiceDefault().ForS3Put(Ref{}); !errors.Is(err, ErrAbsent) {
		t.Errorf("an unset bucket reference produced %v; with nothing to compare against, a downgrade cannot be detected", err)
	}
}

func TestCheckDynamoPut(t *testing.T) {
	cmk, err := Parse(testCMKAlias)
	if err != nil {
		t.Fatal(err)
	}
	otherCMK, err := Parse(testCMKAlias2)
	if err != nil {
		t.Fatal(err)
	}
	keyARN, err := Parse(testKeyARN)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		tenant, table Ref
		wantErr       bool
		wantMsg       string
	}{
		"personal phase, matching service defaults": {
			tenant: DynamoDBServiceDefault(), table: DynamoDBServiceDefault(),
		},
		"personal phase, tenant recorded the s3 alias against the dynamodb table": {
			// §6.3 names both aliases as the personal-phase value of a single
			// attribute, so this combination is expected. With an AWS-managed key
			// there is nothing to pass per request and nothing to shred, so the
			// two references assert the same fact — refusing here would fail
			// every write for a difference with no consequence.
			tenant: S3ServiceDefault(), table: DynamoDBServiceDefault(),
		},
		"tenant on a cmk, table on the aws-managed key": {
			tenant: cmk, table: DynamoDBServiceDefault(),
			wantErr: true, wantMsg: "overclaim",
		},
		"tenant on the aws-managed key, table on a cmk": {
			tenant: DynamoDBServiceDefault(), table: cmk,
			wantErr: true, wantMsg: "misdescribes",
		},
		"tenant and table on the same cmk": {
			tenant: cmk, table: cmk,
		},
		"tenant and table on different cmks": {
			tenant: cmk, table: otherCMK,
			wantErr: true, wantMsg: "table per key",
		},
		"alias and key arn cannot be matched offline": {
			tenant: cmk, table: keyARN,
			wantErr: true, wantMsg: "DescribeKey",
		},
		"unresolved tenant reference": {
			tenant: Ref{}, table: DynamoDBServiceDefault(),
			wantErr: true, wantMsg: "no tenant key reference resolved",
		},
		"unset table reference": {
			tenant: DynamoDBServiceDefault(), table: Ref{},
			wantErr: true, wantMsg: "table key reference is unset",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := c.tenant.CheckDynamoPut(c.table)
			if !c.wantErr {
				if err != nil {
					t.Fatalf("write refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("tenant %s against table %s was allowed; DynamoDB encrypts at table level, so the disagreement would surface only as an erasure that shredded nothing", c.tenant, c.table)
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("refusal did not mention %q: %v", c.wantMsg, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Resolver
// ---------------------------------------------------------------------------

const testTenant = keys.TenantID("t_01HZZ")

// newResolver builds a personal-phase resolver over an in-memory repository. No
// AWS and no credentials, which is what lets this run in the check suite (§0.5A).
func newResolver(t *testing.T) (*Resolver, *repository.Memory) {
	t.Helper()
	repo := repository.NewMemory()
	rs, err := New(repo, Deployment{Bucket: S3ServiceDefault(), Table: DynamoDBServiceDefault()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return rs, repo
}

// putTenant seeds a tenant record with the given kms_key_id attribute value. Takes
// `any` so the wrong-type case can be exercised — DynamoDB will happily store a
// number in an attribute the code expects to be a string.
func putTenant(t *testing.T, repo *repository.Memory, tenant keys.TenantID, attrs map[string]any) {
	t.Helper()
	key, err := keys.Tenant(tenant)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Put(context.Background(), repository.Item{Key: key, Attrs: attrs}); err != nil {
		t.Fatal(err)
	}
}

// The §Phase 0 requirement in one test: a personal-phase tenant resolves through
// the indirection on both write paths, so both are exercised before a CMK exists.
func TestPersonalPhaseTenantResolvesOnBothWritePaths(t *testing.T) {
	rs, repo := newResolver(t)
	putTenant(t, repo, testTenant, map[string]any{TenantAttrKMSKeyID: AWSManagedS3})
	ctx := context.Background()

	enc, err := rs.ForS3Put(ctx, testTenant)
	if err != nil {
		t.Fatalf("ForS3Put: %v", err)
	}
	if enc.SSE() != SSES3 || enc.KMSKeyID() != "" {
		t.Errorf("got SSE=%q key=%q, want %q with no key ID (I8, §6.2)", enc.SSE(), enc.KMSKeyID(), SSES3)
	}
	if err := rs.CheckDynamoPut(ctx, testTenant); err != nil {
		t.Errorf("CheckDynamoPut refused today's only configuration: %v", err)
	}

	ref, err := rs.Resolve(ctx, testTenant)
	if err != nil {
		t.Fatal(err)
	}
	if ref.CryptoShreddable() {
		t.Error("a personal-phase tenant reported as crypto-shreddable; §9.3 says erasure falls back to object deletion plus the retention window")
	}
}

// The whole point of §6.3's "never null and never absent". A default here would be
// invisible: every write would keep working, and the indirection would be dead
// code that nobody notices until a CMK arrives.
func TestResolveRefusesATenantWithNoKeyReference(t *testing.T) {
	cases := map[string]struct {
		attrs   map[string]any
		wantErr error
	}{
		"attribute missing":    {attrs: map[string]any{"plan": "personal"}, wantErr: ErrAbsent},
		"attribute empty":      {attrs: map[string]any{TenantAttrKMSKeyID: ""}, wantErr: ErrAbsent},
		"attribute whitespace": {attrs: map[string]any{TenantAttrKMSKeyID: "   "}, wantErr: ErrAbsent},
		"attribute not a string": {
			attrs: map[string]any{TenantAttrKMSKeyID: 42}, wantErr: ErrMalformed,
		},
		"attribute unparseable": {
			attrs: map[string]any{TenantAttrKMSKeyID: "the-usual-one"}, wantErr: ErrMalformed,
		},
		// §6.3 names null first — "never null and never absent" — and a repair
		// script branching on ErrAbsent to mean "provision this tenant's key
		// reference" must not skip the case the spec put first.
		"attribute null": {
			attrs: map[string]any{TenantAttrKMSKeyID: nil}, wantErr: ErrAbsent,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rs, repo := newResolver(t)
			putTenant(t, repo, testTenant, c.attrs)

			ref, err := rs.Resolve(context.Background(), testTenant)
			if err == nil {
				t.Fatalf("resolved to %s; a substituted default is how the indirection stops being exercised (§6.3)", ref)
			}
			if !errors.Is(err, c.wantErr) {
				t.Errorf("error = %v, want errors.Is(_, %v)", err, c.wantErr)
			}
			if !ref.IsZero() {
				t.Error("a usable Ref was returned alongside the error")
			}

			// And the write paths must refuse too, not merely the resolver: a
			// caller that reached storage anyway would write under the ambient
			// key with no record of what encrypted it. Asserting the sentinel,
			// not merely that something failed — a refusal for the wrong reason
			// loses the operator's fix.
			enc, err := rs.ForS3Put(context.Background(), testTenant)
			if !errors.Is(err, c.wantErr) {
				t.Errorf("ForS3Put error = %v, want errors.Is(_, %v)", err, c.wantErr)
			}
			if !enc.IsZero() {
				t.Errorf("ForS3Put returned %+v alongside an error", enc)
			}
			if err := rs.CheckDynamoPut(context.Background(), testTenant); !errors.Is(err, c.wantErr) {
				t.Errorf("CheckDynamoPut error = %v, want errors.Is(_, %v)", err, c.wantErr)
			}
			// The erasure path must refuse too, and must not hand back a scope
			// whose Caveat() a report would print as though it were resolved.
			scope, err := rs.ErasureScope(context.Background(), testTenant)
			if !errors.Is(err, c.wantErr) {
				t.Errorf("ErasureScope error = %v, want errors.Is(_, %v)", err, c.wantErr)
			}
			if scope.CryptoShreddable() || scope.CoversAllObjects() || scope.CoversDynamoDB() {
				t.Errorf("the scope returned alongside an error claims coverage: %+v", scope)
			}
		})
	}
}

// §9.2 keeps PII out of messages, and in the personal phase tenant_id == user_id.
//
// Every error path is exercised, not just the convenient one. The unprovisioned
// tenant is the headline case — it is the *first* call for any tenant — and
// repository embeds the partition key in every error it returns, including
// ErrNotFound, so wrapping one with %w puts the tenant partition key — prefix plus
// the user's email — in a string the handler logs.
func TestResolveErrorsExcludeTheTenant(t *testing.T) {
	const tenant = keys.TenantID("someone@example.com")
	tenantKey, err := keys.Tenant(tenant)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]func(t *testing.T) (*Resolver, keys.TenantID){
		"no tenant record at all": func(t *testing.T) (*Resolver, keys.TenantID) {
			rs, _ := newResolver(t)
			return rs, tenant
		},
		"tenant record with no key reference": func(t *testing.T) (*Resolver, keys.TenantID) {
			rs, repo := newResolver(t)
			putTenant(t, repo, tenant, map[string]any{"plan": "personal"})
			return rs, tenant
		},
		"storage failure whose message quotes the key": func(t *testing.T) (*Resolver, keys.TenantID) {
			rs, repo := newResolver(t)
			putTenant(t, repo, tenant, map[string]any{TenantAttrKMSKeyID: AWSManagedS3})
			// The shape repository.Dynamo produces: "repository: get %s / %s: %w".
			repo.FailNext(fmt.Errorf("repository: get %s / %s: throttled", tenantKey.PK, tenantKey.SK))
			return rs, tenant
		},
		"malformed tenant id, which keys quotes back": func(t *testing.T) (*Resolver, keys.TenantID) {
			rs, _ := newResolver(t)
			return rs, keys.TenantID("someone@example.com/../other")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			rs, tenant := setup(t)
			_, err := rs.Resolve(context.Background(), tenant)
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if strings.Contains(err.Error(), "someone@example.com") {
				t.Errorf("the error echoes the tenant, which is the user identity in the personal phase (§9.2): %v", err)
			}
			if strings.Contains(err.Error(), tenantKey.PK) {
				t.Errorf("the error echoes the tenant partition key (§9.2): %v", err)
			}
		})
	}
}

// Withholding the message must not cost the caller the reason: the retry decision
// and the unprovisioned-versus-partially-provisioned distinction are both made from
// the wrapped cause.
func TestWithheldStorageMessagesKeepTheirIdentity(t *testing.T) {
	rs, _ := newResolver(t)
	_, err := rs.Resolve(context.Background(), testTenant)
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("a missing tenant record no longer matches repository.ErrNotFound, so a caller cannot tell it from a partial provisioning: %v", err)
	}

	rs2, repo := newResolver(t)
	putTenant(t, repo, testTenant, map[string]any{TenantAttrKMSKeyID: AWSManagedS3})
	throttle := errors.New("throttled")
	// Shaped like repository.Dynamo's own wrapping ("repository: get %s / %s: %w"),
	// with the key built through keys so no prefix literal appears here (I11).
	key, err := keys.Tenant(testTenant)
	if err != nil {
		t.Fatal(err)
	}
	repo.FailNext(fmt.Errorf("repository: get %s / %s: %w", key.PK, key.SK, throttle))
	_, err = rs2.Resolve(context.Background(), testTenant)
	if !errors.Is(err, throttle) {
		t.Errorf("the storage failure's identity was dropped along with its message, so a throttle is no longer distinguishable: %v", err)
	}
	if errors.Is(err, ErrAbsent) || errors.Is(err, repository.ErrNotFound) {
		t.Errorf("a throttle reads as a provisioning defect, which is the retryable/not-retryable distinction the sentinels promise: %v", err)
	}
}

func TestResolveRefusesAnEmptyTenant(t *testing.T) {
	rs, _ := newResolver(t)
	// Whitespace-only included: it would otherwise read a partition nothing
	// writes to, which is harder to notice than an outright error (I11).
	for _, empty := range []keys.TenantID{"", " ", "\t"} {
		// Asserting the reason, not just the refusal: the message must name the
		// invariant so the caller knows the tenant was never read, rather than
		// read and found unprovisioned.
		_, err := rs.Resolve(context.Background(), empty)
		if err == nil {
			t.Fatalf("Resolve(%q) succeeded; every read must be tenant-scoped (I11)", string(empty))
		}
		if !strings.Contains(err.Error(), "I11") {
			t.Errorf("Resolve(%q) refused without naming the invariant: %v", string(empty), err)
		}
		if errors.Is(err, ErrAbsent) {
			t.Errorf("Resolve(%q) reads as an absent key reference, but no tenant record was read at all: %v", string(empty), err)
		}
		enc, err := rs.ForS3Put(context.Background(), empty)
		if err == nil {
			t.Errorf("ForS3Put(%q) succeeded; every read must be tenant-scoped (I11)", string(empty))
		}
		if !enc.IsZero() {
			t.Errorf("ForS3Put(%q) returned %+v alongside an error", string(empty), enc)
		}
		if err := rs.CheckDynamoPut(context.Background(), empty); err == nil {
			t.Errorf("CheckDynamoPut(%q) succeeded; every read must be tenant-scoped (I11)", string(empty))
		}
		if _, err := rs.ErasureScope(context.Background(), empty); err == nil {
			t.Errorf("ErasureScope(%q) succeeded; every read must be tenant-scoped (I11)", string(empty))
		}
	}
}

// An unprovisioned tenant and a partially provisioned one have different fixes, so
// the caller must be able to tell them apart.
func TestResolveDistinguishesAMissingTenantFromAMissingKeyReference(t *testing.T) {
	rs, _ := newResolver(t)
	_, err := rs.Resolve(context.Background(), testTenant)
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("error = %v, want errors.Is(_, repository.ErrNotFound)", err)
	}
	if errors.Is(err, ErrAbsent) {
		t.Error("a missing tenant record reads as a missing kms_key_id; the two have different fixes")
	}
}

// The resolver reads the record on every call, deliberately: a cached reference
// keeps encrypting under a superseded key after a tenant is repointed, and the
// objects that then survive crypto-shredding are unidentifiable.
func TestResolveRereadsTheTenantRecord(t *testing.T) {
	rs, repo := newResolver(t)
	ctx := context.Background()
	putTenant(t, repo, testTenant, map[string]any{TenantAttrKMSKeyID: AWSManagedS3})
	if _, err := rs.Resolve(ctx, testTenant); err != nil {
		t.Fatal(err)
	}

	putTenant(t, repo, testTenant, map[string]any{TenantAttrKMSKeyID: testCMKAlias})
	ref, err := rs.Resolve(ctx, testTenant)
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID() != testCMKAlias {
		t.Fatalf("resolved %s after the tenant was repointed at %s; a stale reference writes new data under a key that will not be shredded", ref, testCMKAlias)
	}
	// And the flip is then visible on the write path with no code change (I8) —
	// here as a refusal, because a single AWS-managed table cannot honour a
	// per-tenant CMK.
	if err := rs.CheckDynamoPut(ctx, testTenant); !errors.Is(err, ErrKeyMismatch) {
		t.Errorf("CheckDynamoPut = %v, want ErrKeyMismatch: the table is still on the AWS-managed key", err)
	}

	// The claim the repoint just turned on. CryptoShreddable() now answers true,
	// and every object written in the AWS-managed period is SSE-S3 under a key the
	// account cannot destroy — so nothing here may report the tenant's data as
	// unrecoverable.
	scope, err := rs.ErasureScope(ctx, testTenant)
	if err != nil {
		t.Fatal(err)
	}
	if !scope.CryptoShreddable() {
		t.Fatal("the repointed tenant is not reported as shreddable at all, so the caveat under test is the wrong one")
	}
	if scope.CoversAllObjects() {
		t.Error("the scope claims every object is under the new key, with no kms_key_id_since recorded to support it (G-021)")
	}
	if scope.CoversDynamoDB() {
		t.Error("the scope claims DynamoDB coverage while the table is on the AWS-managed key; destroying the tenant key shreds no items")
	}
	if scope.PreFlipBoundary() != "" {
		t.Errorf("PreFlipBoundary = %q with nothing recorded; erasure would sort objects against a boundary that was invented", scope.PreFlipBoundary())
	}
	caveat := scope.Caveat()
	for _, want := range []string{
		"every object may predate it",
		"DynamoDB items are NOT covered",
		"pending-deletion window",
	} {
		if !strings.Contains(caveat, want) {
			t.Errorf("the post-repoint caveat omits %q: %s", want, caveat)
		}
	}
}

// The flip in full. The reviewer's reproduction: 90 days of AWS-managed writes, a
// repoint to a CMK, an erasure request on day 92. What the report may say depends
// entirely on what the tenant record proves about *when* the key arrived.
func TestErasureScopeBoundsTheClaimByRepointTime(t *testing.T) {
	const created = "2026-01-01T00:00:00Z"
	const repointed = "2026-04-01T00:00:00Z"

	cases := map[string]struct {
		attrs            map[string]any
		wantCoversAll    bool
		wantBoundary     string
		wantCaveatHas    []string
		wantCaveatLacks  []string
		wantProvenanceIs string
	}{
		"repointed long after the tenant was created": {
			attrs: map[string]any{
				TenantAttrKMSKeyID:      testCMKAlias,
				TenantAttrCreatedAt:     created,
				TenantAttrKMSKeyIDSince: repointed,
			},
			wantBoundary:  repointed,
			wantCaveatHas: []string{"does NOT cover objects written before " + repointed, "wait out"},
			// The objects before the repoint are the ones that survive, so the
			// report must not describe the coverage as total.
			wantCaveatLacks: []string{"all of this tenant's objects"},
		},
		"provisioned onto the key at creation": {
			attrs: map[string]any{
				TenantAttrKMSKeyID:      testCMKAlias,
				TenantAttrCreatedAt:     created,
				TenantAttrKMSKeyIDSince: created,
			},
			wantCoversAll: false,
			wantBoundary:  created,
			wantCaveatHas: []string{"survive this key's destruction"},
		},
		"repoint recorded but creation is not": {
			attrs: map[string]any{
				TenantAttrKMSKeyID:      testCMKAlias,
				TenantAttrKMSKeyIDSince: repointed,
			},
			wantBoundary:    repointed,
			wantCaveatHas:   []string{"does NOT cover objects written before " + repointed},
			wantCaveatLacks: []string{"all of this tenant's objects"},
		},
		"no repoint recorded": {
			attrs: map[string]any{
				TenantAttrKMSKeyID:  testCMKAlias,
				TenantAttrCreatedAt: created,
			},
			wantCaveatHas:   []string{"every object may predate it", "records no " + TenantAttrKMSKeyIDSince},
			wantCaveatLacks: []string{"all of this tenant's objects"},
		},
		"repoint stamp is not a timestamp": {
			attrs: map[string]any{
				TenantAttrKMSKeyID:      testCMKAlias,
				TenantAttrCreatedAt:     created,
				TenantAttrKMSKeyIDSince: "last tuesday",
			},
			wantCaveatHas:    []string{"every object may predate it", "Provisioning defect to fix"},
			wantProvenanceIs: "the tenant record's " + TenantAttrKMSKeyIDSince + " attribute is not an RFC3339 timestamp",
		},
		"repoint stamp is a number": {
			attrs: map[string]any{
				TenantAttrKMSKeyID:      testCMKAlias,
				TenantAttrCreatedAt:     created,
				TenantAttrKMSKeyIDSince: 1767225600,
			},
			wantCaveatHas:    []string{"every object may predate it"},
			wantProvenanceIs: "the tenant record's " + TenantAttrKMSKeyIDSince + " attribute is not a string",
		},
		"repoint stamp is whitespace": {
			attrs: map[string]any{
				TenantAttrKMSKeyID:      testCMKAlias,
				TenantAttrCreatedAt:     created,
				TenantAttrKMSKeyIDSince: "   ",
			},
			wantCaveatHas:    []string{"every object may predate it"},
			wantProvenanceIs: "the tenant record's " + TenantAttrKMSKeyIDSince + " attribute is empty",
		},
		"creation stamp unusable, repoint recorded": {
			// Nothing to compare the repoint against, so it stands as a boundary
			// rather than as proof that no object predates it.
			attrs: map[string]any{
				TenantAttrKMSKeyID:      testCMKAlias,
				TenantAttrCreatedAt:     "the beginning",
				TenantAttrKMSKeyIDSince: repointed,
			},
			wantBoundary:    repointed,
			wantCaveatHas:   []string{"does NOT cover objects written before " + repointed},
			wantCaveatLacks: []string{"all of this tenant's objects"},
		},
		"repoint stamp is null": {
			// A DynamoDB NULL is indistinguishable in effect from absence, and
			// both must land on the unknown case rather than on a claim.
			attrs: map[string]any{
				TenantAttrKMSKeyID:      testCMKAlias,
				TenantAttrCreatedAt:     created,
				TenantAttrKMSKeyIDSince: nil,
			},
			wantCaveatHas: []string{"every object may predate it"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rs, repo := newResolver(t)
			putTenant(t, repo, testTenant, c.attrs)

			scope, err := rs.ErasureScope(context.Background(), testTenant)
			if err != nil {
				t.Fatal(err)
			}
			if got := scope.CoversAllObjects(); got != c.wantCoversAll {
				t.Errorf("CoversAllObjects() = %v, want %v; a wrong answer here is an erasure report that claims more than it delivered (G-021)", got, c.wantCoversAll)
			}
			if got := scope.PreFlipBoundary(); got != c.wantBoundary {
				t.Errorf("PreFlipBoundary() = %q, want %q", got, c.wantBoundary)
			}
			if got := scope.ProvenanceDefect(); got != c.wantProvenanceIs {
				t.Errorf("ProvenanceDefect() = %q, want %q", got, c.wantProvenanceIs)
			}
			caveat := scope.Caveat()
			for _, want := range c.wantCaveatHas {
				if !strings.Contains(caveat, want) {
					t.Errorf("caveat omits %q: %s", want, caveat)
				}
			}
			for _, unwanted := range c.wantCaveatLacks {
				if strings.Contains(caveat, unwanted) {
					t.Errorf("caveat claims %q, which this tenant record does not support: %s", unwanted, caveat)
				}
			}
			// Whatever the provenance, the DynamoDB exclusion stands: the table
			// here is on the AWS-managed key, and DynamoDB encrypts at table
			// level, so destroying a tenant CMK shreds no items.
			if scope.CoversDynamoDB() {
				t.Error("CoversDynamoDB() is true against an AWS-managed table")
			}
			if !strings.Contains(caveat, "DynamoDB items are NOT covered") {
				t.Errorf("caveat does not exclude DynamoDB: %s", caveat)
			}
		})
	}
}

// The other half of the DynamoDB claim: it is allowed to be positive, but only when
// the table is genuinely under the same key. Delegating to CheckDynamoPut is what
// keeps the erasure claim and the write-time refusal from disagreeing.
func TestErasureScopeCoversDynamoDBOnlyWhenTheTableSharesTheKey(t *testing.T) {
	cmk, err := Parse(testCMKAlias)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Parse(testCMKAlias2)
	if err != nil {
		t.Fatal(err)
	}
	const created = "2026-01-01T00:00:00Z"
	attrs := map[string]any{
		TenantAttrKMSKeyID:      testCMKAlias,
		TenantAttrCreatedAt:     created,
		TenantAttrKMSKeyIDSince: created,
	}

	cases := map[string]struct {
		table Ref
		want  bool
	}{
		"table under the tenant's key":    {table: cmk, want: true},
		"table under a different cmk":     {table: other, want: false},
		"table under the aws-managed key": {table: DynamoDBServiceDefault(), want: false},
		"table under an unverifiable arn": {table: mustParse(t, testKeyARN), want: false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			repo := repository.NewMemory()
			rs, err := New(repo, Deployment{Bucket: cmk, Table: c.table})
			if err != nil {
				t.Fatal(err)
			}
			putTenant(t, repo, testTenant, attrs)

			scope, err := rs.ErasureScope(context.Background(), testTenant)
			if err != nil {
				t.Fatal(err)
			}
			if got := scope.CoversDynamoDB(); got != c.want {
				t.Errorf("CoversDynamoDB() = %v, want %v (table %s)", got, c.want, c.table)
			}
			wantClause := "DynamoDB items are NOT covered"
			if c.want {
				wantClause = "DynamoDB items are covered"
			}
			if !strings.Contains(scope.Caveat(), wantClause) {
				t.Errorf("caveat does not state %q: %s", wantClause, scope.Caveat())
			}
			// And it must agree with the write-time check, or one of the two is
			// lying about the same pair of keys.
			writeOK := scope.Ref().CheckDynamoPut(c.table) == nil
			if writeOK != c.want {
				t.Errorf("CheckDynamoPut allows the write = %v while the erasure claim is %v", writeOK, c.want)
			}
		})
	}
}

// An AWS-managed tenant is the personal phase, and there the scope must add nothing
// to the reference-level refusal: no timestamp on the record can make an
// undestroyable key shreddable.
func TestErasureScopeAddsNothingForAnAWSManagedTenant(t *testing.T) {
	rs, repo := newResolver(t)
	putTenant(t, repo, testTenant, map[string]any{
		TenantAttrKMSKeyID:      AWSManagedS3,
		TenantAttrCreatedAt:     "2026-01-01T00:00:00Z",
		TenantAttrKMSKeyIDSince: "2026-01-01T00:00:00Z",
	})

	scope, err := rs.ErasureScope(context.Background(), testTenant)
	if err != nil {
		t.Fatal(err)
	}
	if scope.CryptoShreddable() || scope.CoversAllObjects() || scope.CoversDynamoDB() {
		t.Errorf("a personal-phase tenant claims coverage: %+v", scope)
	}
	if scope.Caveat() != scope.Ref().ErasureCaveat() {
		t.Errorf("the scope's caveat diverges from the reference's for an AWS-managed key:\n%s\n%s", scope.Caveat(), scope.Ref().ErasureCaveat())
	}
	if !strings.Contains(scope.Caveat(), "cannot destroy") {
		t.Errorf("the personal-phase caveat does not say the key cannot be destroyed: %s", scope.Caveat())
	}
}

// A scope whose table key was never recorded must describe that, not omit the
// DynamoDB clause. New refuses an incomplete Deployment, so this is reachable only
// by constructing a scope directly — which is precisely the shape a future caller
// assembling one by hand would produce.
func TestErasureScopeWithNoTableKeyStatesThat(t *testing.T) {
	scope := newErasureScope(mustParse(t, testCMKAlias), Ref{}, map[string]any{})
	if scope.CoversDynamoDB() {
		t.Error("CoversDynamoDB() is true with no table key recorded")
	}
	if !strings.Contains(scope.Caveat(), "no recorded key") {
		t.Errorf("the caveat does not say the table's key is unknown: %s", scope.Caveat())
	}
}

// The health and cost surfaces report what encryption is actually in force, so the
// accessor must return the deployment New was given rather than a zero value that
// would read as "no encryption configured".
func TestResolverReportsItsDeployment(t *testing.T) {
	rs, _ := newResolver(t)
	dep := rs.Deployment()
	if dep.Bucket.ID() != AWSManagedS3 || dep.Table.ID() != AWSManagedDynamoDB {
		t.Errorf("Deployment() = %+v, want the personal-phase service defaults (§6.2, §6.3)", dep)
	}
	// And an unresolved reference must render as such: a report interpolating a
	// zero Ref must not print an empty string where a key belongs.
	if got := (Ref{}).String(); got != "<unresolved>" {
		t.Errorf("(Ref{}).String() = %q, want a visible placeholder", got)
	}
}

func mustParse(t *testing.T, raw string) Ref {
	t.Helper()
	ref, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestNewRefusesAnIncompleteDeployment(t *testing.T) {
	repo := repository.NewMemory()
	cases := map[string]Deployment{
		"both unset":  {},
		"bucket only": {Bucket: S3ServiceDefault()},
		"table only":  {Table: DynamoDBServiceDefault()},
	}
	for name, dep := range cases {
		t.Run(name, func(t *testing.T) {
			// §Phase 0: a missing value must fail the deploy rather than fall
			// back to a default. A defaulted deployment key would move the CMK
			// flip back into this package.
			if _, err := New(repo, dep); !errors.Is(err, ErrAbsent) {
				t.Errorf("New accepted %+v (err=%v)", dep, err)
			}
		})
	}
	if _, err := New(nil, Deployment{Bucket: S3ServiceDefault(), Table: DynamoDBServiceDefault()}); err == nil {
		t.Error("New accepted a nil repository, so the first Resolve would panic instead of failing at wiring time")
	}
}

func TestResolvePropagatesAStorageFailure(t *testing.T) {
	rs, repo := newResolver(t)
	putTenant(t, repo, testTenant, map[string]any{TenantAttrKMSKeyID: AWSManagedS3})
	throttle := errors.New("throttled")
	repo.FailNext(throttle)

	// A read failure must not degrade into "use the service default": that would
	// write objects whose encryption nothing recorded.
	enc, err := rs.ForS3Put(context.Background(), testTenant)
	if err == nil {
		t.Fatal("ForS3Put succeeded despite a storage failure")
	}
	// Asserting *which* failure, not merely that one occurred: ErrAbsent here
	// would mean an unreadable record reads as a provisioning defect, and the
	// sentinel doc promises the opposite — a throttle is the one case worth
	// retrying, and losing that distinction makes the resolver non-retryable
	// without any test going red.
	if !errors.Is(err, throttle) {
		t.Errorf("error = %v, want the storage failure itself", err)
	}
	if errors.Is(err, ErrAbsent) || errors.Is(err, ErrMalformed) {
		t.Errorf("a storage failure reads as a provisioning defect: %v", err)
	}
	if !enc.IsZero() {
		t.Errorf("ForS3Put returned %+v alongside a storage failure", enc)
	}
}
