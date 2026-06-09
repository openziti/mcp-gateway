package agora

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/openziti/agora/sdk/agent"
	"github.com/openziti/agora/sdk/agent/catalog"
	"github.com/openziti/agora/sdk/agent/tunnel"
)

// fakeListener is a minimal net.Listener that records when it is closed.
type fakeListener struct {
	closed bool
}

func (f *fakeListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (f *fakeListener) Close() error              { f.closed = true; return nil }
func (f *fakeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

// fakeOps is a tiny in-memory controller standing in for the Agora SDK
// primitives so the subsystem stays unit-testable without a live controller.
type fakeOps struct {
	rootEndpoint string
	rootSource   string

	newOpts agent.StandaloneOptions

	// tunnels models the controller's tunnel records: name -> mode.
	tunnels map[string]string

	// behavior toggles
	listenErr      error
	listenErrAfter int // apply listenErr once listen calls exceed this count
	createErr      error
	createConflict bool // Create simulates losing a create race
	attachErr      error
	publishErr     error

	// recordings
	created      []tunnel.Spec
	deleted      []string
	attached     []string
	detached     []string
	dialed       []string
	publishSpecs []catalog.PublishSpec
	listeners    []*fakeListener
	sequence     []string
	listenCalls  int
	closed       int
}

func newFakeOps() *fakeOps {
	return &fakeOps{rootEndpoint: "http://controller.example", tunnels: map[string]string{}}
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

func (f *fakeOps) Create(_ context.Context, _ any, spec tunnel.Spec) (*tunnel.Tunnel, error) {
	f.created = append(f.created, spec)
	f.sequence = append(f.sequence, "create:"+spec.Name)
	if f.createConflict {
		f.tunnels[spec.Name] = "tcp" // a racing process provisioned it
		return nil, fmt.Errorf("create: %w", tunnel.ErrConflict)
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	if _, ok := f.tunnels[spec.Name]; ok {
		return nil, fmt.Errorf("create: %w", tunnel.ErrConflict)
	}
	f.tunnels[spec.Name] = string(spec.Mode)
	return &tunnel.Tunnel{ID: "tt_" + spec.Name, Name: spec.Name, Mode: spec.Mode}, nil
}

func (f *fakeOps) GetTunnel(_ context.Context, _ any, name string) (*tunnel.Tunnel, error) {
	mode, ok := f.tunnels[name]
	if !ok {
		return nil, fmt.Errorf("get %q: %w", name, tunnel.ErrNotFound)
	}
	return &tunnel.Tunnel{ID: "tt_" + name, Name: name, Mode: tunnel.Mode(mode)}, nil
}

func (f *fakeOps) Listen(_ context.Context, _ any, name string) (net.Listener, error) {
	f.listenCalls++
	f.sequence = append(f.sequence, "listen:"+name)
	if f.listenErr != nil && f.listenCalls > f.listenErrAfter {
		return nil, f.listenErr
	}
	if _, ok := f.tunnels[name]; !ok {
		return nil, fmt.Errorf("listen %q: %w", name, tunnel.ErrNotFound)
	}
	l := &fakeListener{}
	f.listeners = append(f.listeners, l)
	return l, nil
}

func (f *fakeOps) Delete(_ context.Context, _ any, t *tunnel.Tunnel) error {
	f.deleted = append(f.deleted, t.Name)
	f.sequence = append(f.sequence, "delete")
	delete(f.tunnels, t.Name)
	return nil
}

func (f *fakeOps) Attach(_ context.Context, _ any, name string) (*tunnel.Attachment, error) {
	if f.attachErr != nil {
		return nil, f.attachErr
	}
	f.attached = append(f.attached, name)
	f.sequence = append(f.sequence, "attach:"+name)
	return &tunnel.Attachment{ID: "ta_" + name, TunnelID: "tt_" + name}, nil
}

func (f *fakeOps) Dial(_ context.Context, _ any, name string) (net.Conn, error) {
	f.dialed = append(f.dialed, name)
	return nil, fmt.Errorf("dialed %q", name)
}

func (f *fakeOps) Detach(_ context.Context, _ any, name string) error {
	f.detached = append(f.detached, name)
	f.sequence = append(f.sequence, "detach:"+name)
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

func TestNewSubsystemUsesRuntimelessAgent(t *testing.T) {
	ops := newFakeOps()
	sub, err := newSubsystemWithOps(SubsystemOptions{
		Config:        baseTestConfig(),
		Defaults:      gatewayDefaults(),
		Capabilities:  []string{"mcp-tools"},
		PublishWanted: true,
	}, ops)
	if err != nil {
		t.Fatalf("newSubsystemWithOps returned error: %v", err)
	}
	if sub == nil {
		t.Fatal("expected subsystem")
	}
	if ops.newOpts.WithRuntime {
		t.Fatal("expected runtime-less agent (WithRuntime=false)")
	}
}

func TestSubsystemPublishesHTTPModeWithDialKeyName(t *testing.T) {
	ops := newFakeOps()
	sub, err := newSubsystemWithOps(SubsystemOptions{
		Config:        baseTestConfig(),
		Defaults:      gatewayDefaults(),
		Capabilities:  []string{"mcp-tools"},
		PublishWanted: true,
	}, ops)
	if err != nil {
		t.Fatalf("newSubsystemWithOps returned error: %v", err)
	}
	if err := sub.StartPublishing(context.Background()); err != nil {
		t.Fatalf("StartPublishing returned error: %v", err)
	}
	if len(ops.publishSpecs) != 1 {
		t.Fatalf("publish specs = %#v", ops.publishSpecs)
	}
	spec := ops.publishSpecs[0]
	// serve.tunnel unset ⇒ advertised name follows instance name.
	if spec.Name != "engineering" {
		t.Fatalf("publish name = %q, want %q", spec.Name, "engineering")
	}
	if spec.TunnelMode != catalog.TunnelHTTP {
		t.Fatalf("publish tunnel mode = %q, want %q", spec.TunnelMode, catalog.TunnelHTTP)
	}
	if len(spec.Capabilities) == 0 || spec.Capabilities[0].Name != "mcp-tools" {
		t.Fatalf("capabilities = %#v", spec.Capabilities)
	}
}

func TestSubsystemPublishNameFollowsServeTunnel(t *testing.T) {
	cfg := baseTestConfig()
	cfg.Serve = &ServeConfig{Enabled: true, Tunnel: "persistent-share"}

	ops := newFakeOps()
	sub, err := newSubsystemWithOps(SubsystemOptions{
		Config:        cfg,
		Defaults:      gatewayDefaults(),
		Capabilities:  []string{"mcp-tools"},
		PublishWanted: true, // serve not wanted in this process; name still resolves
	}, ops)
	if err != nil {
		t.Fatalf("newSubsystemWithOps returned error: %v", err)
	}
	if err := sub.StartPublishing(context.Background()); err != nil {
		t.Fatalf("StartPublishing returned error: %v", err)
	}
	if got := ops.publishSpecs[0].Name; got != "persistent-share" {
		t.Fatalf("publish name = %q, want %q (must follow serve.tunnel, not instance_name)", got, "persistent-share")
	}
}

func TestSubsystemEndpointMismatchFails(t *testing.T) {
	ops := newFakeOps()
	ops.rootEndpoint = "http://other.example"
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

func TestSubsystemRequiresCapabilitiesWhenPublishing(t *testing.T) {
	ops := newFakeOps()
	_, err := newSubsystemWithOps(SubsystemOptions{
		Config:        baseTestConfig(),
		Defaults:      gatewayDefaults(),
		PublishWanted: true,
	}, ops)
	if err == nil || !strings.Contains(err.Error(), "derived capabilities are required") {
		t.Fatalf("expected capabilities error, got %v", err)
	}
}

func TestSubsystemCloseOrder(t *testing.T) {
	cfg := baseTestConfig()
	cfg.Serve = &ServeConfig{Enabled: true}

	ops := newFakeOps()
	sub, err := newSubsystemWithOps(SubsystemOptions{
		Config:        cfg,
		Defaults:      gatewayDefaults(),
		Capabilities:  []string{"mcp-tools"},
		ServeWanted:   true,
		PublishWanted: true,
	}, ops)
	if err != nil {
		t.Fatalf("newSubsystemWithOps returned error: %v", err)
	}
	if _, err := sub.Serve(context.Background()); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	if err := sub.Dialer().Attach(context.Background(), "relay"); err != nil {
		t.Fatalf("Attach returned error: %v", err)
	}
	if err := sub.StartPublishing(context.Background()); err != nil {
		t.Fatalf("StartPublishing returned error: %v", err)
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	want := []string{"retract", "delete", "detach:relay", "close"}
	if len(ops.sequence) < len(want) {
		t.Fatalf("sequence too short: %#v", ops.sequence)
	}
	got := ops.sequence[len(ops.sequence)-len(want):]
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cleanup sequence = %#v, want suffix %#v", ops.sequence, want)
		}
	}
}

func baseTestConfig() *Config {
	return &Config{
		Enabled:      true,
		APIEndpoint:  "http://controller.example",
		InstanceName: "engineering",
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
		AgentNamePrefix: "mcp-gateway",
	}
}

// newTestSubsystem builds a subsystem backed by a fresh fakeOps for serve/dial
// tests, returning both so the test can configure and inspect the fake.
func newTestSubsystem(t *testing.T, configure func(*Config)) (*Subsystem, *fakeOps) {
	t.Helper()

	cfg := baseTestConfig()
	if configure != nil {
		configure(cfg)
	}
	ops := newFakeOps()
	sub, err := newSubsystemWithOps(SubsystemOptions{
		Config:       cfg,
		Defaults:     gatewayDefaults(),
		Capabilities: []string{"mcp-tools"},
		ServeWanted:  ServeEnabled(cfg),
	}, ops)
	if err != nil {
		t.Fatalf("newSubsystemWithOps returned error: %v", err)
	}
	return sub, ops
}
