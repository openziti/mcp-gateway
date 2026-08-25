// Package streamable owns the server side of the Streamable HTTP session
// lifecycle shared by mcp-gateway, mcp-bridge, and mcp-tools. each of them
// creates per-frontend-session backend resources and must release them when
// the protocol session ends.
package streamable

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultSessionIdleTimeout bounds resources retained for a client that
// disappears without terminating its Streamable HTTP session.
const DefaultSessionIdleTimeout = 30 * time.Minute

// Sessions owns the protocol sessions created by a streamable HTTP handler.
// Streamable sessions outlive the request that initializes them, so callers
// must close this set during server shutdown.
type Sessions struct {
	mu       sync.Mutex
	sessions map[*mcp.ServerSession]struct{}
	closing  bool
}

// Options configure the wrapped Streamable HTTP handler.
type Options struct {
	// SessionIdleTimeout closes a session that has gone quiet for this long.
	// zero disables idle expiry.
	SessionIdleTimeout time.Duration
	// Stateless serves every request on a throwaway session. the SDK asks for
	// a server per request and closes it before the request returns, so the
	// caller's per-session resources are acquired and released per request.
	Stateless bool
	// JSONResponse prefers JSON over SSE for responses.
	JSONResponse bool
}

// Handler builds a streamable HTTP handler whose per-session resources are
// released when the MCP protocol session ends.
func (s *Sessions) Handler(opts Options, create func(*http.Request) (*mcp.Server, func())) http.Handler {
	created := sync.Map{}
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		server, cleanup := create(r)
		if server != nil {
			created.Store(r, streamableLifecycle{server: server, cleanup: cleanup})
		}
		return server
	}, &mcp.StreamableHTTPOptions{
		SessionTimeout: opts.SessionIdleTimeout,
		Stateless:      opts.Stateless,
		JSONResponse:   opts.JSONResponse,
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		value, ok := created.LoadAndDelete(r)
		if !ok {
			return
		}

		lifecycle := value.(streamableLifecycle)
		var protocolSession *mcp.ServerSession
		for session := range lifecycle.server.Sessions() {
			protocolSession = session
			break
		}
		if protocolSession == nil {
			// the SDK closes a protocol session whose initialization failed
			// before ServeHTTP returns.
			lifecycle.cleanup()
			return
		}

		s.add(protocolSession)
		go func() {
			_ = protocolSession.Wait()
			s.remove(protocolSession)
			lifecycle.cleanup()
		}()
	})
}

type streamableLifecycle struct {
	server  *mcp.Server
	cleanup func()
}

func (s *Sessions) add(session *mcp.ServerSession) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		_ = session.Close()
		return
	}
	if s.sessions == nil {
		s.sessions = make(map[*mcp.ServerSession]struct{})
	}
	s.sessions[session] = struct{}{}
	s.mu.Unlock()
}

func (s *Sessions) remove(session *mcp.ServerSession) {
	s.mu.Lock()
	delete(s.sessions, session)
	s.mu.Unlock()
}

// Close prevents new sessions from surviving registration and closes every
// protocol session currently owned by the set.
func (s *Sessions) Close() error {
	s.mu.Lock()
	s.closing = true
	sessions := make([]*mcp.ServerSession, 0, len(s.sessions))
	for session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.sessions = nil
	s.mu.Unlock()

	var errs []error
	for _, session := range sessions {
		if err := session.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
