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
	if _, _, _, ok, err := st.LatestRouteDecision("sess-1"); ok || err != nil {
		t.Errorf("empty lookup: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	if err := st.RecordRouteDecision("sess-1", "auto", "deep", "opus", time.Now()); err != nil {
		t.Fatal(err)
	}
	alias, tier, model, ok, err := st.LatestRouteDecision("sess-1")
	if err != nil || !ok || alias != "auto" || tier != "deep" || model != "opus" {
		t.Errorf("got alias=%s tier=%s model=%s ok=%v err=%v, want auto/deep/opus/true/nil",
			alias, tier, model, ok, err)
	}

	// A later decision for the same session overwrites, not duplicates.
	if err := st.RecordRouteDecision("sess-1", "auto", "light", "qwen", time.Now()); err != nil {
		t.Fatal(err)
	}
	alias, tier, model, ok, err = st.LatestRouteDecision("sess-1")
	if err != nil || !ok || alias != "auto" || tier != "light" || model != "qwen" {
		t.Errorf("after overwrite: alias=%s tier=%s model=%s ok=%v err=%v, want auto/light/qwen/true/nil",
			alias, tier, model, ok, err)
	}

	// A different session is unaffected.
	if _, _, _, ok, err := st.LatestRouteDecision("sess-2"); ok || err != nil {
		t.Errorf("other session: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}
