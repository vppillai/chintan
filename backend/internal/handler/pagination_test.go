package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vppillai/chintan/backend/internal/middleware"
)

// The list endpoint keeps returning a bare array — Phase 6 owns the envelope —
// but it must no longer stop at whatever fits in one page. The page size and
// the next-page token are reachable from the query string and the response
// headers respectively.
func TestNotesListIsCursorPaginated(t *testing.T) {
	router := isolationRouter(t)
	const total = 7
	for i := 0; i < total; i++ {
		createNoteAs(t, router, "user1", fmt.Sprintf("Note %d", i))
	}

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("pagination did not terminate")
		}
		path := "/v1/notes?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("list: status=%d body=%s", w.Code, w.Body.String())
		}

		var notes []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &notes); err != nil {
			t.Fatalf("response is not a JSON array: %v (body=%s)", err, w.Body.String())
		}
		if len(notes) > 2 {
			t.Fatalf("page has %d notes, want at most the requested 2", len(notes))
		}
		for _, n := range notes {
			if seen[n.ID] {
				t.Fatalf("note %s appeared on two pages", n.ID)
			}
			seen[n.ID] = true
		}

		cursor = w.Header().Get("X-Next-Cursor")
		if cursor == "" {
			break
		}
	}

	if len(seen) != total {
		t.Fatalf("paged through %d notes, want %d", len(seen), total)
	}
}
