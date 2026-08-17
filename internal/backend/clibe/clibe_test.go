package clibe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/maorbril/agentic/internal/anthropic"
	"github.com/maorbril/agentic/internal/backend"
	"github.com/maorbril/agentic/internal/config"
)

type fakeExecutor struct {
	dir    string
	argv   []string
	stdout string
	stderr string
	err    error
}

func (f *fakeExecutor) Run(_ context.Context, dir string, _ []string, argv []string, _ io.Reader, stdout, stderr io.Writer) error {
	f.dir = dir
	f.argv = append([]string(nil), argv...)
	io.WriteString(stdout, f.stdout)
	io.WriteString(stderr, f.stderr)
	return f.err
}

func testCall(t *testing.T, dir string, p config.Provider, modelID string, stream bool) *backend.Call {
	t.Helper()
	raw := []byte(`{"model":"peer","messages":[{"role":"user","content":[{"type":"text","text":"fix the tests"}]}]}`)
	if stream {
		raw = []byte(`{"model":"peer","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"fix the tests"}]}]}`)
	}
	env, err := anthropic.ParseEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set(CwdHeader, dir)
	return &backend.Call{
		Raw:      raw,
		Envelope: env,
		Header:   h,
		Route: config.Resolved{
			Alias:    "peer",
			Provider: p,
			Model:    config.Model{ID: modelID},
		},
	}
}

func loggedInHome(t *testing.T, dialect string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "."+dialect)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestMessagesRunsCodexInSessionDirectory(t *testing.T) {
	dir := t.TempDir()
	exec := &fakeExecutor{stdout: "all fixed\n"}
	b := &Backend{
		Exec:     exec,
		LookPath: func(string) (string, error) { return "/usr/bin/codex", nil },
		Home:     loggedInHome(t, config.CLIDialectCodex),
	}
	p := config.Provider{Type: config.ProviderCLI, Dialect: config.CLIDialectCodex, Sandbox: "workspace-write"}
	rr := httptest.NewRecorder()
	res := b.Messages(context.Background(), testCall(t, dir, p, "gpt-5", false), rr)

	if res.Status != 200 {
		t.Fatalf("status = %d, body = %s", res.Status, rr.Body.String())
	}
	if exec.dir != dir {
		t.Errorf("dir = %q, want %q", exec.dir, dir)
	}
	want := []string{"codex", "exec", "--skip-git-repo-check", "--sandbox", "workspace-write", "-m", "gpt-5", "fix the tests"}
	if !reflect.DeepEqual(exec.argv, want) {
		t.Errorf("argv = %#v, want %#v", exec.argv, want)
	}
	if !strings.Contains(rr.Body.String(), `"text":"all fixed"`) {
		t.Errorf("response missing final output: %s", rr.Body.String())
	}
}

func TestMessagesGrokStreamingSequence(t *testing.T) {
	dir := t.TempDir()
	exec := &fakeExecutor{stdout: `{"result":"done"}`}
	b := &Backend{
		Exec:     exec,
		LookPath: func(string) (string, error) { return "/usr/bin/grok", nil },
		Home:     loggedInHome(t, config.CLIDialectGrok),
	}
	p := config.Provider{Type: config.ProviderCLI, Dialect: config.CLIDialectGrok, Command: "grok-build"}
	rr := httptest.NewRecorder()
	res := b.Messages(context.Background(), testCall(t, dir, p, "grok-4", true), rr)

	if res.Status != 200 {
		t.Fatalf("status = %d, body = %s", res.Status, rr.Body.String())
	}
	want := []string{"grok-build", "--no-auto-update", "--output-format", "plain", "--model", "grok-4", "-p", "fix the tests"}
	if !reflect.DeepEqual(exec.argv, want) {
		t.Errorf("argv = %#v, want %#v", exec.argv, want)
	}
	body := rr.Body.String()
	last := -1
	for _, event := range []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"} {
		i := strings.Index(body[last+1:], "event: "+event)
		if i < 0 {
			t.Fatalf("missing %s after offset %d:\n%s", event, last, body)
		}
		last += i + 1
	}
	if !strings.Contains(body, `"text":"done"`) {
		t.Errorf("stream missing parsed final output: %s", body)
	}
}

