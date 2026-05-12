package agora

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/michaelquigley/df/dl"
	"github.com/openziti/agora/sdk/agent"
	"github.com/openziti/agora/sdk/agent/catalog"
	"github.com/openziti/agora/sdk/agent/tunnel"
)

const cleanupTimeout = 5 * time.Second

var (
	workgroupIDPattern     = regexp.MustCompile(`^wg_[a-z0-9]{12}$`)
	contractIDPattern      = regexp.MustCompile(`^con_[a-z0-9]{12}$`)
	allocateConnectAddress = allocateLoopbackPort
)

// ConnectTarget describes one Agora Layer 1 tunnel to connect locally.
type ConnectTarget struct {
	Key    string
	Tunnel string
}

// SubsystemOptions configures the shared Agora subsystem.
type SubsystemOptions struct {
	Config         *Config
	Defaults       Defaults
	Capabilities   []string
	ConnectTargets []ConnectTarget
	ServeWanted    bool
	PublishWanted  bool
}

// Subsystem manages Agora agent, tunnel, and catalog lifecycle.
type Subsystem struct {
	cfg           *Config
	identity      Identity
	capabilities  []string
	targets       []ConnectTarget
	serveWanted   bool
	publishWanted bool
	wantRuntime   bool
	ops           agoraOps
	agent         any

	runtimeStarted bool
	advertisement  *catalog.Advertisement
	serveStatus    *tunnel.ServeStatus
	connects       map[string]*tunnel.ConnectStatus
	closed         bool

	log *dl.Builder
}

type agoraOps interface {
	NewStandalone(agent.StandaloneOptions) (any, error)
	RootAPIEndpoint(any) (endpoint, source string)
	EnvironmentAPIEndpoint(any) (endpoint string, ok bool)
	StartRuntime(context.Context, any) error
	EnsureConnected(context.Context, any, tunnel.ConnectSpec) (*tunnel.ConnectStatus, error)
	RemoveConnect(context.Context, any, string, string) error
	EnsureServed(context.Context, any, tunnel.ServeSpec) (*tunnel.ServeStatus, error)
	RemoveServe(context.Context, any, string) error
	EnsurePublished(context.Context, any, catalog.PublishSpec) (*catalog.Advertisement, error)
	Retract(context.Context, any, string) error
	Close(context.Context, any) error
}

type defaultOps struct{}

func (defaultOps) NewStandalone(opts agent.StandaloneOptions) (any, error) {
	return agent.NewStandalone(opts)
}

func (defaultOps) RootAPIEndpoint(handle any) (string, string) {
	a := handle.(*agent.Agent)
	if a.EnvRoot() == nil {
		return "", "unset"
	}
	return a.EnvRoot().APIEndpoint()
}

func (defaultOps) EnvironmentAPIEndpoint(handle any) (string, bool) {
	a := handle.(*agent.Agent)
	if a.Environment() == nil {
		return "", false
	}
	return a.Environment().APIEndpoint, true
}

func (defaultOps) StartRuntime(ctx context.Context, handle any) error {
	return handle.(*agent.Agent).StartRuntime(ctx)
}

func (defaultOps) EnsureConnected(ctx context.Context, handle any, spec tunnel.ConnectSpec) (*tunnel.ConnectStatus, error) {
	return tunnel.EnsureConnected(ctx, handle.(*agent.Agent), spec)
}

func (defaultOps) RemoveConnect(ctx context.Context, handle any, name, listenAddress string) error {
	return tunnel.RemoveConnect(ctx, handle.(*agent.Agent), name, listenAddress)
}

func (defaultOps) EnsureServed(ctx context.Context, handle any, spec tunnel.ServeSpec) (*tunnel.ServeStatus, error) {
	return tunnel.EnsureServed(ctx, handle.(*agent.Agent), spec)
}

func (defaultOps) RemoveServe(ctx context.Context, handle any, name string) error {
	return tunnel.RemoveServe(ctx, handle.(*agent.Agent), name)
}

