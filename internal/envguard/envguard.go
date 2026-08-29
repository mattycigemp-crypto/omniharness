// Package envguard keeps secrets out of subprocess environments. The harness
// reads credentials from the process environment (OMNIROUTE_API_KEY), so
// anything it spawns must not inherit them: a shell command or MCP server
// running in the same process tree could otherwise read the key.
package envguard

import (
	"os"
	"strings"
)

// secretVars are the environment variables that must never reach a child
// process. Kept lowercase for matching.
var secretVars = []string{
	"OMNIROUTE_API_KEY",
	"OMNIROUTE_MGMT_TOKEN",
	"OMNIHARNESS_API_KEY",
	"ROUTER_API_KEY",
}

// Filter returns the parent environment minus credential variables. The
// caller can then append its own overrides.
func Filter() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		name := kv[:eq]
		if secret(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func secret(name string) bool {
	for _, s := range secretVars {
		if strings.EqualFold(name, s) {
			return true
		}
	}
	return false
}
