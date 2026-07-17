// Package launch assembles the session environment and spawns Claude Code.
package launch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/maorbril/agentic/internal/config"
	"github.com/maorbril/agentic/internal/router"
	"github.com/maorbril/agentic/internal/store"
)

type Options struct {
	Profile      string
	ModelFlag    string // one-shot main-model override (alias)
	InstanceName string // forwarded to clauder wrap
	NoClauder    bool
	Passthrough  bool
	ClaudeArgs   []string
}

// Token returns the per-install router token, creating it on first use.
func Token(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "token")
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		return strings.TrimSpace(string(data)), nil
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

func newSessionID() string {
	buf := make([]byte, 8)
	rand.Read(buf)
	return fmt.Sprintf("sess-%d-%s", time.Now().Unix(), hex.EncodeToString(buf))
}

// Run launches Claude Code for one session and blocks until it exits.
func Run(ctx context.Context, cfg *config.Config, dataDir string, opts Options, logger *slog.Logger) error {
	profName := opts.Profile
	if profName == "" {
		profName = cfg.DefaultProfile
	}
	var prof config.Profile
	if profName != "" {
		p, ok := cfg.Profiles[profName]
		if !ok {
			return fmt.Errorf("profile %q not found in ~/.agentic/config.yaml", profName)
		}
		prof = p
	}

	env := os.Environ()
	sessionID := newSessionID()

	if prof.Passthrough || opts.Passthrough {
		fmt.Fprintf(os.Stderr, "agentic: passthrough profile — subscription billing, cost tracking unavailable\n")
	} else {
		token, err := Token(dataDir)
		if err != nil {
			return err
		}
		mgr := &router.Manager{Port: cfg.Router.Port, Token: token, DataDir: dataDir, Log: logger}
		routerCtx, cancelRouter := context.WithCancel(context.Background())
		defer cancelRouter()
		go func() {
			if err := mgr.Run(routerCtx); err != nil {
				logger.Error("router", "err", err)
			}
		}()
		if err := mgr.Ensure(ctx); err != nil {
			return err
		}

		model := prof.Model
		if opts.ModelFlag != "" {
			model = opts.ModelFlag
		}
		env = setEnv(env, "ANTHROPIC_BASE_URL", mgr.BaseURL())
		env = setEnv(env, "ANTHROPIC_AUTH_TOKEN", token)
		env = unsetEnv(env, "ANTHROPIC_API_KEY")
		if model != "" {
			env = setEnv(env, "ANTHROPIC_MODEL", model)
		}
		if prof.SmallFast != "" {
			env = setEnv(env, "ANTHROPIC_SMALL_FAST_MODEL", prof.SmallFast)
		}
		for tier, alias := range prof.Tiers {
			env = setEnv(env, "ANTHROPIC_DEFAULT_"+strings.ToUpper(tier)+"_MODEL", alias)
		}
		env = setEnv(env, "ANTHROPIC_CUSTOM_HEADERS",
			fmt.Sprintf("X-Agentic-Session: %s\nX-Agentic-Profile: %s", sessionID, profName))
		env = setEnv(env, "AGENTIC_SESSION_ID", sessionID)
		env = setEnv(env, "AGENTIC_PROFILE", profName)
		if prof.TimeoutMS > 0 {
			env = setEnv(env, "API_TIMEOUT_MS", fmt.Sprint(prof.TimeoutMS))
		}

		recordSession(dataDir, sessionID, profName, true)
		defer func() {
			recordSession(dataDir, sessionID, profName, false)
			printSummary(dataDir, cfg, sessionID, profName)
		}()
	}

	child := buildChild(opts)
	cmd := exec.Command(child[0], child[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launching %s: %w (is Claude Code installed?)", child[0], err)
	}
	go func() {
		for sig := range sigs {
			cmd.Process.Signal(sig)
		}
	}()
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// Claude Code's own exit code (Ctrl-C etc.) — not our error.
		return nil
	}
	return err
}

// buildChild prefers `clauder wrap` when available so cross-instance
// messaging works; env vars pass through its PTY untouched.
func buildChild(opts Options) []string {
	if !opts.NoClauder {
		if _, err := exec.LookPath("clauder"); err == nil {
			args := []string{"clauder", "wrap", "--slave"}
			if opts.InstanceName != "" {
				args = append(args, "--name", opts.InstanceName)
			}
			return append(append(args, "--"), opts.ClaudeArgs...)
		}
	}
	return append([]string{"claude"}, opts.ClaudeArgs...)
}

func recordSession(dataDir, id, profile string, start bool) {
	st, err := store.Open(filepath.Join(dataDir, "agentic.db"))
	if err != nil {
		return
	}
	defer st.Close()
	wd, _ := os.Getwd()
	if start {
		st.StartSession(id, profile, wd, time.Now())
	} else {
		st.EndSession(id, time.Now())
	}
}

func printSummary(dataDir string, cfg *config.Config, sessionID, profile string) {
	st, err := store.OpenReadOnly(filepath.Join(dataDir, "agentic.db"))
	if err != nil {
		return
	}
	defer st.Close()
	dayStart := time.Now().Truncate(24 * time.Hour)
	sess, _ := st.TotalSince(time.Time{}, "", sessionID)
	day, _ := st.TotalSince(dayStart, "", "")
	line := fmt.Sprintf("agentic: session cost $%.2f (profile: %s) — today $%.2f", sess, profile, day)
	if cfg.Budgets != nil && cfg.Budgets.Daily > 0 {
		line += fmt.Sprintf(" / $%.2f daily budget", cfg.Budgets.Daily)
	}
	fmt.Fprintln(os.Stderr, line)
}

func setEnv(env []string, key, value string) []string {
	return append(unsetEnv(env, key), key+"="+value)
}

func unsetEnv(env []string, key string) []string {
	out := env[:0]
	prefix := key + "="
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}
