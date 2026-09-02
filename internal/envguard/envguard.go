// Package envguard keeps secrets out of subprocess environments. The harness
// reads credentials from the process environment (OMNIROUTE_API_KEY), so
// anything it spawns must not inherit them: a shell command or MCP server
// running in the same process tree could otherwise read the key.
//
// The built-in list covers the harness's own credentials only. A subprocess
// still inherits everything else, which is deliberate — an agent asked to run
// `gh pr create` or `npm publish` needs those tools' tokens. Deployments that
// do not want that trade can name more variables through Configure.
package envguard

import (
	"os"
	"path"
	"strings"
	"sync"
)

// secretVars are the environment variables that must never reach a child
// process. Matched case-insensitively.
var secretVars = []string{
	"OMNIROUTE_API_KEY",
	"OMNIROUTE_MGMT_TOKEN",
	"OMNIHARNESS_API_KEY",
	"ROUTER_API_KEY",
}

// environ is a seam so tests can filter a fixed environment.
var environ = os.Environ

var (
	mu    sync.RWMutex
	extra []string
)

// Configure adds environment variable name patterns to strip, on top of the
// built-in credentials. A pattern may use shell-style wildcards and is matched
// case-insensitively, so "*_TOKEN" and "AWS_*" both work. Calling it replaces
// any previous set; an empty slice clears it. The built-ins are never removed.
func Configure(patterns []string) {
	mu.Lock()
	defer mu.Unlock()
	extra = append(extra[:0:0], patterns...)
}

// Filter returns the parent environment minus credential variables. The
// caller can then append its own overrides.
func Filter() []string {
	return filter(environ())
}

func filter(env []string) []string {
	mu.RLock()
	patterns := extra
	mu.RUnlock()

	out := make([]string, 0, len(env))
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		if secret(kv[:eq], patterns) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func secret(name string, patterns []string) bool {
	for _, s := range secretVars {
		if strings.EqualFold(name, s) {
			return true
		}
	}
	upper := strings.ToUpper(name)
	for _, p := range patterns {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		// A malformed pattern must not silently match nothing: fall back to
		// an exact comparison so a plain variable name always works.
		if ok, err := path.Match(p, upper); err != nil {
			if p == upper {
				return true
			}
		} else if ok {
			return true
		}
	}
	return false
}
