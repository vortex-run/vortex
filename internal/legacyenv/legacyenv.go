// Package legacyenv keeps pre-rename settings working. The product was
// renamed from VORTEX to Gubern, and its environment variables from VORTEX_*
// to GUBERN_*. Existing deployments still set the old names, so each
// VORTEX_<NAME> is honoured as GUBERN_<NAME> when the new name isn't set;
// the new name always wins.
package legacyenv

import (
	"os"
	"sort"
	"strings"
)

// Legacy and Current are the old and new environment variable prefixes.
const (
	Legacy  = "VORTEX_"
	Current = "GUBERN_"
)

// Apply copies every set VORTEX_<NAME> to GUBERN_<NAME> in the process
// environment when GUBERN_<NAME> is unset, so all later lookups (and child
// processes) see the new name. It returns the legacy names it applied,
// sorted, for a deprecation notice; names whose new form was already set are
// not applied and not returned.
func Apply() []string {
	var applied []string
	for _, kv := range os.Environ() {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, Legacy) || len(name) == len(Legacy) {
			continue
		}
		newName := Current + name[len(Legacy):]
		if _, set := os.LookupEnv(newName); set {
			continue
		}
		if err := os.Setenv(newName, val); err == nil {
			applied = append(applied, name)
		}
	}
	sort.Strings(applied)
	return applied
}

// Fill adds GUBERN_<NAME> entries to env for every VORTEX_<NAME> entry whose
// new name is missing. For code that works on an environment snapshot
// rather than the live process environment.
func Fill(env map[string]string) {
	for name, val := range env {
		if !strings.HasPrefix(name, Legacy) || len(name) == len(Legacy) {
			continue
		}
		newName := Current + name[len(Legacy):]
		if _, set := env[newName]; !set {
			env[newName] = val
		}
	}
}

// Notice is the one-line deprecation message for applied legacy names, or
// "" when none were applied.
func Notice(applied []string) string {
	if len(applied) == 0 {
		return ""
	}
	renamed := make([]string, len(applied))
	for i, n := range applied {
		renamed[i] = Current + strings.TrimPrefix(n, Legacy)
	}
	return "note: VORTEX_* settings still work but are deprecated; rename " +
		strings.Join(applied, ", ") + " to " + strings.Join(renamed, ", ")
}
