package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
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
	config         *Config
	namespace      *aggregator.Namespace
	sessionFactory *SessionFactory
	share          *Share
	httpServer     *http.Server
	agoraServer    *http.Server
	agoraServe     *mcpagora.Serve
	agoraSubsystem *mcpagora.Subsystem
	agoraDial      aggregator.AgoraDialClient
	ipcClient      *ipc.Client
	ipcCancel      context.CancelFunc
	mainCtx        context.Context // stored for reconnection callback
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
				InstanceName:    "mcp-gateway",
				Description:     "MCP tool gateway",
				AgentNamePrefix: "mcp-gateway",
			},
			Capabilities:  mcpagora.Derive([]string{"mcp-tools"}, gatewayCapabilityExtras(b.config)),
			ServeWanted:   b.config.AgoraServeEnabled(),
			PublishWanted: b.config.AgoraPublishEnabled(),
		})
		if err != nil {
			return err
		}
		b.agoraSubsystem = subsys

		// reserve each unique agora-backend tunnel once, front-loading the
		// controller-side dialer attachment out of the connection hot path.
		dialer := b.agoraSubsystem.Dialer()
		for _, tunnel := range collectAgoraTunnels(b.config.Backends) {
			if err := dialer.Attach(ctx, tunnel); err != nil {
				return err
			}
		}
		b.agoraDial = dialer.HTTPClient
	}

	// discover tools from backends (temporary connections)
	namespace, err := b.discoverTools(ctx, b.agoraDial)
	if err != nil {
		return fmt.Errorf("failed to discover tools: %w", err)
	}
	b.namespace = namespace

	dl.Log().With("tool_count", namespace.Count()).Info("discovered tools from backends")

	// create session factory with namespace
	b.sessionFactory = NewSessionFactory(b.config, namespace, b.agoraDial)

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
		// create-or-bind the serve tunnel and bind the MCP handler directly to
		// the Agora net.Listener — no loopback hop.
		serve, err := b.agoraSubsystem.Serve(ctx)
		if err != nil {
			return fmt.Errorf("failed to serve agora tunnel: %w", err)
		}
		b.agoraServe = serve
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
func (b *Backend) discoverTools(ctx context.Context, agoraDial aggregator.AgoraDialClient) (*aggregator.Namespace, error) {
	// create aggregator config from our embedded config
	aggCfg := &aggregator.Config{
		Aggregator: b.config.Aggregator,
		Backends:   b.config.Backends,
	}

	// create backend manager for discovery
	backends := aggregator.NewBackendManager(aggCfg)
	backends.SetAgoraDialClient(agoraDial)

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
	var servers []*http.Server

	if b.httpServer != nil && b.share != nil {
		servers = append(servers, b.httpServer)
		serveHTTP(b.httpServer, b.share.Listener(), errCh, "zrok")
		dl.Log().Info("serving mcp over zrok share")
	}

	if b.agoraServer != nil && b.agoraServe != nil {
		servers = append(servers, b.agoraServer)
		serveHTTP(b.agoraServer, b.agoraServe.Listener(), errCh, "agora")
		dl.Log().With("tunnel", b.agoraSubsystem.ServeTunnelName()).Info("serving mcp for agora tunnel")
	}

	if len(servers) == 0 {
		return fmt.Errorf("no gateway listeners are configured")
	}

	if b.agoraSubsystem != nil && b.config.AgoraPublishEnabled() {
		if err := b.agoraSubsystem.StartPublishing(ctx); err != nil {
			shutdownHTTPServers(servers)
			return err
		}
	}

	// wait for context cancellation or server error
	select {
	case <-ctx.Done():
		dl.Log().Info("context cancelled, shutting down")
		return shutdownHTTPServers(servers)
	case err := <-errCh:
		shutdownHTTPServers(servers)
		return err
	}
}

// Stop gracefully shuts down the share and session factory.
func (b *Backend) Stop() error {
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

	// Close tears down the serve listener (deleting the tunnel only if we
	// created it), detaches every dial tunnel, and closes the agent.
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

	dl.Log().Info("mcp-gateway stopped")
	return lastErr
}

func serveHTTP(server *http.Server, listener net.Listener, errCh chan<- error, label string) {
	go func() {
		err := server.Serve(listener)
		if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			errCh <- nil
			return
		}
		errCh <- fmt.Errorf("%s listener failed: %w", label, err)
	}()
}

func shutdownHTTPServers(servers []*http.Server) error {
	var errs []error
	for _, server := range servers {
		if err := server.Shutdown(context.Background()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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

// collectAgoraTunnels returns the unique agora_tunnel names across agora
// backends, preserving first-seen order. Each is attached once at startup;
// several backends naming the same tunnel share one attachment.
func collectAgoraTunnels(backends []aggregator.BackendConfig) []string {
	seen := make(map[string]struct{})
	tunnels := make([]string, 0)
	for _, backend := range backends {
		if backend.Transport.Type != "agora" {
			continue
		}
		tunnel := strings.TrimSpace(backend.Transport.AgoraTunnel)
		if tunnel == "" {
			continue
		}
		if _, ok := seen[tunnel]; ok {
			continue
		}
		seen[tunnel] = struct{}{}
		tunnels = append(tunnels, tunnel)
	}
	return tunnels
}

// ShareToken returns the share token after Start().
func (b *Backend) ShareToken() string {
	if b.share == nil {
		return ""
	}
	return b.share.Token()
}
