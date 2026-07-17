package anthropic

import (
	"encoding/json"
	"net/http"
)

// APIError is the Anthropic error body shape.
type APIError struct {
	Type  string      `json:"type"` // always "error"
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// WriteError writes an Anthropic-shaped error response.
func WriteError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(APIError{Type: "error", Error: ErrorDetail{Type: errType, Message: msg}})
}

// ErrorTypeForStatus maps an upstream HTTP status to the Anthropic error
// type Claude Code understands (and bases its retry behavior on).
func ErrorTypeForStatus(status int) string {
	switch status {
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 413:
		return "request_too_large"
	case 429:
		return "rate_limit_error"
	case 503, 529:
		return "overloaded_error"
	default:
		return "api_error"
	}
}
