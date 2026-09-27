// Package push sends Web Push messages signed with the instance's VAPID key
// (RFC 8030, 8291, 8292), through github.com/SherClockHolmes/webpush-go.
//
// It is the one production implementation of pipeline.Pusher. The worker
// builds it only when both halves of the key pair are in SSM
// (docs/design/push.md); without them nothing here is reachable.
package push

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/vppillai/chintan/backend/internal/model"
)

// ttl is how long a push service keeps an undelivered message for a device
// that is offline. An hour: a "filed" that arrives a day late is noise.
const ttl = 3600

// Sender signs and sends.
type Sender struct {
	opts webpush.Options
}

// New builds a Sender from the VAPID pair (base64url, as scripts/vapid-keys.sh
// prints them) and the subject the push service may contact — an https URL or
// a mailto:, which RFC 8292 requires.
func New(publicKey, privateKey, subject string) (*Sender, error) {
	if publicKey == "" || privateKey == "" || subject == "" {
		return nil, errors.New("push: the VAPID public key, private key and subject are all required")
	}
	return &Sender{opts: webpush.Options{
		HTTPClient:      &http.Client{Timeout: 10 * time.Second},
		Subscriber:      subject,
		VAPIDPublicKey:  publicKey,
		VAPIDPrivateKey: privateKey,
		TTL:             ttl,
		Urgency:         webpush.UrgencyHigh,
	}}, nil
}

// Send encrypts payload to the subscription's keys and POSTs it to its
// endpoint. The status is the push service's answer; 404 and 410 mean the
// subscription is gone. The body is drained and never read: nothing in it is
// ours to log.
func (s *Sender) Send(ctx context.Context, sub model.PushSubscription, payload []byte) (int, error) {
	opts := s.opts
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256DH, Auth: sub.Auth},
	}, &opts)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}
