package eval

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRelayAuthPathHeadersAndContainerURL(t *testing.T) {
	seen := make(chan *http.Request, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(r.Context())
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	relay, err := StartRelay(context.Background(), upstream.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if !strings.HasPrefix(relay.URL, "http://127.0.0.1:") {
		t.Fatalf("URL = %q", relay.URL)
	}
	if got, want := relay.ContainerURL, strings.Replace(relay.URL, "127.0.0.1", containerRelayHost, 1); got != want {
		t.Fatalf("ContainerURL = %q, want %q", got, want)
	}

	for _, tc := range []struct {
		name, path, key, auth string
		want                  int
	}{
		{"missing auth", "/v1/messages", "", "", http.StatusUnauthorized},
		{"bad auth", "/v1/messages", "wrong", "", http.StatusUnauthorized},
		{"outside v1", "/agentic/health", "secret", "", http.StatusNotFound},
		{"path traversal", "/v1/../agentic/reload", "secret", "", http.StatusNotFound},
		{"api key", "/v1/messages?beta=1", "secret", "", http.StatusCreated},
		{"bearer", "/v1/messages/count_tokens", "", "Bearer secret", http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, relay.URL+tc.path, strings.NewReader("body"))
			req.Header.Set("x-api-key", tc.key)
			req.Header.Set("Authorization", tc.auth)
			req.Header.Set("X-Agentic-Session", "session-1")
			req.Header.Set("X-Agentic-Profile", "profile-1")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d; body %q", resp.StatusCode, tc.want, body)
			}
			if tc.want == http.StatusCreated {
				got := <-seen
				if got.URL.RequestURI() != tc.path || got.Header.Get("X-Agentic-Session") != "session-1" || got.Header.Get("X-Agentic-Profile") != "profile-1" {
					t.Fatalf("upstream request path=%q session=%q profile=%q", got.URL.RequestURI(), got.Header.Get("X-Agentic-Session"), got.Header.Get("X-Agentic-Profile"))
				}
				if resp.Header.Get("X-Upstream") != "yes" || string(body) != "ok" {
					t.Fatalf("response header=%q body=%q", resp.Header.Get("X-Upstream"), body)
				}
			}
		})
	}
}

func TestRelayStreamsWithoutBuffering(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first\n\n")
		flusher.Flush()
		time.Sleep(500 * time.Millisecond)
		fmt.Fprint(w, "data: second\n\n")
	}))
	defer upstream.Close()
	relay, err := StartRelay(context.Background(), upstream.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()

	req, _ := http.NewRequest(http.MethodPost, relay.URL+"/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer secret")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("first stream line = %q, err %v", line, err)
	}
	if elapsed := time.Since(start); elapsed >= 400*time.Millisecond {
		t.Fatalf("first chunk buffered for %v", elapsed)
	}
}

func TestRelayContextCancellationAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	relay, err := StartRelay(ctx, "http://127.0.0.1:1", "secret")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-relay.done:
	case <-time.After(time.Second):
		t.Fatal("relay did not stop after context cancellation")
	}
	if err := relay.Close(); err != nil {
		t.Fatal(err)
	}
	if err := relay.Close(); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 100 * time.Millisecond}
	if _, err := client.Get(relay.URL + "/v1/messages"); err == nil {
		t.Fatal("request succeeded after relay shutdown")
	}
}

func TestStartRelayRejectsInvalidInputs(t *testing.T) {
	if _, err := StartRelay(nil, "http://example.com", "token"); err == nil {
		t.Error("nil context accepted")
	}
	if _, err := StartRelay(context.Background(), "not a URL", "token"); err == nil {
		t.Error("invalid URL accepted")
	}
	if _, err := StartRelay(context.Background(), "http://example.com", ""); err == nil {
		t.Error("empty token accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := StartRelay(ctx, "http://example.com", "token"); err == nil {
		t.Error("canceled context accepted")
	}
}
