//go:build !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func init() {
	redirectStderr = func(fd uintptr) error {
		return unix.Dup2(int(fd), int(os.Stderr.Fd()))
	}
}
