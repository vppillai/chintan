package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

func TestObjectsPutGetDelete(t *testing.T) {
	objs := memory.NewObjects()
	ctx := context.Background()
	key := "tenants/user1/notes/n1/note.md"
	body := []byte("# Hello")

	if err := objs.Put(ctx, key, body, "text/markdown"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := objs.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("Get = %q, want %q", got, body)
	}
	if err := objs.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = objs.Get(ctx, key)
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("after delete Get err = %v, want ErrNotFound", err)
	}
}

func TestObjectsGetNotFound(t *testing.T) {
	objs := memory.NewObjects()
	_, err := objs.Get(context.Background(), "missing")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestObjectsPresignPutGet(t *testing.T) {
	objs := memory.NewObjects()
	ctx := context.Background()
	key := "tenants/user1/captures/c1/audio.webm"
	ttl := 15 * time.Minute

	putURL, err := objs.PresignPut(ctx, key, "audio/webm", ttl)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if putURL == "" {
		t.Fatal("PresignPut returned empty URL")
	}

	getURL, err := objs.PresignGet(ctx, key, ttl)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	if getURL == "" {
		t.Fatal("PresignGet returned empty URL")
	}
}
