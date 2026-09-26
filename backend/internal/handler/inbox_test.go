package handler_test

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/service"
	"github.com/vppillai/chintan/backend/internal/upload"
)

// deviceKey issues a device for user1 and returns the header a device sends.
func (h *harness) deviceKey(t *testing.T) [2]string {
	t.Helper()
	return [2]string{"Authorization", "Bearer " + h.createDevice(t, "user1", "Kitchen watch").Key}
}

// The inbox's refusals are fixed sentences: a revoked key, a wrong secret and
// no key at all read the same, and the day's limit is its own 429.
func TestInboxRefusesUnknownRevokedAndExhaustedKeys(t *testing.T) {
	h := newHarness(t)
	created := h.createDevice(t, "user1", "Watch")
	key := [2]string{"Authorization", "Bearer " + created.Key}
	text := map[string]any{"text": "buy milk"}

	// The days the router may count the request under. It reads its own
	// clock (handler.Deps carries none to pin), so a request sent just before
	// midnight can land on either side of it; both days are summed.
	now := time.Now().UTC()
	day, nextDay := now.Format("2006-01-02"), now.AddDate(0, 0, 1).Format("2006-01-02")
	if w := h.do(t, http.MethodPost, "/v1/inbox/text", "", text, key); w.Code != http.StatusAccepted {
		t.Fatalf("a live key: status = %d body = %s", w.Code, w.Body.String())
	}
	// The last character is flipped, as #89 does in the service test, so the
	// wrong secret can never be the key; overwriting the last four with 0000
	// made a key that ends in 0000 its own wrong secret.
	wrongSecret := created.Key[:len(created.Key)-1] + "0"
	if strings.HasSuffix(created.Key, "0") {
		wrongSecret = created.Key[:len(created.Key)-1] + "1"
	}
	for name, header := range map[string][2]string{
		"no key":        {"Authorization", ""},
		"not a key":     {"Authorization", "Bearer hello"},
		"wrong secret":  {"Authorization", "Bearer " + wrongSecret},
		"a session jwt": {"Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.e30.sig"},
	} {
		w := h.do(t, http.MethodPost, "/v1/inbox/text", "", text, header)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, w.Code)
			continue
		}
		if p := problemOf(t, w); p["detail"] != "unknown device key" {
			t.Errorf("%s: detail = %q", name, p["detail"])
		}
	}

	// The 200th request of the day passes; the 201st is refused and stays
	// refused.
	stored, err := h.store.GetDevice(context.Background(), "user1", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.RequestsDay = model.DeviceDailyRequestLimit - 1
	if _, err := h.store.PutDevice(context.Background(), "user1", stored); err != nil {
		t.Fatal(err)
	}
	if w := h.do(t, http.MethodPost, "/v1/inbox/text", "", text, key); w.Code != http.StatusAccepted {
		t.Fatalf("200th request: status = %d", w.Code)
	}
	w := h.do(t, http.MethodPost, "/v1/inbox/text", "", text, key)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("201st request: status = %d body = %s", w.Code, w.Body.String())
	}
	if p := problemOf(t, w); p["detail"] != "this device has reached today's limit" {
		t.Fatalf("detail = %q", p["detail"])
	}

	// Revoked: unknown from then on, whatever the counter says.
	if w := h.do(t, http.MethodDelete, "/v1/devices/"+created.ID, "user1", nil); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", w.Code)
	}
	w = h.do(t, http.MethodPost, "/v1/inbox/text", "", text, key)
	if w.Code != http.StatusUnauthorized || problemOf(t, w)["detail"] != "unknown device key" {
		t.Fatalf("revoked key: status = %d body = %s", w.Code, w.Body.String())
	}
	// The one request that got through is counted against the tenant, like
	// the app's own.
	if n := h.usage.Requests("user1", day) + h.usage.Requests("user1", nextDay); n < 1 {
		t.Fatalf("api_requests for the tenant = %d, want the device's request counted", n)
	}
}

