package eval

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestDockerRelayIntegration is opt-in because it needs a running Docker
// daemon and may pull an image. It verifies the actual network path used by
// candidate containers:
//
//	container -> host.docker.internal:<ephemeral> -> 127.0.0.1 router
//
// It also proves the session/profile headers survive the proxy, which is what
// keeps candidate usage and route telemetry attributable.
func TestDockerRelayIntegration(t *testing.T) {
	if testing.Short() || os.Getenv("AGENTIC_DOCKER_INTEGRATION") != "1" {
		t.Skip("set AGENTIC_DOCKER_INTEGRATION=1 to run Docker relay integration")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("docker unavailable: %v: %s", err, out)
	}

	seen := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "reachable")
	}))
	defer upstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	relay, err := StartRelay(ctx, upstream.URL, "relay-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	defer cancel()

	runCtx, runCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer runCancel()
	cmd := exec.CommandContext(runCtx, "docker", "run", "--rm",
		"--add-host=host.docker.internal:host-gateway",
		"curlimages/curl:8.12.1",
		"-fsS", "-X", "POST",
		"-H", "Authorization: Bearer relay-secret",
		"-H", "X-Agentic-Session: docker-integration-session",
		"-H", "X-Agentic-Profile: integration-profile",
		relay.ContainerURL+"/v1/messages?integration=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("container could not reach relay: %v\n%s", err, out)
	}
	if !strings.HasSuffix(strings.TrimSpace(string(out)), "reachable") {
		t.Fatalf("container response = %q", out)
	}
	select {
	case headers := <-seen:
		if headers.Get("X-Agentic-Session") != "docker-integration-session" || headers.Get("X-Agentic-Profile") != "integration-profile" {
			t.Fatalf("headers not preserved: session=%q profile=%q", headers.Get("X-Agentic-Session"), headers.Get("X-Agentic-Profile"))
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive relayed request")
	}
}
