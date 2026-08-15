package upload

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/vppillai/chintan/backend/internal/model"
)

func testPresigner(t *testing.T) *S3Presigner {
	t.Helper()
	client := s3.New(s3.Options{
		Region:      "us-west-2",
		Credentials: credentials.NewStaticCredentialsProvider("AKIAEXAMPLE", "secret", ""),
	})
	p := NewS3(client, "chintan-content-test")
	p.now = func() time.Time { return time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC) }
	return p
}

// The tag has to be inside the signature, not merely suggested alongside it.
//
// S3 rejects a presigned request that sends an unsigned x-amz-* header, so a
// signed x-amz-tagging is the only construction in which the client can neither
// omit the tag nor change it. Without the tag the lifecycle rule matches
// nothing, RetentionDays does nothing, and the v1 defect is intact.
func TestPresignedAudioPutSignsTheRetentionTag(t *testing.T) {
	got, err := testPresigner(t).PresignPut(context.Background(),
		"tenants/user1/captures/c_1/audio.webm", "audio/webm",
		CaptureAudioTags(30), 4<<20, 30*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	parsed, err := url.Parse(got.URL)
	if err != nil {
		t.Fatalf("parse presigned URL: %v", err)
	}
	signed := parsed.Query().Get("X-Amz-SignedHeaders")
	if signed == "" {
		t.Fatalf("presigned URL carries no X-Amz-SignedHeaders: %s", got.URL)
	}
	if !strings.Contains(signed, "x-amz-tagging") {
		t.Fatalf("X-Amz-SignedHeaders = %q; the retention tag is not signed, so an upload can omit it and never expire", signed)
	}
	// Both tags, and both signed. The artifact tag says "this is expirable
	// audio" and is the same on every object; the retention tag is the only
	// thing that can differ per tenant, because a lifecycle rule carries its own
	// ExpirationInDays and cannot read one out of a settings record.
	if want := "chintan-artifact=capture-audio&chintan-retention=30"; got.Headers[TaggingHeader] != want {
		t.Fatalf("headers[%s] = %q, want %q", TaggingHeader, got.Headers[TaggingHeader], want)
	}
	if got.Headers["Content-Type"] != "audio/webm" {
		t.Errorf("headers[Content-Type] = %q", got.Headers["Content-Type"])
	}
	if got.MaxBytes != 4<<20 {
		t.Errorf("max bytes = %d, want the declared size", got.MaxBytes)
	}
	if want := time.Date(2026, 8, 7, 12, 30, 0, 0, time.UTC); !got.ExpiresAt.Equal(want) {
		t.Errorf("expires_at = %s, want %s", got.ExpiresAt, want)
	}
}

// The declared container has to be binding, not advisory.
//
// The SDK drops Content-Type from a presigned PUT because the presign has no
// payload, so the header travels in the response as guidance the client is free
// to ignore: a URL issued for audio/webm accepts any bytes at all under any
// type. Signing it is what makes the declaration mean something.
//
// This does not bound the *size* — SigV4 cannot sign Content-Length on a PUT.
// Only a presigned POST with a Content-Length-Range policy can, and the worker's
// after-the-fact check is what stands in for it today.
func TestPresignedAudioPutSignsTheContentType(t *testing.T) {
	got, err := testPresigner(t).PresignPut(context.Background(),
		"tenants/user1/captures/c_1/audio.webm", "audio/webm",
		CaptureAudioTags(30), 4<<20, 30*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	parsed, err := url.Parse(got.URL)
	if err != nil {
		t.Fatalf("parse presigned URL: %v", err)
	}
	signed := parsed.Query().Get("X-Amz-SignedHeaders")
	if !strings.Contains(signed, "content-type") {
		t.Fatalf("X-Amz-SignedHeaders = %q; Content-Type is not signed, so the URL accepts any container "+
			"and the declared one is decoration", signed)
	}
}

// The peaks object is the client's waveform envelope. It is derived data, not
// source audio, and retention must not take it with the recording.
func TestPresignedPeaksPutCarriesNoRetentionTag(t *testing.T) {
	got, err := testPresigner(t).PresignPut(context.Background(),
		"tenants/user1/captures/c_1/peaks.json", "application/json",
		nil, 2<<20, 30*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if _, ok := got.Headers[TaggingHeader]; ok {
		t.Fatalf("peaks upload carries %s = %q", TaggingHeader, got.Headers[TaggingHeader])
	}
	parsed, _ := url.Parse(got.URL)
	if strings.Contains(parsed.Query().Get("X-Amz-SignedHeaders"), "x-amz-tagging") {
		t.Error("peaks upload signs a tagging header it does not send")
	}
}

func TestEncodeTagsIsStableAndEscaped(t *testing.T) {
	got := EncodeTags(map[string]string{"b": "two words", "a": "one"})
	if got != "a=one&b=two+words" {
		t.Fatalf("EncodeTags = %q, want a=one&b=two+words", got)
	}
	if EncodeTags(nil) != "" {
		t.Error("EncodeTags(nil) must produce no tagging header at all")
	}
}

func TestPresignRejectsAnEmptyKey(t *testing.T) {
	if _, err := testPresigner(t).PresignPut(context.Background(), "  ", "audio/webm", nil, 0, time.Minute); err == nil {
		t.Fatal("expected an error for an empty key")
	}
}

// TestTheRetentionTagCarriesTheTenantsChoice is the defect this closes.
//
// `retention_days` was validated, stored, returned and rendered in the UI while
// nothing in the request path read it: every object got the same constant tag
// and the expiry period came from a CloudFormation parameter. A user asking for
// thirty days kept their audio forever and was told otherwise, which is
// precisely the v1 defect the package comment claims to have fixed.
func TestTheRetentionTagCarriesTheTenantsChoice(t *testing.T) {
	cases := []struct {
		requested int
		want      string
	}{
		// Kept indefinitely: no retention tag at all, so no expiry rule matches.
		{0, "chintan-artifact=capture-audio"},
		{7, "chintan-artifact=capture-audio&chintan-retention=7"},
		{30, "chintan-artifact=capture-audio&chintan-retention=30"},
		{90, "chintan-artifact=capture-audio&chintan-retention=90"},
		{365, "chintan-artifact=capture-audio&chintan-retention=365"},
		// Between tiers: resolved DOWN, because a retention setting is a promise
		// to delete and rounding it up would break that promise quietly.
		{45, "chintan-artifact=capture-audio&chintan-retention=30"},
		{364, "chintan-artifact=capture-audio&chintan-retention=90"},
		// Longer than the longest tier: the longest tier, not "forever".
		{3650, "chintan-artifact=capture-audio&chintan-retention=365"},
		// Shorter than the shortest: the shortest, because the alternative is
		// answering "delete after two days" with "never".
		{2, "chintan-artifact=capture-audio&chintan-retention=7"},
		// A stored value that predates tiers, or a negative one that escaped
		// validation, must not produce a tag no rule matches.
		{-5, "chintan-artifact=capture-audio"},
	}

	for _, tc := range cases {
		got := EncodeTags(CaptureAudioTags(tc.requested))
		if got != tc.want {
			t.Errorf("CaptureAudioTags(%d) = %q, want %q", tc.requested, got, tc.want)
		}
	}
}

// TestEveryRetentionTierHasALifecycleRule ties the tiers to the template.
//
// The tag only expires anything if a rule matches its value. A tier added in Go
// with no rule beside it in infrastructure/template.yaml is the same defect in a
// new place: a setting the user can choose, that is stored and returned, and
// that expires nothing.
func TestEveryRetentionTierHasALifecycleRule(t *testing.T) {
	raw, err := os.ReadFile("../../../infrastructure/template.yaml")
	if err != nil {
		t.Fatalf("read the template: %v", err)
	}
	template := string(raw)

	for _, tier := range model.RetentionTiers {
		// The rule's tag filter and its expiry must both name the tier, or a
		// rule exists that deletes on the wrong day.
		filter := "Value: '" + strconv.Itoa(tier) + "'"
		expiry := "ExpirationInDays: " + strconv.Itoa(tier)
		if !strings.Contains(template, filter) {
			t.Errorf("no lifecycle rule filters on chintan-retention=%d; that tier expires nothing", tier)
		}
		if !strings.Contains(template, expiry) {
			t.Errorf("no lifecycle rule expires after %d days", tier)
		}
	}
}

// TestEveryLifecycleRuleRequiresTheProcessedTag ties the template to
// internal/pipeline.markAudioProcessedIfSafe, the only place that ever sets
// ProcessedTagKey.
//
// A capture whose upload event never reaches the worker — the S3/SQS
// notification lost, the worker dead — sits at `uploaded` with no chance to
// run, and this tag is what keeps its audio out of every rule below until
// transcription actually succeeds, regardless of how old the object gets. A
// rule missing this filter would expire that audio on schedule anyway, on a
// clock that has no idea the pipeline never ran — which is exactly the
// incident internal/pipeline/audio_retention_test.go exists to close on the
// Go side. Checked per-rule rather than as one global Contains: four rules
// share this filter, and a global check would not notice it missing from just
// one of them.
func TestEveryLifecycleRuleRequiresTheProcessedTag(t *testing.T) {
	raw, err := os.ReadFile("../../../infrastructure/template.yaml")
	if err != nil {
		t.Fatalf("read the template: %v", err)
	}
	template := string(raw)

	for _, tier := range model.RetentionTiers {
		id := "ExpireCaptureAudio" + strconv.Itoa(tier)
		start := strings.Index(template, "- Id: "+id)
		if start == -1 {
			t.Fatalf("no rule named %s; TestEveryRetentionTierHasALifecycleRule should already have failed", id)
		}
		block := template[start:]
		if next := strings.Index(block[len("- Id: "+id):], "- Id: "); next != -1 {
			block = block[:len("- Id: "+id)+next]
		}
		if !strings.Contains(block, "chintan-processed") {
			t.Errorf("%s does not filter on chintan-processed; its audio expires on schedule "+
				"whether or not the pipeline ever got a chance to run", id)
		}
	}
}
