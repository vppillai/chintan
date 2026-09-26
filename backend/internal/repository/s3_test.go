package repository_test

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/upload"
)

// These prove the adapter's side of the contract the append protocol rests on
// (docs/design/append-vs-autosave.md): the ETag GetWithETag hands out is the
// one PutIfMatch sends back, a stale one is ErrPreconditionFailed with the
// body untouched, and "" means "must not exist". memory.Objects implements
// those semantics by construction; this is the S3 code path, against a bucket
// that answers the way S3 does.

const testBucket = "chintan-content-test"

func newTestObjects() (*repository.S3Objects, *fakeS3) {
	api := newFakeS3()
	return repository.NewS3ObjectsWithAPI(api, testBucket), api
}

func mustPut(t *testing.T, o *repository.S3Objects, key, body string) {
	t.Helper()
	if err := o.Put(context.Background(), key, []byte(body), "text/markdown"); err != nil {
		t.Fatalf("Put %s: %v", key, err)
	}
}

func TestS3GetOfAMissingKeyIsErrNotFound(t *testing.T) {
	o, _ := newTestObjects()
	ctx := context.Background()

	if _, err := o.Get(ctx, "missing"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("Get err = %v, want ErrNotFound", err)
	}
	body, etag, err := o.GetWithETag(ctx, "missing")
	if !errors.Is(err, repository.ErrNotFound) || body != nil || etag != "" {
		t.Fatalf("GetWithETag = (%q, %q, %v), want (nil, \"\", ErrNotFound) so PutIfMatch reads it as must-not-exist", body, etag, err)
	}
}

func TestS3GetWithETagHandsOutTheTagPutIfMatchMustPresent(t *testing.T) {
	o, api := newTestObjects()
	ctx := context.Background()
	mustPut(t, o, "note.md", "first")

	body, etag, err := o.GetWithETag(ctx, "note.md")
	if err != nil {
		t.Fatalf("GetWithETag: %v", err)
	}
	if string(body) != "first" {
		t.Fatalf("body = %q, want %q", body, "first")
	}
	// The exact string, quotes included: S3 returns the ETag quoted and
	// expects it back the same way, so a trimmed or re-quoted tag is a 412.
	if want := fakeETag([]byte("first")); etag != want {
		t.Fatalf("etag = %q, want the stored object's %q", etag, want)
	}
	if got, _ := o.Get(ctx, "note.md"); string(got) != "first" {
		t.Fatalf("Get = %q, want %q", got, "first")
	}

	if err := o.PutIfMatch(ctx, "note.md", []byte("second"), "text/markdown", etag); err != nil {
		t.Fatalf("PutIfMatch with the current etag: %v", err)
	}
	if got := string(api.body("note.md")); got != "second" {
		t.Fatalf("body after PutIfMatch = %q, want %q", got, "second")
	}
	if _, next, _ := o.GetWithETag(ctx, "note.md"); next == etag {
		t.Fatal("the ETag did not move with the body; a second writer's stale tag would pass")
	}
}

func TestS3PutIfMatch(t *testing.T) {
	cases := []struct {
		name     string
		existing string // "" is no object
		etag     func(current string) string
		wantErr  error
		wantBody string // what the bucket holds afterwards; "" is no object
	}{
		{
			name:     "a stale etag is refused and the body untouched",
			existing: "theirs",
			etag:     func(string) string { return fakeETag([]byte("what I read earlier")) },
			wantErr:  repository.ErrPreconditionFailed,
			wantBody: "theirs",
		},
		{
			name:     "an empty etag creates an absent object",
			etag:     func(string) string { return "" },
			wantBody: "mine",
		},
		{
			name:     "an empty etag refuses to overwrite a present object",
			existing: "theirs",
			etag:     func(string) string { return "" },
			wantErr:  repository.ErrPreconditionFailed,
			wantBody: "theirs",
		},
		{
			name:    "an etag for an object that has since been deleted is refused",
			etag:    func(string) string { return fakeETag([]byte("deleted meanwhile")) },
			wantErr: repository.ErrPreconditionFailed,
		},
		{
			name:     "the current etag writes",
			existing: "theirs",
			etag:     func(current string) string { return current },
			wantBody: "mine",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, api := newTestObjects()
			ctx := context.Background()
			var current string
			if tc.existing != "" {
				mustPut(t, o, "note.md", tc.existing)
				current = fakeETag([]byte(tc.existing))
			}

			err := o.PutIfMatch(ctx, "note.md", []byte("mine"), "text/markdown", tc.etag(current))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("PutIfMatch err = %v, want %v", err, tc.wantErr)
			}
			if got := string(api.body("note.md")); got != tc.wantBody {
				t.Fatalf("bucket holds %q, want %q", got, tc.wantBody)
			}
		})
	}
}

