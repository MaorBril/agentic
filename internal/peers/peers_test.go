package peers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRegistry(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	records := map[string]string{
		"65686.json": `{"pid":65686,"sessionId":"709ae8bc","name":"daily-case-runtime",
			"cwd":"/Users/m/code/secondlife/labs-service","status":"busy","version":"2.1.226",
			"messagingSocketPath":"/tmp/cc-socks/65686.sock","startedAt":1786066338384}`,
		"64257.json": `{"pid":64257,"sessionId":"bc748548","name":"daily-puzzle-frontend-launch",
			"cwd":"/Users/m/code/secondlife/labs-ui","status":"idle","version":"2.1.226",
			"messagingSocketPath":"/tmp/cc-socks/64257.sock","startedAt":1786066297180}`,
		"71949.json": `{"pid":71949,"sessionId":"fe460cf6","name":"labs-service-e4",
			"cwd":"/Users/m/code/labs-service","status":"idle","version":"2.1.223",
			"startedAt":1786057168833}`,
		"garbage.json": `{not json at all`,
	}
	for name, body := range records {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadSkipsMalformedAndSortsNewestFirst(t *testing.T) {
	got, err := Load(writeRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 parseable sessions, got %d: %+v", len(got), got)
	}
	if got[0].Name != "daily-case-runtime" {
		t.Errorf("newest session should sort first, got %q", got[0].Name)
	}
}

func TestReachabilityTracksMessagingSocket(t *testing.T) {
	sessions, _ := Load(writeRegistry(t))
	for _, s := range sessions {
		// labs-service-e4 is on 2.1.223 and registers no socket.
		if want := s.Name != "labs-service-e4"; s.Reachable() != want {
			t.Errorf("%s: Reachable()=%v, want %v", s.Name, s.Reachable(), want)
		}
	}
}

// The name a user says is usually the directory: "labs-service-secondlife-be"
// matches no session name, only a path.
func TestMatchResolvesDirectoryPhrasing(t *testing.T) {
	sessions, _ := Load(writeRegistry(t))
	ranked := Match(sessions, "labs-service-secondlife-be")
	if len(ranked) == 0 {
		t.Fatal("no match for a directory-shaped query")
	}
	if ranked[0].Name != "daily-case-runtime" {
		t.Errorf("want daily-case-runtime, got %q (%+v)", ranked[0].Name, ranked)
	}
	if Ambiguous(ranked) {
		t.Errorf("labs-service should outrank labs-ui, got %+v", ranked)
	}
}

func TestMatchPrefersExactNameOverDirectoryOverlap(t *testing.T) {
	sessions, _ := Load(writeRegistry(t))
	ranked := Match(sessions, "labs-service-e4")
	if ranked[0].Name != "labs-service-e4" {
		t.Errorf("exact name should win, got %q", ranked[0].Name)
	}
}

func TestMatchDropsNonMatchesAndKeepsAllOnEmptyQuery(t *testing.T) {
	sessions, _ := Load(writeRegistry(t))
	if got := Match(sessions, "totally-unrelated"); len(got) != 0 {
		t.Errorf("want no matches, got %+v", got)
	}
	if got := Match(sessions, ""); len(got) != len(sessions) {
		t.Errorf("empty query should return everything, got %d", len(got))
	}
}

func TestInstallGuidanceIsIdempotentAndPreservesOtherText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")

	if action, err := InstallGuidance(path); err != nil || action != Created {
		t.Fatalf("first install: action=%v err=%v", action, err)
	}
	if action, err := InstallGuidance(path); err != nil || action != Unchanged {
		t.Fatalf("re-install should be a no-op: action=%v err=%v", action, err)
	}

	// Someone's own notes around the block must survive a refresh.
	body, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append([]byte("# My notes\n\nkeep me\n\n"), body...), 0o644); err != nil {
		t.Fatal(err)
	}
	if action, err := InstallGuidance(path); err != nil || action != Unchanged {
		t.Fatalf("block unchanged despite surrounding text: action=%v err=%v", action, err)
	}

	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "keep me") {
		t.Error("surrounding text was clobbered")
	}
	if n := strings.Count(string(got), guidanceStart); n != 1 {
		t.Errorf("want exactly one guidance block, got %d", n)
	}
}

func TestInstallGuidanceAppendsToExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	os.WriteFile(path, []byte("# Existing\n\nmy rules\n"), 0o644)

	if action, err := InstallGuidance(path); err != nil || action != Updated {
		t.Fatalf("append: action=%v err=%v", action, err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "my rules") || !strings.Contains(string(got), guidanceEnd) {
		t.Errorf("append lost content or marker:\n%s", got)
	}
}

func TestInstallGuidanceRefusesToManglePartialMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	os.WriteFile(path, []byte(guidanceStart+"\nhalf a block\n"), 0o644)

	if _, err := InstallGuidance(path); err == nil {
		t.Error("want an error on an unterminated block, got nil")
	}
}
