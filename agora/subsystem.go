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
	workgroupIDPattern = regexp.MustCompile(`^wg_[a-z0-9]{12}$`)
	contractIDPattern  = regexp.MustCompile(`^con_[a-z0-9]{12}$`)
)

// SubsystemOptions configures the shared Agora subsystem.
type SubsystemOptions struct {
	Config        *Config
	Defaults      Defaults
	Capabilities  []string
	ServeWanted   bool
	PublishWanted bool
}

// Subsystem manages the single runtime-less Agora agent and its serve, dial,
// and catalog lifecycle. One *agent.Agent backs the serve listener, every
// backend attach/dial, and catalog publish/retract.
type Subsystem struct {
	cfg           *Config
	identity      Identity
	capabilities  []string
	serveWanted   bool
	publishWanted bool
	ops           agoraOps
	agent         any

	advertisement *catalog.Advertisement
	serve         *Serve
	dialer        *Dialer
	closed        bool

	log *dl.Builder
}

// agoraOps is the SDK seam that keeps the subsystem unit-testable without a
// live controller. It wraps the thin Listen/Dial primitives plus catalog.
type agoraOps interface {
	NewStandalone(agent.StandaloneOptions) (any, error)
	RootAPIEndpoint(any) (endpoint, source string)

	// serve side
	Create(context.Context, any, tunnel.Spec) (*tunnel.Tunnel, error)
	GetTunnel(context.Context, any, string) (*tunnel.Tunnel, error)
	Listen(context.Context, any, string) (net.Listener, error)
	Delete(context.Context, any, *tunnel.Tunnel) error

	// dial side
	Attach(context.Context, any, string) (*tunnel.Attachment, error)
	Dial(context.Context, any, string) (net.Conn, error)
	Detach(context.Context, any, string) error

	// catalog
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

func (defaultOps) Create(ctx context.Context, handle any, spec tunnel.Spec) (*tunnel.Tunnel, error) {
	return tunnel.Create(ctx, handle.(*agent.Agent), spec)
}

func (defaultOps) GetTunnel(ctx context.Context, handle any, nameOrID string) (*tunnel.Tunnel, error) {
	return tunnel.Get(ctx, handle.(*agent.Agent), nameOrID)
}

func (defaultOps) Listen(ctx context.Context, handle any, nameOrID string) (net.Listener, error) {
	return tunnel.Listen(ctx, handle.(*agent.Agent), nameOrID)
}

func (defaultOps) Delete(ctx context.Context, handle any, t *tunnel.Tunnel) error {
	return tunnel.Delete(ctx, handle.(*agent.Agent), t)
}

func (defaultOps) Attach(ctx context.Context, handle any, nameOrID string) (*tunnel.Attachment, error) {
	return tunnel.Attach(ctx, handle.(*agent.Agent), nameOrID)
}

func (defaultOps) Dial(ctx context.Context, handle any, nameOrID string) (net.Conn, error) {
	return tunnel.Dial(ctx, handle.(*agent.Agent), nameOrID)
}

func (defaultOps) Detach(ctx context.Context, handle any, nameOrID string) error {
	return tunnel.Detach(ctx, handle.(*agent.Agent), nameOrID)
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

	if publishWanted && !serveWanted {
		if publishExplicit(cfg) {
			return nil, fmt.Errorf("agora.advertisement.publish requires Agora serving")
		}
		publishWanted = false
		dl.Log().Info("skipping agora advertisement publish: Agora serving is disabled")
	}

	// publishing requires workgroup scope IDs (controller-enforced). When
	// publishing is on by *default* and no workgroup IDs are configured,
	// downgrade to serve-only with a notice — an enrolled account without an
	// integration file can still serve. An *explicit* advertisement.publish:
	// true with missing workgroup IDs remains a hard error in validateConfig.
	if publishWanted && !publishExplicit(cfg) && !hasWorkgroupIDs(cfg) {
		publishWanted = false
		dl.Log().Info("skipping agora advertisement publish: no workgroup ids configured (set agora.advertisement.workgroup_ids or an integration file to publish)")
	}

	capabilities := advertisementCapabilities(cfg, opts.Capabilities)
	if err := validateConfig(cfg, publishWanted, capabilities); err != nil {
		return nil, err
	}

	// Listen/Dial are thin primitives with no embedded runtime.
	handle, err := ops.NewStandalone(agent.StandaloneOptions{
		Name:        identity.AgentName,
		Description: identity.Description,
		EnvRoot:     cfg.EnvRoot,
		WithRuntime: false,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize agora agent: %w", err)
	}

	if err := validateAgentEndpoint(cfg, ops, handle); err != nil {
		_ = ops.Close(context.Background(), handle)
		return nil, err
	}

	s := &Subsystem{
		cfg:           cfg,
		identity:      identity,
		capabilities:  append([]string(nil), capabilities...),
		serveWanted:   serveWanted,
		publishWanted: publishWanted,
		ops:           ops,
		agent:         handle,
		log:           dl.Log().With("agent", identity.AgentName).With("instance", identity.InstanceName),
	}
	s.dialer = newDialer(s)
	return s, nil
}

func advertisementCapabilities(cfg *Config, derived []string) []string {
	if cfg != nil && cfg.Advertisement != nil && len(cfg.Advertisement.Capabilities) > 0 {
		return append([]string(nil), cfg.Advertisement.Capabilities...)
	}
	return Derive(nil, derived)
}

func validateConfig(cfg *Config, publishWanted bool, capabilities []string) error {
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

	return nil
}

// validateAgentEndpoint cross-checks the optional agora.api_endpoint config
// value against the enrolled environment. The enrolled environment is the
// source of truth; when the config value is unset, no cross-check applies.
func validateAgentEndpoint(cfg *Config, ops agoraOps, handle any) error {
	rootEndpoint, source := ops.RootAPIEndpoint(handle)
	if strings.TrimSpace(rootEndpoint) == "" {
		return fmt.Errorf("agora environment api endpoint is not configured")
	}
	if strings.TrimSpace(cfg.APIEndpoint) == "" {
		return nil
	}
	if !sameEndpoint(rootEndpoint, cfg.APIEndpoint) {
		return fmt.Errorf("agora.api_endpoint '%s' does not match enrolled environment endpoint '%s' from %s", cfg.APIEndpoint, rootEndpoint, source)
	}
	return nil
}

func sameEndpoint(a, b string) bool {
	return strings.TrimRight(strings.TrimSpace(a), "/") == strings.TrimRight(strings.TrimSpace(b), "/")
}

// ServeTunnelName returns the resolved create-or-bind serve tunnel name — the
// client's dial key. It is the single source of truth shared by Serve and the
// catalog advertisement Name, resolved whether or not serving is enabled here.
func (s *Subsystem) ServeTunnelName() string {
	if s == nil {
		return ""
	}
	return serveTunnelName(s.cfg, s.identity.InstanceName)
}

// StartPublishing ensures the Agora catalog advertisement exists. The
// advertisement Name follows the resolved serve-tunnel name (the dial key) and
// the mode is the constant HTTP label, since MCP always rides HTTP/SSE.
func (s *Subsystem) StartPublishing(ctx context.Context) error {
	if s == nil || !s.publishWanted {
		return nil
	}
	advertisement, err := s.ops.EnsurePublished(ctx, s.agent, catalog.PublishSpec{
		Name:              s.ServeTunnelName(),
		Description:       s.identity.Description,
		Capabilities:      s.catalogCapabilities(),
		WorkgroupScopeIDs: append([]string(nil), s.cfg.Advertisement.WorkgroupIDs...),
		TunnelMode:        catalog.TunnelHTTP,
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

func (s *Subsystem) catalogCapabilities() []catalog.Capability {
	capabilities := make([]catalog.Capability, 0, len(s.capabilities))
	for _, capability := range s.capabilities {
		capabilities = append(capabilities, catalog.Capability{Name: capability})
	}
	return capabilities
}

// Dialer returns the process-wide Agora dialer for agora-backend tunnels.
func (s *Subsystem) Dialer() *Dialer {
	if s == nil {
		return nil
	}
	return s.dialer
}

// Close tears down Agora catalog, serve, dial, and agent resources. Deleting a
// tunnel or detaching revokes at the controller; OpenZiti terminates live
// sessions. Cleanup continues even if a step fails, logging each failure.
func (s *Subsystem) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true

	var firstErr error
	recordErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if s.advertisement != nil {
		if err := s.withCleanupContext(func(ctx context.Context) error {
			return s.ops.Retract(ctx, s.agent, s.advertisement.ID)
		}); err != nil {
			s.log.Warnf("failed to retract agora advertisement '%s': %v", s.advertisement.ID, err)
			recordErr(err)
		}
		s.advertisement = nil
	}

	if s.serve != nil {
		if err := s.withCleanupContext(func(ctx context.Context) error {
			return s.serve.Close(ctx)
		}); err != nil {
			s.log.Warnf("failed to close agora serve: %v", err)
			recordErr(err)
		}
		s.serve = nil
	}

	if s.dialer != nil {
		if err := s.withCleanupContext(func(ctx context.Context) error {
			return s.dialer.Close(ctx)
		}); err != nil {
			s.log.Warnf("failed to close agora dialer: %v", err)
			recordErr(err)
		}
	}

	if s.agent != nil {
		if err := s.withCleanupContext(func(ctx context.Context) error {
			return s.ops.Close(ctx, s.agent)
		}); err != nil {
			s.log.Warnf("failed to close agora agent: %v", err)
			recordErr(err)
		}
	}

	return firstErr
}

func (s *Subsystem) withCleanupContext(fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	return fn(ctx)
}
