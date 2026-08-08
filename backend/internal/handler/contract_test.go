package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// The frontend↔backend contract check, backend half.
//
// The two halves of this product were written in parallel against
// docs/api/openapi.yaml, and the frontend's wire types in
// frontend/src/api/schema.ts are hand-written from that document. Until this
// file existed, nothing anywhere proved that a response this API returns is one
// those types accept, or that a request the frontend builds is one this API
// understands. Both sides could be individually correct and still not meet.
//
// openapi_conformance_test.go checks this API against the *document*. That is a
// different question: a document both sides misread the same way passes it. This
// file checks the API against the *other implementation*.
//
// It works in both directions:
//
//   - Responses. TestContractResponsesAreWhatTheFrontendTypesDeclare drives the
//     real router over httptest, captures one body per interesting shape, and
//     writes them into frontend/src/api/__fixtures__/responses.ts as TypeScript
//     object literals with an explicit type annotation taken from schema.ts.
//     Because an annotated object literal is "fresh", TypeScript applies excess
//     property checking to it: a field this API adds that schema.ts does not
//     declare is a compile error, a field renamed here is a compile error, and a
//     status string outside the declared union is a compile error. `bun run
//     typecheck` is what fails.
//
//   - Requests. TestContractRequestsFromTheFrontendAreAccepted replays
//     frontend/src/api/__fixtures__/requests.json — recorded by driving the real
//     ChintanApi against a stub fetch — through this router. decodeJSON calls
//     DisallowUnknownFields, so a field renamed on the frontend is a 400 here.
//
// Regenerate the response fixtures with:
//
//	CHINTAN_UPDATE_FIXTURES=1 go test ./internal/handler/ -run Contract
const (
	contractFixtureDir   = "../../../frontend/src/api/__fixtures__"
	contractResponsesTS  = contractFixtureDir + "/responses.ts"
	contractRequestsJSON = contractFixtureDir + "/requests.json"
)

// contractUser is the tenant every fixture is captured as.
const contractUser = "user1"

// ---------------------------------------------------------------- responses

// contractFixture is one captured response, ready to be rendered as a typed
// TypeScript constant.
type contractFixture struct {
	// Name is the exported TypeScript identifier.
	Name string
	// Type is the annotation, which is what makes TypeScript check the literal.
	Type string
	// Doc says which operation produced the body.
	Doc string
	// Body is the response, after volatile values are replaced.
	Body any
}

func TestContractResponsesAreWhatTheFrontendTypesDeclare(t *testing.T) {
	fixtures := captureContractFixtures(t)
	rendered := renderContractFixtures(t, fixtures)

	path := filepath.Clean(contractResponsesTS)
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == rendered {
		return
	}

	if os.Getenv("CHINTAN_UPDATE_FIXTURES") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create fixture directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(rendered), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("regenerated %s", path)
		return
	}

	if err != nil {
		t.Fatalf("read %s: %v\nre-run with CHINTAN_UPDATE_FIXTURES=1 to create it", path, err)
	}
	t.Fatalf("the responses this API returns no longer match %s.\n"+
		"Something about the wire shape changed. Re-run with CHINTAN_UPDATE_FIXTURES=1, "+
		"commit the result, and let the frontend typecheck decide whether schema.ts still accepts it.\n\n%s",
		path, firstDifference(string(existing), rendered))
}

