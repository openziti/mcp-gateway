package gateway

import (
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/michaelquigley/df/dl"
	"github.com/openziti/sdk-golang/ziti/edge"
	"github.com/openziti/zrok/v2/environment"
	"github.com/openziti/zrok/v2/environment/env_core"
	"github.com/openziti/zrok/v2/sdk/golang/sdk"
)

// Share wraps a zrok share lifecycle.
type Share struct {
	mu       sync.Mutex
	root     env_core.Root
	share    *sdk.Share
	listener edge.Listener
	token    string
	managed  bool // if true, share is managed by orchestrator (don't delete on close)
	closed   bool
}

// NewShare creates a zrok private share.
func NewShare() (*Share, error) {
	// load zrok environment
	root, err := environment.LoadRoot()
	if err != nil {
		return nil, fmt.Errorf("failed to load zrok environment: %w", err)
	}

	if !root.IsEnabled() {
		return nil, fmt.Errorf("zrok environment is not enabled; run 'zrok enable' first")
	}

	dl.Log().Info("creating zrok private share")

	// create private share with proxy backend mode
	shareReq := &sdk.ShareRequest{
		BackendMode:    sdk.ProxyBackendMode,
		ShareMode:      sdk.PrivateShareMode,
		PermissionMode: sdk.OpenPermissionMode,
	}

	shr, err := sdk.CreateShare(root, shareReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create share: %w", err)
	}

	dl.Log().With("share_token", shr.Token).Info("created zrok share")

	// create listener for the share
	listener, err := sdk.NewListener(shr.Token, root)
	if err != nil {
		// cleanup share on listener failure
		if deleteErr := sdk.DeleteShare(root, shr); deleteErr != nil {
			dl.Log().With("error", deleteErr).Warn("failed to delete share after listener failure")
		}
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}

	dl.Log().With("share_token", shr.Token).Info("listener ready")

	return &Share{
		root:     root,
		share:    shr,
		listener: listener,
		token:    shr.Token,
		managed:  false,
	}, nil
}

// NewShareFromToken creates a Share from an existing share token (managed mode).
// In managed mode, the share is owned by the orchestrator and won't be deleted on Close.
func NewShareFromToken(token string) (*Share, error) {
	root, err := environment.LoadRoot()
	if err != nil {
		return nil, fmt.Errorf("failed to load zrok environment: %w", err)
	}

	if !root.IsEnabled() {
		return nil, fmt.Errorf("zrok environment is not enabled; run 'zrok enable' first")
	}

	dl.Log().With("share_token", token).Info("connecting to existing zrok share")

	listener, err := sdk.NewListener(token, root)
	if err != nil {
		return nil, fmt.Errorf("failed to create listener for share '%s': %w", token, err)
	}

	dl.Log().With("share_token", token).Info("listener ready")

	return &Share{
		root:     root,
		share:    nil, // no share object in managed mode
		listener: listener,
		token:    token,
		managed:  true,
	}, nil
}

// Token returns the share token for client access.
func (s *Share) Token() string {
	return s.token
}

// Listener returns the net.Listener for serving HTTP.
func (s *Share) Listener() net.Listener {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listener
}

// Relisten creates a fresh listener for the existing share token.
func (s *Share) Relisten() (net.Listener, error) {
	listener, err := sdk.NewListener(s.token, s.root)
	if err != nil {
		return nil, fmt.Errorf("failed to create listener for share '%s': %w", s.token, err)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = listener.Close()
		return nil, net.ErrClosed
	}
	s.listener = listener
	s.mu.Unlock()

	dl.Log().With("share_token", s.token).Info("listener ready")
	return listener, nil
}

// Close terminates the share and cleans up resources.
// In managed mode, only the listener is closed (orchestrator owns the share).
func (s *Share) Close() error {
	var lastErr error

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	listener := s.listener
	root := s.root
	share := s.share
	managed := s.managed
	token := s.token
	s.mu.Unlock()

	// close listener first
	if listener != nil {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			dl.Log().With("error", err).Warn("error closing listener")
			lastErr = err
		}
	}

	// delete the share only in standalone mode (not managed by orchestrator)
	if !managed && share != nil && root != nil {
		if err := sdk.DeleteShare(root, share); err != nil {
			dl.Log().With("error", err).Warn("error deleting share")
			lastErr = err
		}
	}

	dl.Log().With("share_token", token).Info("share closed")
	return lastErr
}
