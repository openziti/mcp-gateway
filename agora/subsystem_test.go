package agora

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/openziti/agora/sdk/agent"
	"github.com/openziti/agora/sdk/agent/catalog"
	"github.com/openziti/agora/sdk/agent/tunnel"
)

type fakeOps struct {
	rootEndpoint string
	rootSource   string
	envEndpoint  string

	newOpts      agent.StandaloneOptions
	starts       int
	connectSpecs []tunnel.ConnectSpec
	serveSpecs   []tunnel.ServeSpec
	publishSpecs []catalog.PublishSpec
	removed      []string
	sequence     []string
	closed       int

	connectErr error
	serveErr   error
	publishErr error
}

func (f *fakeOps) NewStandalone(opts agent.StandaloneOptions) (any, error) {
	f.newOpts = opts
	return "agent", nil
}

func (f *fakeOps) RootAPIEndpoint(any) (string, string) {
	if f.rootSource == "" {
		f.rootSource = "test"
	}
	return f.rootEndpoint, f.rootSource
}

func (f *fakeOps) EnvironmentAPIEndpoint(any) (string, bool) {
	return f.envEndpoint, f.envEndpoint != ""
}

func (f *fakeOps) StartRuntime(context.Context, any) error {
	f.starts++
	f.sequence = append(f.sequence, "start")
	return nil
}

func (f *fakeOps) EnsureConnected(_ context.Context, _ any, spec tunnel.ConnectSpec) (*tunnel.ConnectStatus, error) {
	if f.connectErr != nil {
		return nil, f.connectErr
	}
	f.connectSpecs = append(f.connectSpecs, spec)
	f.sequence = append(f.sequence, "connect:"+spec.Name)
	return &tunnel.ConnectStatus{Name: spec.Name, ListenAddress: spec.ListenAddress}, nil
}

func (f *fakeOps) RemoveConnect(_ context.Context, _ any, name, listenAddress string) error {
	f.removed = append(f.removed, "connect:"+name+"@"+listenAddress)
	f.sequence = append(f.sequence, "remove-connect")
	return nil
}

func (f *fakeOps) EnsureServed(_ context.Context, _ any, spec tunnel.ServeSpec) (*tunnel.ServeStatus, error) {
	if f.serveErr != nil {
		return nil, f.serveErr
	}
	f.serveSpecs = append(f.serveSpecs, spec)
	f.sequence = append(f.sequence, "serve")
	return &tunnel.ServeStatus{Name: spec.Name, Mode: spec.Mode, BackendTarget: spec.BackendTarget}, nil
}

func (f *fakeOps) RemoveServe(context.Context, any, string) error {
	f.sequence = append(f.sequence, "remove-serve")
	return nil
}

func (f *fakeOps) EnsurePublished(_ context.Context, _ any, spec catalog.PublishSpec) (*catalog.Advertisement, error) {
	if f.publishErr != nil {
		return nil, f.publishErr
	}
	f.publishSpecs = append(f.publishSpecs, spec)
	f.sequence = append(f.sequence, "publish")
	return &catalog.Advertisement{ID: "adv_abcdefghijkl", Name: spec.Name}, nil
}

func (f *fakeOps) Retract(context.Context, any, string) error {
	f.sequence = append(f.sequence, "retract")
	return nil
}

func (f *fakeOps) Close(context.Context, any) error {
	f.closed++
	f.sequence = append(f.sequence, "close")
	return nil
}

