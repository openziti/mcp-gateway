package aggregator

import (
	"context"
	"strings"
	"testing"
)

func TestConnectAgoraBackendRequiresResolver(t *testing.T) {
	manager := NewBackendManager(testAgoraManagerConfig())

	_, err := manager.connectBackend(context.Background(), testAgoraBackendConfig())
	if err == nil || !strings.Contains(err.Error(), "agora connect resolver is not configured") {
		t.Fatalf("expected missing resolver error, got %v", err)
	}
}

func TestConnectAgoraBackendRequiresResolvedAddress(t *testing.T) {
	manager := NewBackendManager(testAgoraManagerConfig())
	manager.SetConnectResolver(func(string) (string, bool) {
		return "", false
	})

	_, err := manager.connectBackend(context.Background(), testAgoraBackendConfig())
	if err == nil || !strings.Contains(err.Error(), "agora connect address for backend 'remote' was not initialized") {
		t.Fatalf("expected unresolved address error, got %v", err)
	}
}

func TestResolveAgoraLoopbackTrimsAddress(t *testing.T) {
	manager := NewBackendManager(testAgoraManagerConfig())
	manager.SetConnectResolver(func(string) (string, bool) {
		return " 127.0.0.1:43210 ", true
	})

	address, err := manager.resolveAgoraLoopback("remote")
	if err != nil {
		t.Fatalf("resolveAgoraLoopback returned error: %v", err)
	}
	if address != "127.0.0.1:43210" {
		t.Fatalf("address = %q", address)
	}
}

func testAgoraManagerConfig() *Config {
	cfg := DefaultConfig()
	cfg.Backends = []BackendConfig{testAgoraBackendConfig()}
	return cfg
}

func testAgoraBackendConfig() BackendConfig {
	return BackendConfig{
		ID: "remote",
		Transport: TransportConfig{
			Type:        "agora",
			AgoraTunnel: "filesystem-relay",
		},
	}
}
