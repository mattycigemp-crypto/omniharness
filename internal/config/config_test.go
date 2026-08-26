package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsValidate(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.OmniRoute.Endpoint != "http://127.0.0.1:20128" {
		t.Fatalf("endpoint %q", c.OmniRoute.Endpoint)
	}
	if c.Policy.RiskAction["critical"] != "block" {
		t.Fatal("critical must default to block")
	}
	if c.Policy.ShellAllowed {
		t.Fatal("shell must default to disallowed")
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.toml")
	c := Default()
	c.Models.Default = "auto/best-coding"
	c.OmniRoute.Endpoint = "http://127.0.0.1:29999"
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Models.Default != "auto/best-coding" {
		t.Fatalf("models.default after round-trip = %q", loaded.Models.Default)
	}
	if loaded.OmniRoute.Endpoint != "http://127.0.0.1:29999" {
		t.Fatalf("endpoint after round-trip = %q", loaded.OmniRoute.Endpoint)
	}
	// Saving must never write the API key even if one is set in memory.
	c.OmniRoute.APIKey = "sk-super-secret"
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-super-secret") {
		t.Fatal("Save leaked the API key into the config file")
	}
}

func TestSaveRejectsEmptyPath(t *testing.T) {
	cfg := Default()
	if err := cfg.Save(""); err == nil {
		t.Fatal("Save with empty path should fail")
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.OmniRoute.Endpoint != Default().OmniRoute.Endpoint {
		t.Fatal("defaults not applied")
	}
}

func TestEnvVarsOverrideWithNoConfigFile(t *testing.T) {
	// OMNIROUTE_* are the primary names; they must apply even when no config
	// file exists (the common deployment case).
	t.Setenv("OMNIROUTE_URL", "http://127.0.0.1:29999")
	t.Setenv("OMNIROUTE_API_KEY", "sk-env-secret-1234")
	c, err := Load("") // no file path
	if err != nil {
		t.Fatal(err)
	}
	if c.OmniRoute.Endpoint != "http://127.0.0.1:29999" {
		t.Fatalf("endpoint = %q", c.OmniRoute.Endpoint)
	}
	if c.OmniRoute.APIKey != "sk-env-secret-1234" {
		t.Fatalf("api key not applied")
	}

	// Missing file path must behave identically.
	c2, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if c2.OmniRoute.APIKey != "sk-env-secret-1234" {
		t.Fatal("env key must apply without a config file")
	}
}

func TestLegacyEnvAliasesStillWork(t *testing.T) {
	t.Setenv("OMNIHARNESS_ENDPOINT", "http://127.0.0.1:29998")
	t.Setenv("OMNIHARNESS_API_KEY", "sk-legacy-999")
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.OmniRoute.Endpoint != "http://127.0.0.1:29998" || c.OmniRoute.APIKey != "sk-legacy-999" {
		t.Fatalf("legacy env vars ignored: %+v", c.OmniRoute)
	}
}

func TestOMNIROUTEEnvWinsOverLegacyAlias(t *testing.T) {
	t.Setenv("OMNIROUTE_URL", "http://primary:1")
	t.Setenv("OMNIHARNESS_ENDPOINT", "http://legacy:2")
	t.Setenv("OMNIROUTE_API_KEY", "sk-primary")
	t.Setenv("OMNIHARNESS_API_KEY", "sk-legacy")
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.OmniRoute.Endpoint != "http://primary:1" {
		t.Fatalf("endpoint = %q", c.OmniRoute.Endpoint)
	}
	if c.OmniRoute.APIKey != "sk-primary" {
		t.Fatalf("key = %q", c.OmniRoute.APIKey)
	}
}

func TestWriteDefaultNeverContainsSecret(t *testing.T) {
	t.Setenv("OMNIROUTE_API_KEY", "sk-never-persist-abc")
	path := filepath.Join(t.TempDir(), "out.toml")
	if err := WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-never-persist-abc") {
		t.Fatal("secret written to config file")
	}
}

func TestLoadConfigFileWithSecretFieldIsRedactedFromDisk(t *testing.T) {
	// Even if a user manually puts a key in the TOML, WriteDefault must never
	// round-trip it into a new file.
	path := filepath.Join(t.TempDir(), "in.toml")
	if err := os.WriteFile(path, []byte("[omniroute]\napi_key = \"sk-manual-42\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.OmniRoute.APIKey != "sk-manual-42" {
		t.Fatalf("key = %q", c.OmniRoute.APIKey)
	}
	out := filepath.Join(t.TempDir(), "out.toml")
	if err := WriteDefault(out); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	if strings.Contains(string(data), "sk-manual-42") {
		t.Fatal("manual key leaked into generated config")
	}
}

func TestLoadAndValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "omniharness.toml")
	data := `
[omniroute]
endpoint = "http://localhost:9999"
timeout = "30s"

[models]
default = "openai/gpt-5.4"

[models.capabilities]
reasoning = "cursor/claude-opus-4-8-thinking-xhigh"
fast = "openai/gpt-5.4"

[policy]
shell_allowed = true

[policy.risk_action]
high = "ask"
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.OmniRoute.Endpoint != "http://localhost:9999" || c.OmniRoute.Timeout != 30*time.Second {
		t.Fatalf("%+v", c.OmniRoute)
	}
	if c.Models.Capabilities["fast"] != "openai/gpt-5.4" {
		t.Fatalf("%+v", c.Models.Capabilities)
	}
	if !c.Policy.ShellAllowed {
		t.Fatal("shell_allowed not parsed")
	}
}

func TestValidateRejectsBadModelRef(t *testing.T) {
	c := Default()
	c.Models.Default = "no-slash-here"
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for invalid model ref")
	}
	c = Default()
	c.OmniRoute.Endpoint = "not-a-url"
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for bad endpoint")
	}
	c = Default()
	c.Policy.RiskAction["high"] = "maybe"
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for bad risk action")
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("OMNIHARNESS_ENDPOINT", "http://127.0.0.1:5555")
	t.Setenv("OMNIHARNESS_LOG_LEVEL", "debug")
	c := Default()
	c.applyEnv()
	if c.OmniRoute.Endpoint != "http://127.0.0.1:5555" || c.Logging.Level != "debug" {
		t.Fatalf("%+v", c)
	}
}

func TestWriteDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.toml")
	if err := WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("default config not written")
	}
	// Idempotent: second call must not error.
	if err := WriteDefault(path); err != nil {
		t.Fatal(err)
	}
}
