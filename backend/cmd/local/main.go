// Command local is the development path without AWS: the API and the worker
// in one process on a loopback address, over the fake table and the in-memory
// bucket, with a scripted speech-to-text, the replay model with a fixed
// fallback, and one static bearer token for one tenant. What it can and
// cannot do is docs/design/local-dev.md; scripts/dev/local.sh starts it and
// the frontend together.
//
// It is never deployable: it refuses to start unless CHINTAN_LOCAL=1 and the
// listen address is loopback, scripts/build-lambda.sh packages cmd/api and
// cmd/worker only (TestProductionBinaryDoesNotLinkTestDoubles holds both the
// list and the import graphs), and the test doubles it wires are the ones
// that guard keeps out of those binaries.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vppillai/chintan/backend/internal/ask"
	"github.com/vppillai/chintan/backend/internal/auth"
	"github.com/vppillai/chintan/backend/internal/boot"
	"github.com/vppillai/chintan/backend/internal/breaker"
	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/pipeline"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/dynamofake"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/routing"
	"github.com/vppillai/chintan/backend/internal/service"
	"github.com/vppillai/chintan/backend/internal/upload"
)

// config is everything the process reads from the environment.
type config struct {
	// addr is the loopback address to listen on (CHINTAN_LOCAL_ADDR).
	addr string
	// token is the one bearer the verifier accepts (CHINTAN_LOCAL_TOKEN).
	token string
	// origin is the frontend dev server's origin (ALLOWED_ORIGIN), the one
	// CORS allows; the handler refuses a wildcard.
	origin string
	// transcript is what the speech-to-text answers for an upload that
	// carried no ?text= (CHINTAN_LOCAL_TRANSCRIPT).
	transcript string
	// recordings is the replay directory (CHINTAN_LOCAL_RECORDINGS); empty
	// means the fakes answer everything.
	recordings string
	// model is what the replay is keyed on and the breaker prices (LLM_MODEL).
	model string
}

const (
	defaultToken      = "local-dev-token"
	defaultTranscript = "Remind me to call the plumber about the kitchen tap on Tuesday"
	// tenant is the one identity every accepted request runs as.
	tenant = "local"
)

// configFromEnv refuses anything but a deliberate local run: CHINTAN_LOCAL=1
// and a loopback listen address. A static bearer token on a reachable
// interface would be an open instance.
func configFromEnv() (config, error) {
	if os.Getenv("CHINTAN_LOCAL") != "1" {
		return config{}, errors.New("cmd/local runs only with CHINTAN_LOCAL=1; it is the development path without AWS, never a deployment")
	}
	c := config{
		addr:       boot.EnvOr("CHINTAN_LOCAL_ADDR", "127.0.0.1:8787"),
		token:      boot.EnvOr("CHINTAN_LOCAL_TOKEN", defaultToken),
		origin:     boot.EnvOr("ALLOWED_ORIGIN", "http://localhost:5173"),
		transcript: boot.EnvOr("CHINTAN_LOCAL_TRANSCRIPT", defaultTranscript),
		recordings: strings.TrimSpace(os.Getenv("CHINTAN_LOCAL_RECORDINGS")),
		model:      boot.EnvOr("LLM_MODEL", "MiniMax-M3"),
	}
	if err := requireLoopback(c.addr); err != nil {
		return config{}, err
	}
	return c, nil
}

// requireLoopback accepts 127.0.0.0/8, ::1 and "localhost" only.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("CHINTAN_LOCAL_ADDR %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("CHINTAN_LOCAL_ADDR %q is not a loopback address; the static token must not be reachable from the network", addr)
}

// verifier accepts exactly one token for exactly one tenant.
type verifier struct{ token string }

func (v verifier) Verify(_ context.Context, raw string) (auth.Identity, error) {
	if subtle.ConstantTimeCompare([]byte(raw), []byte(v.token)) != 1 {
		return auth.Identity{}, auth.ErrUnauthenticated
	}
	return auth.Identity{UserID: tenant, TenantID: tenant}, nil
}

// objects is the in-memory bucket whose presigned URLs point at this process:
// a PUT or GET on /objects/<key> is the "presigned-equivalent" on loopback.
type objects struct {
	*memory.Objects
	base string
}

