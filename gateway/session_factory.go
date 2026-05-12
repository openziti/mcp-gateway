package gateway

import (
	"context"
	"sync"

	"github.com/michaelquigley/df/dl"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openziti/mcp-gateway/aggregator"
)

// SessionFactory creates isolated ClientSessions for each incoming connection.
type SessionFactory struct {
	config    *Config
	namespace *aggregator.Namespace
	impl      *mcp.Implementation
	resolver  aggregator.ConnectResolver

	mu       sync.Mutex
	sessions map[string]*ClientSession
}

// NewSessionFactory creates a factory for client sessions.
// the namespace should be pre-populated with all available tools from backends.
func NewSessionFactory(config *Config, namespace *aggregator.Namespace, resolver aggregator.ConnectResolver) *SessionFactory {
	return &SessionFactory{
		config:    config,
		namespace: namespace,
		resolver:  resolver,
		impl: &mcp.Implementation{
			Name:    config.Aggregator.Name,
			Version: config.Aggregator.Version,
		},
		sessions: make(map[string]*ClientSession),
	}
}

// CreateSession creates a new isolated session with connections to all backends.
// the caller is responsible for calling session.Close() when done.
func (f *SessionFactory) CreateSession(ctx context.Context, client *ClientContext) (*ClientSession, error) {
	session, err := NewClientSession(ctx, f.config, f.namespace, client, f.resolver)
	if err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.sessions[session.ID()] = session
	f.mu.Unlock()

	return session, nil
}

// RemoveSession removes a session from tracking.
// this should be called after session.Close().
func (f *SessionFactory) RemoveSession(sessionID string) {
	f.mu.Lock()
	delete(f.sessions, sessionID)
	f.mu.Unlock()

	dl.Log().With("session_id", sessionID).With("active_sessions", f.ActiveSessionCount()).Debug("session removed from factory")
}

// Implementation returns the MCP implementation info.
func (f *SessionFactory) Implementation() *mcp.Implementation {
	return f.impl
}

// Namespace returns the shared namespace.
func (f *SessionFactory) Namespace() *aggregator.Namespace {
	return f.namespace
}

// ActiveSessionCount returns the number of active sessions.
func (f *SessionFactory) ActiveSessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

// Close closes all active sessions and shuts down the factory.
func (f *SessionFactory) Close() error {
	f.mu.Lock()
	sessions := make([]*ClientSession, 0, len(f.sessions))
	for _, s := range f.sessions {
		sessions = append(sessions, s)
	}
	f.sessions = make(map[string]*ClientSession)
	f.mu.Unlock()

	dl.Log().With("session_count", len(sessions)).Info("closing all sessions")

	for _, s := range sessions {
		s.Close()
	}

	return nil
}