// firstDifference reports the first line that differs, which is the whole of
// what a reader needs to see.
func firstDifference(have, want string) string {
	haveLines := strings.Split(have, "\n")
	wantLines := strings.Split(want, "\n")
	for i := 0; i < len(haveLines) || i < len(wantLines); i++ {
		h, w := "", ""
		if i < len(haveLines) {
			h = haveLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if h != w {
			return fmt.Sprintf("first difference at line %d:\n  committed: %s\n  produced:  %s", i+1, h, w)
		}
	}
	return "files differ only in length"
}

// captureContractFixtures drives the real router once per shape.
//
// Every body here comes out of an actual HTTP response, not out of a struct
// literal: a handler that forgets to serialise a field must be able to fail
// this.
//
// One table, one entry per wire shape: splitting it would hide the thing worth
// seeing, which is the whole surface at once.
//
//nolint:funlen // See above.
func captureContractFixtures(t *testing.T) []contractFixture {
	t.Helper()

	var out []contractFixture
	add := func(name, tsType, doc string, w *httptest.ResponseRecorder) {
		t.Helper()
		out = append(out, contractFixture{
			Name: name,
			Type: tsType,
			Doc:  doc,
			Body: decodeContractBody(t, name, w),
		})
	}

	// ---- health and readiness
	h := newHarness(t)
	add("health", "{ status: 'ok' }", "GET /v1/health → 200",
		h.do(t, http.MethodGet, "/v1/health", "", nil))
	add("ready", "ReadinessWire", "GET /v1/health/ready → 200",
		h.do(t, http.MethodGet, "/v1/health/ready", "", nil))
	add("readyDegraded", "ProblemWire", "GET /v1/health/ready → 503, a dependency is not answering",
		newHarness(t, withBrokenStore()).do(t, http.MethodGet, "/v1/health/ready", "", nil))

	// ---- settings
	h = newHarness(t)
	add("settings", "SettingsWire", "GET /v1/settings → 200, the defaults a new tenant gets",
		h.do(t, http.MethodGet, "/v1/settings", contractUser, nil))
	add("settingsStored", "SettingsWire",
		"PUT /v1/settings → 200. This is what was STORED, not what was sent: v1 echoed the request back and hid every coercion.",
		h.do(t, http.MethodPut, "/v1/settings", contractUser, map[string]any{
			"cleanup_mode": "polished", "retention_days": 30, "theme": "nocturne",
		}))

	// ---- notes
	h = newHarness(t)
	tagged := h.createNote(t, contractUser, "Kitchen rebuild", map[string]any{
		"body":    "Quotes are in. The tiler can start on the fourteenth.",
		"aliases": []string{"kitchen", "reno"},
		"tags":    []string{"house", "money"},
	})
	plain := h.createNote(t, contractUser, "Reading list", map[string]any{
		"tags": []string{"house"},
	})
	// Verbatim can only be set by an update, so a note that carries it can only
	// appear in a fixture this way. It is in the list shape whether or not
	// schema.ts declares it, which is exactly the kind of one-sided field this
	// test exists to surface.
	h.do(t, http.MethodPatch, "/v1/notes/"+plain.ID, contractUser,
		map[string]any{"version": plain.Version, "verbatim": true})

	h.putCapture(t, model.CaptureIndex{
		ID: "c_note_1", UserID: contractUser, NoteID: tagged.ID,
		Status: model.StatusAppended, CreatedAt: model.Now(),
		AppendedAt: time.Now().Unix(), DurationMS: 18_400,
		SegmentsKey: "tenants/user1/captures/c_note_1/segments.json",
		PeaksKey:    "tenants/user1/captures/c_note_1/peaks.json",
	})

	add("notesPage", "Page<NoteWire>", "GET /v1/notes → 200. The envelope is {items, cursor}; a bare array plus X-Next-Cursor was the v1 shape and is gone.",
		h.do(t, http.MethodGet, "/v1/notes", contractUser, nil))
	add("noteDetail", "NoteDetailWire", "GET /v1/notes/{noteId} → 200, with body and captures",
		h.do(t, http.MethodGet, "/v1/notes/"+tagged.ID, contractUser, nil))
	add("noteCreated", "NoteWire", "POST /v1/notes → 201",
		h.do(t, http.MethodPost, "/v1/notes", contractUser, map[string]any{"title": "A new thought"}))
	add("tagsPage", "Page<TagWire>", "GET /v1/tags → 200, one entry per tag in use with its count",
		h.do(t, http.MethodGet, "/v1/tags", contractUser, nil))
	add("searchPage", "Page<SearchHitWire>",
		"GET /v1/search → 200. matched_in names the fields that matched; excerpt is the surrounding context.",
		h.do(t, http.MethodGet, "/v1/search?q=tiler", contractUser, nil))
	add("matchResponse", "MatchResponseWire", "POST /v1/notes/match → 200",
		h.do(t, http.MethodPost, "/v1/notes/match", contractUser, map[string]any{"query": "kitchen"}))

	// ---- captures
	h = newHarness(t)
	note := h.createNote(t, contractUser, "Destination", nil)
	h.putCapture(t, model.CaptureIndex{
		ID: "c_appended", UserID: contractUser, NoteID: note.ID,
		Status: model.StatusAppended, CreatedAt: model.Now(),
		AppendedAt: time.Now().Unix(), DurationMS: 9_100,
		SegmentsKey: "tenants/user1/captures/c_appended/segments.json",
		PeaksKey:    "tenants/user1/captures/c_appended/peaks.json",
		AudioKey:    "tenants/user1/captures/c_appended/audio.webm",
	})
	// A capture with nowhere to go must still be findable. It was not, once —
	// the tenant list walked the note index and a capture with no note fell out
	// of it entirely.
	h.putCapture(t, model.CaptureIndex{
		ID: "c_needs_target", UserID: contractUser, Status: model.StatusNeedsTarget,
		CreatedAt: model.Now(), SuggestedTitle: "Kitchen rebuild", RouteConfidence: 0.41,
	})
	// The other half of a routing suggestion, and the one v1 led with: an
	// existing note the router named but was not confident enough to write to
	// unasked. Exactly one of the two fields is ever set, so a fixture carrying
	// only SuggestedTitle would leave the frontend's handling of this branch
	// unchecked.
	//
	// The destination is written with a fixed id rather than through
	// createNote, whose ids embed the creation instant: a fixture is compared
	// byte for byte against the committed copy, so a value that changes every
	// run would fail the "the committed fixtures are the ones the two sides
	// produce" gate on every unrelated build.
	suggestedNote, err := h.store.PutNote(t.Context(), contractUser, model.NoteIndex{
		ID: "contract-suggested-note", Title: "Kitchen rebuild", UpdatedAt: contractTime,
		S3MarkdownKey: "tenants/user1/notes/contract-suggested-note/note.md",
		S3MetaKey:     "tenants/user1/notes/contract-suggested-note/meta.json",
	})
	if err != nil {
		t.Fatalf("seed the suggested note: %v", err)
	}
	suggested := h.putCapture(t, model.CaptureIndex{
		ID: "c_needs_target_note", UserID: contractUser, Status: model.StatusNeedsTarget,
		CreatedAt: model.Now(), SuggestedNoteID: suggestedNote.ID, RouteConfidence: 0.62,
	})
	failed := h.putCapture(t, model.CaptureIndex{
		ID: "c_failed", UserID: contractUser, NoteID: note.ID,
		Status: model.StatusFailed, CreatedAt: model.Now(),
		Error:    "the speech provider returned 503",
		AudioKey: "tenants/user1/captures/c_failed/audio.webm",
	})

	add("capturesPage", "Page<CaptureWire>",
		"GET /v1/captures → 200. Includes the unrouted needs_target capture the progress card has to show.",
		h.do(t, http.MethodGet, "/v1/captures", contractUser, nil))
	add("captureSuggestedNote", "CaptureWire",
		"GET /v1/captures/{captureId} → 200 for a needs_target capture the router matched to an existing note. "+
			"`suggested_note_id` is what the \"Add to <note>\" prompt is built from.",
		h.do(t, http.MethodGet, "/v1/captures/"+suggested.ID, contractUser, nil))
	add("captureFailed", "CaptureWire",
		"GET /v1/captures/{captureId} → 200 for a failed capture. `error` is the text the progress card renders.",
		h.do(t, http.MethodGet, "/v1/captures/"+failed.ID, contractUser, nil))
	add("captureDownload", "PresignedDownloadWire", "GET /v1/captures/{captureId}/download?kind=audio → 200",
		h.do(t, http.MethodGet, "/v1/captures/"+failed.ID+"/download?kind=audio", contractUser, nil))

	// The tag-aware presigner is wired in only here. The default harness signs
	// through the in-memory object store, which cannot bind tags and honestly
	// declines to advertise a header its signature does not cover — so it would
	// produce a fixture with no x-amz-tagging in it and quietly stop pinning the
	// one header an upload cannot survive losing.
	tagged2 := newHarness(t)
	tagged2.captures.WithUploads(taggingPresigner{})
	add("captureCreated", "CaptureCreatedWire",
		"POST /v1/captures → 201. upload.headers reaches the client verbatim; x-amz-tagging is inside the signature, so dropping it makes the PUT 403.",
		tagged2.do(t, http.MethodPost, "/v1/captures", contractUser, map[string]any{
			"content_type": "audio/webm", "size_bytes": 4 << 20, "duration_ms": 12_000,
		}))

	// ---- export
	h = newHarness(t)
	add("exportJob", "ExportJobWire", "POST /v1/export → 202",
		h.do(t, http.MethodPost, "/v1/export", contractUser, nil))

	// ---- biometric unlock
	h = newHarness(t)
	add("webauthnStatus", "{ enrolled: boolean }", "GET /v1/auth/webauthn/status → 200",
		h.do(t, http.MethodGet, "/v1/auth/webauthn/status", contractUser, nil))
	add("webauthnOptions", "WebAuthnOptionsWire", "POST /v1/auth/webauthn/register/options → 200",
		h.do(t, http.MethodPost, "/v1/auth/webauthn/register/options", contractUser, nil))
	add("tokenSet", "TokenSetWire", "POST /v1/auth/webauthn/login → 200, the Cognito token set as it arrives",
		h.do(t, http.MethodPost, "/v1/auth/webauthn/login", "", verifyBody()))

	// ---- problem documents
	h = newHarness(t)
	add("problemNotFound", "ProblemWire", "GET /v1/notes/{noteId} → 404",
		h.do(t, http.MethodGet, "/v1/notes/missing", contractUser, nil))
	add("problemValidation", "ProblemWire", "PUT /v1/settings → 400",
		h.do(t, http.MethodPut, "/v1/settings", contractUser, map[string]any{"theme": "puce"}))

	contended := h.createNote(t, contractUser, "Contended", nil)
	h.do(t, http.MethodPatch, "/v1/notes/"+contended.ID, contractUser,
		map[string]any{"version": contended.Version, "title": "Winner"})
	add("problemConflict", "ProblemWire",
		"PATCH /v1/notes/{noteId} → 409. current_version is what lets an optimistic-concurrency loser reconcile instead of guessing.",
		h.do(t, http.MethodPatch, "/v1/notes/"+contended.ID, contractUser,
			map[string]any{"version": contended.Version, "title": "Loser"}))

	capped := newHarness(t)
	capped.spend.capped = true
	add("problemSpendCapped", "ProblemWire",
		"POST /v1/captures → 429 for the daily spend cap, which the client must not treat as a retryable 429.",
		capped.do(t, http.MethodPost, "/v1/captures", contractUser, map[string]any{"content_type": "audio/webm"}))

	// The batch purge, with all three outcomes in one response: the client
	// renders a per-note result list and has to handle a batch that partly
	// failed, which is the normal case for anything cascading into S3.
	purgeH := newHarness(t)
	purgeArchived := purgeH.createNote(t, contractUser, "Done with this", nil)
	if w := purgeH.do(t, http.MethodDelete, "/v1/notes/"+purgeArchived.ID, contractUser, nil); w.Code != http.StatusNoContent {
		t.Fatalf("archive for the purge fixture = %d", w.Code)
	}
	purgeActive := purgeH.createNote(t, contractUser, "Still in use", nil)
	add("notePurgeResults", "NotePurgeResponseWire",
		"POST /v1/notes/purge → 200. One result per note: purged, not_found, and an active note refused. 200 even when some failed, because no transaction spans DynamoDB and S3.",
		purgeH.do(t, http.MethodPost, "/v1/notes/purge", contractUser, map[string]any{
			"note_ids": []string{purgeArchived.ID, "note_missing", purgeActive.ID},
		}))

	// The three biometric outcomes, as fixtures, because the frontend has to
	// branch on all three and was branching on English prose. Recording each
	// response is what stops a `type` string being changed on one side only.
	notEnrolled := newHarness(t)
	notEnrolled.webauthn.beginErr = service.ErrWebAuthnNotEnrolled
	add("problemBiometricNotEnrolled", "ProblemWire",
		"POST /v1/auth/webauthn/login/options → 404 when nothing is enrolled. Offer enrolment; do not retry.",
		notEnrolled.do(t, http.MethodPost, "/v1/auth/webauthn/login/options", "", nil))

	add("problemBiometricUnavailable", "ProblemWire",
		"POST /v1/auth/webauthn/login/options → 503 when the instance has no biometric support at all. Hide the control.",
		newHarness(t, withoutWebAuthn()).
			do(t, http.MethodPost, "/v1/auth/webauthn/login/options", "", nil))

	reEnrol := newHarness(t)
	reEnrol.webauthn.finishLogErr = service.ErrWebAuthnReEnrolRequired
	add("problemBiometricReEnrolment", "ProblemWire",
		"POST /v1/auth/webauthn/login → 401 when the credential verified but its sealed token cannot be opened. Offer enrolment again, do not fall back to a plain sign-in.",
		reEnrol.do(t, http.MethodPost, "/v1/auth/webauthn/login", "", verifyBody()))

	return out
}

func decodeContractBody(t *testing.T, name string, w *httptest.ResponseRecorder) any {
	t.Helper()
	if w.Body.Len() == 0 {
		t.Fatalf("fixture %q captured an empty body (status %d)", name, w.Code)
	}
	var body any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("fixture %q is not JSON: %v (status %d, body %s)", name, err, w.Code, w.Body.String())
	}
	return stabilise(body)
}

