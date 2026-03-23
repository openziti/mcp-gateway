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
	rootCmd.AddCommand(newHTTPCommand().cmd)
}

type httpCommand struct {
	bind         string
	stateless    bool
	jsonResponse bool
	cmd          *cobra.Command
}

func newHTTPCommand() *httpCommand {
	cmd := &cobra.Command{
		Use:   "http <shareToken>",
		Short: "serve mcp over http (streamable http transport)",
		Args:  cobra.ExactArgs(1),
	}
	command := &httpCommand{cmd: cmd}
	cmd.Flags().StringVar(&command.bind, "bind", "127.0.0.1:8080", "address to bind to")
	cmd.Flags().BoolVar(&command.stateless, "stateless", false, "run in stateless mode")
	cmd.Flags().BoolVar(&command.jsonResponse, "json-response", false, "prefer json responses over sse")
	cmd.RunE = command.run
	return command
}

func (cmd *httpCommand) run(_ *cobra.Command, args []string) (retErr error) {
	shareToken := args[0]

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
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

	opts := &tools.HTTPOptions{
		Address:      cmd.bind,
		Stateless:    cmd.stateless,
		JSONResponse: cmd.jsonResponse,
	}

	dl.Log().With("bind", cmd.bind).Info("starting http server")

	if err := c.RunHTTP(ctx, opts); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run failed: %w", err)
	}

	return nil
}