func (o *objects) objectURL(key string) string { return o.base + "/objects/" + key }

func (o *objects) PresignPut(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	return o.objectURL(key), nil
}

func (o *objects) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	return o.objectURL(key), nil
}

// presigner is the upload.Presigner over the same URLs, carrying the tags
// the way the S3 one does, so the client's PUT is the one it sends to S3.
type presigner struct{ objects *objects }

func (p presigner) PresignPut(_ context.Context, key, contentType string, tags map[string]string, maxBytes int64, ttl time.Duration) (upload.Presigned, error) {
	headers := map[string]string{"Content-Type": contentType}
	if tagging := upload.EncodeTags(tags); tagging != "" {
		headers[upload.TaggingHeader] = tagging
	}
	return upload.Presigned{URL: p.objects.objectURL(key), ExpiresAt: time.Now().UTC().Add(ttl), MaxBytes: maxBytes, Headers: headers}, nil
}

// stt answers the transcript an upload asked for (?text= on its PUT), else
// the fixed one, through the fake so the shape is the fake's.
type stt struct {
	objects    *objects
	transcript string
	mu         sync.Mutex
	texts      map[string]string
}

func (s *stt) set(key, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.texts[key] = text
}

func (s *stt) Transcribe(ctx context.Context, in provider.Audio) (provider.Transcription, error) {
	key := strings.TrimPrefix(in.URL, s.objects.base+"/objects/")
	s.mu.Lock()
	text, ok := s.texts[key]
	s.mu.Unlock()
	if !ok {
		text = s.transcript
	}
	return (&fake.STT{Response: text, Duration: 3}).Transcribe(ctx, in)
}

// llm answers from the recordings when the prompt was recorded and from the
// fakes otherwise, so a fixture transcript shows the model's real reply and
// anything else still files.
type llm struct {
	replay   *provider.OpenAICleanup
	fallback *fake.LLM
	router   *fake.Router
}

func (l *llm) miss(err error) bool { return l.replay == nil || errors.Is(err, provider.ErrNoRecording) }

func (l *llm) Cleanup(ctx context.Context, raw, language string) (provider.Cleaned, error) {
	if l.replay != nil {
		if out, err := l.replay.Cleanup(ctx, raw, language); !l.miss(err) {
			return out, err
		}
	}
	return l.fallback.Cleanup(ctx, raw, language)
}

func (l *llm) CleanNote(ctx context.Context, mode model.NoteCleanMode, body, language, title string) (provider.Cleaned, error) {
	if l.replay != nil {
		if out, err := l.replay.CleanNote(ctx, mode, body, language, title); !l.miss(err) {
			return out, err
		}
	}
	return l.fallback.CleanNote(ctx, mode, body, language, title)
}

func (l *llm) Items(ctx context.Context, transcript, listTitle, language string) (provider.ChecklistItems, error) {
	if l.replay != nil {
		if out, err := l.replay.Items(ctx, transcript, listTitle, language); !l.miss(err) {
			return out, err
		}
	}
	return l.fallback.Items(ctx, transcript, listTitle, language)
}

func (l *llm) Ask(ctx context.Context, q ask.Prompt) (provider.Answer, error) {
	if l.replay != nil {
		if out, err := l.replay.Ask(ctx, q); !l.miss(err) {
			return out, err
		}
	}
	return l.fallback.Ask(ctx, q)
}

func (l *llm) Route(ctx context.Context, transcript string, candidates []routing.Candidate, language string) (provider.RouteDecision, error) {
	if l.replay != nil {
		if out, err := l.replay.Route(ctx, transcript, candidates, language); !l.miss(err) {
			return out, err
		}
	}
	return l.router.Route(ctx, transcript, candidates, language)
}

// invoker is the worker hand-off without Lambda: the payload the API would
// send, run on a goroutine, as an Event invocation is. No retries and no
// dead-letter queue; a failed run is one log line.
type invoker struct {
	handle func(context.Context, json.RawMessage) error
}

func (i invoker) send(inv pipeline.Invocation) error {
	raw, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	go func() {
		if err := i.handle(context.Background(), raw); err != nil {
			slog.Error("worker invocation failed", slog.String("error", err.Error()))
		}
	}()
	return nil
}

