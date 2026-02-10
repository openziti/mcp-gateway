package main

import (
	"os"
	"syscall"
)

func init() {
	redirectStderr = func(fd uintptr) error {
		return syscall.Dup3(int(fd), int(os.Stderr.Fd()), 0)
	}
}
