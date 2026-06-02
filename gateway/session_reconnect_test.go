package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openziti/mcp-gateway/aggregator"
)

type fakeMcpSession struct {
	mu           sync.Mutex
	errs         []error
	result       *mcp.CallToolResult
	calls        int
	closeCalls   int
	waitForCalls int
	waitCh       chan struct{}
}

func (s *fakeMcpSession) CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	s.mu.Lock()
	call := s.calls
	s.calls++
	waitCh := s.waitCh
	if s.waitForCalls > 0 && s.calls == s.waitForCalls && waitCh != nil {
		close(waitCh)
	}
	s.mu.Unlock()

	if waitCh != nil {
		<-waitCh
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if call < len(s.errs) {
		return nil, s.errs[call]
	}
	if s.result != nil {
		return s.result, nil
	}
	return &mcp.CallToolResult{}, nil
}

func (s *fakeMcpSession) Close() error {
	s.mu.Lock()
	s.closeCalls++
	s.mu.Unlock()
	return nil
}

func (s *fakeMcpSession) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *fakeMcpSession) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCalls
}

func newReconnectTestSession(oldSession *fakeMcpSession) (*ClientSession, context.CancelFunc, *aggregator.BackendConfig) {
	namespace := aggregator.NewNamespace(":")
	namespace.AddTools("backend", []*mcp.Tool{{Name: "tool"}}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cfg := DefaultConfig()
	backendCfg := aggregator.BackendConfig{
		ID: "backend",
		Transport: aggregator.TransportConfig{
			Type: "http",
		},
	}
	oldBackend := &sessionBackend{
		id:      "backend",
		cfg:     backendCfg,
		session: oldSession,
	}
	cs := &ClientSession{
		id:              "session",
		config:          cfg,
		namespace:       namespace,
		backends:        map[string]*sessionBackend{"backend": oldBackend},
		reconnectGuards: make(map[string]*sync.Mutex),
		ctx:             ctx,
		cancel:          cancel,
	}
	return cs, cancel, &backendCfg
}

func TestClientSessionReconnectsOnceForDeadTransportErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "connection closed", err: mcp.ErrConnectionClosed},
		{name: "eof", err: io.EOF},
		{name: "unexpected eof", err: fmt.Errorf("wrapped: %w", io.ErrUnexpectedEOF)},
		{name: "net closed", err: fmt.Errorf("wrapped: %w", net.ErrClosed)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldSession := &fakeMcpSession{errs: []error{tt.err}}
			cs, cancel, backendCfg := newReconnectTestSession(oldSession)
			defer cancel()

			freshSession := &fakeMcpSession{}
			connectCalls := 0
			cs.connectBackendFunc = func(context.Context, aggregator.BackendConfig) (*sessionBackend, error) {
				connectCalls++
				return &sessionBackend{id: "backend", cfg: *backendCfg, session: freshSession}, nil
			}

			result, err := cs.CallTool(context.Background(), "backend:tool", map[string]any{"x": 1})
			if err != nil {
				t.Fatalf("CallTool returned error: %v", err)
			}
			if result == nil {
				t.Fatal("expected result")
			}
			if connectCalls != 1 {
				t.Fatalf("connect calls = %d, want 1", connectCalls)
			}
			if freshSession.callCount() != 1 {
				t.Fatalf("fresh session calls = %d, want 1", freshSession.callCount())
			}
			if oldSession.closeCount() != 1 {
				t.Fatalf("old close calls = %d, want 1", oldSession.closeCount())
			}
		})
	}
}

func TestClientSessionReconnectRetriesAtMostOnce(t *testing.T) {
	oldSession := &fakeMcpSession{errs: []error{mcp.ErrConnectionClosed}}
	cs, cancel, backendCfg := newReconnectTestSession(oldSession)
	defer cancel()

	freshSession := &fakeMcpSession{errs: []error{mcp.ErrConnectionClosed}}
	connectCalls := 0
	cs.connectBackendFunc = func(context.Context, aggregator.BackendConfig) (*sessionBackend, error) {
		connectCalls++
		return &sessionBackend{id: "backend", cfg: *backendCfg, session: freshSession}, nil
	}

	_, err := cs.CallTool(context.Background(), "backend:tool", nil)
	if !errors.Is(err, mcp.ErrConnectionClosed) {
		t.Fatalf("error = %v, want connection closed", err)
	}
	if connectCalls != 1 {
		t.Fatalf("connect calls = %d, want 1", connectCalls)
	}
}

