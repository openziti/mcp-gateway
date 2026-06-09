package aggregator

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestConnectAgoraBackendRequiresDialClient(t *testing.T) {
	manager := NewBackendManager(testAgoraManagerConfig())

	_, err := manager.connectBackend(context.Background(), testAgoraBackendConfig())
	if err == nil || !strings.Contains(err.Error(), "agora dial client is not configured") {
		t.Fatalf("expected missing dial client error, got %v", err)
	}
}

func TestResolveAgoraDialClientReturnsConfigured(t *testing.T) {
	manager := NewBackendManager(testAgoraManagerConfig())
	want := &http.Client{}
	var gotTunnel string
	manager.SetAgoraDialClient(func(tunnel string) (*http.Client, error) {
		gotTunnel = tunnel
		return want, nil
	})

	got, err := manager.resolveAgoraDialClient(testAgoraBackendConfig())
	if err != nil {
		t.Fatalf("resolveAgoraDialClient returned error: %v", err)
	}
	if got != want {
		t.Fatal("expected the configured dial client")
	}
	if gotTunnel != "filesystem-relay" {
		t.Fatalf("dial client resolved for tunnel %q", gotTunnel)
	}
}

func TestResolveAgoraDialClientPropagatesError(t *testing.T) {
	manager := NewBackendManager(testAgoraManagerConfig())
	manager.SetAgoraDialClient(func(string) (*http.Client, error) {
		return nil, errors.New("tunnel was not attached at startup")
	})

	_, err := manager.resolveAgoraDialClient(testAgoraBackendConfig())
	if err == nil || !strings.Contains(err.Error(), "was not attached at startup") {
		t.Fatalf("expected propagated dial error, got %v", err)
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
