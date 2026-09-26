package repository_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/url"
	"sort"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// fakeS3 is one in-memory bucket speaking the six calls S3Objects makes, with
// the answers S3 gives where the adapter's correctness depends on them: the
// ETag that a conditional write must present, 412 PreconditionFailed when
// If-Match names another ETag or a missing object and when If-None-Match: *
// meets one, NoSuchKey on a GET and NotFound on a HEAD of a missing key, and
// the Tagging query string bound on a PUT. It exists because the append
// protocol was otherwise proven only against memory.Objects, which implements
// the semantics the adapter is supposed to have rather than exercising them.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]*fakeS3Object
	// fail, when set, is returned by every call: the transport fault the
	// adapter must wrap rather than read as "missing" or "lost the race".
	fail error
}

type fakeS3Object struct {
	body        []byte
	contentType string
	tags        map[string]string
	etag        string
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string]*fakeS3Object{}}
}

// fakeETag is shaped like S3's — a quoted hex digest — so a test that asserts
// the exact string PutIfMatch must send back is asserting on the shape the
// real service returns, quotes included.
func fakeETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// tags returns a copy of the object's tag set, or nil for a missing object.
func (f *fakeS3) tags(key string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[key]
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, v := range obj.tags {
		out[k] = v
	}
	return out
}

// body returns the stored bytes, or nil for a missing object.
func (f *fakeS3) body(key string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[key]
	if !ok {
		return nil
	}
	return append([]byte(nil), obj.body...)
}

func preconditionFailed() error {
	return &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "At least one of the pre-conditions you specified did not hold"}
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	obj, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &types.NoSuchKey{Message: aws.String("The specified key does not exist.")}
	}
	return &s3.GetObjectOutput{
		Body:        io.NopCloser(bytes.NewReader(obj.body)),
		ContentType: aws.String(obj.contentType),
		ETag:        aws.String(obj.etag),
	}, nil
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	key := aws.ToString(in.Key)
	existing, exists := f.objects[key]
	switch {
	case in.IfNoneMatch != nil && aws.ToString(in.IfNoneMatch) == "*" && exists:
		return nil, preconditionFailed()
	case in.IfMatch != nil && (!exists || existing.etag != aws.ToString(in.IfMatch)):
		return nil, preconditionFailed()
	}
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	if in.Tagging != nil {
		parsed, err := url.ParseQuery(aws.ToString(in.Tagging))
		if err != nil {
			return nil, &smithy.GenericAPIError{Code: "InvalidTag", Message: err.Error()}
		}
		for k := range parsed {
			tags[k] = parsed.Get(k)
		}
	}
	// A PUT replaces the object whole, tag set included: an untagged PUT over
	// a tagged object leaves it untagged, as on S3.
	f.objects[key] = &fakeS3Object{body: body, contentType: aws.ToString(in.ContentType), tags: tags, etag: fakeETag(body)}
	return &s3.PutObjectOutput{ETag: aws.String(f.objects[key].etag)}, nil
}

func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	obj, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		// A HEAD has no body to carry an error document, so S3 answers a bare
		// 404, which the SDK surfaces as types.NotFound rather than NoSuchKey.
		return nil, &types.NotFound{}
	}
	return &s3.HeadObjectOutput{ContentType: aws.String(obj.contentType), ETag: aws.String(obj.etag), ContentLength: aws.Int64(int64(len(obj.body)))}, nil
}

func (f *fakeS3) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	// S3 answers 204 whether or not the key held anything.
	delete(f.objects, aws.ToString(in.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeS3) GetObjectTagging(_ context.Context, in *s3.GetObjectTaggingInput, _ ...func(*s3.Options)) (*s3.GetObjectTaggingOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	obj, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &types.NoSuchKey{Message: aws.String("The specified key does not exist.")}
	}
	keys := make([]string, 0, len(obj.tags))
	for k := range obj.tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := &s3.GetObjectTaggingOutput{}
	for _, k := range keys {
		out.TagSet = append(out.TagSet, types.Tag{Key: aws.String(k), Value: aws.String(obj.tags[k])})
	}
	return out, nil
}

func (f *fakeS3) PutObjectTagging(_ context.Context, in *s3.PutObjectTaggingInput, _ ...func(*s3.Options)) (*s3.PutObjectTaggingOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	obj, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &types.NoSuchKey{Message: aws.String("The specified key does not exist.")}
	}
	tags := map[string]string{}
	if in.Tagging != nil {
		for _, tag := range in.Tagging.TagSet {
			tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
	}
	obj.tags = tags
	return &s3.PutObjectTaggingOutput{}, nil
}