func TestSubsystemBootstrapConnects(t *testing.T) {
	stubConnectAddress(t)
	ops := &fakeOps{rootEndpoint: "http://controller.example", envEndpoint: "http://controller.example"}
	cfg := baseTestConfig()
	cfg.Advertisement.Publish = boolPtr(false)

	subsystem, err := newSubsystemWithOps(SubsystemOptions{
		Config: cfg,
		Defaults: Defaults{
			InstanceName:    "mcp-gateway",
			Description:     "MCP tool gateway",
			TunnelMode:      "tcp",
			AgentNamePrefix: "mcp-gateway",
		},
		ConnectTargets: []ConnectTarget{{Key: "filesystem", Tunnel: "filesystem-relay"}},
	}, ops)
	if err != nil {
		t.Fatalf("newSubsystemWithOps returned error: %v", err)
	}
	if !ops.newOpts.WithRuntime {
		t.Fatal("expected standalone agent with runtime")
	}
	if err := subsystem.BootstrapConnects(context.Background()); err != nil {
		t.Fatalf("BootstrapConnects returned error: %v", err)
	}
	if ops.starts != 1 {
		t.Fatalf("StartRuntime calls = %d, want 1", ops.starts)
	}
	if len(ops.connectSpecs) != 1 || ops.connectSpecs[0].Name != "filesystem-relay" {
		t.Fatalf("unexpected connect specs: %#v", ops.connectSpecs)
	}
	if !strings.HasPrefix(ops.connectSpecs[0].ListenAddress, "127.0.0.1:") {
		t.Fatalf("listen address = %q", ops.connectSpecs[0].ListenAddress)
	}
	if address, ok := subsystem.ConnectAddress("filesystem"); !ok || address != ops.connectSpecs[0].ListenAddress {
		t.Fatalf("ConnectAddress = %q, %v", address, ok)
	}
}

func TestSubsystemServingAndPublishingAreIndependent(t *testing.T) {
	ops := &fakeOps{rootEndpoint: "http://controller.example", envEndpoint: "http://controller.example"}
	cfg := baseTestConfig()
	cfg.Serve = &ServeConfig{Enabled: true, Grants: []string{"alice@example.com"}}

	subsystem, err := newSubsystemWithOps(SubsystemOptions{
		Config:        cfg,
		Defaults:      gatewayDefaults(),
		Capabilities:  []string{"mcp-tools"},
		ServeWanted:   true,
		PublishWanted: true,
	}, ops)
	if err != nil {
		t.Fatalf("newSubsystemWithOps returned error: %v", err)
	}
	if err := subsystem.StartServing(context.Background(), "127.0.0.1:8080"); err != nil {
		t.Fatalf("StartServing returned error: %v", err)
	}
	if len(ops.serveSpecs) != 1 {
		t.Fatalf("serve specs = %#v", ops.serveSpecs)
	}
	if len(ops.publishSpecs) != 0 {
		t.Fatalf("publishing should be independent of serving: %#v", ops.publishSpecs)
	}
	if err := subsystem.StartPublishing(context.Background()); err != nil {
		t.Fatalf("StartPublishing returned error: %v", err)
	}
	if len(ops.publishSpecs) != 1 {
		t.Fatalf("publish specs = %#v", ops.publishSpecs)
	}
	if got := ops.sequence; len(got) < 3 || got[0] != "start" || got[1] != "serve" || got[2] != "publish" {
		t.Fatalf("unexpected sequence: %#v", got)
	}
}

func TestSubsystemPublishesWithoutServeRuntime(t *testing.T) {
	ops := &fakeOps{rootEndpoint: "http://controller.example"}
	cfg := baseTestConfig()

	subsystem, err := newSubsystemWithOps(SubsystemOptions{
		Config:        cfg,
		Defaults:      gatewayDefaults(),
		Capabilities:  []string{"mcp-tools"},
		PublishWanted: true,
	}, ops)
	if err != nil {
		t.Fatalf("newSubsystemWithOps returned error: %v", err)
	}
	if ops.newOpts.WithRuntime {
		t.Fatal("expected publish-only agent without runtime")
	}
	if err := subsystem.StartPublishing(context.Background()); err != nil {
		t.Fatalf("StartPublishing returned error: %v", err)
	}
	if ops.starts != 0 {
		t.Fatalf("StartRuntime calls = %d, want 0", ops.starts)
	}
	if len(ops.publishSpecs) != 1 {
		t.Fatalf("publish specs = %#v", ops.publishSpecs)
	}
	if ops.publishSpecs[0].Capabilities[0].Name != "mcp-tools" {
		t.Fatalf("capabilities = %#v", ops.publishSpecs[0].Capabilities)
	}
}

