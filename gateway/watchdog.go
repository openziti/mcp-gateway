package gateway

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/michaelquigley/df/dl"
)

type closeChecker interface {
	IsClosed() bool
}

type establishedCounter interface {
	GetEstablishedCount() uint
}

type listenerHealthFunc func(net.Listener) (closeChecker, establishedCounter)

type watchdog struct {
	resilient *resilientListener
	relisten  func() (net.Listener, error)
	healthOf  listenerHealthFunc
	cfg       ResilienceConfig
	failFast  bool
	token     string
	now       func() time.Time

	zeroSince         time.Time
	zeroGeneration    uint64
	pendingGeneration uint64
	countedGeneration uint64
	failedRebuilds    int

	warnedNoEstablished bool
	warnedNoClose       bool
}

func newWatchdog(resilient *resilientListener, relisten func() (net.Listener, error), cfg ResilienceConfig, failFast bool, token string) *watchdog {
	return &watchdog{
		resilient: resilient,
		relisten:  relisten,
		healthOf:  listenerHealthOf,
		cfg:       cfg,
		failFast:  failFast,
		token:     token,
		now:       time.Now,
	}
}

func listenerHealthOf(listener net.Listener) (closeChecker, establishedCounter) {
	closer, _ := listener.(closeChecker)
	counter, _ := listener.(establishedCounter)
	return closer, counter
}

func (w *watchdog) run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.check()
		case <-ctx.Done():
			return
		}
	}
}

func (w *watchdog) check() {
	listener, generation := w.resilient.Current()
	closer, counter := w.healthOf(listener)

	if counter == nil && !w.warnedNoEstablished {
		dl.Log().Warn("established-count health unavailable, count-based rebuild disabled")
		w.warnedNoEstablished = true
	}
	if closer == nil && !w.warnedNoClose {
		dl.Log().Warn("closed-listener health unavailable")
		w.warnedNoClose = true
	}

	closed := closer != nil && closer.IsClosed()
	if counter != nil {
		count := counter.GetEstablishedCount()
		if !closed && count > 0 {
			w.markHealthy()
			return
		}
		if closed {
			w.handleDead("closed", generation, &count)
			return
		}
		w.handleZeroEstablished(generation, count)
		return
	}

	if closer != nil {
		if closed {
			w.handleDead("closed", generation, nil)
			return
		}
		w.markHealthy()
	}
}

func (w *watchdog) handleZeroEstablished(generation uint64, count uint) {
	now := w.now()
	if w.zeroSince.IsZero() || w.zeroGeneration != generation {
		w.zeroSince = now
		w.zeroGeneration = generation
		return
	}
	if now.Sub(w.zeroSince) < w.cfg.ZeroEstablishedGrace {
		return
	}

	if w.pendingGeneration == generation && w.countedGeneration != generation {
		if w.recordFailedAttempt(fmt.Errorf("share listener generation '%d' stayed at zero established terminators", generation)) {
			return
		}
		w.countedGeneration = generation
	}

	w.handleDead("zero_established", generation, &count)
}

func (w *watchdog) handleDead(reason string, generation uint64, count *uint) {
	if reason != "zero_established" && w.pendingGeneration == generation && w.countedGeneration != generation {
		if w.recordFailedAttempt(fmt.Errorf("share listener generation '%d' became dead before becoming healthy", generation)) {
			return
		}
		w.countedGeneration = generation
	}

	fields := dl.Log().
		With("share_token", w.token).
		With("generation", generation).
		With("reason", reason).
		With("rebuild_failures", w.failedRebuilds)
	if count != nil {
		fields = fields.With("established_terminators", *count)
	}
	fields.Warn("share listener lost, rebuilding")

	listener, err := w.relisten()
	if err != nil {
		rebuildErr := fmt.Errorf("share listener rebuild failed: %w", err)
		dl.Log().
			With("share_token", w.token).
			With("generation", generation).
			With("error", err).
			Warn("share listener rebuild failed")
		w.recordFailedAttempt(rebuildErr)
		return
	}

	w.resilient.Swap(listener)
	_, newGeneration := w.resilient.Current()
	w.pendingGeneration = newGeneration
	w.countedGeneration = 0
	w.zeroSince = time.Time{}
	w.zeroGeneration = 0

	dl.Log().
		With("share_token", w.token).
		With("old_generation", generation).
		With("generation", newGeneration).
		With("rebuild_failures", w.failedRebuilds).
		Info("share listener rebuilt")
}

func (w *watchdog) recordFailedAttempt(err error) bool {
	w.failedRebuilds++
	if w.failedRebuilds < w.cfg.MaxRebuildFailures {
		return false
	}

	escalationErr := fmt.Errorf("share listener recovery failed after '%d' attempts: %w", w.failedRebuilds, err)
	if w.failFast {
		dl.Log().
			With("share_token", w.token).
			With("rebuild_failures", w.failedRebuilds).
			With("error", escalationErr).
			Error("share listener unrecoverable, shutting down for restart")
		w.resilient.Fail(escalationErr)
		return true
	}

	dl.Log().
		With("share_token", w.token).
		With("rebuild_failures", w.failedRebuilds).
		With("error", escalationErr).
		Error("share listener unrecoverable, awaiting orchestrator restart")
	return false
}

func (w *watchdog) markHealthy() {
	w.zeroSince = time.Time{}
	w.zeroGeneration = 0
	w.pendingGeneration = 0
	w.countedGeneration = 0
	w.failedRebuilds = 0
}
