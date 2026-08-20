package gateway

import (
	"fmt"
	"net"

	"github.com/michaelquigley/df/dl"
	"github.com/openziti/sdk-golang/ziti/edge"
	"github.com/openziti/zrok/v2/environment"
	"github.com/openziti/zrok/v2/environment/env_core"
	"github.com/openziti/zrok/v2/sdk/golang/sdk"
)

// Share wraps a zrok share lifecycle.
type Share struct {
	root     env_core.Root
	share    *sdk.Share
	listener edge.Listener
	token    string
	managed  bool // if true, share is managed by orchestrator (don't delete on close)
}

// NewShare creates a zrok private share.
func NewShare(accessGrants []string) (*Share, error) {
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
	shareReq := newShareRequest(accessGrants)

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

func newShareRequest(accessGrants []string) *sdk.ShareRequest {
	return &sdk.ShareRequest{
		BackendMode:    sdk.ProxyBackendMode,
		ShareMode:      sdk.PrivateShareMode,
		PermissionMode: sdk.ClosedPermissionMode,
		AccessGrants:   append([]string(nil), accessGrants...),
	}
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
	return s.listener
}

// Close terminates the share and cleans up resources.
// In managed mode, only the listener is closed (orchestrator owns the share).
func (s *Share) Close() error {
	var lastErr error

	// close listener first
	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			dl.Log().With("error", err).Warn("error closing listener")
			lastErr = err
		}
	}

	// delete the share only in standalone mode (not managed by orchestrator)
	if !s.managed && s.share != nil && s.root != nil {
		if err := sdk.DeleteShare(s.root, s.share); err != nil {
			dl.Log().With("error", err).Warn("error deleting share")
			lastErr = err
		}
	}

	dl.Log().With("share_token", s.token).Info("share closed")
	return lastErr
}
