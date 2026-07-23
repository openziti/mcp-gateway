package aggregator

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestStdioEnvironmentClosedExcludesHostEnvironment(t *testing.T) {
	t.Setenv("MCP_GATEWAY_TEST_SECRET", "hostvalue")
	env := StdioEnvironment(TransportConfig{
		EnvPolicy: EnvPolicyClosed,
		Env:       map[string]string{"B": "2", "A": "1"},
	})
	if !slices.Equal(env, []string{"A=1", "B=2"}) {
		t.Fatalf("closed environment = %v, want exactly the sorted configured entries", env)
	}

	env = StdioEnvironment(TransportConfig{EnvPolicy: EnvPolicyClosed})
	if env == nil || len(env) != 0 {
		t.Fatalf("closed empty environment = %#v, want non-nil empty (nil means inherit)", env)
	}
}

func TestStdioEnvironmentAdditiveDefaultInherits(t *testing.T) {
	t.Setenv("MCP_GATEWAY_TEST_MARKER", "present")
	for _, policy := range []string{"", EnvPolicyAdditive} {
		env := StdioEnvironment(TransportConfig{
			EnvPolicy: policy,
			Env:       map[string]string{"EXTRA": "1"},
		})
		if !slices.Contains(env, "MCP_GATEWAY_TEST_MARKER=present") {
			t.Fatalf("policy %q did not inherit the host environment", policy)
		}
		if !slices.Contains(env, "EXTRA=1") {
			t.Fatalf("policy %q dropped the configured entry", policy)
		}
		if len(env) != len(os.Environ())+1 {
			t.Fatalf("policy %q environment length = %d, want host+1", policy, len(env))
		}
	}
}

func TestValidateRejectsUnknownEnvPolicy(t *testing.T) {
	config := &Config{Backends: []BackendConfig{{
		ID:   "b",
		Name: "b",
		Transport: TransportConfig{
			Type:      "stdio",
			Command:   "/bin/true",
			EnvPolicy: "inherit-some",
		},
	}}}
	err := config.Validate()
	if err == nil || !strings.Contains(err.Error(), "env_policy") {
		t.Fatalf("unknown env_policy error = %v", err)
	}
}