func (defaultOps) EnsurePublished(ctx context.Context, handle any, spec catalog.PublishSpec) (*catalog.Advertisement, error) {
	return catalog.EnsurePublished(ctx, handle.(*agent.Agent), spec)
}

func (defaultOps) Retract(ctx context.Context, handle any, advertisementID string) error {
	return catalog.Retract(ctx, handle.(*agent.Agent), advertisementID)
}

func (defaultOps) Close(ctx context.Context, handle any) error {
	return handle.(*agent.Agent).Close(ctx)
}

// NewSubsystem creates an Agora subsystem when Agora is enabled.
func NewSubsystem(opts SubsystemOptions) (*Subsystem, error) {
	return newSubsystemWithOps(opts, defaultOps{})
}

func newSubsystemWithOps(opts SubsystemOptions, ops agoraOps) (*Subsystem, error) {
	cfg := opts.Config
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}
	if ops == nil {
		ops = defaultOps{}
	}

	identity, err := resolveIdentity(cfg, opts.Defaults)
	if err != nil {
		return nil, err
	}

	serveWanted := opts.ServeWanted && ServeEnabled(cfg)
	publishWanted := opts.PublishWanted && AdvertisementPublish(cfg)
	targets := normalizeConnectTargets(opts.ConnectTargets)
	wantRuntime := serveWanted || len(targets) > 0

	capabilities := advertisementCapabilities(cfg, opts.Capabilities)
	if err := validateConfig(cfg, identity, targets, publishWanted, capabilities); err != nil {
		return nil, err
	}

	handle, err := ops.NewStandalone(agent.StandaloneOptions{
		Name:        identity.AgentName,
		Description: identity.Description,
		EnvRoot:     cfg.EnvRoot,
		WithRuntime: wantRuntime,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize agora agent: %w", err)
	}

	if err := validateAgentEndpoint(cfg, wantRuntime, ops, handle); err != nil {
		_ = ops.Close(context.Background(), handle)
		return nil, err
	}

	return &Subsystem{
		cfg:           cfg,
		identity:      identity,
		capabilities:  append([]string(nil), capabilities...),
		targets:       targets,
		serveWanted:   serveWanted,
		publishWanted: publishWanted,
		wantRuntime:   wantRuntime,
		ops:           ops,
		agent:         handle,
		connects:      map[string]*tunnel.ConnectStatus{},
		log:           dl.Log().With("agent", identity.AgentName).With("instance", identity.InstanceName),
	}, nil
}

func advertisementCapabilities(cfg *Config, derived []string) []string {
	if cfg != nil && cfg.Advertisement != nil && len(cfg.Advertisement.Capabilities) > 0 {
		return append([]string(nil), cfg.Advertisement.Capabilities...)
	}
	return Derive(nil, derived)
}

func normalizeConnectTargets(targets []ConnectTarget) []ConnectTarget {
	normalized := make([]ConnectTarget, 0, len(targets))
	for _, target := range targets {
		normalized = append(normalized, ConnectTarget{
			Key:    strings.TrimSpace(target.Key),
			Tunnel: strings.TrimSpace(target.Tunnel),
		})
	}
	return normalized
}

func validateConfig(cfg *Config, identity Identity, targets []ConnectTarget, publishWanted bool, capabilities []string) error {
	if strings.TrimSpace(cfg.APIEndpoint) == "" {
		return fmt.Errorf("agora.api_endpoint is required when agora is enabled")
	}

	if publishWanted {
		if cfg.Advertisement == nil || len(cfg.Advertisement.WorkgroupIDs) == 0 {
			return fmt.Errorf("agora.advertisement.workgroup_ids requires at least one ID when publishing is enabled")
		}
		for _, id := range cfg.Advertisement.WorkgroupIDs {
			if !workgroupIDPattern.MatchString(strings.TrimSpace(id)) {
				return fmt.Errorf("invalid agora workgroup id '%s'", id)
			}
		}
		if cfg.Advertisement.ContractID != "" && !contractIDPattern.MatchString(strings.TrimSpace(cfg.Advertisement.ContractID)) {
			return fmt.Errorf("invalid agora contract id '%s'", cfg.Advertisement.ContractID)
		}
		if len(capabilities) == 0 {
			return fmt.Errorf("agora.advertisement.capabilities or derived capabilities are required when publishing is enabled")
		}
		for _, capability := range capabilities {
			if strings.TrimSpace(capability) == "" {
				return fmt.Errorf("agora.advertisement.capabilities cannot contain empty entries")
			}
		}
	}

	if identity.TunnelMode == "" {
		return fmt.Errorf("agora tunnel identity is unresolved")
	}

	seen := map[string]struct{}{}
	for _, target := range targets {
		key := strings.TrimSpace(target.Key)
		if key == "" {
			return fmt.Errorf("agora connect target key is required")
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate agora connect target key '%s'", key)
		}
		seen[key] = struct{}{}
		if strings.TrimSpace(target.Tunnel) == "" {
			return fmt.Errorf("agora connect target '%s' tunnel is required", key)
		}
	}

	return nil
}

