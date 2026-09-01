// Package config loads, validates and supplies OmniHarness configuration.
// Configuration is TOML with sensible defaults. API keys may be supplied by
// environment or saved explicitly through the interactive TUI.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the root configuration document.
type Config struct {
	OmniRoute   OmniRoute   `toml:"omniroute"`
	Models      Models      `toml:"models"`
	Budgets     Budgets     `toml:"budgets"`
	Policy      Policy      `toml:"policy"`
	Persistence Persistence `toml:"persistence"`
	Telemetry   Telemetry   `toml:"telemetry"`
	TUI         TUI         `toml:"tui"`
	Benchmark   Benchmark   `toml:"benchmark"`
	Logging     Logging     `toml:"logging"`
	MCP         MCP         `toml:"mcp"`
}

// MCP configures Model Context Protocol servers to load as tools.
type MCP struct {
	Servers []MCServer `toml:"servers"`
}

// MCServer is an MCP server process definition.
type MCServer struct {
	Name    string   `toml:"name"`
	Command string   `toml:"command"`
	Args    []string `toml:"args,omitempty"`
	Env     []string `toml:"env,omitempty"`
}

// OmniRoute configures the gateway connection.
type OmniRoute struct {
	// Endpoint is the base URL of the OmniRoute server, e.g.
	// http://localhost:20128
	Endpoint string `toml:"endpoint"`
	// Timeout bounds a single model request.
	Timeout time.Duration `toml:"timeout"`
	// APIKey is optional; most OmniRoute deployments authenticate implicitly.
	APIKey string `toml:"api_key,omitempty"`
}

// Models configures model selection intent.
type Models struct {
	// Default is the provider/model used when no capability is specified.
	Default string `toml:"default"`
	// Capabilities maps capability names ("reasoning", "fast", "cheap",
	// "long-context", "coding", "vision", "research", "review") to
	// provider/model strings. Empty values fall back to Default.
	Capabilities map[string]string `toml:"capabilities"`
}

// Budgets are task-level resource ceilings; zero means unlimited.
type Budgets struct {
	MaxTokens     int64         `toml:"max_tokens,omitempty"`
	MaxCostUSD    float64       `toml:"max_cost_usd,omitempty"`
	MaxDuration   time.Duration `toml:"max_duration,omitempty"`
	MaxAgents     int           `toml:"max_agents,omitempty"`
	MaxToolCalls  int           `toml:"max_tool_calls,omitempty"`
	MaxRepairCycl int           `toml:"max_repair_cycles,omitempty"`
}

// Policy configures the security engine.
type Policy struct {
	// RiskAction maps risk class (low|medium|high|critical) to
	// "allow" | "ask" | "block".
	RiskAction map[string]string `toml:"risk_action"`
	// AllowedTools restricts the tool set; empty means all registered tools.
	AllowedTools []string `toml:"allowed_tools,omitempty"`
	// BlockedTools always denies these tools regardless of risk action.
	BlockedTools []string `toml:"blocked_tools,omitempty"`
	// WorkspaceRoot confines filesystem tools to this directory tree.
	WorkspaceRoot string `toml:"workspace_root,omitempty"`
	// ShellAllowed enables the shell tool (off by default for safety).
	ShellAllowed bool `toml:"shell_allowed"`
	// GitPushRequiresApproval forces approval for git push even when risk
	// action says allow.
	GitPushRequiresApproval bool `toml:"git_push_requires_approval"`
}

// Persistence configures durable state.
type Persistence struct {
	// Dir is where the SQLite database and artifacts live.
	Dir string `toml:"dir,omitempty"`
}

// Telemetry configures metric recording.
type Telemetry struct {
	Enabled       bool `toml:"enabled"`
	RetentionDays int  `toml:"retention_days,omitempty"`
}

// TUI configures presentation preferences.
type TUI struct {
	// Color toggles lipgloss color output.
	Color bool `toml:"color"`
	// RefreshMS controls the event refresh cadence in milliseconds.
	RefreshMS int `toml:"refresh_ms,omitempty"`
}

// Benchmark configures the benchmark command defaults.
type Benchmark struct {
	Iterations int      `toml:"iterations,omitempty"`
	Models     []string `toml:"models,omitempty"`
	Tasks      []string `toml:"tasks,omitempty"`
}

// Logging configures structured logging.
type Logging struct {
	Level  string `toml:"level,omitempty"`  // debug | info | warn | error
	Format string `toml:"format,omitempty"` // text | json
}

