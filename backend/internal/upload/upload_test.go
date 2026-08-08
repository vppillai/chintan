package upload

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
		CaptureAudioTags(), 4<<20, 30*time.Minute)
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
	if got.Headers[TaggingHeader] != "chintan-artifact=capture-audio" {
		t.Fatalf("headers[%s] = %q, want chintan-artifact=capture-audio",
			TaggingHeader, got.Headers[TaggingHeader])
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
