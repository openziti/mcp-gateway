//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func init() {
	ignoreOrchSignals = func() {
		signal.Ignore(syscall.SIGPIPE, syscall.SIGHUP, syscall.SIGURG)
	}
	termSignals = func() []os.Signal {
		return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	}
}
