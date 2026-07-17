package anthropic

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// SSEWriter emits Anthropic-grammar SSE events, flushing after each one.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	started bool
}

func NewSSEWriter(w http.ResponseWriter) *SSEWriter {
	flusher, _ := w.(http.Flusher)
	return &SSEWriter{w: w, flusher: flusher}
}

// Event writes one `event:`/`data:` pair. The first call sets the SSE
// response headers.
func (s *SSEWriter) Event(name string, payload any) error {
	if !s.started {
		h := s.w.Header()
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		s.started = true
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return err
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return nil
}

func (s *SSEWriter) Ping() error {
	return s.Event("ping", map[string]string{"type": "ping"})
}

// ErrorEvent emits a mid-stream error event (mirrors Anthropic's own
// behavior; Claude Code retries on it).
func (s *SSEWriter) ErrorEvent(errType, msg string) error {
	return s.Event("error", APIError{Type: "error", Error: ErrorDetail{Type: errType, Message: msg}})
}
