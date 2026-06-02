package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/michaelquigley/df/dd"
	"github.com/michaelquigley/df/dl"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openziti/mcp-gateway/aggregator"
	mcpagora "github.com/openziti/mcp-gateway/agora"
	"github.com/openziti/mcp-gateway/gateway/ipc"
	"github.com/openziti/mcp-gateway/model"
)

// Backend manages the lifecycle of a zrok share serving MCP with per-client sessions.
type Backend struct {
	config           *Config
	namespace        *aggregator.Namespace
	sessionFactory   *SessionFactory
	share            *Share
	httpServer       *http.Server
	agoraServer      *http.Server
	agoraListener    net.Listener
	agoraSubsystem   *mcpagora.Subsystem
	connectResolver  aggregator.ConnectResolver
	ipcClient        *ipc.Client
	ipcCancel        context.CancelFunc
	mainCtx          context.Context // stored for reconnection callback
	resilienceCancel context.CancelFunc
	resilienceWG     sync.WaitGroup
	teardownOnce     sync.Once
	teardownErr      error
}

// New creates a Backend from config.
func New(cfg *Config) (*Backend, error) {
	return &Backend{
		config: cfg,
	}, nil
}

// NewFromFile creates a Backend by loading config from YAML.
func NewFromFile(path string) (*Backend, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	return New(cfg)
}

// Start initializes the namespace, session factory, creates/connects zrok share, and outputs token.
func (b *Backend) Start(ctx context.Context) (err error) {
	dl.Log().Info("starting mcp-gateway")
	defer func() {
		if err != nil {
			if stopErr := b.Stop(); stopErr != nil {
				dl.Log().With("error", stopErr).Warn("failed to clean up after start error")
			}
		}
	}()

	if b.config.Agora != nil && b.config.Agora.Enabled {
		subsys, err := mcpagora.NewSubsystem(mcpagora.SubsystemOptions{
			Config: b.config.Agora,
			Defaults: mcpagora.Defaults{
				InstanceName:       "mcp-gateway",
				Description:        "MCP tool gateway",
				TunnelMode:         "tcp",
				AgentNamePrefix:    "mcp-gateway",
				AllowedTunnelModes: []string{"tcp", "http"},
			},
			Capabilities:   mcpagora.Derive([]string{"mcp-tools"}, gatewayCapabilityExtras(b.config)),
			ConnectTargets: collectAgoraConnectTargets(b.config.Backends),
			ServeWanted:    b.config.AgoraServeEnabled(),
			PublishWanted:  b.config.AgoraPublishEnabled(),
		})
		if err != nil {
			return err
		}
		b.agoraSubsystem = subsys
		if err := b.agoraSubsystem.BootstrapConnects(ctx); err != nil {
			return err
		}
		b.connectResolver = b.agoraSubsystem.ConnectAddress
	}

	// discover tools from backends (temporary connections)
	namespace, err := b.discoverTools(ctx, b.connectResolver)
	if err != nil {
		return fmt.Errorf("failed to discover tools: %w", err)
	}
	b.namespace = namespace

	dl.Log().With("tool_count", namespace.Count()).Info("discovered tools from backends")

	// create session factory with namespace
	b.sessionFactory = NewSessionFactory(b.config, namespace, b.connectResolver)

	// create or connect to zrok share
	if b.config.ZrokShareEnabled() {
		var share *Share
		if b.config.ShareToken != "" {
			// managed mode: connect to existing share created by orchestrator
			share, err = NewShareFromToken(b.config.ShareToken)
			if err != nil {
				return fmt.Errorf("failed to connect to share '%s': %w", b.config.ShareToken, err)
			}
		} else {
			// standalone mode: create new share
			share, err = NewShare()
			if err != nil {
				return fmt.Errorf("failed to create share: %w", err)
			}
		}
		b.share = share
	}

	handler := b.createHTTPHandler()
	if b.share != nil {
		b.httpServer = &http.Server{Handler: handler}
	}

	if b.config.AgoraServeEnabled() {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("failed to create agora listener: %w", err)
		}
		b.agoraListener = listener
		b.agoraServer = &http.Server{Handler: handler}
	}

	// connect to orchestrator if configured (managed mode)
	if b.config.Orchestrator != nil && b.config.ShareToken != "" && b.share != nil {
		b.mainCtx = ctx

		ipcCfg := &ipc.Config{
			SocketPath:        b.config.Orchestrator.SocketPath,
			HeartbeatInterval: b.config.Orchestrator.HeartbeatInterval,
			ReconnectInterval: 5 * time.Second, // fixed reconnect interval for local socket
		}
		b.ipcClient = ipc.NewClient(b.share.Token(), ipcCfg)

		// set up reconnection callback to restart heartbeat and shutdown listener
		b.ipcClient.OnReconnect = func() {
			// cancel old heartbeat context if it exists
			if b.ipcCancel != nil {
				b.ipcCancel()
			}
			// start new heartbeat loop
			b.startHeartbeatAndShutdownListener()
		}

		if err := b.ipcClient.Connect(ctx); err != nil {
			dl.Log().With("error", err).Warn("failed to connect to orchestrator, will retry in background")
			// start reconnection loop instead of giving up
			b.ipcClient.StartReconnectLoop(ctx)
		} else {
			// register with orchestrator and start heartbeat
			if err := b.ipcClient.Register(); err != nil {
				dl.Log().With("error", err).Warn("failed to register with orchestrator, will retry")
				b.ipcClient.StartReconnectLoop(ctx)
			} else {
				b.startHeartbeatAndShutdownListener()
			}
		}
	}

	if b.share != nil {
		// output share token to stdout for orchestrator capture (useful in standalone mode)
		if json, err := dd.UnbindJSON(&model.TokenOutput{ShareToken: b.share.Token()}); err == nil {
			fmt.Println(string(json))
		} else {
			dl.Log().With("error", err).Info("failed to unbind token")
		}
		dl.Log().With("share_token", b.share.Token()).Info("mcp-gateway zrok share ready")
	}

	dl.Log().Info("mcp-gateway started")
	return nil
}

