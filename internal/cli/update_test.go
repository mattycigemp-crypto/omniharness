package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstalledFromNpm(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{`C:\Users\wegot\AppData\Roaming\npm\node_modules\omniharness-cli\vendor\win32-x64\omniharness.exe`, true},
		{`/usr/local/lib/node_modules/omniharness-cli/vendor/linux-x64/omniharness`, true},
		{`C:\AI\omniharness\bin\omniharness.exe`, false},
		{`C:\AI\omniharness\npm\vendor\win32-x64\omniharness.exe`, false}, // repo build dir, not node_modules
		{`/c/AI/omniharness/bin/omniharness`, false},
	}
	for _, c := range cases {
		if got := installedFromNpm(c.path); got != c.want {
			t.Errorf("installedFromNpm(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.1.0", 0},
		{"0.1.0", "0.1.1", -1},
		{"0.1.1", "0.1.0", 1},
		{"0.1.0", "0.2.0", -1},
		{"1.0.0", "0.9.9", 1},
		{"0.1.10", "0.1.9", 1}, // numeric, not lexicographic
		{"0.1.0", "0.1.0-beta", 0},
		{"1.2.3", "1.2", 1}, // longer wins when prefixes tie
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseVersionToleratesJunk(t *testing.T) {
	// Should not panic and should produce a comparable number.
	if got := parseVersion("v0.1.0"); got[0] != 0 || got[1] != 1 {
		t.Fatalf("parseVersion(v0.1.0) = %v", got)
	}
}

func TestUpdateNoticeFor(t *testing.T) {
	newer := updateNoticeFor("0.1.0", "0.1.2", true)
	if !strings.Contains(newer, "update available") || !strings.Contains(newer, "0.1.2") || !strings.Contains(newer, "omniharness update") {
		t.Fatalf("newer notice = %q", newer)
	}
	// Up to date, ahead of published, and source builds show nothing.
	if got := updateNoticeFor("0.1.2", "0.1.2", true); got != "" {
		t.Fatalf("up to date notice = %q", got)
	}
	if got := updateNoticeFor("0.1.3", "0.1.2", true); got != "" {
		t.Fatalf("ahead notice = %q", got)
	}
	if got := updateNoticeFor("0.1.0", "0.1.2", false); got != "" {
		t.Fatalf("source build notice = %q", got)
	}
	if got := updateNoticeFor("0.1.0", "", true); got != "" {
		t.Fatalf("unknown latest notice = %q", got)
	}
}

func TestCachedLatestVersionUsesFreshCache(t *testing.T) {
	// A fresh cache must be honored without touching the network.
	dir := t.TempDir()
	data, _ := json.Marshal(updateCheckCache{CheckedAt: time.Now(), Latest: "9.9.9"})
	if err := os.WriteFile(filepath.Join(dir, "update-check.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	latest, err := cachedLatestVersion(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if latest != "9.9.9" {
		t.Fatalf("latest = %q", latest)
	}
}