func validateAgentEndpoint(cfg *Config, wantRuntime bool, ops agoraOps, handle any) error {
	rootEndpoint, source := ops.RootAPIEndpoint(handle)
	if strings.TrimSpace(rootEndpoint) == "" {
		return fmt.Errorf("agora environment api endpoint is not configured")
	}
	if !sameEndpoint(rootEndpoint, cfg.APIEndpoint) {
		return fmt.Errorf("agora.api_endpoint '%s' does not match enrolled environment endpoint '%s' from %s", cfg.APIEndpoint, rootEndpoint, source)
	}

	if wantRuntime {
		envEndpoint, ok := ops.EnvironmentAPIEndpoint(handle)
		if !ok || strings.TrimSpace(envEndpoint) == "" {
			return fmt.Errorf("agora runtime requires an enrolled environment api endpoint")
		}
		if !sameEndpoint(envEndpoint, cfg.APIEndpoint) {
			return fmt.Errorf("agora.api_endpoint '%s' does not match enrolled runtime environment endpoint '%s'", cfg.APIEndpoint, envEndpoint)
		}
	}

	return nil
}

func sameEndpoint(a, b string) bool {
	return strings.TrimRight(strings.TrimSpace(a), "/") == strings.TrimRight(strings.TrimSpace(b), "/")
}

// BootstrapConnects establishes loopback listeners for configured upstream tunnels.
func (s *Subsystem) BootstrapConnects(ctx context.Context) error {
	if s == nil || len(s.targets) == 0 {
		return nil
	}
	if err := s.startRuntime(ctx); err != nil {
		return err
	}

	for _, target := range s.targets {
		listenAddress, err := allocateConnectAddress()
		if err != nil {
			_ = s.Close()
			return err
		}
		status, err := s.ops.EnsureConnected(ctx, s.agent, tunnel.ConnectSpec{
			Name:          target.Tunnel,
			ListenAddress: listenAddress,
		})
		if err != nil {
			_ = s.Close()
			return fmt.Errorf("ensure agora connect for '%s': %w", target.Key, err)
		}
		if status.ListenAddress == "" {
			status.ListenAddress = listenAddress
		}
		s.connects[target.Key] = status
		s.log.Infof("agora connect ready for '%s' service='%s' listen='%s'", target.Key, status.Name, status.ListenAddress)
	}

	return nil
}

// StartServing ensures the Agora serve actor forwards to backendTarget.
func (s *Subsystem) StartServing(ctx context.Context, backendTarget string) error {
	if s == nil || !s.serveWanted {
		return nil
	}
	backendTarget = strings.TrimSpace(backendTarget)
	if backendTarget == "" {
		return fmt.Errorf("agora serve backend target is required")
	}
	if err := s.startRuntime(ctx); err != nil {
		return err
	}
	status, err := s.ops.EnsureServed(ctx, s.agent, tunnel.ServeSpec{
		Name:          s.identity.InstanceName,
		Mode:          tunnel.Mode(s.identity.TunnelMode),
		BackendTarget: backendTarget,
		GrantEmails:   append([]string(nil), s.cfg.Serve.Grants...),
	})
	if err != nil {
		_ = s.Close()
		return fmt.Errorf("ensure agora serve: %w", err)
	}
	s.serveStatus = status
	s.log.Infof("agora serve ready name='%s' mode='%s' backend='%s'", status.Name, status.Mode, status.BackendTarget)

	return nil
}

