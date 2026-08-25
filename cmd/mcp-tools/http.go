package main

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/michaelquigley/df/dl"
	"github.com/openziti/mcp-gateway/streamable"
	"github.com/openziti/mcp-gateway/tools"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(newHTTPCommand().cmd)
}

type httpCommand struct {
	bind                 string
	agoraTunnel          string
	agoraIntegrationFile string
	stateless            bool
	jsonResponse         bool
	sessionIdleTimeout   time.Duration
	cmd                  *cobra.Command
}

func newHTTPCommand() *httpCommand {
	cmd := &cobra.Command{
		Use:   "http [<shareToken>]",
		Short: "serve mcp over http (streamable http transport)",
		Args:  cobra.MaximumNArgs(1),
	}
	command := &httpCommand{cmd: cmd}
	cmd.Flags().StringVar(&command.bind, "bind", "127.0.0.1:8080", "address to bind to")
	cmd.Flags().StringVar(&command.agoraTunnel, "agora", "", "agora tunnel to connect to")
	cmd.Flags().StringVar(&command.agoraIntegrationFile, "agora-integration-file", "", "path to Agora integration file")
	cmd.Flags().BoolVar(&command.stateless, "stateless", false, "run in stateless mode")
	cmd.Flags().BoolVar(&command.jsonResponse, "json-response", false, "prefer json responses over sse")
	cmd.Flags().DurationVar(&command.sessionIdleTimeout, "session-idle-timeout", streamable.DefaultSessionIdleTimeout, "close Streamable HTTP sessions after this much inactivity (0 disables)")
	cmd.RunE = command.run
	return command
}

func (cmd *httpCommand) run(_ *cobra.Command, args []string) (retErr error) {
	// a negative duration reaches the SDK as "never expire", which is the
	// opposite of the documented zero opt-out. gateway and bridge already
	// refuse it.
	if cmd.sessionIdleTimeout < 0 {
		return fmt.Errorf("session idle timeout must not be negative")
	}

	target, err := resolveToolsTarget(args, cmd.agoraTunnel, cmd.agoraIntegrationFile)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	c, err := newToolsClient(target)
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
		Address:            cmd.bind,
		Stateless:          cmd.stateless,
		JSONResponse:       cmd.jsonResponse,
		SessionIdleTimeout: &cmd.sessionIdleTimeout,
	}

	dl.Log().With("bind", cmd.bind).Info("starting http server")

	if err := c.RunHTTP(ctx, opts); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run failed: %w", err)
	}

	return nil
}