func TestMessagesFailureIsFinalTextNotErrorEvent(t *testing.T) {
	dir := t.TempDir()
	exec := &fakeExecutor{stderr: "authentication expired", err: errors.New("exit status 1")}
	b := &Backend{
		Exec:     exec,
		LookPath: func(string) (string, error) { return "/usr/bin/codex", nil },
		Home:     loggedInHome(t, config.CLIDialectCodex),
	}
	p := config.Provider{Type: config.ProviderCLI, Dialect: config.CLIDialectCodex}
	rr := httptest.NewRecorder()
	res := b.Messages(context.Background(), testCall(t, dir, p, "", true), rr)

	if res.Status != 502 || res.ErrType != "api_error" {
		t.Fatalf("result = %+v", res)
	}
	body := rr.Body.String()
	if strings.Contains(body, "event: error") {
		t.Fatalf("failure must not emit retryable SSE error event:\n%s", body)
	}
	if !strings.Contains(body, "authentication expired") || !strings.Contains(body, "event: message_stop") {
		t.Errorf("failure should be final assistant text: %s", body)
	}
}

func TestMessagesNonStreamingFailureUsesHTTPErrorStatus(t *testing.T) {
	dir := t.TempDir()
	exec := &fakeExecutor{stderr: "bad credentials", err: errors.New("exit status 1")}
	b := &Backend{
		Exec:     exec,
		LookPath: func(string) (string, error) { return "/usr/bin/codex", nil },
		Home:     loggedInHome(t, config.CLIDialectCodex),
	}
	rr := httptest.NewRecorder()
	res := b.Messages(context.Background(), testCall(t, dir, config.Provider{Type: config.ProviderCLI, Dialect: config.CLIDialectCodex}, "", false), rr)
	if res.Status != 502 || rr.Code != 502 {
		t.Fatalf("result status=%d HTTP status=%d body=%s", res.Status, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "bad credentials") {
		t.Errorf("body missing failure details: %s", rr.Body.String())
	}
}

func TestMessagesRefusesMissingBinaryBeforeExecution(t *testing.T) {
	dir := t.TempDir()
	exec := &fakeExecutor{}
	b := &Backend{
		Exec:     exec,
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		Home:     loggedInHome(t, config.CLIDialectCodex),
	}
	rr := httptest.NewRecorder()
	res := b.Messages(context.Background(), testCall(t, dir, config.Provider{Type: config.ProviderCLI, Dialect: config.CLIDialectCodex}, "", false), rr)

	if res.Status != 400 || !strings.Contains(rr.Body.String(), "npm install -g @openai/codex") {
		t.Fatalf("result = %+v, body = %s", res, rr.Body.String())
	}
	if exec.argv != nil {
		t.Errorf("executor was invoked: %#v", exec.argv)
	}
}

func TestMessagesRefusesMissingWorkingDirectory(t *testing.T) {
	exec := &fakeExecutor{}
	b := &Backend{
		Exec:     exec,
		LookPath: func(string) (string, error) { return "/usr/bin/codex", nil },
		Home:     loggedInHome(t, config.CLIDialectCodex),
	}
	call := testCall(t, t.TempDir(), config.Provider{Type: config.ProviderCLI, Dialect: config.CLIDialectCodex}, "", false)
	call.Header.Del(CwdHeader)
	rr := httptest.NewRecorder()
	res := b.Messages(context.Background(), call, rr)
	if res.Status != 400 || !strings.Contains(rr.Body.String(), CwdHeader) {
		t.Fatalf("result = %+v, body = %s", res, rr.Body.String())
	}
	if exec.argv != nil {
		t.Errorf("executor was invoked: %#v", exec.argv)
	}
}

func TestMessagesRefusesMissingLogin(t *testing.T) {
	dir := t.TempDir()
	exec := &fakeExecutor{}
	b := &Backend{
		Exec:     exec,
		LookPath: func(string) (string, error) { return "/usr/bin/grok", nil },
		Home:     t.TempDir(),
	}
	t.Setenv("XAI_API_KEY", "")
	rr := httptest.NewRecorder()
	res := b.Messages(context.Background(), testCall(t, dir, config.Provider{Type: config.ProviderCLI, Dialect: config.CLIDialectGrok}, "", false), rr)

	if res.Status != 400 || !strings.Contains(rr.Body.String(), "grok login") {
		t.Fatalf("result = %+v, body = %s", res, rr.Body.String())
	}
	if exec.argv != nil {
		t.Errorf("executor was invoked: %#v", exec.argv)
	}
}

func TestTaskTextUsesLastUserTurnAndToolResult(t *testing.T) {
	req, err := anthropic.ParseRequest([]byte(`{
		"model":"peer",
		"messages":[
			{"role":"user","content":"old"},
			{"role":"assistant","content":"working"},
			{"role":"user","content":[
				{"type":"text","text":"new"},
				{"type":"tool_result","tool_use_id":"x","content":"result"}
			]}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := taskText(req); got != "new\n\nresult" {
		t.Errorf("taskText = %q", got)
	}
}
