package repository_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/dynamofake"
)

// ------------------------------------------------------------ idempotency

func TestIdempotencyClaimReplayAndInFlight(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	// First caller owns the key.
	rec, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-1")
	if err != nil {
		t.Fatalf("BeginIdempotent: %v", err)
	}
	if rec != nil {
		t.Fatalf("first caller got a record %+v, want nil so it performs the work", rec)
	}

	// A second, genuinely concurrent attempt is told to back off rather than
	// duplicating the work.
	if _, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-1"); !errors.Is(err, repository.ErrIdempotencyInFlight) {
		t.Fatalf("concurrent attempt err = %v, want ErrIdempotencyInFlight", err)
	}

	if err := store.CompleteIdempotent(ctx, "tenant-a", "key-1", 201, "application/json", []byte(`{"id":"note_1"}`)); err != nil {
		t.Fatalf("CompleteIdempotent: %v", err)
	}

	replay, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-1")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay == nil {
		t.Fatal("replay returned no record; the original response is lost")
	}
	if replay.Status != 201 || string(replay.Response) != `{"id":"note_1"}` || replay.ContentType != "application/json" {
		t.Fatalf("replay = %+v, want the original 201 application/json response", replay)
	}
}

func TestIdempotencyRejectsAReusedKeyWithADifferentBody(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	if _, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-1"); err != nil {
		t.Fatalf("BeginIdempotent: %v", err)
	}
	if _, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-DIFFERENT"); !errors.Is(err, repository.ErrIdempotencyKeyReused) {
		t.Fatalf("err = %v, want ErrIdempotencyKeyReused", err)
	}
}

func TestIdempotencyKeysAreTenantScoped(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	if _, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-1"); err != nil {
		t.Fatalf("tenant-a: %v", err)
	}
	// The same key for a different tenant is a different key.
	rec, err := store.BeginIdempotent(ctx, "tenant-b", "key-1", "fp-1")
	if err != nil {
		t.Fatalf("tenant-b: %v", err)
	}
	if rec != nil {
		t.Fatal("tenant-b was handed tenant-a's idempotency record")
	}
}

// An SDK-level retry of a committed conditional PutItem comes back as
// ConditionalCheckFailed. The attempt token is what stops that from locking the
// original caller out of its own key for the whole TTL.
func TestIdempotencyOwnRetryIsNotTreatedAsADuplicate(t *testing.T) {
	api := dynamofake.New()
	retrier := &putRetryingDynamo{Fake: api}
	store := repository.NewDynamoStore(retrier, tableName)

	rec, err := store.BeginIdempotent(context.Background(), "tenant-a", "key-1", "fp-1")
	if err != nil {
		t.Fatalf("BeginIdempotent under an SDK retry = %v, want the caller to own its own key", err)
	}
	if rec != nil {
		t.Fatalf("got record %+v, want nil so the caller proceeds", rec)
	}
	if !retrier.retried {
		t.Fatal("the test did not actually exercise a duplicated PutItem")
	}
}

// putRetryingDynamo commits the first PutItem twice, mimicking an SDK retry
// whose first response was lost.
type putRetryingDynamo struct {
	*dynamofake.Fake
	retried bool
}

func (d *putRetryingDynamo) PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	out, err := d.Fake.PutItem(ctx, in, opts...)
	if err != nil || d.retried {
		return out, err
	}
	d.retried = true
	// Same request again: the item now exists, so the condition fails.
	return d.Fake.PutItem(ctx, in, opts...)
}