func TestClientSessionDoesNotReconnectForLiveFailures(t *testing.T) {
	tests := []struct {
		name    string
		session *fakeMcpSession
		wantErr error
	}{
		{
			name:    "tool error result",
			session: &fakeMcpSession{result: &mcp.CallToolResult{IsError: true}},
		},
		{
			name:    "protocol error",
			session: &fakeMcpSession{errs: []error{errors.New("wire error")}},
			wantErr: errors.New("wire error"),
		},
		{
			name:    "deadline exceeded",
			session: &fakeMcpSession{errs: []error{context.DeadlineExceeded}},
			wantErr: context.DeadlineExceeded,
		},
		{
			name:    "canceled",
			session: &fakeMcpSession{errs: []error{context.Canceled}},
			wantErr: context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs, cancel, backendCfg := newReconnectTestSession(tt.session)
			defer cancel()

			connectCalls := 0
			cs.connectBackendFunc = func(context.Context, aggregator.BackendConfig) (*sessionBackend, error) {
				connectCalls++
				return &sessionBackend{id: "backend", cfg: *backendCfg, session: &fakeMcpSession{}}, nil
			}

			result, err := cs.CallTool(context.Background(), "backend:tool", nil)
			if tt.wantErr != nil {
				if err == nil || (!errors.Is(err, tt.wantErr) && err.Error() != tt.wantErr.Error()) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
			}
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result == nil || !result.IsError {
					t.Fatalf("expected IsError result, got %#v", result)
				}
			}
			if connectCalls != 0 {
				t.Fatalf("connect calls = %d, want 0", connectCalls)
			}
		})
	}
}

func TestClientSessionConcurrentTransportFailuresReconnectOnce(t *testing.T) {
	waitCh := make(chan struct{})
	oldSession := &fakeMcpSession{
		errs:         []error{mcp.ErrConnectionClosed, mcp.ErrConnectionClosed},
		waitForCalls: 2,
		waitCh:       waitCh,
	}
	cs, cancel, backendCfg := newReconnectTestSession(oldSession)
	defer cancel()

	freshSession := &fakeMcpSession{}
	var connectMu sync.Mutex
	connectCalls := 0
	cs.connectBackendFunc = func(context.Context, aggregator.BackendConfig) (*sessionBackend, error) {
		connectMu.Lock()
		connectCalls++
		connectMu.Unlock()
		return &sessionBackend{id: "backend", cfg: *backendCfg, session: freshSession}, nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := cs.CallTool(context.Background(), "backend:tool", nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("CallTool returned error: %v", err)
		}
	}
	if connectCalls != 1 {
		t.Fatalf("connect calls = %d, want 1", connectCalls)
	}
	if freshSession.callCount() != 2 {
		t.Fatalf("fresh calls = %d, want 2", freshSession.callCount())
	}
}

func TestClientSessionCloseDuringReconnectDiscardsFreshBackend(t *testing.T) {
	oldSession := &fakeMcpSession{errs: []error{mcp.ErrConnectionClosed}}
	cs, cancel, backendCfg := newReconnectTestSession(oldSession)
	defer cancel()

	started := make(chan struct{})
	release := make(chan struct{})
	freshSession := &fakeMcpSession{}
	cs.connectBackendFunc = func(context.Context, aggregator.BackendConfig) (*sessionBackend, error) {
		close(started)
		<-release
		return &sessionBackend{id: "backend", cfg: *backendCfg, session: freshSession}, nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(context.Background(), "backend:tool", nil)
		done <- err
	}()

	<-started
	if err := cs.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	close(release)

	err := <-done
	if !errors.Is(err, mcp.ErrConnectionClosed) {
		t.Fatalf("CallTool error = %v, want original connection error", err)
	}
	if freshSession.closeCount() != 1 {
		t.Fatalf("fresh close calls = %d, want 1", freshSession.closeCount())
	}
	cs.mu.Lock()
	backendCount := len(cs.backends)
	cs.mu.Unlock()
	if backendCount != 0 {
		t.Fatalf("closed session retained backends: %d", backendCount)
	}
}
