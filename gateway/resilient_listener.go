package gateway

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// resilientListener lets an HTTP server keep serving across listener swaps.
type resilientListener struct {
	mu         sync.Mutex
	inner      net.Listener
	generation uint64
	closed     bool
	terminal   error
	lastAccept atomic.Int64
}

func newResilientListener(inner net.Listener) *resilientListener {
	listener := &resilientListener{
		inner: inner,
	}
	listener.lastAccept.Store(time.Now().Unix())
	return listener
}

func (l *resilientListener) Accept() (net.Conn, error) {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return nil, net.ErrClosed
		}
		if l.terminal != nil {
			err := l.terminal
			l.mu.Unlock()
			return nil, err
		}
		inner := l.inner
		generation := l.generation
		l.mu.Unlock()

		if inner == nil {
			return nil, net.ErrClosed
		}

		conn, err := inner.Accept()
		if err == nil {
			l.lastAccept.Store(time.Now().Unix())
			return conn, nil
		}

		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return nil, net.ErrClosed
		}
		if l.terminal != nil {
			terminal := l.terminal
			l.mu.Unlock()
			return nil, terminal
		}
		if l.generation != generation {
			l.mu.Unlock()
			continue
		}
		l.mu.Unlock()
		return nil, err
	}
}

func (l *resilientListener) Swap(inner net.Listener) {
	l.mu.Lock()
	if l.closed || l.terminal != nil {
		l.mu.Unlock()
		if inner != nil {
			_ = inner.Close()
		}
		return
	}
	old := l.inner
	l.inner = inner
	l.generation++
	if old != nil {
		_ = old.Close()
	}
	l.mu.Unlock()
}

func (l *resilientListener) Fail(err error) {
	if err == nil {
		err = errors.New("listener failed")
	}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.terminal = err
	inner := l.inner
	if inner != nil {
		_ = inner.Close()
	}
	l.mu.Unlock()
}

func (l *resilientListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return net.ErrClosed
	}
	l.closed = true
	inner := l.inner
	var err error
	if inner != nil {
		err = inner.Close()
	}
	l.mu.Unlock()
	return err
}

func (l *resilientListener) Addr() net.Addr {
	l.mu.Lock()
	inner := l.inner
	l.mu.Unlock()
	if inner == nil {
		return nil
	}
	return inner.Addr()
}

func (l *resilientListener) SecondsSinceLastAccept() int64 {
	last := l.lastAccept.Load()
	if last == 0 {
		return 0
	}
	elapsed := time.Now().Unix() - last
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

func (l *resilientListener) Current() (net.Listener, uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner, l.generation
}