// Default returns the default configuration.
func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{
		OmniRoute: OmniRoute{
			Endpoint: "http://localhost:20128",
			Timeout:  120 * time.Second,
		},
		Models: Models{
			// OmniRoute's auto/* combos route to whatever provider is actually
			// provisioned, so they work out of the box on any instance. Explicit
			// provider-pinned ids (e.g. cursor/claude-…) only work when that
			// provider is configured and funded.
			Default: "auto/best-coding",
			Capabilities: map[string]string{
				"reasoning":    "auto/best-reasoning",
				"fast":         "auto/best-fast",
				"cheap":        "",
				"long-context": "",
				"coding":       "auto/best-coding",
				"vision":       "",
				"research":     "",
				"review":       "",
			},
		},
		Budgets: Budgets{
			MaxTokens:     0,
			MaxCostUSD:    0,
			MaxDuration:   0,
			MaxAgents:     4,
			MaxToolCalls:  0,
			MaxRepairCycl: 3,
		},
		Policy: Policy{
			RiskAction: map[string]string{
				"low":      "allow",
				"medium":   "allow",
				"high":     "ask",
				"critical": "block",
			},
			ShellAllowed:            false,
			GitPushRequiresApproval: true,
		},
		Persistence: Persistence{
			Dir: filepath.Join(home, ".omniharness"),
		},
		Telemetry: Telemetry{
			Enabled:       true,
			RetentionDays: 90,
		},
		TUI: TUI{
			Color:     true,
			RefreshMS: 100,
		},
		Benchmark: Benchmark{
			Iterations: 3,
		},
		Logging: Logging{
			Level:  "info",
			Format: "text",
		}, MCP: MCP{},
	}
}

// Load reads a TOML file over the defaults, then applies environment
// overrides. A missing file is not an error; defaults are used.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return cfg, fmt.Errorf("read config %s: %w", path, err)
			}
		}
		if err := toml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
		if err := cfg.Validate(); err != nil {
			return cfg, fmt.Errorf("invalid config %s: %w", path, err)
		}
	}
	cfg.applyEnv()
	return cfg, nil
}

// Validate checks configuration invariants.
func (c *Config) Validate() error {
	if c.OmniRoute.Endpoint == "" {
		return fmt.Errorf("omniroute.endpoint must be set")
	}
	if !strings.HasPrefix(c.OmniRoute.Endpoint, "http://") && !strings.HasPrefix(c.OmniRoute.Endpoint, "https://") {
		return fmt.Errorf("omniroute.endpoint must be an http(s) URL, got %q", c.OmniRoute.Endpoint)
	}
	if c.OmniRoute.Timeout <= 0 {
		return fmt.Errorf("omniroute.timeout must be positive")
	}
	for cap, m := range c.Models.Capabilities {
		if !validCapability(cap) {
			return fmt.Errorf("unknown model capability %q", cap)
		}
		if m != "" && !validModelRef(m) {
			return fmt.Errorf("invalid provider/model reference %q for capability %q", m, cap)
		}
	}
	if c.Models.Default != "" && !validModelRef(c.Models.Default) {
		return fmt.Errorf("invalid default model reference %q (want provider/model)", c.Models.Default)
	}
	for risk, action := range c.Policy.RiskAction {
		switch risk {
		case "low", "medium", "high", "critical":
		default:
			return fmt.Errorf("unknown risk class %q", risk)
		}
		switch action {
		case "allow", "ask", "block":
		default:
			return fmt.Errorf("risk_action for %q must be allow|ask|block, got %q", risk, action)
		}
	}
	if c.Persistence.Dir == "" {
		return fmt.Errorf("persistence.dir must be set")
	}
	if c.TUI.RefreshMS <= 0 {
		return fmt.Errorf("tui.refresh_ms must be positive")
	}
	if c.Telemetry.RetentionDays <= 0 {
		return fmt.Errorf("telemetry.retention_days must be positive")
	}
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("logging.level must be debug|info|warn|error")
	}
	return nil
}

func validModelRef(m string) bool {
	i := strings.Index(m, "/")
	return i > 0 && i < len(m)-1
}

func validCapability(c string) bool {
	switch c {
	case "reasoning", "fast", "cheap", "long-context", "coding", "vision", "research", "review":
		return true
	}
	return false
}

// applyEnv applies environment overrides. The OMNIROUTE_* names are primary
// (they match OmniRoute's own server conventions); OMNIHARNESS_* remain as
// legacy aliases. Environment values take precedence over the config file.
func (c *Config) applyEnv() {
	if v := os.Getenv("OMNIROUTE_URL"); v != "" {
		c.OmniRoute.Endpoint = v
	} else if v := os.Getenv("OMNIHARNESS_ENDPOINT"); v != "" {
		c.OmniRoute.Endpoint = v
	}
	if v := os.Getenv("OMNIROUTE_API_KEY"); v != "" {
		c.OmniRoute.APIKey = v
	} else if v := os.Getenv("OMNIHARNESS_API_KEY"); v != "" {
		c.OmniRoute.APIKey = v
	}
	if v := os.Getenv("OMNIHARNESS_DATA_DIR"); v != "" {
		c.Persistence.Dir = v
	}
	if v := os.Getenv("OMNIHARNESS_LOG_LEVEL"); v != "" {
		c.Logging.Level = v
	}
	if v := os.Getenv("OMNIHARNESS_WORKSPACE"); v != "" {
		c.Policy.WorkspaceRoot = v
	}
}

// DefaultPath returns the conventional config file location.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".omniharness.toml")
}

// Save writes the configuration back to path as TOML. Used by `stack set` and
// the TUI settings so endpoint, API key, and model choices persist across
// launches.
func (c *Config) Save(path string) error {
	if path == "" {
		return fmt.Errorf("no config path")
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		return fmt.Errorf("encode config %s: %w", path, err)
	}
	return nil
}

// WriteDefault writes the default config to path if it does not exist.
func WriteDefault(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	cfg := Default()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}
