package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Push subscription errors. The sentences reach the user as written.
var (
	ErrPushEndpointInvalid   = errors.New("the subscription endpoint must be an https URL")
	ErrPushKeysRequired      = errors.New("the subscription needs its p256dh and auth keys")
	ErrPushSubscriptionLimit = errors.New("you already have ten notification subscriptions; remove one first")
)

// pushKeyCharset is base64url, which is how the browser hands the keys over.
var pushKeyCharset = regexp.MustCompile(`^[A-Za-z0-9_-]+=*$`)

// PushSubscribeRequest is what the browser's PushSubscription.toJSON() gives
// the app, plus a label for the list.
type PushSubscribeRequest struct {
	Endpoint string
	P256DH   string
	Auth     string
	Label    string
}

// PushSubscriptionID is the row id for an endpoint: the first sixteen hex
// characters of its SHA-256. The client computes the same, so it can name
// its own subscription without reading the list.
func PushSubscriptionID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:8])
}

// PushService keeps the browser subscriptions the worker notifies when a
// recording files (docs/design/push.md).
type PushService struct {
	store repository.Store
	now   func() time.Time
}

// NewPushService creates a push subscription service.
func NewPushService(store repository.Store) *PushService {
	return &PushService{store: store, now: time.Now}
}

// WithClock replaces the wall clock. Test seam.
func (s *PushService) WithClock(now func() time.Time) *PushService {
	s.now = now
	return s
}

// Subscribe stores a browser's subscription, idempotently on its endpoint:
// the same endpoint again rewrites the keys and keeps the row's creation
// time. A new endpoint past ten is refused, as an eleventh device is.
func (s *PushService) Subscribe(ctx context.Context, userID string, req PushSubscribeRequest) (model.PushSubscription, error) {
	endpoint := strings.TrimSpace(req.Endpoint)
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(endpoint) > model.MaxPushEndpointBytes {
		return model.PushSubscription{}, ErrPushEndpointInvalid
	}
	for _, key := range []string{req.P256DH, req.Auth} {
		if key == "" || len(key) > model.MaxPushKeyBytes || !pushKeyCharset.MatchString(key) {
			return model.PushSubscription{}, ErrPushKeysRequired
		}
	}
	// The label is the browser's own name for itself; a long one is cut
	// rather than refused, since nothing was typed.
	label := []rune(strings.Join(strings.Fields(req.Label), " "))
	if len(label) > model.MaxPushLabelRunes {
		label = label[:model.MaxPushLabelRunes]
	}

	existing, err := s.store.ListPushSubscriptions(ctx, userID)
	if err != nil {
		return model.PushSubscription{}, fmt.Errorf("failed to list push subscriptions: %w", err)
	}
	sub := model.PushSubscription{
		ID:        PushSubscriptionID(endpoint),
		TenantID:  userID,
		Endpoint:  endpoint,
		P256DH:    req.P256DH,
		Auth:      req.Auth,
		Label:     string(label),
		CreatedAt: model.FormatTime(s.now()),
	}
	replaced := false
	for _, e := range existing {
		if e.ID == sub.ID {
			sub.CreatedAt = e.CreatedAt
			replaced = true
		}
	}
	if !replaced && len(existing) >= model.MaxPushSubscriptionsPerTenant {
		return model.PushSubscription{}, ErrPushSubscriptionLimit
	}
	if err := s.store.PutPushSubscription(ctx, userID, sub); err != nil {
		return model.PushSubscription{}, fmt.Errorf("failed to store push subscription: %w", err)
	}
	return sub, nil
}

// ListSubscriptions is the tenant's subscriptions, oldest first.
func (s *PushService) ListSubscriptions(ctx context.Context, userID string) ([]model.PushSubscription, error) {
	subs, err := s.store.ListPushSubscriptions(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list push subscriptions: %w", err)
	}
	return subs, nil
}

// Unsubscribe removes one subscription; an id the tenant does not have is
// ErrNotFound.
func (s *PushService) Unsubscribe(ctx context.Context, userID, id string) error {
	if err := s.store.DeletePushSubscription(ctx, userID, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return err
		}
		return fmt.Errorf("failed to delete push subscription: %w", err)
	}
	return nil
}