// ------------------------------------------------------------- stabilising

// contractTime is what every timestamp in a fixture becomes.
const contractTime = "2026-01-01T00:00:00.000000000Z"

// volatileStrings maps a field whose value changes every run to a stable stand-in.
//
// Only the value is replaced, never the field's presence and never its JSON
// type: a null stays null, so the difference between `"error": null` and
// `"error": "…"` — which is the difference the progress card renders — survives.
var volatileStrings = map[string]string{
	"id":             "fixture-id",
	"note_id":        "fixture-note-id",
	"auto_select_id": "fixture-note-id",
	"challenge_id":   "fixture-challenge-id",
	"correlation_id": "00000000-0000-4000-8000-000000000000",
	"instance":       "/v1/fixture",
	"url":            "https://example.invalid/presigned",
	"created_at":     contractTime,
	"updated_at":     contractTime,
	"expires_at":     contractTime,
	"appended_at":    contractTime,
	"purge_after":    contractTime,
}

// volatileNumbers is the same idea for measured values.
var volatileNumbers = map[string]float64{
	"latency_ms": 1,
	"bytes":      2048,
	"score":      0.87,
	"confidence": 0.87,
}

func stabilise(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			// A headers map is user-visible protocol, not incidental: every entry
			// is inside the presigned signature. It is copied through untouched.
			if key == "headers" {
				out[key] = value
				continue
			}
			if replacement, ok := volatileStrings[key]; ok {
				if _, isString := value.(string); isString {
					out[key] = replacement
					continue
				}
			}
			if replacement, ok := volatileNumbers[key]; ok {
				if _, isNumber := value.(float64); isNumber {
					out[key] = replacement
					continue
				}
			}
			out[key] = stabilise(value)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, stabilise(item))
		}
		return out
	default:
		return v
	}
}

