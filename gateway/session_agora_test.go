package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/openziti/mcp-gateway/aggregator"
)

func TestClientSessionAgoraBackendRequiresResolver(t *testing.T) {
	session := &ClientSession{}

	_, err := session.connectBackend(context.Background(), testAgoraSessionBackendConfig())
	if err == nil || !strings.Contains(err.Error(), "agora connect resolver is not configured") {
		t.Fatalf("expected missing resolver error, got %v", err)
	}
}

func TestClientSessionAgoraBackendRequiresResolvedAddress(t *testing.T) {
	session := &ClientSession{
		resolver: func(string) (string, bool) {
			return "", false
		},
	}

	_, err := session.connectBackend(context.Background(), testAgoraSessionBackendConfig())
	if err == nil || !strings.Contains(err.Error(), "agora connect address for backend 'remote' was not initialized") {
		t.Fatalf("expected unresolved address error, got %v", err)
	}
}

func testAgoraSessionBackendConfig() aggregator.BackendConfig {
	return aggregator.BackendConfig{
		ID: "remote",
		Transport: aggregator.TransportConfig{
			Type:        "agora",
			AgoraTunnel: "filesystem-relay",
		},
	}
}
