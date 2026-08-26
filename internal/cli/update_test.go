package cli

import "testing"

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
