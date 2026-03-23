package main

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/michaelquigley/df/dl"
	"github.com/openziti/mcp-gateway/tools"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(newRunCommand().cmd)
}

type runCommand struct {
	cmd *cobra.Command
}

type toolsClient interface {
	Start(context.Context) error
	Run(context.Context) error
	RunHTTP(context.Context, *tools.HTTPOptions) error
	Stop() error
}

var newToolsClient = func(shareToken string) (toolsClient, error) {
	return tools.New(shareToken)
}

func newRunCommand() *runCommand {
	cmd := &cobra.Command{
		Use:   "run <shareToken>",
		Short: "connect to an mcp gateway share",
		Args:  cobra.ExactArgs(1),
	}
	command := &runCommand{cmd: cmd}
	cmd.RunE = command.run
	return command
}

func (cmd *runCommand) run(_ *cobra.Command, args []string) (retErr error) {
	shareToken := args[0]

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	c, err := newToolsClient(shareToken)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	if err := c.Start(ctx); err != nil {
		return fmt.Errorf("failed to start: %w", err)
	}
	defer func() {
		if err := c.Stop(); err != nil {
			if retErr == nil {
				retErr = fmt.Errorf("failed to stop client: %w", err)
			} else {
				dl.Log().With("error", err).Warn("failed to stop client during cleanup")
			}
		}
	}()

	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run failed: %w", err)
	}

	return nil
}
