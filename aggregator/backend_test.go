package aggregator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAggregatorDiscoversEveryToolPage(t *testing.T) {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "paged-backend", Version: "1.0.0"},
		&mcp.ServerOptions{PageSize: 1},
	)
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, nil)
	}

	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, nil))
	t.Cleanup(httpServer.Close)

	cfg := DefaultConfig()
	cfg.Aggregator.Connection.ConnectTimeout = 5 * time.Second
	cfg.Backends = []BackendConfig{{
		ID: "paged",
		Transport: TransportConfig{
			Type:          "http",
			Endpoint:      httpServer.URL,
			Protocol:      "streamable",
			AllowInsecure: true,
		},
	}}

	agg, err := New(cfg)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	if err := agg.Start(ctx); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := agg.Stop(); err != nil {
			t.Errorf("Stop returned error: %v", err)
		}
	})

	if got := agg.ToolCount(); got != 3 {
		t.Fatalf("ToolCount = %d, want 3", got)
	}
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		if _, ok := agg.Namespace().GetTool("paged_" + name); !ok {
			t.Errorf("namespace is missing tool %q", name)
		}
	}
}