// -------------------------------------------------------------- rendering

func renderContractFixtures(t *testing.T, fixtures []contractFixture) string {
	t.Helper()

	var b strings.Builder
	b.WriteString(`/**
 * GENERATED — do not edit by hand.
 *
 * Every constant below is a real response body, captured from the Go router in
 * internal/handler over httptest and written out by
 * TestContractResponsesAreWhatTheFrontendTypesDeclare. Regenerate with:
 *
 *     cd backend && CHINTAN_UPDATE_FIXTURES=1 go test ./internal/handler/ -run Contract
 *
 * The type annotations are the point. An annotated object literal is "fresh" to
 * TypeScript, so excess property checking applies: if the backend adds, renames
 * or retypes a field that schema.ts does not declare the same way, "bun run
 * typecheck" fails here. That is the only thing standing between two
 * independently written implementations and a field name that only one of them
 * changed.
 *
 * Values that differ every run — ids, timestamps, presigned URLs, measured
 * latencies — are replaced with stable stand-ins so the file is reviewable. The
 * SHAPE is never altered: presence, absence, null and type all survive, and
 * upload.headers is copied through verbatim because every entry in it is inside
 * the presigned signature.
 */

`)

	types := neededSchemaTypes(fixtures)
	b.WriteString("import type {\n")
	for _, name := range types {
		b.WriteString("  " + name + ",\n")
	}
	b.WriteString("} from '../schema.ts';\n")

	for _, f := range fixtures {
		encoded, err := encodeContractJSON(f.Body)
		if err != nil {
			t.Fatalf("encode fixture %q: %v", f.Name, err)
		}
		b.WriteString("\n/** " + f.Doc + " */\n")
		b.WriteString("export const " + f.Name + ": " + f.Type + " = " + encoded + ";\n")
	}

	// Every enum below is declared twice, once per language, and a fixture only
	// ever contains the members that happened to occur in it. Emitting the Go
	// side lets the Vitest half compare the whole sets.
	b.WriteString("\n/** Every CaptureStatus the Go backend can write. Compared against CAPTURE_STATUSES. */\n")
	b.WriteString("export const BACKEND_CAPTURE_STATUSES = " + mustEncodeStrings(t, backendCaptureStatuses()) + " as const;\n")
	b.WriteString("\n/**\n" +
		" * The statuses the pipeline is still moving through — service.CaptureIsPending.\n" +
		" * This is the polling question, and its complement must be exactly the\n" +
		" * frontend's TERMINAL_CAPTURE_STATUSES: a status in neither set is one the\n" +
		" * progress card polls forever.\n" +
		" */\n")
	b.WriteString("export const BACKEND_PENDING_CAPTURE_STATUSES = " + mustEncodeStrings(t, backendPendingCaptureStatuses()) + " as const;\n")
	b.WriteString("\n/** The field names search can report in matched_in. Must be a subset of SearchField. */\n")
	b.WriteString("export const BACKEND_SEARCH_MATCH_FIELDS = " + mustEncodeStrings(t, backendSearchMatchFields()) + " as const;\n")
	b.WriteString("\n/** The audio container types POST /v1/captures accepts. Must contain every CaptureContentType. */\n")
	b.WriteString("export const BACKEND_CAPTURE_CONTENT_TYPES = " + mustEncodeStrings(t, backendCaptureContentTypes()) + " as const;\n")

	return b.String()
}

