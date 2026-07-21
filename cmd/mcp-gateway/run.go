package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/michaelquigley/df/dl"
	mcpagora "github.com/openziti/mcp-gateway/agora"
	"github.com/openziti/mcp-gateway/gateway"
	"github.com/spf13/cobra"
)

// redirectStderr is set in platform_unix.go to redirect stderr via unix.Dup2.
var redirectStderr func(fd uintptr) error

func init() {
	rootCmd.AddCommand(newRunCommand().cmd)
}

type runCommand struct {
	cmd                  *cobra.Command
	network              string
	agoraIntegrationFile string
}

type gatewayRunner interface {
	Start(context.Context) error
	Run(context.Context) error
	Stop() error
}

var loadGatewayConfig = gateway.LoadConfigRaw

var newGatewayRunner = func(cfg *gateway.Config) (gatewayRunner, error) {
	return gateway.New(cfg)
}

func newRunCommand() *runCommand {
	cobraCmd := &cobra.Command{
		Use:   "run <configPath>",
		Short: "Run the MCP gateway",
		Args:  cobra.ExactArgs(1),
	}
	command := &runCommand{cmd: cobraCmd}
	cobraCmd.Flags().StringVar(&command.network, "network", "", "network shortcut: zrok or agora")
	cobraCmd.Flags().StringVar(&command.agoraIntegrationFile, "agora-integration-file", "", "path to Agora integration file (overrides config)")
	cobraCmd.RunE = command.run
	return command
}

func (cmd *runCommand) run(_ *cobra.Command, args []string) (retErr error) {
	configPath := args[0]

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// load config first to check for log file redirection
	cfg, err := loadGatewayConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if err := cmd.applyOverrides(cfg); err != nil {
		return err
	}
	if err := mcpagora.ResolveConfig(cfg.Agora); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	// if log file is specified, redirect logging to it with JSON format
	// this allows gateway to survive orchestrator kill -9 (no inherited FDs)
	if cfg.LogFile != "" {
		logFile, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("failed to open log file '%s': %w", cfg.LogFile, err)
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

	b, err := newGatewayRunner(cfg)
	if err != nil {
		return fmt.Errorf("failed to create gateway: %w", err)
	}

	if err := b.Start(ctx); err != nil {
		return fmt.Errorf("failed to start: %w", err)
	}
	defer func() {
		if err := b.Stop(); err != nil {
			if retErr == nil {
				retErr = fmt.Errorf("failed to stop gateway: %w", err)
			} else {
				dl.Log().With("error", err).Warn("failed to stop gateway during cleanup")
			}
		}
	}()

	if err := b.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run failed: %w", err)
	}

	return nil
}

func (cmd *runCommand) applyOverrides(cfg *gateway.Config) error {
	if cmd.network != "" && cmd.network != "zrok" && cmd.network != "agora" {
		return fmt.Errorf("invalid --network value '%s' (expected 'zrok' or 'agora')", cmd.network)
	}

	if cmd.network == "agora" {
		if cfg.Agora == nil {
			cfg.Agora = &mcpagora.Config{}
		}
		cfg.Agora.Enabled = true
	}

	agoraIntegrationFile := cmd.agoraIntegrationFile
	if agoraIntegrationFile == "" {
		agoraIntegrationFile = os.Getenv("AGORA_MCP_GATEWAY_INTEGRATION_FILE")
	}
	if agoraIntegrationFile != "" {
		if cfg.Agora == nil {
			cfg.Agora = &mcpagora.Config{}
		}
		cfg.Agora.IntegrationFile = agoraIntegrationFile
	}

	return nil
}