func TestAbandonIdempotent(t *testing.T) {
	cases := []struct {
		name          string
		arrange       func(t *testing.T, store *repository.DynamoStore)
		wantReplay    bool
		wantFirstCall bool
	}{
		{
			name: "releases a claim so the retry runs the work instead of waiting on it",
			arrange: func(t *testing.T, store *repository.DynamoStore) {
				if _, err := store.BeginIdempotent(context.Background(), "tenant-a", "key-1", "fp-1"); err != nil {
					t.Fatalf("BeginIdempotent: %v", err)
				}
			},
			wantFirstCall: true,
		},
		{
			name: "leaves a completed record alone: the replay is still owed",
			arrange: func(t *testing.T, store *repository.DynamoStore) {
				ctx := context.Background()
				if _, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-1"); err != nil {
					t.Fatalf("BeginIdempotent: %v", err)
				}
				if err := store.CompleteIdempotent(ctx, "tenant-a", "key-1", 201, "application/json", []byte(`{"id":"note_1"}`)); err != nil {
					t.Fatalf("CompleteIdempotent: %v", err)
				}
			},
			wantReplay: true,
		},
		{
			name:          "a key nobody claimed is nothing to release",
			arrange:       func(*testing.T, *repository.DynamoStore) {},
			wantFirstCall: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := newTestStore(t)
			ctx := context.Background()
			tc.arrange(t, store)

			if err := store.AbandonIdempotent(ctx, "tenant-a", "key-1"); err != nil {
				t.Fatalf("AbandonIdempotent: %v", err)
			}

			rec, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-1")
			switch {
			case err != nil:
				t.Fatalf("BeginIdempotent after abandon: %v", err)
			case tc.wantReplay && (rec == nil || rec.Status != 201):
				t.Fatalf("BeginIdempotent returned %+v, want the completed 201 record replayed", rec)
			case tc.wantFirstCall && rec != nil:
				t.Fatalf("BeginIdempotent returned %+v, want nil so the retry performs the work", rec)
			}
		})
	}
}

// ------------------------------------------------------------------- asks

func TestPutAskGetAskRoundTrip(t *testing.T) {
	store, api := newTestStore(t)
	ctx := context.Background()
	expires := time.Now().Add(model.AskTTL).Unix()
	ask := model.Ask{
		ID: "ask_1", UserID: "tenant-a", Status: model.AskAnswered,
		Question:        "what did I say about the roof?",
		History:         []model.AskTurn{{Question: "earlier", Answer: "nothing"}},
		Answer:          "You said the gutter leaks.",
		Grounded:        true,
		Sources:         []model.AskSource{{NoteID: "note_1", Title: "House"}},
		NotesConsidered: 3,
		CreatedAt:       "2026-09-26T10:00:00Z",
		AnsweredAt:      "2026-09-26T10:00:04Z",
		ExpiresAt:       expires,
	}

	if err := store.PutAsk(ctx, "tenant-a", ask); err != nil {
		t.Fatalf("PutAsk: %v", err)
	}
	got, err := store.GetAsk(ctx, "tenant-a", "ask_1")
	if err != nil {
		t.Fatalf("GetAsk: %v", err)
	}
	if !reflect.DeepEqual(got, ask) {
		t.Fatalf("GetAsk = %+v, want %+v", got, ask)
	}

	// The row's only lifecycle is its TTL, so the attribute DynamoDB expires
	// on must carry ExpiresAt; a row without it lives forever.
	item := api.Item("USER#tenant-a", "ASK#ask_1")
	if item == nil {
		t.Fatal("no ASK#ask_1 row under USER#tenant-a")
	}
	if ttl := dynamofake.Scalar(item["ttl"]); ttl != strconv.FormatInt(expires, 10) {
		t.Fatalf("ttl attribute = %q, want %d", ttl, expires)
	}
}

func TestGetAskOfAMissingRowIsErrNotFound(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	if err := store.PutAsk(ctx, "tenant-a", model.Ask{ID: "ask_1", ExpiresAt: 1}); err != nil {
		t.Fatalf("PutAsk: %v", err)
	}

	for name, tenant := range map[string]string{"never asked": "tenant-a", "another tenant's ask": "tenant-b"} {
		id := "ask_1"
		if name == "never asked" {
			id = "ask_2"
		}
		if _, err := store.GetAsk(ctx, tenant, id); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("%s: GetAsk err = %v, want ErrNotFound", name, err)
		}
	}
}

func TestPutAskRefusesARowWithoutAnID(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.PutAsk(context.Background(), "tenant-a", model.Ask{}); err == nil {
		t.Fatal("PutAsk accepted an ask with no id; the row would have an empty sort key")
	}
}
