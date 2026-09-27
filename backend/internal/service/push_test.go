package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/dynamofake"
)

func pushRequest(n int) PushSubscribeRequest {
	return PushSubscribeRequest{
		Endpoint: fmt.Sprintf("https://push.example/send/%d", n),
		P256DH:   "BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM",
		Auth:     "tBHItJI5svbpez7KI4CCXg",
		Label:    "  Safari on   iPhone ",
	}
}

// The same endpoint subscribes once: the row is rewritten and keeps its
// creation time, so a browser that re-subscribes on every start never fills
// the quota.
func TestPushSubscribeIsIdempotentOnTheEndpoint(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	svc := NewPushService(dynamofake.NewStore()).WithClock(func() time.Time { return clock })

	first, err := svc.Subscribe(ctx, "user1", pushRequest(1))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if first.ID != PushSubscriptionID(pushRequest(1).Endpoint) || len(first.ID) != 16 {
		t.Fatalf("id = %q, want the endpoint's sixteen-hex digest", first.ID)
	}
	// The same literal is pinned in the app's NotificationsCard.test.tsx: the
	// browser computes this id to find and remove its own row, so the two
	// digests diverging must fail a test on both sides.
	if got := PushSubscriptionID("https://push.example/a"); got != "378f13c84514cc9b" {
		t.Fatalf("PushSubscriptionID = %q, want 378f13c84514cc9b (SHA-256 first sixteen hex)", got)
	}
	if first.Label != "Safari on iPhone" {
		t.Fatalf("label = %q, want the browser's name with its spaces folded", first.Label)
	}

	clock = clock.Add(time.Hour)
	again := pushRequest(1)
	again.Auth = "newAuthKey_0123456789"
	second, err := svc.Subscribe(ctx, "user1", again)
	if err != nil {
		t.Fatalf("Subscribe again: %v", err)
	}
	if second.ID != first.ID || second.CreatedAt != first.CreatedAt || second.Auth != again.Auth {
		t.Fatalf("re-subscribe = %+v, want the same row with the new key and the old created_at", second)
	}
	subs, _ := svc.ListSubscriptions(ctx, "user1")
	if len(subs) != 1 {
		t.Fatalf("%d rows after subscribing twice with one endpoint", len(subs))
	}
}

func TestPushSubscribeEnforcesTheQuotaAndTheShape(t *testing.T) {
	ctx := context.Background()
	svc := NewPushService(dynamofake.NewStore())
	for i := 0; i < model.MaxPushSubscriptionsPerTenant; i++ {
		if _, err := svc.Subscribe(ctx, "user1", pushRequest(i)); err != nil {
			t.Fatalf("Subscribe %d: %v", i, err)
		}
	}
	if _, err := svc.Subscribe(ctx, "user1", pushRequest(99)); !errors.Is(err, ErrPushSubscriptionLimit) {
		t.Fatalf("eleventh endpoint: %v, want ErrPushSubscriptionLimit", err)
	}
	// An endpoint already held is not an eleventh.
	if _, err := svc.Subscribe(ctx, "user1", pushRequest(3)); err != nil {
		t.Fatalf("re-subscribing at the limit: %v", err)
	}
	// Another tenant has its own ten.
	if _, err := svc.Subscribe(ctx, "user2", pushRequest(1)); err != nil {
		t.Fatalf("another tenant: %v", err)
	}

	bad := pushRequest(1)
	bad.Endpoint = "http://push.example/insecure"
	if _, err := svc.Subscribe(ctx, "user3", bad); !errors.Is(err, ErrPushEndpointInvalid) {
		t.Fatalf("http endpoint: %v", err)
	}
	bad = pushRequest(1)
	bad.Auth = "not base64url!"
	if _, err := svc.Subscribe(ctx, "user3", bad); !errors.Is(err, ErrPushKeysRequired) {
		t.Fatalf("bad auth key: %v", err)
	}
	bad = pushRequest(1)
	bad.P256DH = ""
	if _, err := svc.Subscribe(ctx, "user3", bad); !errors.Is(err, ErrPushKeysRequired) {
		t.Fatalf("missing p256dh: %v", err)
	}
}

func TestPushUnsubscribeIsScopedToTheTenant(t *testing.T) {
	ctx := context.Background()
	svc := NewPushService(dynamofake.NewStore())
	sub, err := svc.Subscribe(ctx, "user1", pushRequest(1))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := svc.Unsubscribe(ctx, "user2", sub.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("another tenant's unsubscribe: %v, want ErrNotFound", err)
	}
	if err := svc.Unsubscribe(ctx, "user1", sub.ID); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	if err := svc.Unsubscribe(ctx, "user1", sub.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("second unsubscribe: %v, want ErrNotFound", err)
	}
}