func (i invoker) InvokeCapture(_ context.Context, tenantID, captureID, reason string) error {
	return i.send(pipeline.Invocation{TenantID: tenantID, CaptureID: captureID, Reason: reason})
}

func (i invoker) InvokeCleanNote(_ context.Context, tenantID, noteID string, mode model.NoteCleanMode, requestedAt string) error {
	return i.send(pipeline.Invocation{Task: pipeline.TaskCleanNote, TenantID: tenantID, NoteID: noteID, Mode: string(mode), RequestedAt: requestedAt})
}

func (i invoker) InvokeAsk(_ context.Context, tenantID, askID string) error {
	return i.send(pipeline.Invocation{Task: pipeline.TaskAsk, TenantID: tenantID, AskID: askID})
}

func (i invoker) InvokeRegenerateNote(_ context.Context, tenantID, noteID string, captureIDs []string) error {
	return i.send(pipeline.Invocation{Task: pipeline.TaskRegenerateNote, TenantID: tenantID, NoteID: noteID, CaptureIDs: captureIDs})
}

// counter is the instance spend row over a map.
type counter struct {
	mu     sync.Mutex
	totals map[string]int64
}

func (c *counter) Add(_ context.Context, day string, delta int64) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totals[day] += delta
	return c.totals[day], nil
}

// build wires the API, the worker and the bucket endpoint over the fakes
// into one handler. base is this process's own origin, which the presigned
// URLs point at.
func build(c config, base string) (http.Handler, error) {
	store := dynamofake.NewStore()
	objs := &objects{Objects: memory.NewObjects(), base: base}
	usageRows := memory.NewUsage()
	speech := &stt{objects: objs, transcript: c.transcript, texts: map[string]string{}}
	brain := &llm{fallback: &fake.LLM{}, router: &fake.Router{}}
	if c.recordings != "" {
		replay, err := provider.NewReplayLLM(c.recordings, c.model)
		if err != nil {
			return nil, err
		}
		brain.replay = replay
	}

	notes := service.NewNotesService(store, objs)
	// The breaker is required by the pipeline and prices every fake call
	// against a cap of zero, which counts and refuses nothing.
	spend := breaker.New(&counter{totals: map[string]int64{}}, meter.DefaultPrices, 0, breaker.WithUsage(usageRows))
	var work invoker
	p, err := pipeline.New(pipeline.Config{
		Store: store, Objects: objs, STT: speech, LLM: brain, Router: brain, Notes: notes, Breaker: spend,
		CleanInvoker: &work,
		STTProvider:  "groq", STTModel: "whisper-large-v3-turbo", LLMProvider: "openai", LLMModel: c.model,
	})
	if err != nil {
		return nil, err
	}
	work.handle = pipeline.NewWorker(p).Handle

	// The same wiring as cmd/api's build; a service added there and not here
	// answers 503 on its route, which the boot test's round trip catches for
	// the capture path.
	notesService := service.NewNotesService(store, objs).WithInvoker(work)
	settings := service.NewSettingsService(store)
	captures := service.NewCaptureService(store, objs).WithUploads(presigner{objs}).WithInvoker(work).WithNoteCreator(notesService)
	api := handler.New(handler.Deps{
		Notes:         notesService,
		Settings:      settings,
		Captures:      captures,
		Search:        service.NewSearchService(notesService),
		Tags:          service.NewTagsService(notesService),
		Export:        service.NewExportService(notesService, captures, settings, objs),
		Readiness:     service.NewReadinessService(store, objs),
		Spend:         service.NewSpendGate(&counter{totals: map[string]int64{}}, 0),
		Usage:         usageRows,
		Requests:      usageRows,
		Storage:       service.NewStorageService(store),
		Ask:           service.NewAskService(store, work),
		Devices:       service.NewDeviceService(store),
		Push:          service.NewPushService(store),
		Store:         store,
		Verifier:      verifier{token: c.token},
		AllowedOrigin: c.origin,
	})

	mux := http.NewServeMux()
	mux.Handle("/v1/", api)
	mux.Handle("/objects/", cors(c.origin, bucket{objects: objs, stt: speech, work: work}))
	// The hosted UI's three routes, so the app's own sign-in works unchanged:
	// VITE_COGNITO_DOMAIN points here, the code is a constant and the token
	// set carries the one bearer the verifier accepts.
	mux.HandleFunc("GET /oauth2/authorize", func(w http.ResponseWriter, r *http.Request) {
		back, err := url.Parse(r.URL.Query().Get("redirect_uri"))
		if err != nil || back.Host == "" {
			http.Error(w, "redirect_uri is required", http.StatusBadRequest)
			return
		}
		q := back.Query()
		q.Set("code", "local")
		q.Set("state", r.URL.Query().Get("state"))
		back.RawQuery = q.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /oauth2/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token": c.token, "access_token": c.token, "refresh_token": c.token,
			"expires_in": 86400, "token_type": "Bearer",
		})
	})
	mux.HandleFunc("GET /logout", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Query().Get("logout_uri"), http.StatusFound)
	})
	return mux, nil
}

