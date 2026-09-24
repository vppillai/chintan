package service

import (
	"context"
	"errors"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// downObjects is an object store whose writes fail.
type downObjects struct{ repository.Objects }

func (downObjects) Put(context.Context, string, []byte, string) error { return errors.New("s3 down") }
func (downObjects) PutTagged(context.Context, string, []byte, string, map[string]string) error {
	return errors.New("s3 down")
}

// An inbox request whose object write fails leaves no capture row behind:
// the row would otherwise sit at uploaded with nothing behind it until the
// stuck sweep failed it (audio), or claim a transcript that was never
// written (text).
func TestInboxIngestLeavesNoRowWhenTheObjectWriteFails(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	svc := NewCaptureService(store, downObjects{memory.NewObjects()})
	source := model.DeviceSource("dev_000000000001")
	if _, err := svc.IngestAudio(ctx, "u1", CaptureRequest{ContentType: "audio/webm", Source: source}, []byte("bytes")); err == nil {
		t.Fatal("IngestAudio succeeded with the object store down")
	}
	if _, err := svc.IngestText(ctx, "u1", CaptureRequest{Source: source}, "buy milk"); err == nil {
		t.Fatal("IngestText succeeded with the object store down")
	}
	page, err := store.ListCaptures(ctx, "u1", repository.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("captures left behind: %+v", page.Items)
	}
}
