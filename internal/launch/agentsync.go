package launch

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/maorbril/agentic/internal/agents"
	"github.com/maorbril/agentic/internal/config"
)

// offerAgentSync prompts once, at launch, when the generated subagents have
// drifted from the configured model aliases. Deliberately quiet: it says
// nothing when in sync, when not attached to a terminal (piped/headless runs
// must never block on input), when AGENTIC_NO_AGENT_SYNC is set, or when the
// user already declined this exact config state.
func offerAgentSync(cfg *config.Config, dataDir string) {
	if os.Getenv("AGENTIC_NO_AGENT_SYNC") != "" {
		return
	}
	if !isInteractive() {
		return
	}
	dir, err := agents.Dir()
	if err != nil {
		return
	}
	changes, err := agents.Diff(cfg, dir)
	if err != nil || len(changes) == 0 {
		return // in sync (or unreadable) — stay silent
	}
	fp := agents.Fingerprint(cfg)
	if agents.Declined(dataDir, fp) {
		return // already said no to this exact state
	}

	fmt.Fprintf(os.Stderr, "\nagentic: %s\n", summarize(changes))
	fmt.Fprintf(os.Stderr, "  These let you target a specific model by name (e.g. subagent_type: %q).\n",
		firstName(changes))
	fmt.Fprint(os.Stderr, "  Update ~/.claude/agents now? [y/N] ")

	if !readYes() {
		if err := agents.RecordDeclined(dataDir, fp); err == nil {
			fmt.Fprintln(os.Stderr, "  skipped — won't ask again until your model aliases change (`agentic agents sync` anytime)")
		}
		return
	}
	applied, err := agents.Sync(cfg, dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ could not write subagents: %v\n", err)
		return
	}
	agents.ClearDeclined(dataDir)
	fmt.Fprintf(os.Stderr, "  ✓ synced %d subagent(s) — available in new sessions\n", len(applied))
}

func summarize(changes []agents.Change) string {
	var create, update, remove int
	for _, c := range changes {
		switch c.Kind {
		case "create":
			create++
		case "update":
			update++
		case "remove":
			remove++
		}
	}
	var parts []string
	if create > 0 {
		parts = append(parts, fmt.Sprintf("%d new", create))
	}
	if update > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", update))
	}
	if remove > 0 {
		parts = append(parts, fmt.Sprintf("%d stale", remove))
	}
	return "model-alias subagents out of date (" + strings.Join(parts, ", ") + ")"
}

// firstName returns a created/updated subagent name for the example line,
// preferring one the user will actually gain.
func firstName(changes []agents.Change) string {
	for _, c := range changes {
		if c.Kind == "create" {
			return c.Name
		}
	}
	return changes[0].Name
}

// isInteractive reports whether stdin and stderr are both terminals, so a
// prompt can be shown and answered.
func isInteractive() bool {
	return isTerminal(os.Stdin) && isTerminal(os.Stderr)
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func readYes() bool {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