// shutdownListener listens for shutdown commands from the orchestrator.
func (b *Backend) shutdownListener(ctx context.Context) {
	select {
	case reason := <-b.ipcClient.ShutdownCh():
		dl.Log().With("reason", reason).Info("received shutdown command from orchestrator")
		b.Stop()
	case <-ctx.Done():
	}
}

// startHeartbeatAndShutdownListener starts the heartbeat loop and shutdown listener.
// this is called after initial connection and after reconnection.
func (b *Backend) startHeartbeatAndShutdownListener() {
	ipcCtx, cancel := context.WithCancel(b.mainCtx)
	b.ipcCancel = cancel
	b.ipcClient.StartHeartbeat(ipcCtx)
	go b.shutdownListener(ipcCtx)
}

// discoverTools connects to all backends temporarily to discover available tools.
// the connections are closed after discovery; per-client sessions will reconnect.
func (b *Backend) discoverTools(ctx context.Context, resolver aggregator.ConnectResolver) (*aggregator.Namespace, error) {
	// create aggregator config from our embedded config
	aggCfg := &aggregator.Config{
		Aggregator: b.config.Aggregator,
		Backends:   b.config.Backends,
	}

	// create backend manager for discovery
	backends := aggregator.NewBackendManager(aggCfg)
	backends.SetConnectResolver(resolver)

	// connect to all backends
	if err := backends.Connect(ctx); err != nil {
		return nil, err
	}

	// build namespace with tools from each backend
	namespace := aggregator.NewNamespace(b.config.Aggregator.Separator)
	for _, bcfg := range b.config.Backends {
		backend, ok := backends.GetBackend(bcfg.ID)
		if !ok {
			continue
		}
		namespace.AddTools(bcfg.ID, backend.Tools(), &bcfg.Tools)
	}

	// close discovery connections - per-client sessions will make their own
	if err := backends.Close(); err != nil {
		dl.Log().With("error", err).Warn("error closing discovery connections")
	}

	return namespace, nil
}

