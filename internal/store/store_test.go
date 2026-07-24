package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRouteDecisionRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "agentic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// No decision recorded yet.
	if _, _, _, _, ok, err := st.LatestRouteDecision("sess-1"); ok || err != nil {
		t.Errorf("empty lookup: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	if err := st.RecordRouteDecision("sess-1", "auto", "deep", "opus", "size:light→deep", time.Now()); err != nil {
		t.Fatal(err)
	}
	alias, tier, model, reason, ok, err := st.LatestRouteDecision("sess-1")
	if err != nil || !ok || alias != "auto" || tier != "deep" || model != "opus" || reason != "size:light→deep" {
		t.Errorf("got alias=%s tier=%s model=%s reason=%q ok=%v err=%v, want auto/deep/opus/\"size:light→deep\"/true/nil",
			alias, tier, model, reason, ok, err)
	}

	// A later decision for the same session overwrites, not duplicates.
	if err := st.RecordRouteDecision("sess-1", "auto", "light", "qwen", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	alias, tier, model, reason, ok, err = st.LatestRouteDecision("sess-1")
	if err != nil || !ok || alias != "auto" || tier != "light" || model != "qwen" || reason != "" {
		t.Errorf("after overwrite: alias=%s tier=%s model=%s reason=%q ok=%v err=%v, want auto/light/qwen/\"\"/true/nil",
			alias, tier, model, reason, ok, err)
	}

	// A different session is unaffected.
	if _, _, _, _, ok, err := st.LatestRouteDecision("sess-2"); ok || err != nil {
		t.Errorf("other session: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestGoalDecisionRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "agentic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// No decision recorded yet.
	if _, _, ok, err := st.LatestGoalDecision("sess-1"); ok || err != nil {
		t.Errorf("empty lookup: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	if err := st.RecordGoalDecision("sess-1", true, "polling a long build", time.Now()); err != nil {
		t.Fatal(err)
	}
	goal, reason, ok, err := st.LatestGoalDecision("sess-1")
	if err != nil || !ok || !goal || reason != "polling a long build" {
		t.Errorf("got goal=%v reason=%q ok=%v err=%v, want true/\"polling a long build\"/true/nil",
			goal, reason, ok, err)
	}

	// A later decision for the same session overwrites, not duplicates.
	if err := st.RecordGoalDecision("sess-1", false, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	goal, reason, ok, err = st.LatestGoalDecision("sess-1")
	if err != nil || !ok || goal || reason != "" {
		t.Errorf("after overwrite: goal=%v reason=%q ok=%v err=%v, want false/\"\"/true/nil",
			goal, reason, ok, err)
	}

	// A different session is unaffected.
	if _, _, ok, err := st.LatestGoalDecision("sess-2"); ok || err != nil {
		t.Errorf("other session: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestActiveSessions(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "agentic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now().Truncate(time.Second)
	st.StartSession("sess-live", "main", "/tmp/a", now.Add(-time.Hour))
	st.StartSession("sess-done", "main", "/tmp/b", now.Add(-2*time.Hour))
	st.EndSession("sess-done", now)
	st.RecordUsage(UsageEvent{TS: now.Add(-time.Minute), SessionID: "sess-live", InputTokens: 10})

	active, err := st.ActiveSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != "sess-live" {
		t.Fatalf("active = %+v, want just sess-live", active)
	}
	a := active[0]
	if a.Profile != "main" || a.WorkDir != "/tmp/a" {
		t.Errorf("session fields: %+v", a)
	}
	if !a.StartedAt.Equal(now.Add(-time.Hour)) {
		t.Errorf("StartedAt = %v", a.StartedAt)
	}
	if !a.LastSeen.Equal(now.Add(-time.Minute)) {
		t.Errorf("LastSeen = %v, want usage event time", a.LastSeen)
	}

	// A session with no usage has zero LastSeen.
	st.StartSession("sess-quiet", "cheap", "/tmp/c", now)
	active, _ = st.ActiveSessions()
	if len(active) != 2 || active[0].ID != "sess-quiet" || !active[0].LastSeen.IsZero() {
		t.Errorf("active = %+v, want sess-quiet first with zero LastSeen", active)
	}
}
