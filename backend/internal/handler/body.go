package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

// Body and field caps.
//
// Every request body and every free-text field is bounded. An unbounded PATCH
// body goes straight into a 512MB Lambda, and an uncapped alias list is a
// stored token-cost amplifier: every alias is rendered into the routing prompt
// for every future capture, so one long alias is paid for forever.
const (
	// MaxNoteBodyBytes is the largest note body accepted.
	MaxNoteBodyBytes = 1 << 20
	// MaxNoteRequestBytes is MaxNoteBodyBytes plus room for the JSON envelope
	// and the other fields, so a body exactly at the limit still fits.
	MaxNoteRequestBytes = MaxNoteBodyBytes + (64 << 10)
	// MaxSmallRequestBytes bounds every request that is not a note body.
	MaxSmallRequestBytes = 64 << 10

	// MaxTitleRunes bounds a title as the OpenAPI declares it. The store
	// sanitises further; this is the point at which an over-long title is
	// refused rather than quietly cut.
	MaxTitleRunes = 200
	// MaxAliases and MaxAliasRunes bound the alias list.
	MaxAliases     = 32
	MaxAliasRunes  = 120
	MaxTags        = 32
	MaxTagRunes    = 40
	MaxMatchQuery  = 2000
	MaxSearchQuery = service.MaxSearchQueryRunes
	// MaxIdempotencyKey and MinIdempotencyKey match the OpenAPI header bounds.
	MinIdempotencyKey = 8
	MaxIdempotencyKey = 128
)

// errBodyTooLarge is returned when the request body exceeded its cap.
var errBodyTooLarge = errors.New("request body too large")

// readBody reads at most limit bytes. It is the only place a request body is
// read, so there is no route where the limit was forgotten.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errBodyTooLarge
		}
		return nil, err
	}
	return raw, nil
}

// decodeJSON reads and decodes a bounded JSON body, answering the client
// directly on failure. It reports whether the handler should continue.
//
// Unknown fields are rejected. A client that misspells "verbatim" should learn
// that now rather than discover months later that the flag never applied.
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	raw, err := readBody(w, r, limit)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			httperr.PayloadTooLarge(w, r, fmt.Sprintf("the request body exceeds %d bytes", limit))
			return false
		}
		httperr.BadRequest(w, r, "the request body could not be read")
		return false
	}
	if len(raw) == 0 {
		httperr.BadRequest(w, r, "a JSON body is required")
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		httperr.BadRequest(w, r, "the request body is not valid JSON for this endpoint")
		return false
	}
	return true
}

// validationError is a client-visible field problem.
type validationError struct{ detail string }

func (e validationError) Error() string { return e.detail }

func invalid(format string, args ...any) error {
	return validationError{detail: fmt.Sprintf(format, args...)}
}

// checkTitle bounds a title.
func checkTitle(title string) error {
	if strings.TrimSpace(title) == "" {
		return invalid("title is required")
	}
	if n := len([]rune(title)); n > MaxTitleRunes {
		return invalid("title is %d characters; the limit is %d", n, MaxTitleRunes)
	}
	return nil
}

// checkStrings bounds a list's length and each entry's length.
func checkStrings(name string, values []string, maxCount, maxRunes int) error {
	if len(values) > maxCount {
		return invalid("%s has %d entries; the limit is %d", name, len(values), maxCount)
	}
	for _, v := range values {
		if n := len([]rune(v)); n > maxRunes {
			return invalid("one %s entry is %d characters; the limit is %d", name, n, maxRunes)
		}
	}
	return nil
}

// checkBody bounds a note body in bytes, which is what storage charges for.
func checkBody(body string) error {
	if len(body) > MaxNoteBodyBytes {
		return invalid("the note body is %d bytes; the limit is %d", len(body), MaxNoteBodyBytes)
	}
	return nil
}

// answerValidation writes the response for a validation error and reports
// whether it handled one.
func answerValidation(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var v validationError
	if errors.As(err, &v) {
		// A field over its limit is 413 when it is the body — the thing the cap
		// exists to bound — and 400 when it is a short field the client simply
		// got wrong.
		if strings.Contains(v.detail, "note body is") {
			httperr.PayloadTooLarge(w, r, v.detail)
			return true
		}
		httperr.BadRequest(w, r, v.detail)
		return true
	}
	fail(w, r, err)
	return true
}

// listOptions reads cursor pagination parameters, bounded as the OpenAPI
// declares them.
func listOptions(r *http.Request) (repository.ListOptions, error) {
	q := r.URL.Query()
	opts := repository.ListOptions{Cursor: q.Get("cursor")}
	if err := service.ValidateCursor(opts.Cursor); err != nil {
		return repository.ListOptions{}, err
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 1 {
			return repository.ListOptions{}, invalid("limit must be a whole number of at least 1")
		}
		if n > int64(repository.MaxListLimit) {
			return repository.ListOptions{}, invalid("limit is %d; the maximum is %d", n, repository.MaxListLimit)
		}
		opts.Limit = int32(n)
	}
	return opts, nil
}
