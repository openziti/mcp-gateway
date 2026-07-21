package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openziti/mcp-gateway/aggregator"
)

func TestClientSessionPolicyDeniesBeforeBackendDispatch(t *testing.T) {
	root := t.TempDir()
	policy, err := aggregator.NewCallPolicy(aggregator.PolicyConfig{Paths: []aggregator.PathPolicyConfig{{
		Tool: "read_file", Argument: "path", Roots: []string{root},
	}}}, root)
	if err != nil {
		t.Fatal(err)
	}
	namespace := aggregator.NewNamespace("_")
	namespace.AddTools("filesystem", []*mcp.Tool{{Name: "read_file"}}, nil)
	session := &ClientSession{
		id:        "test-session",
		config:    &Config{Aggregator: aggregator.AggregatorConfig{Connection: aggregator.ConnectionConfig{CallTimeout: time.Second}}},
		namespace: namespace,
		backends: map[string]*sessionBackend{
			"filesystem": {id: "filesystem", policy: policy},
		},
	}

	result, err := session.CallTool(context.Background(), "filesystem_read_file", json.RawMessage(`{"path":"`+filepath.ToSlash(t.TempDir())+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("policy denial result = %#v", result)
	}

	result, err = session.CallTool(context.Background(), "filesystem_read_file", json.RawMessage(`{"path":"/outside","path":"`+filepath.ToSlash(root)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, `duplicate JSON object key "path"`) {
		t.Fatalf("duplicate-key policy denial result = %#v", result)
	}
}

func TestAuditArgsPreservesCompleteStructuredArguments(t *testing.T) {
	content := strings.Repeat("x", 800)
	got, ok := auditArgs(json.RawMessage(`{"content":"` + content + `"}`)).(map[string]any)
	if !ok {
		t.Fatalf("audit args type = %T", got)
	}
	if got["content"] != content {
		t.Fatalf("audit content length = %d, want %d", len(got["content"].(string)), len(content))
	}
}
