package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Pusher delivers one Web Push message to one subscription and reports the
// push service's HTTP status. The production one signs with the instance's
// VAPID key (internal/push); tests hand in a recorder. Nil in Config means
// the instance has no key pair and nothing is sent (docs/design/push.md).
type Pusher interface {
	Send(ctx context.Context, sub model.PushSubscription, payload []byte) (int, error)
}

// pushPayload is what the service worker receives: which capture ended how,
// and the note's title so the notification can name it. Never the text — a
// notification may be read from a lock screen.
type pushPayload struct {
	Type      string `json:"type"`
	CaptureID string `json:"capture_id"`
	NoteID    string `json:"note_id,omitempty"`
	Title     string `json:"title,omitempty"`
}

// The push service's two ways of saying a subscription is gone (RFC 8030
// §7.3). Numeric because the package's tests already name an `http` of
// their own. A rotated VAPID key is not one of them: the service then
// answers 401 or 403 (RFC 8292 §4.2) for every row, which counts as a
// failure below, and the person turns the switch off and on again
// (docs/design/push.md, Keys). Pruning on 403 would also wipe every row
// the first time a misconfigured subject or clock signed a bad token.
const (
	pushStatusNotFound = 404
	pushStatusGone     = 410
)

// pushSendTimeout bounds one send. A push service that does not answer in
// five seconds is not going to; the list is the truth and the next open
// reads it.
const pushSendTimeout = 5 * time.Second

// wantsPush is Home's own rule for which captures a person is not already
// watching (R5-RC-2): a device's capture always, the app's own only when
// nobody chose its note — a recording made into a note in the app is on
// screen while it files.
func wantsPush(c model.CaptureIndex) bool {
	return strings.HasPrefix(c.Source, "device:") || !c.TargetSource.Targeted()
}

// pushKind is the payload's type for a final status, or "" for one that is
// not announced: no_content and spend_capped are the app's to show.
func pushKind(status model.CaptureStatus) string {
	switch status {
	case model.StatusAppended, model.StatusNeedsTarget, model.StatusFailed:
		return string(status)
	default:
		return ""
	}
}

// notify sends one push per subscription of the capture's tenant after the
// capture reached a final state a person would want to hear about. Every
// failure here is logged and counted, never returned: the capture is done,
// and a Lambda retry for a notification would run the pipeline again for a
// finished row that returns at the top of run.
func (p *Pipeline) notify(ctx context.Context, capture model.CaptureIndex) {
	kind := pushKind(capture.Status)
	if p.cfg.Pusher == nil || kind == "" || !wantsPush(capture) {
		return
	}
	log := obs.Log(ctx).With(slog.String("capture_id", capture.ID))
	subs, err := p.cfg.Store.ListPushSubscriptions(ctx, capture.UserID)
	if err != nil {
		log.Warn("could not list push subscriptions; no notification sent", slog.String("error", err.Error()))
		return
	}
	if len(subs) == 0 {
		return
	}
	payload := pushPayload{Type: kind, CaptureID: capture.ID, NoteID: capture.NoteID}
	if capture.NoteID != "" {
		// The title only; a note that vanished meanwhile is announced without one.
		if note, err := p.cfg.Store.GetNote(ctx, capture.UserID, capture.NoteID); err == nil {
			payload.Title = note.Title
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Error("could not encode push payload", slog.String("error", err.Error()))
		return
	}

	for _, sub := range subs {
		sendCtx, cancel := context.WithTimeout(ctx, pushSendTimeout)
		status, err := p.cfg.Pusher.Send(sendCtx, sub, body)
		cancel()
		switch {
		case err == nil && status/100 == 2:
			sub.LastSuccessAt = model.FormatTime(p.now())
			sub.Failures = 0
			obs.Count(ctx, "PushSent", map[string]string{"Kind": kind})
		case err == nil && (status == pushStatusNotFound || status == pushStatusGone):
			// The browser unsubscribed, or the subscription expired: the
			// push service says so once and the row goes with it.
			if derr := p.cfg.Store.DeletePushSubscription(ctx, capture.UserID, sub.ID); derr != nil && !errors.Is(derr, repository.ErrNotFound) {
				log.Warn("could not remove a gone push subscription", slog.String("subscription_id", sub.ID), slog.String("error", derr.Error()))
			}
			log.Info("push subscription gone; removed", slog.String("subscription_id", sub.ID), slog.Int("status", status))
			obs.Count(ctx, "PushSubscriptionsPruned", nil)
			continue
		default:
			sub.Failures++
			// The error text of a failed POST names the URL, which is the
			// endpoint; only the reason is logged.
			log.Warn("push send failed", slog.String("subscription_id", sub.ID), slog.Int("status", status),
				slog.Int64("failures", sub.Failures), slog.String("error", pushErrorText(err)))
			obs.Count(ctx, "PushSendFailures", map[string]string{"Kind": kind})
		}
		// The counters only, and only on a row that still exists: the person
		// may have turned the switch off between the list and this send, and
		// a whole-row write would enrol the browser again.
		if uerr := p.cfg.Store.UpdatePushSubscriptionResult(ctx, capture.UserID, sub.ID, sub.LastSuccessAt, sub.Failures); uerr != nil && !errors.Is(uerr, repository.ErrNotFound) {
			log.Warn("could not record the push result", slog.String("subscription_id", sub.ID), slog.String("error", uerr.Error()))
		}
	}
}

// pushErrorText is an error without the endpoint a *url.Error carries.
func pushErrorText(err error) string {
	if err == nil {
		return ""
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Op + ": " + uerr.Err.Error()
	}
	return err.Error()
}