// StartPublishing ensures the Agora catalog advertisement exists.
func (s *Subsystem) StartPublishing(ctx context.Context) error {
	if s == nil || !s.publishWanted {
		return nil
	}
	advertisement, err := s.ops.EnsurePublished(ctx, s.agent, catalog.PublishSpec{
		Name:              s.identity.InstanceName,
		Description:       s.identity.Description,
		Capabilities:      s.catalogCapabilities(),
		WorkgroupScopeIDs: append([]string(nil), s.cfg.Advertisement.WorkgroupIDs...),
		TunnelMode:        catalog.TunnelMode(s.identity.TunnelMode),
		ContractID:        s.cfg.Advertisement.ContractID,
	})
	if err != nil {
		_ = s.Close()
		return fmt.Errorf("publish agora advertisement: %w", err)
	}
	s.advertisement = advertisement
	s.log.Infof("agora advertisement published id='%s' name='%s'", advertisement.ID, advertisement.Name)

	return nil
}

func (s *Subsystem) startRuntime(ctx context.Context) error {
	if !s.wantRuntime || s.runtimeStarted {
		return nil
	}
	if err := s.ops.StartRuntime(ctx, s.agent); err != nil {
		return fmt.Errorf("start agora runtime: %w", err)
	}
	s.runtimeStarted = true
	s.log.Info("agora runtime started")
	return nil
}

func (s *Subsystem) catalogCapabilities() []catalog.Capability {
	capabilities := make([]catalog.Capability, 0, len(s.capabilities))
	for _, capability := range s.capabilities {
		capabilities = append(capabilities, catalog.Capability{Name: capability})
	}
	return capabilities
}

// ConnectAddress returns the local loopback address for a connected target.
func (s *Subsystem) ConnectAddress(key string) (string, bool) {
	if s == nil {
		return "", false
	}
	status, ok := s.connects[key]
	if !ok || status == nil || status.ListenAddress == "" {
		return "", false
	}
	return status.ListenAddress, true
}

// Close tears down Agora catalog, serve, connect, and agent resources.
func (s *Subsystem) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true

	var firstErr error
	if s.advertisement != nil {
		if err := s.withCleanupContext(func(ctx context.Context) error {
			return s.ops.Retract(ctx, s.agent, s.advertisement.ID)
		}); err != nil {
			s.log.Warnf("failed to retract agora advertisement '%s': %v", s.advertisement.ID, err)
			firstErr = err
		}
		s.advertisement = nil
	}

	if s.serveStatus != nil {
		if err := s.withCleanupContext(func(ctx context.Context) error {
			return s.ops.RemoveServe(ctx, s.agent, s.identity.InstanceName)
		}); err != nil {
			s.log.Warnf("failed to remove agora serve '%s': %v", s.identity.InstanceName, err)
			if firstErr == nil {
				firstErr = err
			}
		}
		s.serveStatus = nil
	}

	for key, status := range s.connects {
		status := status
		if status == nil {
			continue
		}
		if err := s.withCleanupContext(func(ctx context.Context) error {
			return s.ops.RemoveConnect(ctx, s.agent, status.Name, status.ListenAddress)
		}); err != nil {
			s.log.Warnf("failed to remove agora connect '%s': %v", key, err)
			if firstErr == nil {
				firstErr = err
			}
		}
		delete(s.connects, key)
	}

	if s.agent != nil {
		if err := s.withCleanupContext(func(ctx context.Context) error {
			return s.ops.Close(ctx, s.agent)
		}); err != nil {
			s.log.Warnf("failed to close agora agent: %v", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}

func (s *Subsystem) withCleanupContext(fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	return fn(ctx)
}

func allocateLoopbackPort() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("allocate agora connect port: %w", err)
	}
	defer listener.Close()
	return listener.Addr().String(), nil
}
