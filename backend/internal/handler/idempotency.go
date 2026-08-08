package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// HeaderIdempotencyKey is the request header a client replays a POST with.
const HeaderIdempotencyKey = "Idempotency-Key"

// HeaderIdempotencyReplayed marks a response served from a stored record rather
// than by doing the work again.
const HeaderIdempotencyReplayed = "Idempotency-Replayed"

// captureWriter buffers a response so it can be stored against an idempotency
// key before it is sent.
//
// Buffering is only done for requests that carry a key, and every such request
// has already passed through a MaxBytesReader, so the memory is bounded by the
// same caps that bound the request.
type captureWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (c *captureWriter) Header() http.Header { return c.header }

func (c *captureWriter) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(b)
}

// idempotent wraps a handler so that replaying an Idempotency-Key returns the
// original response instead of doing the work twice.
//
// The failure it exists to prevent is concrete: a double-tap on a flaky mobile
// link creating two notes, or a retried capture appending the same dictation to
// a note twice.
//
// A request without the header is passed straight through. The header is
// optional in the contract, and refusing an unkeyed POST would break every
// caller that has no idea what it is for.
func (rt *router) idempotent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(HeaderIdempotencyKey)
		if key == "" {
			next(w, r)
			return
		}
		if len(key) < MinIdempotencyKey || len(key) > MaxIdempotencyKey {
			httperr.BadRequest(w, r, "Idempotency-Key must be between 8 and 128 characters")
			return
		}
		tenantID, ok := middleware.GetUserID(r.Context())
		if !ok {
			httperr.Unauthorized(w, r, "authentication required")
			return
		}
		if rt.Store == nil {
			// No store means no way to honour the promise. Doing the work anyway
			// and pretending is worse than saying so.
			httperr.ServiceUnavailable(w, r, "idempotent replay is not configured on this instance")
			return
		}

		// The body has to be read to fingerprint it, and read again by the
		// handler, so it is buffered here — under the route's own cap.
		raw, err := readBody(w, r, rt.bodyLimit(r))
		if err != nil {
			if errors.Is(err, errBodyTooLarge) {
				httperr.PayloadTooLarge(w, r, "the request body exceeds this endpoint's limit")
				return
			}
			httperr.BadRequest(w, r, "the request body could not be read")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))

		// The fingerprint binds the key to this exact request. Replaying a key
		// with a different body returns the stored response of a request the
		// caller did not make, which is worse than failing.
		sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), raw...))
		fingerprint := hex.EncodeToString(sum[:])

		record, err := rt.Store.BeginIdempotent(r.Context(), tenantID, key, fingerprint)
		if err != nil {
			fail(w, r, err)
			return
		}
		if record != nil {
			obs.Log(r.Context()).Info("idempotent replay",
				slog.Int("status", record.Status),
				slog.Int("bytes", len(record.Response)))
			for k, v := range map[string]string{
				"Content-Type":            "application/json",
				HeaderIdempotencyReplayed: "true",
			} {
				w.Header().Set(k, v)
			}
			w.WriteHeader(record.Status)
			_, _ = w.Write(record.Response)
			return
		}

		buffered := &captureWriter{header: http.Header{}}
		next(buffered, r)
		if buffered.status == 0 {
			buffered.status = http.StatusOK
		}

		// A 5xx is not recorded. It is not a result the caller should be pinned
		// to for the record's whole lifetime, and a retry of a transient
		// failure must be allowed to succeed.
		if buffered.status < 500 {
			if err := rt.Store.CompleteIdempotent(r.Context(), tenantID, key, buffered.status, buffered.body.Bytes()); err != nil &&
				!errors.Is(err, repository.ErrNotFound) {
				obs.Log(r.Context()).Warn("could not record idempotent response",
					slog.String("error", err.Error()))
			}
		}

		for k, values := range buffered.header {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(buffered.status)
		_, _ = w.Write(buffered.body.Bytes())
	}
}
