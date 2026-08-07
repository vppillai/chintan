package httperr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vppillai/chintan/backend/internal/repository"
)

func TestWriteJSONMapsWrappedErrNotFoundTo404(t *testing.T) {
	w := httptest.NewRecorder()
	err := fmt.Errorf("get note: %w", repository.ErrNotFound)

	WriteJSON(w, err, http.StatusInternalServerError)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}

	var resp ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
}
