package aggregator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCallPolicyEnforcesPathContainment(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	outside := filepath.Join(out, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	policy := testCallPolicy(t, root)
	for name, args := range map[string]any{
		"absolute":         map[string]any{"path": inside},
		"relative":         json.RawMessage(`{"path":"inside.txt"}`),
		"new write target": map[string]any{"path": filepath.Join(root, "new.txt")},
	} {
		t.Run(name, func(t *testing.T) {
			if err := policy.Enforce("read_file", args); err != nil {
				t.Fatalf("inside path denied: %v", err)
			}
		})
	}
	for name, args := range map[string]any{
		"outside":        map[string]any{"path": outside},
		"traversal":      map[string]any{"path": filepath.Join(root, "..", filepath.Base(out), "outside.txt")},
		"missing":        map[string]any{},
		"wrong type":     map[string]any{"path": 42},
		"missing parent": map[string]any{"path": filepath.Join(root, "missing", "new.txt")},
	} {
		t.Run(name, func(t *testing.T) {
			if err := policy.Enforce("read_file", args); err == nil {
				t.Fatal("outside or malformed path accepted")
			}
		})
	}
	if err := policy.Enforce("unconfigured_tool", map[string]any{"path": outside}); err != nil {
		t.Fatalf("unconfigured tool changed existing behavior: %v", err)
	}
}

func TestCallPolicyRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	policy := testCallPolicy(t, root)
	if err := policy.Enforce("read_file", map[string]any{"path": filepath.Join(link, "secret.txt")}); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestCallPolicyRejectsDanglingSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	out := t.TempDir()
	link := filepath.Join(root, "output")
	if err := os.Symlink(filepath.Join(out, "new-file"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	policy := testCallPolicy(t, root)
	if err := policy.Enforce("read_file", map[string]any{"path": link}); err == nil {
		t.Fatal("dangling symlink escape accepted")
	}
}

func TestCallPolicyDenialNamesGovernedArgument(t *testing.T) {
	policy := testCallPolicy(t, t.TempDir())
	err := policy.Enforce("read_file", nil)
	if err == nil || !strings.Contains(err.Error(), `argument "path"`) {
		t.Fatalf("malformed-arguments denial = %v", err)
	}
}

func TestCallPolicyPrepareRejectsDuplicateJSONKeys(t *testing.T) {
	policy := testCallPolicy(t, t.TempDir())
	_, err := policy.Prepare("read_file", json.RawMessage(`{"path":"/outside","path":"/allowed"}`))
	if err == nil || !strings.Contains(err.Error(), `duplicate JSON object key "path"`) {
		t.Fatalf("duplicate governed argument error = %v", err)
	}

	raw := json.RawMessage(`{"nested":{"key":1,"key":2}}`)
	settled, err := policy.Prepare("unconfigured_tool", raw)
	if err != nil {
		t.Fatalf("ungoverned arguments rejected: %v", err)
	}
	got, ok := settled.(json.RawMessage)
	if !ok || string(got) != string(raw) {
		t.Fatalf("ungoverned arguments changed: %#v", settled)
	}
}

func TestCallPolicyPrepareSettlesArgumentsOnce(t *testing.T) {
	root := t.TempDir()
	arguments := &countingArguments{path: root}
	policy := testCallPolicy(t, root)
	settled, err := policy.Prepare("read_file", arguments)
	if err != nil {
		t.Fatal(err)
	}
	if arguments.calls != 1 {
		t.Fatalf("argument marshal calls = %d, want 1", arguments.calls)
	}
	settledMap, ok := settled.(map[string]any)
	if !ok || settledMap["path"] != root {
		t.Fatalf("settled arguments = %#v", settled)
	}
	if _, err := json.Marshal(settled); err != nil {
		t.Fatal(err)
	}
	if arguments.calls != 1 {
		t.Fatalf("dispatch re-marshaled original arguments %d times", arguments.calls)
	}
}

type countingArguments struct {
	calls int
	path  string
}

func (a *countingArguments) MarshalJSON() ([]byte, error) {
	a.calls++
	return json.Marshal(map[string]any{"path": a.path})
}

func TestCallPolicyValidatesConfiguredTools(t *testing.T) {
	policy := testCallPolicy(t, t.TempDir())
	if err := policy.ValidateTools([]*mcp.Tool{{Name: "read_file"}}); err != nil {
		t.Fatal(err)
	}
	err := policy.ValidateTools([]*mcp.Tool{{Name: "write_file"}})
	if err == nil || !strings.Contains(err.Error(), `"read_file"`) {
		t.Fatalf("unknown policy tool error = %v", err)
	}
}

func TestBackendPolicyDeniesBeforeBackendDispatch(t *testing.T) {
	root := t.TempDir()
	policy := testCallPolicy(t, root)
	backend := &Backend{id: "filesystem", policy: policy}
	result, err := backend.CallTool(context.Background(), "read_file", map[string]any{"path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("policy denial result = %#v", result)
	}
}

func TestServerPolicyDeniesMalformedArgumentsBeforeGenericParsing(t *testing.T) {
	policy := testCallPolicy(t, t.TempDir())
	namespace := NewNamespace("_")
	namespace.AddTools("filesystem", []*mcp.Tool{{Name: "read_file", InputSchema: map[string]any{"type": "object"}}}, nil)
	backends := &BackendManager{backends: map[string]*Backend{
		"filesystem": {id: "filesystem", policy: policy},
	}}
	server := NewServer(DefaultConfig(), namespace, backends)
	result, err := server.CallTool(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
		Name:      "filesystem_read_file",
		Arguments: json.RawMessage(`{"path":`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError || len(result.Content) != 1 {
		t.Fatalf("policy denial result = %#v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, `gateway policy denied tool call`) || !strings.Contains(text.Text, `argument "path"`) {
		t.Fatalf("malformed-arguments result = %#v", result.Content)
	}
}

func TestValidatePathPolicy(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	valid := func() *Config {
		return &Config{Backends: []BackendConfig{{
			ID:        "filesystem",
			Transport: TransportConfig{Type: "stdio", Command: "mcp-filesystem"},
			Policy: PolicyConfig{Paths: []PathPolicyConfig{{
				Tool: "read_file", Argument: "path", Roots: []string{root},
			}}},
		}}}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}

	tests := map[string]func(*Config){
		"remote backend": func(cfg *Config) {
			cfg.Backends[0].Transport.Type = "zrok"
			cfg.Backends[0].Transport.ShareToken = "share"
			cfg.Backends[0].Transport.Command = ""
		},
		"missing tool":  func(cfg *Config) { cfg.Backends[0].Policy.Paths[0].Tool = "" },
		"missing arg":   func(cfg *Config) { cfg.Backends[0].Policy.Paths[0].Argument = "" },
		"relative root": func(cfg *Config) { cfg.Backends[0].Policy.Paths[0].Roots = []string{"relative"} },
		"unclean root": func(cfg *Config) {
			cfg.Backends[0].Policy.Paths[0].Roots = []string{root + string(filepath.Separator) + "."}
		},
		"duplicate root": func(cfg *Config) { cfg.Backends[0].Policy.Paths[0].Roots = []string{root, root} },
		"duplicate rule": func(cfg *Config) {
			cfg.Backends[0].Policy.Paths = append(cfg.Backends[0].Policy.Paths, cfg.Backends[0].Policy.Paths[0])
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := valid()
			mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

func TestNewCallPolicyRejectsMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	_, err := NewCallPolicy(PolicyConfig{Paths: []PathPolicyConfig{{
		Tool: "read_file", Argument: "path", Roots: []string{root},
	}}}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("missing root error = %v", err)
	}
}

func testCallPolicy(t *testing.T, root string) *CallPolicy {
	t.Helper()
	policy, err := NewCallPolicy(PolicyConfig{Paths: []PathPolicyConfig{{
		Tool: "read_file", Argument: "path", Roots: []string{root},
	}}}, root)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}
