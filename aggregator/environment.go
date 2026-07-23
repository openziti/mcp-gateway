package aggregator

import (
	"fmt"
	"os"
	"sort"
)

const (
	// EnvPolicyAdditive appends configured entries to the gateway process's
	// own environment. this is the default and the historical behavior.
	EnvPolicyAdditive = "additive"
	// EnvPolicyClosed starts the backend with exactly the configured entries
	// and nothing inherited from the gateway process. embedding callers such
	// as Sterling use this so a spawned shim can never expose host secrets
	// (for example via /proc/<pid>/environ) to the tool tree below it.
	EnvPolicyClosed = "closed"
)

// StdioEnvironment is the single owner of backend process environment
// construction for every stdio spawn path.
func StdioEnvironment(transport TransportConfig) []string {
	var env []string
	if transport.EnvPolicy != EnvPolicyClosed {
		env = os.Environ()
	}
	keys := make([]string, 0, len(transport.Env))
	for key := range transport.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+transport.Env[key])
	}
	if env == nil {
		// a non-nil empty environment: exec.Cmd treats nil as "inherit".
		env = []string{}
	}
	return env
}

func validateEnvPolicy(transport TransportConfig, index int) error {
	switch transport.EnvPolicy {
	case "", EnvPolicyAdditive, EnvPolicyClosed:
		return nil
	default:
		return &ConfigError{
			Field:   fmt.Sprintf("backends[%d].transport.env_policy", index),
			Message: fmt.Sprintf("unknown env_policy '%s' (want '%s' or '%s')", transport.EnvPolicy, EnvPolicyAdditive, EnvPolicyClosed),
		}
	}
}