// Text needs no transcription: the capture starts at transcribed with the
// text where the transcript would be, no audio and no duration, carrying the
// device as its source, and the worker is invoked for it at once.
func TestInboxTextIsACaptureAlreadyTranscribed(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Groceries", nil)
	key := h.deviceKey(t)

	w := h.do(t, http.MethodPost, "/v1/inbox/text", "", map[string]any{"text": "  buy milk and eggs ", "note_id": note.ID}, key,
		[2]string{"Idempotency-Key", "watch-note-0001"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var accepted handler.InboxAccepted
	decodeInto(t, w, &accepted)
	c := accepted.Capture
	if c.Status != string(model.StatusTranscribed) || c.HasAudio || c.DurationMS != nil || !c.Targeted {
		t.Fatalf("capture on the wire = %+v", c)
	}
	if !strings.HasPrefix(c.Source, "device:dev_") {
		t.Fatalf("source = %q, want the device", c.Source)
	}
	if c.NoteID == nil || *c.NoteID != note.ID {
		t.Fatalf("note_id = %v", c.NoteID)
	}
	stored, err := h.store.GetCapture(context.Background(), "user1", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AudioKey != "" || stored.PeaksKey != "" || stored.RawKey == "" {
		t.Fatalf("stored capture = %+v; want a raw key and no audio or peaks key", stored)
	}
	raw, err := h.objects.Get(context.Background(), stored.RawKey)
	if err != nil || string(raw) != "buy milk and eggs" {
		t.Fatalf("raw transcript = %q, %v", raw, err)
	}
	if len(h.worker.calls) != 1 || h.worker.calls[0] != "user1/"+c.ID+"/inbox-text" {
		t.Fatalf("worker calls = %v, want one hand-off for the capture", h.worker.calls)
	}

	// The same Idempotency-Key replays the same capture rather than making a
	// second one.
	again := h.do(t, http.MethodPost, "/v1/inbox/text", "", map[string]any{"text": "  buy milk and eggs ", "note_id": note.ID}, key,
		[2]string{"Idempotency-Key", "watch-note-0001"})
	if again.Header().Get(handler.HeaderIdempotencyReplayed) != "true" || !strings.Contains(again.Body.String(), c.ID) {
		t.Fatalf("replay: status = %d replayed = %q", again.Code, again.Header().Get(handler.HeaderIdempotencyReplayed))
	}
	if len(h.worker.calls) != 1 {
		t.Fatalf("the replay handed the capture over again: %v", h.worker.calls)
	}

	// The recordings list renders it without a player, and a request to
	// transcribe it again is refused rather than sent to a provider.
	w = h.do(t, http.MethodGet, "/v1/captures?note_id="+note.ID, "user1", nil)
	if !strings.Contains(w.Body.String(), `"has_audio":false`) {
		t.Fatalf("recordings list: %s", w.Body.String())
	}
	stored.Status = model.StatusAppended
	h.putCapture(t, stored)
	if w := h.do(t, http.MethodPost, "/v1/captures/"+c.ID+"/retranscribe", "user1", nil); w.Code != http.StatusConflict {
		t.Fatalf("retranscribe a text capture: status = %d body = %s", w.Code, w.Body.String())
	}
	if w := h.do(t, http.MethodGet, "/v1/captures/"+c.ID+"/download?kind=audio", "user1", nil); w.Code != http.StatusNotFound {
		t.Fatalf("download audio of a text capture: status = %d", w.Code)
	}

	// Bounds.
	for name, body := range map[string]map[string]any{
		"empty":     {"text": "   "},
		"too long":  {"text": strings.Repeat("x", service.MaxInboxTextRunes+1)},
		"bad field": {"text": "x", "title": "no"},
	} {
		if w := h.do(t, http.MethodPost, "/v1/inbox/text", "", body, key); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, w.Code)
		}
	}
	if w := h.do(t, http.MethodPost, "/v1/inbox/text", "", map[string]any{"text": "x", "note_id": "missing"}, key); w.Code != http.StatusNotFound {
		t.Errorf("unknown note: status = %d, want 404", w.Code)
	}
}

// The text route reads X-Chintan-Note-Id as the audio route does, for a
// client that can set headers but cannot build the JSON (the Devices card
// and the README promise it for both). The body's note_id wins when both
// are sent; another tenant's id is the same fixed 404 as no note at all.
func TestInboxTextTakesTheNoteIdHeader(t *testing.T) {
	h := newHarness(t)
	groceries := h.createNote(t, "user1", "Groceries", nil)
	errands := h.createNote(t, "user1", "Errands", nil)
	theirs := h.createNote(t, "user2", "Not yours", nil)
	key := h.deviceKey(t)

	post := func(t *testing.T, body map[string]any, headers ...[2]string) *httptest.ResponseRecorder {
		t.Helper()
		return h.do(t, http.MethodPost, "/v1/inbox/text", "", body, append([][2]string{key}, headers...)...)
	}
	noteOf := func(t *testing.T, w *httptest.ResponseRecorder) string {
		t.Helper()
		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		var accepted handler.InboxAccepted
		decodeInto(t, w, &accepted)
		if accepted.Capture.NoteID == nil {
			t.Fatalf("capture on the wire = %+v; want a note", accepted.Capture)
		}
		return *accepted.Capture.NoteID
	}

	if got := noteOf(t, post(t, map[string]any{"text": "buy milk"}, [2]string{handler.HeaderInboxNoteID, groceries.ID})); got != groceries.ID {
		t.Errorf("header alone: note_id = %q, want %q", got, groceries.ID)
	}
	if got := noteOf(t, post(t, map[string]any{"text": "post the letter", "note_id": errands.ID}, [2]string{handler.HeaderInboxNoteID, groceries.ID})); got != errands.ID {
		t.Errorf("body and header: note_id = %q, want the body's %q", got, errands.ID)
	}

	missing := post(t, map[string]any{"text": "x"}, [2]string{handler.HeaderInboxNoteID, "missing"})
	otherTenant := post(t, map[string]any{"text": "x"}, [2]string{handler.HeaderInboxNoteID, theirs.ID})
	if missing.Code != http.StatusNotFound || otherTenant.Code != http.StatusNotFound {
		t.Fatalf("missing = %d, another tenant's = %d; want 404 for both", missing.Code, otherTenant.Code)
	}
	if want, got := problemOf(t, missing)["detail"], problemOf(t, otherTenant)["detail"]; got != want || want != "no such resource" {
		t.Errorf("another tenant's note: detail = %q, want the missing note's %q", got, want)
	}
}

// One-shot audio: the API writes the object itself, to the capture's audio
// key, with the declared content type and the tenant's retention tags, so the
// bucket's notification and the lifecycle rules treat it as any upload.
func TestInboxAudioWritesTheObjectTheWayAClientPutWould(t *testing.T) {
	h := newHarness(t)
	if w := h.do(t, http.MethodPut, "/v1/settings", "user1", map[string]any{"retention_days": 30}); w.Code != http.StatusOK {
		t.Fatalf("settings: %d", w.Code)
	}
	note := h.createNote(t, "user1", "Dictation", nil)
	key := h.deviceKey(t)
	audio := []byte("ID3 fake mp3 bytes")

	w := h.do(t, http.MethodPost, "/v1/inbox/audio", "", audio, key,
		[2]string{"Content-Type", "audio/mpeg"},
		[2]string{handler.HeaderInboxNoteID, note.ID},
		[2]string{handler.HeaderInboxLanguage, "ml"},
		[2]string{handler.HeaderInboxDurationMS, "4200"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var accepted handler.InboxAccepted
	decodeInto(t, w, &accepted)
	c := accepted.Capture
	if c.Status != string(model.StatusUploaded) || !c.HasAudio || c.HasPeaks || c.DurationMS == nil || *c.DurationMS != 4200 {
		t.Fatalf("capture on the wire = %+v", c)
	}
	stored, err := h.store.GetCapture(context.Background(), "user1", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(stored.AudioKey, "/audio.mp3") || stored.RequestedLanguage != "ml" || stored.Source != model.DeviceSource(strings.TrimPrefix(c.Source, "device:")) {
		t.Fatalf("stored capture = %+v", stored)
	}
	body, err := h.objects.Get(context.Background(), stored.AudioKey)
	if err != nil || string(body) != string(audio) {
		t.Fatalf("audio object = %q, %v", body, err)
	}
	tags := h.objects.Tags(stored.AudioKey)
	if tags[upload.ArtifactTagKey] != upload.ArtifactCaptureAudio || tags[upload.RetentionTagKey] != "30" {
		t.Fatalf("audio object tags = %v, want the capture-audio artifact and the tenant's 30-day retention", tags)
	}
	if len(h.worker.calls) != 0 {
		t.Fatalf("the API invoked the worker for an upload the bucket notifies on: %v", h.worker.calls)
	}

	// The audio type comes from the header alone, and an unusable one is
	// refused before anything is written.
	for name, hdrs := range map[string][][2]string{
		"no content type":  {{"Content-Type", ""}},
		"not audio":        {{"Content-Type", "text/plain"}},
		"a bad duration":   {{"Content-Type", "audio/webm"}, {handler.HeaderInboxDurationMS, "soon"}},
		"a bad language":   {{"Content-Type", "audio/webm"}, {handler.HeaderInboxLanguage, "english"}},
		"an archived note": {{"Content-Type", "audio/webm"}, {handler.HeaderInboxNoteID, archivedNote(t, h)}},
	} {
		hdrs = append([][2]string{key}, hdrs...)
		w := h.do(t, http.MethodPost, "/v1/inbox/audio", "", audio, hdrs...)
		if w.Code != http.StatusBadRequest && w.Code != http.StatusConflict {
			t.Errorf("%s: status = %d, want 400 or 409", name, w.Code)
		}
	}
	if w := h.do(t, http.MethodPost, "/v1/inbox/audio", "", []byte{}, key, [2]string{"Content-Type", "audio/webm"}); w.Code != http.StatusBadRequest {
		t.Errorf("empty body: status = %d, want 400", w.Code)
	}
	w = h.do(t, http.MethodPost, "/v1/inbox/audio", "", make([]byte, service.MaxInboxAudioBytes+1), key, [2]string{"Content-Type", "audio/webm"})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("one byte over the cap: status = %d, want 413", w.Code)
	}
	if w := h.do(t, http.MethodPost, "/v1/inbox/audio", "", make([]byte, service.MaxInboxAudioBytes), key, [2]string{"Content-Type", "audio/wav"}); w.Code != http.StatusAccepted {
		t.Errorf("exactly the cap: status = %d, want 202", w.Code)
	}
}

// archivedNote makes and archives a note, returning its id.
func archivedNote(t *testing.T, h *harness) string {
	t.Helper()
	n := h.createNote(t, "user1", "Old", nil)
	if w := h.do(t, http.MethodDelete, "/v1/notes/"+n.ID, "user1", nil); w.Code != http.StatusNoContent {
		t.Fatalf("archive: %d", w.Code)
	}
	return n.ID
}

// The two-step route is POST /v1/captures with the device as the source: the
// same 201, the same presigned PUTs, and the language for this recording.
func TestInboxCapturesIsBeginCaptureForADevice(t *testing.T) {
	h := newHarness(t)
	h.captures.WithUploads(taggingPresigner{})
	key := h.deviceKey(t)

	w := h.do(t, http.MethodPost, "/v1/inbox/captures", "", map[string]any{
		"content_type": "audio/mp4", "size_bytes": 2048, "duration_ms": 3000, "language": "auto",
	}, key)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var created handler.CaptureCreated
	decodeInto(t, w, &created)
	if created.Upload.URL == "" || created.Upload.Headers[upload.TaggingHeader] == "" || created.PeaksUpload == nil {
		t.Fatalf("created = %+v", created)
	}
	if !strings.HasPrefix(created.Capture.Source, "device:") || created.Capture.Targeted {
		t.Fatalf("capture = %+v", created.Capture)
	}
	stored, err := h.store.GetCapture(context.Background(), "user1", created.Capture.ID)
	if err != nil || stored.RequestedLanguage != model.LanguageAuto {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	// The app's own captures still say app.
	w = h.do(t, http.MethodPost, "/v1/captures", "user1", map[string]any{"content_type": "audio/webm"})
	decodeInto(t, w, &created)
	if created.Capture.Source != "app" || !created.Capture.HasAudio {
		t.Fatalf("app capture = %+v", created.Capture)
	}
	// And the inbox does not answer a session.
	if w := h.do(t, http.MethodPost, "/v1/inbox/captures", "user1", map[string]any{"content_type": "audio/webm"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("a session on the inbox: status = %d", w.Code)
	}
}

// The key is one credential in three spellings: Bearer, bare (a webhook form
// with header key/value pairs and no scheme — the Pebble Index ring's) and
// X-Device-Key. Anything else in either header is the one fixed 401.
func TestInboxTakesTheKeyBareAndInXDeviceKey(t *testing.T) {
	h := newHarness(t)
	created := h.createDevice(t, "user1", "Ring")
	text := map[string]any{"text": "buy milk"}

	for name, header := range map[string][2]string{
		"bearer":       {"Authorization", "Bearer " + created.Key},
		"bare":         {"Authorization", created.Key},
		"bare, spaced": {"Authorization", "  " + created.Key + " "},
		"x-device-key": {handler.HeaderDeviceKey, created.Key},
	} {
		if w := h.do(t, http.MethodPost, "/v1/inbox/text", "", text, header); w.Code != http.StatusAccepted {
			t.Errorf("%s: status = %d body = %s", name, w.Code, w.Body.String())
		}
	}
	for name, header := range map[string][2]string{
		"bare, not a key":         {"Authorization", "hello"},
		"bare, a session jwt":     {"Authorization", "eyJhbGciOiJSUzI1NiJ9.e30.sig"},
		"x-device-key, not a key": {handler.HeaderDeviceKey, "hello"},
	} {
		w := h.do(t, http.MethodPost, "/v1/inbox/text", "", text, header)
		if w.Code != http.StatusUnauthorized || problemOf(t, w)["detail"] != "unknown device key" {
			t.Errorf("%s: status = %d body = %s", name, w.Code, w.Body.String())
		}
	}
	// X-Device-Key decides when both are sent, so a client whose
	// Authorization header is spoken for is not refused for it.
	w := h.do(t, http.MethodPost, "/v1/inbox/text", "", text,
		[2]string{"Authorization", "Basic c29tZWJvZHk6ZWxzZQ=="}, [2]string{handler.HeaderDeviceKey, created.Key})
	if w.Code != http.StatusAccepted {
		t.Errorf("X-Device-Key beside a foreign Authorization: status = %d body = %s", w.Code, w.Body.String())
	}
}

// ringForm builds the multipart/form-data body the Pebble Index ring's
// webhook posts: client and recordedAt always, an audio part when audio is
// not nil (under audioType, or with no Content-Type when that is ""), and a
// transcription part when one is given.
func ringForm(t *testing.T, audio []byte, audioType, transcription string) (body []byte, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(mw.WriteField("client", "ring"))
	must(mw.WriteField("recordedAt", "1790000000000"))
	if audio != nil {
		hdr := textproto.MIMEHeader{"Content-Disposition": {`form-data; name="audio"; filename="memo.m4a"`}}
		if audioType != "" {
			hdr.Set("Content-Type", audioType)
		}
		part, err := mw.CreatePart(hdr)
		must(err)
		_, err = part.Write(audio)
		must(err)
	}
	if transcription != "" {
		must(mw.WriteField("transcription", transcription))
	}
	must(mw.Close())
	return buf.Bytes(), mw.FormDataContentType()
}

// The ring's webhook posts a form. Its audio part is ingested as a raw body
// is — the part's type on the object, the tenant's retention tags, the
// bucket's notification and no worker call; its transcription alone takes
// the text route; both together, the audio wins.
func TestInboxAudioTakesTheRingsForm(t *testing.T) {
	h := newHarness(t)
	if w := h.do(t, http.MethodPut, "/v1/settings", "user1", map[string]any{"retention_days": 30}); w.Code != http.StatusOK {
		t.Fatalf("settings: %d", w.Code)
	}
	note := h.createNote(t, "user1", "Dictation", nil)
	key := h.deviceKey(t)
	audio := []byte("ftyp fake m4a bytes")
	post := func(t *testing.T, body []byte, contentType string, headers ...[2]string) (accepted handler.InboxAccepted, stored model.CaptureIndex) {
		t.Helper()
		headers = append([][2]string{key, {"Content-Type", contentType}}, headers...)
		w := h.do(t, http.MethodPost, "/v1/inbox/audio", "", body, headers...)
		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		decodeInto(t, w, &accepted)
		var err error
		if stored, err = h.store.GetCapture(context.Background(), "user1", accepted.Capture.ID); err != nil {
			t.Fatal(err)
		}
		return accepted, stored
	}

	t.Run("audio", func(t *testing.T) {
		body, contentType := ringForm(t, audio, "audio/mp4", "")
		accepted, stored := post(t, body, contentType,
			[2]string{handler.HeaderInboxNoteID, note.ID}, [2]string{handler.HeaderInboxDurationMS, "4200"})
		c := accepted.Capture
		if c.Status != string(model.StatusUploaded) || !c.HasAudio || c.HasPeaks || c.DurationMS == nil || *c.DurationMS != 4200 || c.NoteID == nil || *c.NoteID != note.ID {
			t.Fatalf("capture on the wire = %+v", c)
		}
		if !strings.HasSuffix(stored.AudioKey, "/audio.m4a") || stored.RawKey != "" || stored.Source != model.DeviceSource(strings.TrimPrefix(c.Source, "device:")) {
			t.Fatalf("stored capture = %+v; want audio/mp4 on the key and the device as source", stored)
		}
		object, err := h.objects.Get(context.Background(), stored.AudioKey)
		if err != nil || string(object) != string(audio) {
			t.Fatalf("audio object = %q, %v", object, err)
		}
		tags := h.objects.Tags(stored.AudioKey)
		if tags[upload.ArtifactTagKey] != upload.ArtifactCaptureAudio || tags[upload.RetentionTagKey] != "30" {
			t.Fatalf("audio object tags = %v", tags)
		}
		if len(h.worker.calls) != 0 {
			t.Fatalf("the API invoked the worker for an upload the bucket notifies on: %v", h.worker.calls)
		}
	})
	t.Run("audio with its own type", func(t *testing.T) {
		body, contentType := ringForm(t, audio, "audio/mpeg", "")
		if _, stored := post(t, body, contentType); !strings.HasSuffix(stored.AudioKey, "/audio.mp3") {
			t.Fatalf("audio key = %q, want the part's audio/mpeg", stored.AudioKey)
		}
	})
	t.Run("transcription only", func(t *testing.T) {
		before := len(h.worker.calls)
		body, contentType := ringForm(t, nil, "", "  remember the gutter  ")
		accepted, stored := post(t, body, contentType, [2]string{handler.HeaderInboxNoteID, note.ID})
		c := accepted.Capture
		if c.Status != string(model.StatusTranscribed) || c.HasAudio || c.DurationMS != nil || c.NoteID == nil || *c.NoteID != note.ID {
			t.Fatalf("capture on the wire = %+v", c)
		}
		if stored.AudioKey != "" || stored.RawKey == "" {
			t.Fatalf("stored capture = %+v; want a raw key and no audio key", stored)
		}
		raw, err := h.objects.Get(context.Background(), stored.RawKey)
		if err != nil || string(raw) != "remember the gutter" {
			t.Fatalf("raw transcript = %q, %v", raw, err)
		}
		if len(h.worker.calls) != before+1 {
			t.Fatalf("worker calls = %v, want one hand-off for the text", h.worker.calls)
		}
	})
	t.Run("transcription with invalid UTF-8", func(t *testing.T) {
		body, contentType := ringForm(t, nil, "", "caf\xff\xfe au lait")
		_, stored := post(t, body, contentType)
		raw, err := h.objects.Get(context.Background(), stored.RawKey)
		if err != nil || string(raw) != "caf\uFFFD au lait" {
			t.Fatalf("raw transcript = %q, %v; want the invalid run replaced with U+FFFD", raw, err)
		}
	})
	t.Run("both: the audio wins", func(t *testing.T) {
		before := len(h.worker.calls)
		body, contentType := ringForm(t, audio, "audio/mp4", "the ring's own words")
		accepted, stored := post(t, body, contentType)
		if !accepted.Capture.HasAudio || stored.RawKey != "" || len(h.worker.calls) != before {
			t.Fatalf("capture = %+v stored = %+v worker = %v; want the audio filed and the transcription dropped", accepted.Capture, stored, h.worker.calls)
		}
		object, err := h.objects.Get(context.Background(), stored.AudioKey)
		if err != nil || string(object) != string(audio) {
			t.Fatalf("audio object = %q, %v", object, err)
		}
	})

	emptyForm, emptyType := ringForm(t, nil, "", "")
	emptyAudio, emptyAudioType := ringForm(t, []byte{}, "audio/mp4", "")
	untyped, untypedType := ringForm(t, audio, "", "")
	notAudio, notAudioType := ringForm(t, audio, "text/plain", "")
	longText, longTextType := ringForm(t, nil, "", strings.Repeat("x", service.MaxInboxTextRunes+1))
	overCap, overCapType := ringForm(t, make([]byte, service.MaxInboxAudioBytes+1), "audio/mp4", "")
	for name, tc := range map[string]struct {
		body        []byte
		contentType string
		status      int
		detail      string
	}{
		"no usable part":   {emptyForm, emptyType, http.StatusBadRequest, "the form has no audio or transcription part"},
		"an empty audio":   {emptyAudio, emptyAudioType, http.StatusBadRequest, "the form has no audio or transcription part"},
		"untyped audio":    {untyped, untypedType, http.StatusBadRequest, "Content-Type is required: the recording's audio type"},
		"not audio":        {notAudio, notAudioType, http.StatusBadRequest, "content_type must be one of audio/webm, audio/ogg, audio/mp4, audio/m4a, audio/mpeg, audio/wav, audio/x-wav"},
		"too long a text":  {longText, longTextType, http.StatusBadRequest, ""},
		"no boundary seen": {[]byte("not a form"), "multipart/form-data; boundary=x", http.StatusBadRequest, "the form has no audio or transcription part"},
		"one byte over":    {overCap, overCapType, http.StatusRequestEntityTooLarge, "the recording exceeds 4194304 bytes"},
		"multipart/mixed":  {emptyForm, strings.Replace(emptyType, "form-data", "mixed", 1), http.StatusBadRequest, ""},
		"boundary missing": {emptyForm, "multipart/form-data", http.StatusBadRequest, "the request body could not be read"},
	} {
		w := h.do(t, http.MethodPost, "/v1/inbox/audio", "", tc.body, key, [2]string{"Content-Type", tc.contentType})
		if w.Code != tc.status {
			t.Errorf("%s: status = %d, want %d; body = %s", name, w.Code, tc.status, w.Body.String())
			continue
		}
		if tc.detail != "" && problemOf(t, w)["detail"] != tc.detail {
			t.Errorf("%s: detail = %q, want %q", name, problemOf(t, w)["detail"], tc.detail)
		}
	}
}
