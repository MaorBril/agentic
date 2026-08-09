// Package peers reads Claude Code's session registry and resolves the
// approximate names people use for their sessions ("the labs-service one")
// to a specific session.
//
// Session names are auto-derived from whatever the session is working on, so
// the name a user says is usually the project directory rather than the
// session name — and ListAgents, the only thing that can mint the [ref] a
// cross-session SendMessage needs, does not expose directories. Matching has
// to happen out here, against the registry, where both fields exist.
package peers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Session struct {
	PID       int
	SessionID string
	Name      string
	Dir       string
	Status    string
	Version   string
	Socket    string // messagingSocketPath — empty on builds without native messaging
	Started   time.Time
}

// Reachable reports whether the session can receive a message at all. Builds
// before Claude Code 2.1.224 register no socket and stay invisible to
// ListAgents until they restart.
func (s Session) Reachable() bool { return s.Socket != "" }

type Ranked struct {
	Session
	Score int
}

// Load parses every session record in dir, newest first. Unreadable or
// malformed records are skipped rather than failing the whole listing — the
// registry is written by another process and may be mid-write.
func Load(dir string) ([]Session, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var sessions []Session
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var raw struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
			Name      string `json:"name"`
			Dir       string `json:"cwd"`
			Status    string `json:"status"`
			Version   string `json:"version"`
			Socket    string `json:"messagingSocketPath"`
			StartedAt int64  `json:"startedAt"`
		}
		if json.Unmarshal(data, &raw) != nil || raw.PID == 0 {
			continue
		}
		sessions = append(sessions, Session{
			PID:       raw.PID,
			SessionID: raw.SessionID,
			Name:      raw.Name,
			Dir:       raw.Dir,
			Status:    raw.Status,
			Version:   raw.Version,
			Socket:    raw.Socket,
			Started:   time.UnixMilli(raw.StartedAt),
		})
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Started.After(sessions[j].Started)
	})
	return sessions, nil
}

// Match ranks sessions against an approximate name, scoring how many of the
// query's words appear in the session name or its directory. An exact name
// hit outranks any amount of directory overlap. Sessions matching nothing are
// dropped; an empty query returns everything unscored.
func Match(sessions []Session, query string) []Ranked {
	tokens := tokenize(query)
	ranked := make([]Ranked, 0, len(sessions))
	for _, s := range sessions {
		if len(tokens) == 0 {
			ranked = append(ranked, Ranked{Session: s})
			continue
		}
		haystack := strings.ToLower(s.Name + " " + s.Dir)
		score := 0
		for _, tok := range tokens {
			if strings.Contains(haystack, tok) {
				score++
			}
		}
		if score == 0 {
			continue
		}
		if strings.EqualFold(strings.Join(tokens, "-"), s.Name) {
			score += len(tokens) // exact name beats directory overlap
		}
		ranked = append(ranked, Ranked{Session: s, Score: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })
	return ranked
}

// Ambiguous reports whether the top two matches are tied, meaning the query
// does not actually pick one session.
func Ambiguous(ranked []Ranked) bool {
	return len(ranked) > 1 && ranked[0].Score == ranked[1].Score
}

func tokenize(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !('a' <= r && r <= 'z' || '0' <= r && r <= '9')
	})
	return fields
}
