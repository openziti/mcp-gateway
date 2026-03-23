package main

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/michaelquigley/df/dl"
	"github.com/openziti/mcp-gateway/bridge"
	"github.com/openziti/mcp-gateway/build"
	"github.com/spf13/cobra"
)

var (
	env        []string
	workingDir string
	shareToken string
)

var rootCmd = &cobra.Command{
	Use:           "mcp-bridge <command> [args...]",
	Short:         "bridge a local stdio mcp server to the network via zrok",
	Version:       build.String(),
	Args:          cobra.MinimumNArgs(1),
	RunE:          run,
	SilenceErrors: true,
	SilenceUsage:  true,
}

func init() {
	rootCmd.Flags().StringArrayVar(&env, "env", nil, "environment variables in KEY=VALUE format (can be specified multiple times)")
	rootCmd.Flags().StringVar(&workingDir, "working-dir", "", "working directory for the command")
	rootCmd.Flags().StringVar(&shareToken, "share-token", "", "pre-created zrok share token (managed mode)")
}

type bridgeRunner interface {
	Start(context.Context) error
	Run(context.Context) error
	Stop() error
}

var newBridgeRunner = func(cfg *bridge.Config) (bridgeRunner, error) {
	return bridge.New(cfg)
}

func run(_ *cobra.Command, args []string) (retErr error) {
	command := args[0]

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// parse environment variables
	envMap := make(map[string]string)
	for _, e := range env {
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				envMap[e[:i]] = e[i+1:]
				break
			}
		}
	}

	cfg := &bridge.Config{
		Command:    command,
		Args:       args[1:],
		Env:        envMap,
		WorkingDir: workingDir,
		ShareToken: shareToken,
	}

	b, err := newBridgeRunner(cfg)
	if err != nil {
		return fmt.Errorf("failed to create bridge: %w", err)
	}

	if err := b.Start(ctx); err != nil {
		return fmt.Errorf("failed to start: %w", err)
	}
	defer func() {
		if err := b.Stop(); err != nil {
			if retErr == nil {
				retErr = fmt.Errorf("failed to stop bridge: %w", err)
			} else {
				dl.Log().With("error", err).Warn("failed to stop bridge during cleanup")
			}
		}
	}()

	if err := b.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run failed: %w", err)
	}

	return nil
}

func main() {
	dl.Init(dl.DefaultOptions().SetTrimPrefix("github.com/openziti/"))
	if err := rootCmd.Execute(); err != nil {
		dl.Fatalf(err)
	}
}
