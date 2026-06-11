package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/openziti/mcp-gateway/aggregator"
)

func TestClientSessionAgoraBackendRequiresDialClient(t *testing.T) {
	session := &ClientSession{}

	_, err := session.connectBackend(context.Background(), testAgoraSessionBackendConfig())
	if err == nil || !strings.Contains(err.Error(), "agora dial client is not configured") {
		t.Fatalf("expected missing dial client error, got %v", err)
	}
}

func TestClientSessionAgoraBackendPropagatesDialError(t *testing.T) {
	session := &ClientSession{
		agoraDial: func(string) (*http.Client, error) {
			return nil, context.DeadlineExceeded
		},
	}

	_, err := session.connectBackend(context.Background(), testAgoraSessionBackendConfig())
	if err == nil || !strings.Contains(err.Error(), "agora dial client for backend 'remote'") {
		t.Fatalf("expected propagated dial client error, got %v", err)
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
