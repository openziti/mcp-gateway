package aggregator

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type connectResult struct {
	session *mcp.ClientSession
	err     error
}

// ConnectWithTimeout bounds the initial connect window for long-lived transports
// without attaching the timeout to the session lifetime.
func ConnectWithTimeout(ctx context.Context, timeout time.Duration, connect func(context.Context) (*mcp.ClientSession, error)) (*mcp.ClientSession, error) {
	if timeout <= 0 {
		return connect(ctx)
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	resultCh := make(chan connectResult, 1)

	go func() {
		session, err := connect(sessionCtx)
		resultCh <- connectResult{session: session, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case result := <-resultCh:
		if result.err != nil {
			cancel()
			return nil, result.err
		}
		// the session was connected on sessionCtx and must outlive this call,
		// so we cannot cancel here. sessionCtx is a child of ctx, so release
		// it when ctx is done (which already auto-cancels it) — explicit
		// handoff cleanup rather than a leaked cancel.
		context.AfterFunc(ctx, cancel)
		return result.session, nil
	case <-ctx.Done():
		cancel()
		result := <-resultCh
		if result.session != nil {
			_ = result.session.Close()
		}
		return nil, ctx.Err()
	case <-timer.C:
		cancel()
		result := <-resultCh
		if result.session != nil {
			_ = result.session.Close()
		}
		return nil, context.DeadlineExceeded
	}
}
