package agora

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
)

// Dialer hands out shared *http.Client values for agora-backend tunnels,
// parallel to the gateway's zrok Access. Each unique tunnel is attached once at
// startup — a control-plane reservation, idempotent per (environment, tunnel) —
// and detached once at process shutdown. The attachment is explicitly NOT
// acquired per client session; per-connection work is the Dial inside
// DialContext, and the shared client is safe for concurrent use.
type Dialer struct {
	sub     *Subsystem
	mu      sync.Mutex
	clients map[string]*http.Client // keyed by tunnel name
}

func newDialer(sub *Subsystem) *Dialer {
	return &Dialer{sub: sub, clients: map[string]*http.Client{}}
}

// Attach reserves the dialer attachment for tunnel and builds+caches the shared
// HTTP client whose Transport.DialContext ignores addr and routes every request
// through tunnel.Dial. It is called once per unique tunnel at startup; calling
// it again for an already-attached name is a no-op. The controller makes Attach
// idempotent, so a redundant reservation returns the existing one — a real
// conflict (ambiguous name / UDP) surfaces as an error.
func (d *Dialer) Attach(ctx context.Context, tunnelName string) error {
	if d == nil {
		return fmt.Errorf("agora dialer is nil")
	}
	tunnelName = strings.TrimSpace(tunnelName)
	if tunnelName == "" {
		return fmt.Errorf("agora dial tunnel is required")
	}

	d.mu.Lock()
	_, exists := d.clients[tunnelName]
	d.mu.Unlock()
	if exists {
		return nil
	}

	if _, err := d.sub.ops.Attach(ctx, d.sub.agent, tunnelName); err != nil {
		return fmt.Errorf("attach agora tunnel '%s': %w", tunnelName, err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return d.sub.ops.Dial(ctx, d.sub.agent, tunnelName)
			},
		},
	}

	d.mu.Lock()
	if _, exists := d.clients[tunnelName]; !exists {
		d.clients[tunnelName] = client
	}
	d.mu.Unlock()

	d.sub.log.Infof("agora dial attached tunnel '%s'", tunnelName)
	return nil
}

// HTTPClient returns the shared client for an already-attached tunnel. It never
// attaches and never changes any count; an unknown name (never attached at
// startup) is a programming error.
func (d *Dialer) HTTPClient(tunnelName string) (*http.Client, error) {
	if d == nil {
		return nil, fmt.Errorf("agora dialer is nil")
	}
	tunnelName = strings.TrimSpace(tunnelName)
	d.mu.Lock()
	client, ok := d.clients[tunnelName]
	d.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("agora tunnel '%s' was not attached at startup", tunnelName)
	}
	return client, nil
}

// Close detaches every attached tunnel exactly once. This is the only release
// path; because it runs only as the whole process exits, no backend can ever
// strand a sibling that shares the tunnel.
func (d *Dialer) Close(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	names := make([]string, 0, len(d.clients))
	for name := range d.clients {
		names = append(names, name)
	}
	d.clients = map[string]*http.Client{}
	d.mu.Unlock()

	var firstErr error
	for _, name := range names {
		if err := d.sub.ops.Detach(ctx, d.sub.agent, name); err != nil {
			d.sub.log.Warnf("failed to detach agora tunnel '%s': %v", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
