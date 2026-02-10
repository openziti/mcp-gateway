package main

import (
	"context"
	"errors"
	"os"
	"os/signal"

	"github.com/michaelquigley/df/dl"
	"github.com/openziti/mcp-gateway/gateway"
	"github.com/spf13/cobra"
)

// Platform hooks implemented in platform_*.go files.
// redirectStderr: on Linux uses syscall.Dup3; nil on other platforms.
// ignoreOrchSignals: on Unix ignores SIGPIPE/SIGHUP/SIGURG; nil on Windows.
// termSignals: returns platform-appropriate termination signals for NotifyContext.
var (
	redirectStderr  func(fd uintptr) error
	ignoreOrchSignals func()
	termSignals     func() []os.Signal
)

func init() {
	rootCmd.AddCommand(newRunCommand().cmd)
}

type runCommand struct {
	cmd *cobra.Command
}

func newRunCommand() *runCommand {
	cmd := &cobra.Command{
		Use:   "run <configPath>",
		Short: "Run the MCP gateway",
		Args:  cobra.ExactArgs(1),
	}
	command := &runCommand{cmd: cmd}
	cmd.Run = command.run
	return command
}

func (cmd *runCommand) run(_ *cobra.Command, args []string) {
	configPath := args[0]

	// ignore signals that could cause unexpected exit when orchestrator disconnects
	if ignoreOrchSignals != nil {
		ignoreOrchSignals()
	}

	sigs := []os.Signal{os.Interrupt}
	if termSignals != nil {
		sigs = termSignals()
	}
	ctx, cancel := signal.NotifyContext(context.Background(), sigs...)
	defer cancel()

	// load config first to check for log file redirection
	cfg, err := gateway.LoadConfig(configPath)
	if err != nil {
		dl.Fatalf("failed to load config: %v", err)
	}

	// if log file is specified, redirect logging to it with JSON format
	// this allows gateway to survive orchestrator kill -9 (no inherited FDs)
	if cfg.LogFile != "" {
		logFile, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			dl.Fatalf("failed to open log file '%s': %v", cfg.LogFile, err)
		}
		defer logFile.Close()
		dl.Init(dl.DefaultOptions().SetTrimPrefix("github.com/openziti/").SetOutput(logFile).JSON())

		// redirect stderr to log file so we can see panic messages
		// (panics go to stderr, not to the logger)
		if redirectStderr != nil {
			if err := redirectStderr(logFile.Fd()); err != nil {
				dl.Warnf("failed to redirect stderr to log file: %v", err)
			}
		}
	}

	b, err := gateway.New(cfg)
	if err != nil {
		dl.Fatalf("failed to create gateway: %v", err)
	}

	if err := b.Start(ctx); err != nil {
		dl.Fatalf("failed to start: %v", err)
	}
	defer b.Stop()

	if err := b.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		dl.Fatalf("error: %v", err)
	}
}
