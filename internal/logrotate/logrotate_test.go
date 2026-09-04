package logrotate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func lines(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestRotatesAtTheCeiling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.log")
	w, err := Open(path, 32, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	for i := 0; i < 10; i++ {
		if _, err := fmt.Fprintf(w, "record-%02d\n", i); err != nil {
			t.Fatal(err)
		}
	}
	// 10 records of 10 bytes against a 32-byte ceiling: the current file
	// holds the newest few, older ones live in the generations.
	if got := len(lines(t, path)); got > 32 {
		t.Errorf("current log is %d bytes, want <= 32", got)
	}
	if !strings.Contains(lines(t, path), "record-09") {
		t.Error("newest record is not in the current log")
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("expected a rotated generation: %v", err)
	}
}

func TestKeepsOnlyNGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.log")
	w, err := Open(path, 16, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	for i := 0; i < 40; i++ {
		if _, err := fmt.Fprintf(w, "line-%03d\n", i); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	// router.log plus at most two generations, however many rotations ran.
	if len(names) != 3 {
		t.Errorf("found %v, want router.log plus exactly 2 generations", names)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Error("a third generation survived the keep limit")
	}
}

func TestOversizedExistingLogRotatesRatherThanTruncating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.log")
	// stand in for the 26MB log an older build left behind
	if err := os.WriteFile(path, []byte(strings.Repeat("old\n", 50)), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := Open(path, 32, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := fmt.Fprintln(w, "new"); err != nil {
		t.Fatal(err)
	}
	if got := lines(t, path); strings.Contains(got, "old") {
		t.Error("the oversized log was appended to instead of rotated")
	}
	if got := lines(t, path+".1"); !strings.Contains(got, "old") {
		t.Error("the previous contents were discarded rather than kept as a generation")
	}
}

func TestKeepZeroRestartsTheLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.log")
	w, err := Open(path, 16, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	for i := 0; i < 10; i++ {
		if _, err := fmt.Fprintf(w, "line-%03d\n", i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Error("keep 0 should leave no generations behind")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("found %d files, want just the log", len(entries))
	}
}

func TestConcurrentWritersDoNotRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.log")
	w, err := Open(path, 64, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if _, err := fmt.Fprintf(w, "g%d-i%02d\n", g, i); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestRejectsNonsenseLimits(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(filepath.Join(dir, "a.log"), 0, 1); err == nil {
		t.Error("maxBytes 0 should be rejected")
	}
	if _, err := Open(filepath.Join(dir, "b.log"), 16, -1); err == nil {
		t.Error("negative keep should be rejected")
	}
}
