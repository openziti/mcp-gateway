package aggregator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConnectWithTimeout_ReturnsDeadlineExceeded(t *testing.T) {
	timedOut := make(chan struct{})

	start := time.Now()
	_, err := ConnectWithTimeout(context.Background(), 30*time.Millisecond, func(ctx context.Context) (*mcp.ClientSession, error) {
		<-ctx.Done()
		close(timedOut)
		return nil, ctx.Err()
	})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("expected timeout to return promptly")
	}

	select {
	case <-timedOut:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("expected connect context to be canceled")
	}
}

func TestConnectWithTimeout_DoesNotCancelHealthySessionAfterTimeoutWindow(t *testing.T) {
	var sessionCtx context.Context

	session, err := ConnectWithTimeout(context.Background(), 20*time.Millisecond, func(ctx context.Context) (*mcp.ClientSession, error) {
		sessionCtx = ctx
		return &mcp.ClientSession{}, nil
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if session == nil {
		t.Fatalf("expected session")
	}

	time.Sleep(40 * time.Millisecond)

	select {
	case <-sessionCtx.Done():
		t.Fatalf("expected session context to remain active after connect timeout window")
	default:
	}
}

func TestConnectWithTimeout_ReturnsParentCancellation(t *testing.T) {
	parentCtx, cancel := context.WithCancel(context.Background())
	canceled := make(chan struct{})

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := ConnectWithTimeout(parentCtx, time.Second, func(ctx context.Context) (*mcp.ClientSession, error) {
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected parent cancellation, got %v", err)
	}

	select {
	case <-canceled:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("expected connect context to be canceled")
	}
}