// bucket serves the presigned-equivalent PUT and GET on /objects/<key>. A PUT
// of a recording is the bucket's ObjectCreated notification: the same S3
// event shape the worker parses, so the pipeline runs as it does deployed.
type bucket struct {
	objects *objects
	stt     *stt
	work    invoker
}

// maxObjectBytes bounds a PUT the way the worker bounds a recording.
const maxObjectBytes = service.MaxCaptureBytes + 1

func (b bucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/objects/")
	switch r.Method {
	case http.MethodGet:
		body, err := b.objects.Get(r.Context(), key)
		if errors.Is(err, repository.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentTypeFor(key))
		_, _ = w.Write(body)
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, maxObjectBytes))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tags, _ := url.ParseQuery(r.Header.Get(upload.TaggingHeader))
		flat := map[string]string{}
		for k := range tags {
			flat[k] = tags.Get(k)
		}
		if err := b.objects.PutTagged(r.Context(), key, body, r.Header.Get("Content-Type"), flat); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if text := strings.TrimSpace(r.URL.Query().Get("text")); text != "" {
			b.stt.set(key, text)
		}
		w.WriteHeader(http.StatusOK)
		b.notify(key, len(body))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// notify hands the worker the ObjectCreated event for key; one that is not a
// recording (the peaks, a note body) is addressed to no capture and dropped
// there, as the bucket's filter would not deliver it.
func (b bucket) notify(key string, size int) {
	raw, err := json.Marshal(map[string]any{"Records": []map[string]any{{
		"eventSource": "aws:s3",
		"eventTime":   time.Now().UTC().Format(time.RFC3339Nano),
		"s3":          map[string]any{"object": map[string]any{"key": key, "size": size}},
	}}})
	if err != nil {
		return
	}
	go func() {
		if err := b.work.handle(context.Background(), raw); err != nil {
			slog.Error("worker run failed", slog.String("error", err.Error()))
		}
	}()
}

func contentTypeFor(key string) string {
	switch filepath.Ext(key) {
	case ".json":
		return "application/json"
	case ".webm":
		return "audio/webm"
	case ".mp4", ".m4a":
		return "audio/mp4"
	case ".ogg":
		return "audio/ogg"
	}
	return "application/octet-stream"
}

// cors is the bucket's CORS rule: the app's origin only, and the headers a
// presigned PUT carries. The API's own middleware covers /v1.
func cors(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Origin"); got != "" && got == origin {
			w.Header().Set("Access-Control-Allow-Origin", got)
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, "+upload.TaggingHeader)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	obs.Setup(boot.LogLevel())
	c, err := configFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", c.addr)
	if err != nil {
		log.Fatal(err)
	}
	h, err := build(c, "http://"+ln.Addr().String())
	if err != nil {
		log.Fatal(err)
	}
	slog.Info("chintan local is listening",
		slog.String("addr", ln.Addr().String()),
		slog.String("origin_allowed", c.origin),
		slog.Bool("replay", c.recordings != ""))
	// ponytail: no graceful shutdown; everything is in memory and Ctrl-C loses it by design.
	log.Fatal(http.Serve(ln, h))
}
