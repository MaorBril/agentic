// Package agents generates Claude Code subagent definitions for the model
// aliases in the user's config.
//
// Why this exists: Claude Code's built-in Agent tool takes a fixed
// model parameter (sonnet | opus | haiku | fable), so a routed alias like
// "qwen" can never be selected through it. A subagent *definition's* model
// frontmatter has no such restriction — and behind a custom
// ANTHROPIC_BASE_URL (which agentic always sets) Claude Code passes the
// string through unvalidated — so one generated definition per alias makes
// every configured model selectable by name (subagent_type:
// "agentic-qwen").
//
// The alias list comes from the user's own config, so the generated set is
// whatever that install has configured — nothing is hardcoded.
package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/maorbril/agentic/internal/config"
)

// Prefix marks the files this package owns. Anything without it in
// ~/.claude/agents is a user's own agent and is never read, written, or
// deleted.
const Prefix = "agentic-"

// Definition is one generated subagent file.
type Definition struct {
	Alias    string // model alias from config
	Name     string // subagent name (Prefix + slug)
	Filename string // Name + ".md"
	Body     string // full file contents
}

// Desired returns the definitions implied by cfg, sorted by name. Routing
// rules (e.g. "auto") are excluded: they are classifier rules rather than
// concrete models, and a subagent that silently re-tiers itself would make
// the chosen model unpredictable.
func Desired(cfg *config.Config) []Definition {
	if cfg == nil {
		return nil
	}
	out := make([]Definition, 0, len(cfg.Models))
	for alias := range cfg.Models {
		out = append(out, definitionFor(cfg, alias))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func definitionFor(cfg *config.Config, alias string) Definition {
	name := Prefix + slug(alias)
	m := cfg.Models[alias]

	// Describe the model concretely so the orchestrating model can tell the
	// generated agents apart when choosing one.
	var facts []string
	if m.ID != "" {
		facts = append(facts, "upstream model "+m.ID)
	}
	if m.Provider != "" {
		facts = append(facts, "provider "+m.Provider)
	}
	if b := m.ContextBudget(); b > 0 {
		facts = append(facts, fmt.Sprintf("%s context budget", humanTokens(int64(b))))
	}
	detail := ""
	if len(facts) > 0 {
		detail = " (" + strings.Join(facts, ", ") + ")"
	}

	body := fmt.Sprintf(`---
name: %s
description: Runs the task on the %q model alias%s, routed through agentic. Use when you specifically want this model — for cost, context size, or capability reasons — rather than the default sonnet/opus/haiku tiers.
model: %s
---

You are running on the %q model alias, routed through the local agentic
router to %s.

Complete the task you were given and report the result. Your final message
is the return value the caller receives, so make it the answer itself — not
a description of what you did.
`, name, alias, detail, alias, alias, orUnknown(m.ID))

	return Definition{Alias: alias, Name: name, Filename: name + ".md", Body: body}
}

// slug makes an alias safe for a subagent name / filename. Claude Code
// subagent names are referenced verbatim, so keep them to a conservative
// character set.
func slug(alias string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(alias) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			// Collapse runs of separators (".", "_", " ", "/") to one dash.
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func orUnknown(s string) string {
	if s == "" {
		return "its configured upstream model"
	}
	return s
}

func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%dK", n/1_000)
	default:
		return fmt.Sprint(n)
	}
}

// Dir returns ~/.claude/agents.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "agents"), nil
}

// Change is one pending difference between config and disk.
type Change struct {
	Name string
	Kind string // "create" | "update" | "remove"
}

// Diff compares the definitions cfg implies against the agentic-* files on
// disk. Files without Prefix are ignored entirely. A "remove" is reported
// for an agentic-* file whose alias is no longer configured.
func Diff(cfg *config.Config, dir string) ([]Change, error) {
	want := map[string]Definition{}
	for _, d := range Desired(cfg) {
		want[d.Filename] = d
	}

	have := map[string][]byte{}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, Prefix) || !strings.HasSuffix(name, ".md") {
			continue // not ours
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		have[name] = data
	}

	var changes []Change
	for fn, d := range want {
		existing, ok := have[fn]
		switch {
		case !ok:
			changes = append(changes, Change{Name: d.Name, Kind: "create"})
		case string(existing) != d.Body:
			changes = append(changes, Change{Name: d.Name, Kind: "update"})
		}
	}
	for fn := range have {
		if _, ok := want[fn]; !ok {
			changes = append(changes, Change{Name: strings.TrimSuffix(fn, ".md"), Kind: "remove"})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Kind != changes[j].Kind {
			return changes[i].Kind < changes[j].Kind
		}
		return changes[i].Name < changes[j].Name
	})
	return changes, nil
}

// Sync writes the definitions cfg implies and removes stale agentic-* files.
// Only files carrying Prefix are ever written or deleted.
func Sync(cfg *config.Config, dir string) ([]Change, error) {
	changes, err := Diff(cfg, dir)
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	byName := map[string]Definition{}
	for _, d := range Desired(cfg) {
		byName[d.Name] = d
	}
	for _, c := range changes {
		path := filepath.Join(dir, c.Name+".md")
		if c.Kind == "remove" {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			continue
		}
		if err := os.WriteFile(path, []byte(byName[c.Name].Body), 0o644); err != nil {
			return nil, err
		}
	}
	return changes, nil
}

// Fingerprint hashes the definitions cfg implies. Used to remember that the
// user declined a specific config state, so the launch-time offer stays
// quiet until the aliases actually change again.
func Fingerprint(cfg *config.Config) string {
	h := sha256.New()
	for _, d := range Desired(cfg) {
		h.Write([]byte(d.Filename))
		h.Write([]byte{0})
		h.Write([]byte(d.Body))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// declinedFile is where a declined fingerprint is remembered, inside the
// agentic data dir (not ~/.claude — this is agentic's own state).
func declinedFile(dataDir string) string {
	return filepath.Join(dataDir, "agents-declined")
}

// Declined reports whether the user already said no to this exact config
// state. Any read error means "not declined" — a missing or corrupt state
// file must never suppress the offer silently.
func Declined(dataDir, fingerprint string) bool {
	data, err := os.ReadFile(declinedFile(dataDir))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == fingerprint
}

// RecordDeclined remembers that the user declined this config state.
func RecordDeclined(dataDir, fingerprint string) error {
	return os.WriteFile(declinedFile(dataDir), []byte(fingerprint+"\n"), 0o600)
}

// ClearDeclined forgets any declined state (called after a successful sync,
// so a later change offers again).
func ClearDeclined(dataDir string) {
	os.Remove(declinedFile(dataDir))
}
