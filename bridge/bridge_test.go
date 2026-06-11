package bridge

import (
	"testing"

	mcpagora "github.com/openziti/mcp-gateway/agora"
)

func TestBridgeCommandTagInference(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{
			name: "npx scoped package",
			cfg:  &Config{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem"}},
			want: "filesystem",
		},
		{
			name: "uvx package",
			cfg:  &Config{Command: "uvx", Args: []string{"mcp-server-git"}},
			want: "git",
		},
		{
			name: "docker image",
			cfg:  &Config{Command: "docker", Args: []string{"run", "mcp/postgres"}},
			want: "postgres",
		},
		{
			name: "bare command",
			cfg:  &Config{Command: "mcp-server-filesystem"},
			want: "filesystem",
		},
		{
			name: "override",
			cfg:  &Config{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem"}, AgoraCapabilityTag: "files"},
			want: "files",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bridgeCommandTag(tt.cfg); got != tt.want {
				t.Fatalf("tag = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBridgeCapabilityExtrasAddsServeTag(t *testing.T) {
	cfg := &Config{
		Command: "mcp-server-filesystem",
		Zrok:    &ZrokConfig{Share: &ZrokShareConfig{Enabled: false}},
		Agora: &mcpagora.Config{
			Enabled: true,
			Serve:   &mcpagora.ServeConfig{Enabled: true},
		},
	}

	got := bridgeCapabilityExtras(cfg)
	want := []string{"filesystem", "agora-serve"}
	if len(got) != len(want) {
		t.Fatalf("extras = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extras = %#v, want %#v", got, want)
		}
	}
}
