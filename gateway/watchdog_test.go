package gateway

import (
	"errors"
	"net"
	"testing"
	"time"
)

type watchdogTestListener struct {
	established uint
	closed      bool
	closeCalls  int
}

func (l *watchdogTestListener) Accept() (net.Conn, error) {
	return nil, net.ErrClosed
}

func (l *watchdogTestListener) Close() error {
	l.closed = true
	l.closeCalls++
	return nil
}

func (l *watchdogTestListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
}

func (l *watchdogTestListener) IsClosed() bool {
	return l.closed
}

func (l *watchdogTestListener) GetEstablishedCount() uint {
	return l.established
}

type watchdogClosedOnlyListener struct {
	closed bool
}

func (l *watchdogClosedOnlyListener) Accept() (net.Conn, error) {
	return nil, net.ErrClosed
}

func (l *watchdogClosedOnlyListener) Close() error {
	l.closed = true
	return nil
}

func (l *watchdogClosedOnlyListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
}

func (l *watchdogClosedOnlyListener) IsClosed() bool {
	return l.closed
}

func newTestWatchdog(initial net.Listener, relisten func() (net.Listener, error)) (*watchdog, *resilientListener, *time.Time) {
	now := time.Unix(100, 0)
	resilient := newResilientListener(initial)
	w := newWatchdog(resilient, relisten, ResilienceConfig{
		WatchdogEnabled:      true,
		PollInterval:         time.Millisecond,
		ZeroEstablishedGrace: 50 * time.Millisecond,
		MaxRebuildFailures:   2,
		HeartbeatInterval:    0,
	}, true, "share")
	w.now = func() time.Time { return now }
	return w, resilient, &now
}

func TestWatchdogRebuildsOnlyAfterZeroEstablishedGrace(t *testing.T) {
	initial := &watchdogTestListener{}
	relistenCalls := 0
	w, _, now := newTestWatchdog(initial, func() (net.Listener, error) {
		relistenCalls++
		return &watchdogTestListener{established: 1}, nil
	})

	w.check()
	*now = now.Add(25 * time.Millisecond)
	w.check()
	if relistenCalls != 0 {
		t.Fatalf("relisten called before grace elapsed: %d", relistenCalls)
	}

	initial.established = 1
	w.check()
	initial.established = 0
	w.check()
	*now = now.Add(25 * time.Millisecond)
	w.check()
	if relistenCalls != 0 {
		t.Fatalf("transient zero reused stale grace timer")
	}

	*now = now.Add(51 * time.Millisecond)
	w.check()
	if relistenCalls != 1 {
		t.Fatalf("relisten calls = %d, want 1", relistenCalls)
	}
}

func TestWatchdogSwapGetsFreshGraceAndFailureResetRequiresHealth(t *testing.T) {
	initial := &watchdogTestListener{}
	replacements := []*watchdogTestListener{{}, {}}
	relistenCalls := 0
	w, resilient, now := newTestWatchdog(initial, func() (net.Listener, error) {
		listener := replacements[relistenCalls]
		relistenCalls++
		return listener, nil
	})

	w.check()
	*now = now.Add(51 * time.Millisecond)
	w.check()
	if relistenCalls != 1 {
		t.Fatalf("initial rebuild calls = %d, want 1", relistenCalls)
	}
	if w.failedRebuilds != 0 {
		t.Fatalf("bare swap reset/count mismatch, failures = %d", w.failedRebuilds)
	}

	w.check()
	*now = now.Add(49 * time.Millisecond)
	w.check()
	if relistenCalls != 1 {
		t.Fatalf("replacement did not get a fresh grace window")
	}

	*now = now.Add(2 * time.Millisecond)
	w.check()
	if relistenCalls != 2 {
		t.Fatalf("dead-on-arrival replacement did not rebuild again")
	}
	if w.failedRebuilds != 1 {
		t.Fatalf("dead-on-arrival replacement failures = %d, want 1", w.failedRebuilds)
	}

	current, _ := resilient.Current()
	current.(*watchdogTestListener).established = 1
	w.check()
	if w.failedRebuilds != 0 {
		t.Fatalf("healthy observation did not reset failures: %d", w.failedRebuilds)
	}
}

func TestWatchdogClosedReplacementCountsFailedAttempt(t *testing.T) {
	initial := &watchdogTestListener{}
	replacements := []*watchdogTestListener{{closed: true}, {established: 1}}
	relistenCalls := 0
	w, _, now := newTestWatchdog(initial, func() (net.Listener, error) {
		listener := replacements[relistenCalls]
		relistenCalls++
		return listener, nil
	})

	w.check()
	*now = now.Add(51 * time.Millisecond)
	w.check()
	if relistenCalls != 1 {
		t.Fatalf("initial rebuild calls = %d, want 1", relistenCalls)
	}

	w.check()
	if relistenCalls != 2 {
		t.Fatalf("closed replacement did not rebuild again")
	}
	if w.failedRebuilds != 1 {
		t.Fatalf("closed replacement failures = %d, want 1", w.failedRebuilds)
	}
}

func TestWatchdogFailFastAfterConsecutiveRelistenErrors(t *testing.T) {
	initial := &watchdogTestListener{}
	w, resilient, now := newTestWatchdog(initial, func() (net.Listener, error) {
		return nil, errors.New("fabric down")
	})

	w.check()
	*now = now.Add(51 * time.Millisecond)
	w.check()
	w.check()

	if resilient.terminal == nil {
		t.Fatal("expected fail-fast terminal error")
	}
}

func TestWatchdogManagedKeepsRetryingAfterEscalation(t *testing.T) {
	initial := &watchdogTestListener{}
	relistenCalls := 0
	w, resilient, now := newTestWatchdog(initial, func() (net.Listener, error) {
		relistenCalls++
		return nil, errors.New("fabric down")
	})
	w.failFast = false

	w.check()
	*now = now.Add(51 * time.Millisecond)
	w.check()
	w.check()
	w.check()

	if resilient.terminal != nil {
		t.Fatalf("managed watchdog should not fail listener: %v", resilient.terminal)
	}
	if relistenCalls < 3 {
		t.Fatalf("managed watchdog stopped retrying after escalation: %d", relistenCalls)
	}
}

func TestWatchdogClosedOnlyFallback(t *testing.T) {
	open := &watchdogClosedOnlyListener{}
	relistenCalls := 0
	w, _, _ := newTestWatchdog(open, func() (net.Listener, error) {
		relistenCalls++
		return &watchdogClosedOnlyListener{}, nil
	})

	w.check()
	if relistenCalls != 0 {
		t.Fatalf("open closed-only listener rebuilt: %d", relistenCalls)
	}

	open.closed = true
	w.check()
	if relistenCalls != 1 {
		t.Fatalf("closed-only listener did not rebuild: %d", relistenCalls)
	}
}
