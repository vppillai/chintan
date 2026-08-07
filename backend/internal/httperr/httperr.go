package httperr

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/vppillai/chintan/backend/internal/repository"
)

type ErrorResponse struct {
	Error string `json:"error"`
}

// WriteJSON writes a JSON error response with appropriate status code
func WriteJSON(w http.ResponseWriter, err error, defaultStatus int) {
	status := defaultStatus

	if errors.Is(err, repository.ErrNotFound) {
		status = http.StatusNotFound
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	
	response := ErrorResponse{Error: err.Error()}
	json.NewEncoder(w).Encode(response)
}

// BadRequest writes a 400 Bad Request error
func BadRequest(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	
	response := ErrorResponse{Error: message}
	json.NewEncoder(w).Encode(response)
}

// Unauthorized writes a 401 Unauthorized error
func Unauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	
	response := ErrorResponse{Error: message}
	json.NewEncoder(w).Encode(response)
}

// InternalServerError writes a 500 Internal Server Error
func InternalServerError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	
	response := ErrorResponse{Error: "internal server error"}
	json.NewEncoder(w).Encode(response)
}