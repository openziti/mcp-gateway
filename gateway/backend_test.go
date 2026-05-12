package gateway

import (
	"net"
	"testing"

	"github.com/openziti/mcp-gateway/aggregator"
	mcpagora "github.com/openziti/mcp-gateway/agora"
)

type fakeListener struct {
	addr net.Addr
}

func (f fakeListener) Accept() (net.Conn, error) {
	return nil, net.ErrClosed
}

func (f fakeListener) Close() error {
	return nil
}

func (f fakeListener) Addr() net.Addr {
	return f.addr
}

func TestAgoraServeBackendTargetFormatsByTunnelMode(t *testing.T) {
	tcpBackend := &Backend{
		config: &Config{Agora: &mcpagora.Config{TunnelMode: "tcp"}},
		agoraListener: fakeListener{
			addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43210},
		},
	}
	if got := tcpBackend.agoraServeBackendTarget(); got != "127.0.0.1:43210" {
		t.Fatalf("tcp backend target = %q", got)
	}

	httpBackend := &Backend{
		config: &Config{Agora: &mcpagora.Config{TunnelMode: "http"}},
		agoraListener: fakeListener{
			addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43211},
		},
	}
	if got := httpBackend.agoraServeBackendTarget(); got != "http://127.0.0.1:43211" {
		t.Fatalf("http backend target = %q", got)
	}
}

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