func TestS3PutTaggedBindsTheTagsOnTheWrite(t *testing.T) {
	o, api := newTestObjects()
	ctx := context.Background()

	want := upload.CaptureAudioTags(30)
	if len(want) != 2 {
		t.Fatalf("CaptureAudioTags(30) = %v; the test needs the artifact and the retention tag to prove a set is bound", want)
	}
	if err := o.PutTagged(ctx, "audio.webm", []byte("bytes"), "audio/webm", want); err != nil {
		t.Fatalf("PutTagged: %v", err)
	}
	if got := api.tags("audio.webm"); !reflect.DeepEqual(got, want) {
		t.Fatalf("tags = %v, want %v", got, want)
	}

	// A plain Put binds nothing, so the lifecycle rule never matches a note
	// body or a transcript.
	mustPut(t, o, "note.md", "words")
	if got := api.tags("note.md"); len(got) != 0 {
		t.Fatalf("Put bound tags %v; only PutTagged may", got)
	}
}

func TestS3MarkProcessedMergesWithoutDroppingTheRetentionTags(t *testing.T) {
	o, api := newTestObjects()
	ctx := context.Background()

	retention := upload.CaptureAudioTags(30)
	if err := o.PutTagged(ctx, "audio.webm", []byte("bytes"), "audio/webm", retention); err != nil {
		t.Fatalf("PutTagged: %v", err)
	}

	// Twice: a retried worker must not duplicate the tag or fail.
	for i := 0; i < 2; i++ {
		if err := o.MarkProcessed(ctx, "audio.webm"); err != nil {
			t.Fatalf("MarkProcessed #%d: %v", i+1, err)
		}
	}
	want := map[string]string{repository.ProcessedTagKey: repository.ProcessedTagValue}
	for k, v := range retention {
		want[k] = v
	}
	if got := api.tags("audio.webm"); !reflect.DeepEqual(got, want) {
		t.Fatalf("tags after MarkProcessed = %v, want the retention tags plus the processed tag %v", got, want)
	}

	// Nothing to protect or expire: not an error.
	if err := o.MarkProcessed(ctx, "gone.webm"); err != nil {
		t.Fatalf("MarkProcessed of a missing key = %v, want nil", err)
	}
}

func TestS3Exists(t *testing.T) {
	o, _ := newTestObjects()
	ctx := context.Background()
	mustPut(t, o, "peaks.json", "[]")

	for key, want := range map[string]bool{"peaks.json": true, "never-uploaded.json": false} {
		got, err := o.Exists(ctx, key)
		if err != nil {
			t.Fatalf("Exists(%s): %v", key, err)
		}
		if got != want {
			t.Fatalf("Exists(%s) = %v, want %v", key, got, want)
		}
	}
}

// S3 answers a DELETE of a missing key with 204, and the adapter passes that
// through: the cascade delete retries by deleting again, and "already gone"
// must be progress. memory.Objects returns ErrNotFound here, which every
// caller tolerates, so the two doubles disagree only where nothing depends on
// it.
func TestS3DeleteOfAMissingKeyIsNotAnError(t *testing.T) {
	o, api := newTestObjects()
	ctx := context.Background()
	mustPut(t, o, "raw.txt", "words")

	if err := o.Delete(ctx, "raw.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if api.body("raw.txt") != nil {
		t.Fatal("the object is still in the bucket")
	}
	if err := o.Delete(ctx, "raw.txt"); err != nil {
		t.Fatalf("Delete of a missing key = %v, want nil", err)
	}
}