// neededSchemaTypes is the sorted set of schema.ts names the annotations use.
func neededSchemaTypes(fixtures []contractFixture) []string {
	known := map[string]bool{
		"CaptureCreatedWire": true, "CaptureWire": true, "ExportJobWire": true,
		"MatchResponseWire": true, "NoteDetailWire": true, "NoteWire": true,
		"NotePurgeResponseWire": true,
		"Page":                  true, "PresignedDownloadWire": true, "ProblemWire": true,
		"ReadinessWire": true, "SearchHitWire": true, "SettingsWire": true,
		"TagWire": true, "TokenSetWire": true, "WebAuthnOptionsWire": true,
	}
	seen := map[string]bool{}
	for _, f := range fixtures {
		for name := range known {
			if strings.Contains(f.Type, name) {
				seen[name] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// encodeContractJSON renders a body as a TypeScript expression. JSON is a subset
// of TypeScript expression syntax, so the encoder's output is already valid —
// only HTML escaping has to be turned off to keep it readable.
func encodeContractJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

func mustEncodeStrings(t *testing.T, values []string) string {
	t.Helper()
	encoded, err := encodeContractJSON(values)
	if err != nil {
		t.Fatalf("encode enum: %v", err)
	}
	return encoded
}

// backendCaptureStatuses is every status this backend writes, in pipeline order.
// It is spelled out rather than derived because Go has no enumerable enum; the
// guard below is what stops the list rotting.
func backendCaptureStatuses() []string {
	return []string{
		string(model.StatusUploaded),
		string(model.StatusTranscribing),
		string(model.StatusRouting),
		string(model.StatusCleaning),
		string(model.StatusAppending),
		string(model.StatusAppended),
		string(model.StatusNeedsTarget),
		string(model.StatusNoContent),
		string(model.StatusFailed),
		string(model.StatusSpendCapped),
	}
}

// backendPendingCaptureStatuses asks the production predicate, not a second
// copy of the list.
func backendPendingCaptureStatuses() []string {
	out := []string{}
	for _, s := range backendCaptureStatuses() {
		if service.CaptureIsPending(model.CaptureStatus(s)) {
			out = append(out, s)
		}
	}
	return out
}

// backendSearchMatchFields is what SearchService can put in matched_in.
func backendSearchMatchFields() []string {
	return []string{service.MatchTitle, service.MatchAlias, service.MatchTag, service.MatchBody}
}

// backendCaptureContentTypes is what POST /v1/captures accepts, asked of the
// service rather than transcribed from it: a container the frontend offers and
// the backend refuses is a recording the user cannot upload.
func backendCaptureContentTypes() []string {
	candidates := []string{
		"audio/webm", "audio/mp4", "audio/ogg", "audio/wav", "audio/mpeg",
		"audio/mp3", "audio/m4a", "audio/wave", "audio/x-wav", "audio/x-m4a",
	}
	accepted := []string{}
	for _, candidate := range candidates {
		svc := service.NewCaptureService(memory.NewStore(), memory.NewObjects())
		if _, err := svc.BeginCapture(context.Background(), contractUser, service.CaptureRequest{
			ContentType: candidate,
		}); err == nil {
			accepted = append(accepted, candidate)
		}
	}
	return accepted
}

// TestContractCaptureStatusListIsComplete guards the hand-written list above.
//
// model.StatusTranscribed and model.StatusCleaned are internal stages the API
// reports as their -ing forms, so they are deliberately absent; everything else
// must be present or the frontend is being told about an enum that is missing a
// member.
func TestContractCaptureStatusListIsComplete(t *testing.T) {
	declared := map[string]bool{}
	for _, s := range backendCaptureStatuses() {
		declared[s] = true
	}
	for _, s := range []model.CaptureStatus{
		model.StatusUploaded, model.StatusAppended, model.StatusFailed,
		model.StatusNeedsTarget, model.StatusNoContent, model.StatusTranscribing,
		model.StatusRouting, model.StatusCleaning, model.StatusAppending,
		model.StatusSpendCapped,
	} {
		if !declared[string(s)] {
			t.Errorf("%q is a status this backend writes but the contract fixture does not export it, "+
				"so the frontend's CaptureStatus union is never checked against it", s)
		}
	}
}

// ----------------------------------------------------------------- requests

// frontendRequest is one call recorded from the real ChintanApi.
type frontendRequest struct {
	Name   string          `json:"name"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// routeMissDetail is what the router's own fallback says when no handler matches.
// A resource that is merely absent produces a different one, and the difference
// is what separates "the frontend called a path that does not exist" from "the
// fixture id is not in this store".
const routeMissDetail = "no route matches this path"

// TestContractRequestsFromTheFrontendAreAccepted replays the frontend's own
// requests through the real router.
//
// The recording is produced by frontend/src/api/contract-requests.test.ts, which
// drives ChintanApi against a stub fetch — so these are the exact method, path,
// query string and JSON body the app will send, not a transcription of them.
//
// The assertion is that the backend UNDERSTOOD each one. A 404 for an id this
// harness does not hold is fine; a 404 from the route table is not, and neither
// is a 400 — decodeJSON rejects unknown fields, so a field the frontend renamed
// on its own lands here as "the request body is not valid JSON for this
// endpoint".
func TestContractRequestsFromTheFrontendAreAccepted(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(contractRequestsJSON))
	if err != nil {
		t.Fatalf("read %s: %v\nRun `bun run test` in frontend/ to record it.", contractRequestsJSON, err)
	}
	var requests []frontendRequest
	if err := json.Unmarshal(raw, &requests); err != nil {
		t.Fatalf("decode %s: %v", contractRequestsJSON, err)
	}
	if len(requests) < 20 {
		t.Fatalf("only %d recorded requests; the frontend has more operations than that, "+
			"so the recorder and the API client have drifted", len(requests))
	}

	for _, req := range requests {
		t.Run(req.Name, func(t *testing.T) {
			h := seededContractHarness(t)
			path := substituteCursor(t, h, req.Path)

			var body any
			if len(req.Body) > 0 {
				body = []byte(req.Body)
			}
			w := h.do(t, req.Method, path, contractUser, body,
				[2]string{"Idempotency-Key", "contract-" + req.Name})

			switch {
			case w.Code == http.StatusMethodNotAllowed:
				t.Fatalf("%s %s: the backend answers 405; the frontend and the route table disagree about the method",
					req.Method, path)
			case w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), routeMissDetail):
				t.Fatalf("%s %s: no such route. The frontend calls a path this API does not serve.",
					req.Method, path)
			case w.Code == http.StatusBadRequest:
				t.Fatalf("%s %s: the backend rejected the body or query the frontend builds: %s",
					req.Method, path, w.Body.String())
			case w.Code == http.StatusUnsupportedMediaType, w.Code == http.StatusUnprocessableEntity:
				t.Fatalf("%s %s: the backend answers %d for the frontend's request: %s",
					req.Method, path, w.Code, w.Body.String())
			}
		})
	}
}

// cursorPlaceholder is what the recorder writes where a continuation token
// belongs.
//
// A cursor is opaque and validated — it carries the partition it was issued for,
// so handing one query's cursor to another is refused rather than honoured. The
// frontend therefore cannot invent one that would be accepted, and inventing one
// is not what it does in production either: it echoes back the cursor it was
// given. This substitutes a token issued by the same collection, so the page-two
// request is a real one.
const cursorPlaceholder = "__CONTRACT_CURSOR__"

func substituteCursor(t *testing.T, h *harness, path string) string {
	t.Helper()
	if !strings.Contains(path, cursorPlaceholder) {
		return path
	}

	source := "/v1/notes?limit=1"
	if strings.HasPrefix(path, "/v1/captures") {
		source = "/v1/captures?limit=1"
	}
	w := h.do(t, http.MethodGet, source, contractUser, nil)
	var page struct {
		Cursor string `json:"cursor"`
	}
	decodeInto(t, w, &page)
	if page.Cursor == "" {
		t.Fatalf("%s issued no cursor, so nothing can stand in for %s; seed more rows",
			source, cursorPlaceholder)
	}
	return strings.Replace(path, cursorPlaceholder, url.QueryEscape(page.Cursor), 1)
}

// seededContractHarness holds the fixed identifiers the recorder uses, so a
// request that names one reaches a real handler rather than stopping at a 404
// that would hide a shape problem behind it.
func seededContractHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.store.PutNote(ctx, contractUser, model.NoteIndex{
		ID: "contract-note", Title: "Contract note", UpdatedAt: model.Now(), CreatedAt: model.Now(),
	}); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	// Restore and permanent delete only make sense against an archived note, and
	// a request that stops at "this note is not archived" would prove nothing
	// about its shape.
	if _, err := h.store.PutNote(ctx, contractUser, model.NoteIndex{
		ID: "contract-archived-note", Title: "Archived note", UpdatedAt: model.Now(), CreatedAt: model.Now(),
	}); err != nil {
		t.Fatalf("seed archived note: %v", err)
	}
	if _, err := h.notes.ArchiveNote(ctx, contractUser, "contract-archived-note"); err != nil {
		t.Fatalf("archive seed note: %v", err)
	}
	// A second active note and a second capture, so a limit of one leaves
	// something behind and the store issues a real cursor for the paged
	// requests to carry.
	if _, err := h.store.PutNote(ctx, contractUser, model.NoteIndex{
		ID: "contract-note-2", Title: "Second contract note", UpdatedAt: model.Now(), CreatedAt: model.Now(),
	}); err != nil {
		t.Fatalf("seed second note: %v", err)
	}
	if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID: "contract-capture-2", UserID: contractUser, NoteID: "contract-note",
		Status: model.StatusAppended, CreatedAt: model.Now(),
	}); err != nil {
		t.Fatalf("seed second capture: %v", err)
	}
	if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID: "contract-capture", UserID: contractUser, Status: model.StatusNeedsTarget,
		CreatedAt: model.Now(), AudioKey: "tenants/user1/captures/contract-capture/audio.webm",
		SegmentsKey: "tenants/user1/captures/contract-capture/segments.json",
		PeaksKey:    "tenants/user1/captures/contract-capture/peaks.json",
		RawKey:      "tenants/user1/captures/contract-capture/raw.txt",
		CleanKey:    "tenants/user1/captures/contract-capture/clean.txt",
	}); err != nil {
		t.Fatalf("seed capture: %v", err)
	}
	return h
}
