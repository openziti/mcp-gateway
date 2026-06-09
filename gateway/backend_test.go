package gateway

import (
	"testing"

	"github.com/openziti/mcp-gateway/aggregator"
	mcpagora "github.com/openziti/mcp-gateway/agora"
)

func TestGatewayCapabilityExtrasSortsBackendIDsAndAddsServeTag(t *testing.T) {
	cfg := &Config{
		Agora: &mcpagora.Config{
			Enabled: true,
			Serve:   &mcpagora.ServeConfig{Enabled: true},
		},
		Backends: []aggregator.BackendConfig{
			{ID: "github"},
			{ID: "filesystem"},
		},
	}

	got := gatewayCapabilityExtras(cfg)
	want := []string{"filesystem", "github", "agora-serve"}
	if len(got) != len(want) {
		t.Fatalf("extras = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extras = %#v, want %#v", got, want)
		}
	}
}

func TestCollectAgoraTunnelsDedupes(t *testing.T) {
	tunnels := collectAgoraTunnels([]aggregator.BackendConfig{
		{
			ID:        "filesystem",
			Transport: aggregator.TransportConfig{Type: "agora", AgoraTunnel: " filesystem-relay "},
		},
		{
			ID:        "filesystem-2",
			Transport: aggregator.TransportConfig{Type: "agora", AgoraTunnel: "filesystem-relay"},
		},
		{
			ID:        "github",
			Transport: aggregator.TransportConfig{Type: "zrok", ShareToken: "share"},
		},
		{
			ID:        "notes",
			Transport: aggregator.TransportConfig{Type: "agora", AgoraTunnel: "notes-relay"},
		},
	})

	want := []string{"filesystem-relay", "notes-relay"}
	if len(tunnels) != len(want) {
		t.Fatalf("tunnels = %#v, want %#v", tunnels, want)
	}
	for i := range want {
		if tunnels[i] != want[i] {
			t.Fatalf("tunnels = %#v, want %#v", tunnels, want)
		}
	}
}