// Every call the adapter makes, so a fault and a cancellation can be checked
// against each without a test per method.
var s3Calls = []struct {
	name string
	call func(ctx context.Context, o *repository.S3Objects) error
}{
	{"Put", func(ctx context.Context, o *repository.S3Objects) error {
		return o.Put(ctx, "k", []byte("b"), "text/plain")
	}},
	{"PutTagged", func(ctx context.Context, o *repository.S3Objects) error {
		return o.PutTagged(ctx, "k", []byte("b"), "text/plain", map[string]string{"a": "b"})
	}},
	{"Get", func(ctx context.Context, o *repository.S3Objects) error { _, err := o.Get(ctx, "k"); return err }},
	{"GetWithETag", func(ctx context.Context, o *repository.S3Objects) error {
		_, _, err := o.GetWithETag(ctx, "k")
		return err
	}},
	{"PutIfMatch", func(ctx context.Context, o *repository.S3Objects) error {
		return o.PutIfMatch(ctx, "k", []byte("b"), "text/plain", "")
	}},
	{"Exists", func(ctx context.Context, o *repository.S3Objects) error { _, err := o.Exists(ctx, "k"); return err }},
	{"Delete", func(ctx context.Context, o *repository.S3Objects) error { return o.Delete(ctx, "k") }},
	{"MarkProcessed", func(ctx context.Context, o *repository.S3Objects) error { return o.MarkProcessed(ctx, "k") }},
}

// A transport fault is neither "missing" nor "lost the race": the adapter must
// not translate it into a sentinel a caller would act on — an append that read
// a 5xx as ErrNotFound would write over the note from an empty body.
func TestS3WrapsATransportFault(t *testing.T) {
	for _, c := range s3Calls {
		t.Run(c.name, func(t *testing.T) {
			o, api := newTestObjects()
			fault := errors.New("dial tcp: connection refused")
			api.fail = fault
			err := c.call(context.Background(), o)
			if !errors.Is(err, fault) {
				t.Fatalf("err = %v, want it to wrap the transport fault", err)
			}
			if errors.Is(err, repository.ErrNotFound) || errors.Is(err, repository.ErrPreconditionFailed) {
				t.Fatalf("err = %v reads as a sentinel a caller would act on", err)
			}
		})
	}
}

func TestS3RefusesACancelledContextBeforeCalling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range s3Calls {
		t.Run(c.name, func(t *testing.T) {
			o, api := newTestObjects()
			api.fail = errors.New("the client was called after cancellation")
			if err := c.call(ctx, o); !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
		})
	}
	for _, name := range []string{"PresignPut", "PresignGet"} {
		t.Run(name, func(t *testing.T) {
			o := repository.NewS3Objects(offlineS3Client(), testBucket)
			var err error
			if name == "PresignPut" {
				_, err = o.PresignPut(ctx, "k", "audio/webm", time.Minute)
			} else {
				_, err = o.PresignGet(ctx, "k", time.Minute)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
		})
	}
}

// offlineS3Client is a real client with static credentials: signing a URL is
// arithmetic over the request, so it needs no network and no account.
func offlineS3Client() *s3.Client {
	return s3.New(s3.Options{
		Region:      "us-west-2",
		Credentials: credentials.NewStaticCredentialsProvider("AKIAEXAMPLE", "secret", ""),
	})
}

func TestS3PresignedURLsNameTheKeyAndTheExpiry(t *testing.T) {
	o := repository.NewS3Objects(offlineS3Client(), testBucket)
	ctx := context.Background()
	const key = "tenants/user1/captures/cap1/audio.webm"

	for name, presign := range map[string]func() (string, error){
		"PresignPut": func() (string, error) { return o.PresignPut(ctx, key, "audio/webm", 15*time.Minute) },
		"PresignGet": func() (string, error) { return o.PresignGet(ctx, key, 15*time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := presign()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("%s returned %q, not a URL: %v", name, raw, err)
			}
			if !strings.Contains(u.Host, testBucket) && !strings.Contains(u.Path, testBucket) {
				t.Fatalf("%s: %q names neither the bucket in the host nor the path", name, raw)
			}
			if !strings.HasSuffix(u.Path, "/"+key) {
				t.Fatalf("%s: path %q does not end in the key", name, u.Path)
			}
			if got := u.Query().Get("X-Amz-Expires"); got != "900" {
				t.Fatalf("%s: X-Amz-Expires = %q, want 900 (fifteen minutes)", name, got)
			}
			if u.Query().Get("X-Amz-Signature") == "" {
				t.Fatalf("%s: %q carries no signature", name, raw)
			}
		})
	}
}
