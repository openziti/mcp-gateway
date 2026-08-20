package agora

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/openziti/agora/sdk/agent/tunnel"
)

// Serve is a handle to a create-or-bind Agora serve listener, parallel to the
// gateway's zrok Share. When this process created the tunnel it is managed and
// deleted on Close; when it bound to a pre-existing (operator-provisioned)
// tunnel it is left intact — the persistent named share.
type Serve struct {
	sub      *Subsystem
	tunnel   *tunnel.Tunnel // set only when we created it
	listener net.Listener
	managed  bool
	closed   bool
}

// Serve resolves the serve-tunnel name and returns a bound listener. If a
// tunnel already exists under that name the process binds to it (managed=false,
// left intact); otherwise it creates the tunnel (mode TCP, with grants) and
// will delete it on Close (managed=true). Serve is all-or-nothing: on any error
// it unwinds its own partial state and returns nothing to clean up.
func (s *Subsystem) Serve(ctx context.Context) (*Serve, error) {
	if s == nil {
		return nil, fmt.Errorf("agora subsystem is nil")
	}
	name := s.ServeTunnelName()
	if name == "" {
		return nil, fmt.Errorf("agora serve tunnel name is unresolved")
	}

	// Listen first; Listen accepts an existing tcp- or http-mode tunnel and
	// returns ErrNotFound when nothing is provisioned under the name.
	listener, err := s.ops.Listen(ctx, s.agent, name)
	if err == nil {
		return s.boundServe(ctx, name, listener)
	}
	if !errors.Is(err, tunnel.ErrNotFound) {
		return nil, fmt.Errorf("listen on agora tunnel '%s': %w", name, err)
	}

	// Nothing provisioned yet — create the tunnel, then listen on it.
	var grants []string
	if s.cfg.Serve != nil {
		grants = append([]string(nil), s.cfg.Serve.Grants...)
	}
	created, err := s.ops.Create(ctx, s.agent, tunnel.Spec{
		Name:        name,
		Mode:        tunnel.ModeTCP,
		GrantEmails: grants,
	})
	if err != nil {
		if errors.Is(err, tunnel.ErrConflict) {
			// Lost a create race: another process provisioned it. Bind instead.
			listener, lerr := s.ops.Listen(ctx, s.agent, name)
			if lerr != nil {
				return nil, fmt.Errorf("listen on agora tunnel '%s': %w", name, lerr)
			}
			return s.boundServe(ctx, name, listener)
		}
		return nil, fmt.Errorf("create agora tunnel '%s': %w", name, err)
	}

	listener, err = s.ops.Listen(ctx, s.agent, name)
	if err != nil {
		// Unwind the tunnel we just created (delete-only-what-we-created).
		if delErr := s.withCleanupContext(func(cleanupCtx context.Context) error {
			return s.ops.Delete(cleanupCtx, s.agent, created)
		}); delErr != nil {
			s.log.Warnf("failed to delete agora tunnel '%s' after listen failure: %v", name, delErr)
		}
		return nil, fmt.Errorf("listen on agora tunnel '%s': %w", name, err)
	}

	s.log.Infof("agora serve created tunnel '%s' mode='tcp'", name)
	sv := &Serve{sub: s, tunnel: created, listener: listener, managed: true}
	s.serve = sv
	return sv, nil
}

// boundServe finishes the bind path: enforce the TCP-mode requirement and, on
// failure, close the listener we opened (leaving the bound tunnel intact).
func (s *Subsystem) boundServe(ctx context.Context, name string, listener net.Listener) (*Serve, error) {
	if err := s.requireTCPMode(ctx, name); err != nil {
		_ = listener.Close()
		return nil, err
	}
	s.log.Infof("agora serve bound to existing tunnel '%s'", name)
	sv := &Serve{sub: s, listener: listener, managed: false}
	s.serve = sv
	return sv, nil
}

// requireTCPMode fails fast when binding to a tunnel whose mode is not TCP.
// Listen itself does not enforce this (it accepts http-mode tunnels), so the
// check is explicit.
func (s *Subsystem) requireTCPMode(ctx context.Context, name string) error {
	existing, err := s.ops.GetTunnel(ctx, s.agent, name)
	if err != nil {
		return fmt.Errorf("resolve agora serve tunnel '%s': %w", name, err)
	}
	if existing == nil {
		return fmt.Errorf("agora serve tunnel '%s' could not be resolved", name)
	}
	if !strings.EqualFold(string(existing.Mode), string(tunnel.ModeTCP)) {
		return fmt.Errorf("agora serve tunnel '%s' exists with mode '%s', expected tcp", name, existing.Mode)
	}
	return nil
}

// Listener returns the net.Listener the MCP HTTP/SSE handler binds to.
func (sv *Serve) Listener() net.Listener {
	if sv == nil {
		return nil
	}
	return sv.listener
}

// Managed reports whether this process created the tunnel (and will delete it).
func (sv *Serve) Managed() bool {
	return sv != nil && sv.managed
}

// Close closes the listener and deletes the tunnel only when this process
// created it. OpenZiti terminates live sessions on delete; the app never
// force-closes established connections.
func (sv *Serve) Close(ctx context.Context) error {
	if sv == nil || sv.closed {
		return nil
	}
	sv.closed = true

	var firstErr error
	if sv.listener != nil {
		if err := sv.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			firstErr = err
		}
	}
	if sv.managed && sv.tunnel != nil {
		if err := sv.sub.ops.Delete(ctx, sv.sub.agent, sv.tunnel); err != nil {
			sv.sub.log.Warnf("failed to delete agora tunnel '%s': %v", sv.tunnel.Name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
