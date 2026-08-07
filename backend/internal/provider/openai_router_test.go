package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vppillai/chintan/backend/internal/routing"
)

func TestParseRouteDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		wantAction RouteAction
		wantNoteID string
		wantConf   float64
		wantErr    bool
	}{
		{
			name:       "plain json append",
			raw:        `{"action":"append","note_id":"n1","confidence":0.9,"content":"the gutter leaks"}`,
			wantAction: RouteAppend,
			wantNoteID: "n1",
			wantConf:   0.9,
		},
		{
			name:       "fenced json",
			raw:        "```json\n{\"action\":\"new\",\"title\":\"Dentist\",\"confidence\":1,\"content\":\"book dentist\"}\n```",
			wantAction: RouteNew,
			wantConf:   1,
		},
		{
			name:       "surrounding prose",
			raw:        `Sure! Here is the decision: {"action":"new","title":"Dentist","confidence":0.5,"content":"x"} Hope that helps.`,
			wantAction: RouteNew,
			wantConf:   0.5,
		},
		{
			name:       "confidence clamped",
			raw:        `{"action":"new","title":"T","confidence":7,"content":"x"}`,
			wantAction: RouteNew,
			wantConf:   1,
		},
		{name: "append without note id", raw: `{"action":"append","confidence":1,"content":"x"}`, wantErr: true},
		{name: "unknown action", raw: `{"action":"delete","content":"x"}`, wantErr: true},
		{name: "no json at all", raw: `I could not decide.`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseRouteDecision(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRouteDecision: %v", err)
			}
			if got.Action != tt.wantAction {
				t.Errorf("action = %q, want %q", got.Action, tt.wantAction)
			}
			if got.NoteID != tt.wantNoteID {
				t.Errorf("note_id = %q, want %q", got.NoteID, tt.wantNoteID)
			}
			if got.Confidence != tt.wantConf {
				t.Errorf("confidence = %v, want %v", got.Confidence, tt.wantConf)
			}
		})
	}
}

// routerServer replies with a fixed assistant message.
func routerServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": content}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRouteRejectsUnknownNoteID(t *testing.T) {
	t.Parallel()

	srv := routerServer(t, `{"action":"append","note_id":"does-not-exist","confidence":1,"content":"x"}`)
	llm, err := NewOpenAICleanup("k", srv.URL, "m", srv.Client())
	if err != nil {
		t.Fatalf("NewOpenAICleanup: %v", err)
	}

	_, err = llm.Route(context.Background(), "some words", []routing.Candidate{{NoteID: "n1", Title: "Roof"}})
	if err == nil {
		t.Fatal("expected error for a note id that was not offered")
	}
}

func TestRouteAcceptsOfferedNoteID(t *testing.T) {
	t.Parallel()

	srv := routerServer(t, `{"action":"append","note_id":"n1","confidence":0.88,"content":"the gutter leaks"}`)
	llm, err := NewOpenAICleanup("k", srv.URL, "m", srv.Client())
	if err != nil {
		t.Fatalf("NewOpenAICleanup: %v", err)
	}

	decision, err := llm.Route(context.Background(), "add to roof note the gutter leaks", []routing.Candidate{{NoteID: "n1", Title: "Roof"}})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.NoteID != "n1" || decision.Action != RouteAppend {
		t.Fatalf("decision = %+v", decision)
	}
	if decision.Content != "the gutter leaks" {
		t.Errorf("content = %q, want the instruction stripped", decision.Content)
	}
}

func TestRouteFallsBackToFullTranscriptWhenContentMissing(t *testing.T) {
	t.Parallel()

	srv := routerServer(t, `{"action":"new","title":"Dentist","confidence":1}`)
	llm, err := NewOpenAICleanup("k", srv.URL, "m", srv.Client())
	if err != nil {
		t.Fatalf("NewOpenAICleanup: %v", err)
	}

	decision, err := llm.Route(context.Background(), "book the dentist", nil)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.Content != "book the dentist" {
		t.Errorf("content = %q, want the original transcript", decision.Content)
	}
}
