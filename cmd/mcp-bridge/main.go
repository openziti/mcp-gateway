package main

import (
	"context"
	"errors"
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
	Use:     "mcp-bridge <command> [args...]",
	Short:   "bridge a local stdio mcp server to the network via zrok",
	Version: build.String(),
	Args:    cobra.MinimumNArgs(1),
	Run:     run,
}

func init() {
	rootCmd.Flags().StringArrayVar(&env, "env", nil, "environment variables in KEY=VALUE format (can be specified multiple times)")
	rootCmd.Flags().StringVar(&workingDir, "working-dir", "", "working directory for the command")
	rootCmd.Flags().StringVar(&shareToken, "share-token", "", "pre-created zrok share token (managed mode)")
}

func run(_ *cobra.Command, args []string) {
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

	b, err := bridge.New(cfg)
	if err != nil {
		dl.Fatalf("failed to create bridge: %v", err)
	}

	if err := b.Start(ctx); err != nil {
		dl.Fatalf("failed to start: %v", err)
	}
	defer b.Stop()

	if err := b.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		dl.Fatalf("error: %v", err)
	}
}

func main() {
	dl.Init(dl.DefaultOptions().SetTrimPrefix("github.com/openziti/"))
	if err := rootCmd.Execute(); err != nil {
		dl.Fatalf(err)
	}
}