func TestSubsystemCloseOrder(t *testing.T) {
	stubConnectAddress(t)
	ops := &fakeOps{rootEndpoint: "http://controller.example", envEndpoint: "http://controller.example"}
	cfg := baseTestConfig()
	cfg.Serve = &ServeConfig{Enabled: true}

	subsystem, err := newSubsystemWithOps(SubsystemOptions{
		Config:         cfg,
		Defaults:       gatewayDefaults(),
		Capabilities:   []string{"mcp-tools"},
		ConnectTargets: []ConnectTarget{{Key: "filesystem", Tunnel: "filesystem-relay"}},
		ServeWanted:    true,
		PublishWanted:  true,
	}, ops)
	if err != nil {
		t.Fatalf("newSubsystemWithOps returned error: %v", err)
	}
	if err := subsystem.BootstrapConnects(context.Background()); err != nil {
		t.Fatalf("BootstrapConnects returned error: %v", err)
	}
	if err := subsystem.StartServing(context.Background(), "127.0.0.1:8080"); err != nil {
		t.Fatalf("StartServing returned error: %v", err)
	}
	if err := subsystem.StartPublishing(context.Background()); err != nil {
		t.Fatalf("StartPublishing returned error: %v", err)
	}
	if err := subsystem.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	wantSuffix := []string{"retract", "remove-serve", "remove-connect", "close"}
	got := ops.sequence[len(ops.sequence)-len(wantSuffix):]
	for i := range wantSuffix {
		if got[i] != wantSuffix[i] {
			t.Fatalf("cleanup sequence = %#v, want suffix %#v", ops.sequence, wantSuffix)
		}
	}
}

func TestSubsystemEndpointMismatchFails(t *testing.T) {
	ops := &fakeOps{rootEndpoint: "http://other.example", envEndpoint: "http://other.example"}
	_, err := newSubsystemWithOps(SubsystemOptions{
		Config:        baseTestConfig(),
		Defaults:      gatewayDefaults(),
		Capabilities:  []string{"mcp-tools"},
		PublishWanted: true,
	}, ops)
	if err == nil {
		t.Fatal("expected endpoint mismatch error")
	}
}

func TestSubsystemConnectFailureCleansUp(t *testing.T) {
	stubConnectAddress(t)
	ops := &fakeOps{
		rootEndpoint: "http://controller.example",
		envEndpoint:  "http://controller.example",
		connectErr:   errors.New("boom"),
	}
	cfg := baseTestConfig()
	cfg.Advertisement.Publish = boolPtr(false)

	subsystem, err := newSubsystemWithOps(SubsystemOptions{
		Config:         cfg,
		Defaults:       gatewayDefaults(),
		ConnectTargets: []ConnectTarget{{Key: "filesystem", Tunnel: "filesystem-relay"}},
	}, ops)
	if err != nil {
		t.Fatalf("newSubsystemWithOps returned error: %v", err)
	}
	if err := subsystem.BootstrapConnects(context.Background()); err == nil {
		t.Fatal("expected connect failure")
	}
	if ops.closed != 1 {
		t.Fatalf("Close calls = %d, want 1", ops.closed)
	}
}

func TestSubsystemRequiresCapabilitiesWhenPublishing(t *testing.T) {
	ops := &fakeOps{rootEndpoint: "http://controller.example"}
	_, err := newSubsystemWithOps(SubsystemOptions{
		Config:        baseTestConfig(),
		Defaults:      gatewayDefaults(),
		PublishWanted: true,
	}, ops)
	if err == nil || !strings.Contains(err.Error(), "derived capabilities are required") {
		t.Fatalf("expected capabilities error, got %v", err)
	}
}

func baseTestConfig() *Config {
	return &Config{
		Enabled:      true,
		APIEndpoint:  "http://controller.example",
		InstanceName: "engineering",
		TunnelMode:   "tcp",
		Advertisement: &AdvertisementConfig{
			WorkgroupIDs: []string{"wg_abcdefghijkl"},
			ContractID:   "con_abcdefghijkl",
		},
	}
}

func gatewayDefaults() Defaults {
	return Defaults{
		InstanceName:    "mcp-gateway",
		Description:     "MCP tool gateway",
		TunnelMode:      "tcp",
		AgentNamePrefix: "mcp-gateway",
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func stubConnectAddress(t *testing.T) {
	t.Helper()

	orig := allocateConnectAddress
	next := 0
	allocateConnectAddress = func() (string, error) {
		next++
		return fmt.Sprintf("127.0.0.1:%d", 40000+next), nil
	}
	t.Cleanup(func() {
		allocateConnectAddress = orig
	})
}