// createHTTPHandler creates an HTTP handler that spawns per-client sessions.
func (b *Backend) createHTTPHandler() http.Handler {
	return mcp.NewSSEHandler(func(r *http.Request) *mcp.Server {
		// extract client context from HTTP request
		client := NewClientContext(r)

		// create new isolated session for this client
		session, err := b.sessionFactory.CreateSession(r.Context(), client)
		if err != nil {
			dl.Log().With("error", err).Error("failed to create client session")
			return nil
		}

		// cleanup when client disconnects
		go func() {
			<-r.Context().Done()
			session.Close()
			b.sessionFactory.RemoveSession(session.ID())
		}()

		return session.CreateMCPServer(b.sessionFactory.Implementation())
	}, nil)
}

// createHTTPServer creates an HTTP server that spawns per-client sessions.
func (b *Backend) createHTTPServer() *http.Server {
	return &http.Server{
		Handler: b.createHTTPHandler(),
	}
}

// Run serves MCP over the configured network listeners.
// this blocks until the context is cancelled.
func (b *Backend) Run(ctx context.Context) error {
	dl.Log().Info("serving mcp-gateway")

	errCh := make(chan error, 2)

	if b.httpServer != nil && b.share != nil {
		resilient := newResilientListener(b.share.Listener())
		serveHTTP(b.httpServer, resilient, errCh, "zrok")
		b.startResilience(ctx, resilient)
		dl.Log().Info("serving mcp over zrok share")
	}

	if b.agoraServer != nil && b.agoraListener != nil {
		serveHTTP(b.agoraServer, b.agoraListener, errCh, "agora")
		dl.Log().With("listen", b.agoraListener.Addr().String()).Info("serving mcp for agora tunnel")
	}

	if b.httpServer == nil && b.agoraServer == nil {
		return fmt.Errorf("no gateway listeners are configured")
	}

	if b.agoraSubsystem != nil {
		if b.config.AgoraServeEnabled() {
			target := b.agoraServeBackendTarget()
			if err := b.agoraSubsystem.StartServing(ctx, target); err != nil {
				b.teardown()
				return err
			}
		}
		if b.config.AgoraPublishEnabled() {
			if err := b.agoraSubsystem.StartPublishing(ctx); err != nil {
				b.teardown()
				return err
			}
		}
	}

	// wait for context cancellation or server error
	select {
	case <-ctx.Done():
		dl.Log().Info("context cancelled, shutting down")
		return b.teardown()
	case err := <-errCh:
		b.teardown()
		return err
	}
}

// Stop gracefully shuts down the share and session factory.
func (b *Backend) Stop() error {
	return b.teardown()
}

func (b *Backend) startResilience(ctx context.Context, resilient *resilientListener) {
	if b.config == nil || !b.config.Resilience.WatchdogEnabled {
		return
	}

	resilienceCtx, cancel := context.WithCancel(ctx)
	b.resilienceCancel = cancel

	w := newWatchdog(resilient, b.share.Relisten, b.config.Resilience, b.config.Orchestrator == nil, b.share.Token())
	b.resilienceWG.Add(1)
	go func() {
		defer b.resilienceWG.Done()
		w.run(resilienceCtx)
	}()

	if b.config.Resilience.HeartbeatInterval > 0 {
		b.resilienceWG.Add(1)
		go func() {
			defer b.resilienceWG.Done()
			b.livenessHeartbeat(resilienceCtx, resilient, b.config.Resilience.HeartbeatInterval)
		}()
	}
}

func (b *Backend) livenessHeartbeat(ctx context.Context, resilient *resilientListener, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			activeSessions := 0
			if b.sessionFactory != nil {
				activeSessions = b.sessionFactory.ActiveSessionCount()
			}

			listener, _ := resilient.Current()
			_, counter := listenerHealthOf(listener)
			log := dl.Log().
				With("active_sessions", activeSessions).
				With("seconds_since_last_accept", resilient.SecondsSinceLastAccept())
			if counter != nil {
				log = log.With("established_terminators", counter.GetEstablishedCount())
			}
			log.Info("gateway alive")
		case <-ctx.Done():
			return
		}
	}
}

