package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "agentic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func record(t *testing.T, st *Store, e UsageEvent) {
	t.Helper()
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	if e.Status == 0 {
		e.Status = 200
	}
	if err := st.RecordUsage(e); err != nil {
		t.Fatal(err)
	}
}

func TestEstimateCalibration(t *testing.T) {
	st := openTestStore(t)
	// 25 requests where the estimator guessed 1000 and upstream billed
	// 800: it runs 25% high, so the correction is 0.8.
	for i := 0; i < 25; i++ {
		record(t, st, UsageEvent{Model: "gpt", InputTokens: 800, EstInput: 1000})
	}
	// A model with too few samples must not produce a correction.
	record(t, st, UsageEvent{Model: "rare", InputTokens: 500, EstInput: 1000})
	// Failed requests and unestimated rows are excluded.
	record(t, st, UsageEvent{Model: "gpt", InputTokens: 9_000_000, EstInput: 1000, Status: 500, ErrType: "api_error"})
	record(t, st, UsageEvent{Model: "gpt", InputTokens: 9_000_000, EstInput: 0})

	rows, err := st.EstimateCalibration(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	byModel := map[string]CalibrationRow{}
	for _, r := range rows {
		byModel[r.Model] = r
	}
	gpt, ok := byModel["gpt"]
	if !ok {
		t.Fatal("no calibration row for gpt")
	}
	if gpt.Requests != 25 {
		t.Errorf("Requests = %d, want 25 (errors and unestimated rows excluded)", gpt.Requests)
	}
	if r := gpt.Ratio(); r < 0.79 || r > 0.81 {
		t.Errorf("Ratio() = %v, want ~0.8", r)
	}
	if rare := byModel["rare"]; rare.Requests != 1 {
		t.Errorf("rare row = %+v; low-sample models are reported, callers filter", rare)
	}
}

// Cache reads count toward the true input side: they are tokens the model
// held, which is what a context budget measures.
func TestEstimateCalibrationCountsCachedInput(t *testing.T) {
	st := openTestStore(t)
	for i := 0; i < 20; i++ {
		record(t, st, UsageEvent{Model: "m", InputTokens: 100, CacheReadTokens: 700, CacheWriteTokens: 200, EstInput: 1000})
	}
	rows, err := st.EstimateCalibration(time.Now().Add(-time.Hour))
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].Ratio() != 1.0 {
		t.Errorf("Ratio() = %v, want 1.0", rows[0].Ratio())
	}
}

func TestComposition(t *testing.T) {
	st := openTestStore(t)
	for i := 0; i < 4; i++ {
		record(t, st, UsageEvent{SessionID: "s1", Model: "m", EstInput: 100_000, EstSystem: 10_000, EstTools: 40_000})
	}
	record(t, st, UsageEvent{SessionID: "s2", Model: "m", EstInput: 1000, EstSystem: 100, EstTools: 100})

	rows, err := st.Composition("s1", time.Time{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	r := rows[0]
	if r.Requests != 4 || r.System != 10_000 || r.Tools != 40_000 || r.Messages != 50_000 {
		t.Errorf("composition = %+v", r)
	}
	if got := r.FixedFraction(); got != 0.5 {
		t.Errorf("FixedFraction() = %v, want 0.5", got)
	}

	// Session-less lookup aggregates everything in the window.
	all, err := st.Composition("", time.Now().Add(-time.Hour))
	if err != nil || len(all) != 1 || all[0].Requests != 5 {
		t.Fatalf("all=%v err=%v", all, err)
	}
}

func TestSpendCacheHitRate(t *testing.T) {
	st := openTestStore(t)
	record(t, st, UsageEvent{Model: "m", InputTokens: 250, CacheReadTokens: 750, OutputTokens: 10})
	rows, err := st.SpendSince(time.Now().Add(-time.Hour), "model")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].InputTokens != 1000 || rows[0].CacheReadTokens != 750 {
		t.Fatalf("row = %+v", rows[0])
	}
	if got := rows[0].CacheHitRate(); got != 0.75 {
		t.Errorf("CacheHitRate() = %v, want 0.75", got)
	}
	if got := (SpendRow{}).CacheHitRate(); got != 0 {
		t.Errorf("empty row hit rate = %v", got)
	}
}