func (b *Backend) teardown() error {
	b.teardownOnce.Do(func() {
		dl.Log().Info("stopping mcp-gateway")

		var lastErr error

		// report stopping state to orchestrator
		if b.ipcClient != nil {
			b.ipcClient.ReportStatus("stopping", nil)
		}

		// cancel IPC heartbeat loop
		if b.ipcCancel != nil {
			b.ipcCancel()
		}

		if b.resilienceCancel != nil {
			b.resilienceCancel()
		}
		b.resilienceWG.Wait()

		if b.httpServer != nil {
			if err := b.httpServer.Shutdown(context.Background()); err != nil {
				dl.Log().With("error", err).Warn("error shutting down server")
				lastErr = err
			}
		}

		if b.agoraServer != nil {
			if err := b.agoraServer.Shutdown(context.Background()); err != nil {
				dl.Log().With("error", err).Warn("error shutting down agora server")
				lastErr = err
			}
		}

		if b.agoraListener != nil {
			if err := b.agoraListener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				dl.Log().With("error", err).Warn("error closing agora listener")
				lastErr = err
			}
		}

		if b.agoraSubsystem != nil {
			if err := b.agoraSubsystem.Close(); err != nil {
				dl.Log().With("error", err).Warn("error closing agora subsystem")
				lastErr = err
			}
		}

		if b.share != nil {
			if err := b.share.Close(); err != nil {
				dl.Log().With("error", err).Warn("error closing share")
				lastErr = err
			}
		}

		if b.sessionFactory != nil {
			if err := b.sessionFactory.Close(); err != nil {
				dl.Log().With("error", err).Warn("error closing session factory")
				lastErr = err
			}
		}

		// close IPC client
		if b.ipcClient != nil {
			if err := b.ipcClient.Close(); err != nil {
				dl.Log().With("error", err).Warn("error closing ipc client")
				lastErr = err
			}
		}

		b.teardownErr = lastErr
		dl.Log().Info("mcp-gateway stopped")
	})

	return b.teardownErr
}

func serveHTTP(server *http.Server, listener net.Listener, errCh chan<- error, label string) {
	go func() {
		err := server.Serve(listener)
		if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			dl.Log().
				With("listener", label).
				With("error", err).
				Info("serve loop returned")
			errCh <- nil
			return
		}
		wrapped := fmt.Errorf("%s listener failed: %w", label, err)
		dl.Log().
			With("listener", label).
			With("error", err).
			Error("serve loop returned")
		errCh <- wrapped
	}()
}

func (b *Backend) agoraServeBackendTarget() string {
	if b.agoraListener == nil {
		return ""
	}

	address := b.agoraListener.Addr().String()
	mode := "tcp"
	if b.config != nil && b.config.Agora != nil && strings.TrimSpace(b.config.Agora.TunnelMode) != "" {
		mode = strings.ToLower(strings.TrimSpace(b.config.Agora.TunnelMode))
	}
	if mode == "http" {
		return "http://" + address
	}
	return address
}

func gatewayCapabilityExtras(cfg *Config) []string {
	if cfg == nil {
		return nil
	}

	extras := make([]string, 0, len(cfg.Backends)+1)
	for _, backend := range cfg.Backends {
		if strings.TrimSpace(backend.ID) != "" {
			extras = append(extras, strings.TrimSpace(backend.ID))
		}
	}
	sort.Strings(extras)
	if cfg.AgoraServeEnabled() {
		extras = append(extras, "agora-serve")
	}
	return extras
}

func collectAgoraConnectTargets(backends []aggregator.BackendConfig) []mcpagora.ConnectTarget {
	targets := make([]mcpagora.ConnectTarget, 0)
	for _, backend := range backends {
		if backend.Transport.Type != "agora" {
			continue
		}
		targets = append(targets, mcpagora.ConnectTarget{
			Key:    strings.TrimSpace(backend.ID),
			Tunnel: strings.TrimSpace(backend.Transport.AgoraTunnel),
		})
	}
	return targets
}

// ShareToken returns the share token after Start().
func (b *Backend) ShareToken() string {
	if b.share == nil {
		return ""
	}
	return b.share.Token()
}
