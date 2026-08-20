package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/michaelquigley/df/dl"
	mcpagora "github.com/openziti/mcp-gateway/agora"
	"github.com/openziti/mcp-gateway/bridge"
	"github.com/spf13/cobra"
)

var (
	env                  []string
	workingDir           string
	shareToken           string
	accessGrants         []string
	network              string
	agoraTunnel          string
	agoraIntegrationFile string
)

var rootCmd = &cobra.Command{
	Use:           "mcp-bridge <command> [args...]",
	Short:         "bridge a local stdio mcp server to the network via zrok",
	Args:          cobra.MinimumNArgs(1),
	RunE:          run,
	SilenceErrors: true,
	SilenceUsage:  true,
}

func init() {
	rootCmd.Flags().StringArrayVar(&env, "env", nil, "environment variables in KEY=VALUE format (can be specified multiple times)")
	rootCmd.Flags().StringVar(&workingDir, "working-dir", "", "working directory for the command")
	rootCmd.Flags().StringVar(&shareToken, "share-token", "", "pre-created zrok share token (managed mode)")
	rootCmd.Flags().StringArrayVar(&accessGrants, "access-grant", nil, "zrok account email granted access (can be specified multiple times)")
	rootCmd.Flags().StringVar(&network, "network", "", "network shortcut: zrok or agora")
	rootCmd.Flags().StringVar(&agoraTunnel, "agora-tunnel", "", "agora tunnel name to serve (bind if it exists, else create+delete; default: instance name)")
	rootCmd.Flags().StringVar(&agoraIntegrationFile, "agora-integration-file", "", "path to Agora integration file (overrides config)")
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

	if err := applyOverrides(cfg); err != nil {
		return err
	}
	if err := mcpagora.ResolveConfig(cfg.Agora); err != nil {
		return err
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

func applyOverrides(cfg *bridge.Config) error {
	if network != "" && network != "zrok" && network != "agora" {
		return fmt.Errorf("invalid --network value '%s' (expected 'zrok' or 'agora')", network)
	}

	if network == "agora" {
		if cfg.Agora == nil {
			cfg.Agora = &mcpagora.Config{}
		}
		cfg.Agora.Enabled = true
		if cfg.Agora.Serve == nil {
			cfg.Agora.Serve = &mcpagora.ServeConfig{}
		}
		cfg.Agora.Serve.Enabled = true
		// publishing is left at its default (publish when workgroup IDs are
		// available) rather than forced on, so an enrolled account without an
		// integration file can serve without publishing.
		if cfg.Zrok == nil {
			cfg.Zrok = &bridge.ZrokConfig{}
		}
		if cfg.Zrok.Share == nil {
			cfg.Zrok.Share = &bridge.ZrokShareConfig{}
		}
		cfg.Zrok.Share.Enabled = false
	}

	if len(accessGrants) > 0 {
		if cfg.Zrok == nil {
			cfg.Zrok = &bridge.ZrokConfig{}
		}
		if cfg.Zrok.Share == nil {
			cfg.Zrok.Share = &bridge.ZrokShareConfig{Enabled: true}
		}
		cfg.Zrok.Share.AccessGrants = append([]string(nil), accessGrants...)
	}

	if tunnel := strings.TrimSpace(agoraTunnel); tunnel != "" {
		if cfg.Agora == nil {
			cfg.Agora = &mcpagora.Config{}
		}
		if cfg.Agora.Serve == nil {
			cfg.Agora.Serve = &mcpagora.ServeConfig{}
		}
		cfg.Agora.Serve.Tunnel = tunnel
	}

	integrationFile := agoraIntegrationFile
	if integrationFile == "" {
		integrationFile = os.Getenv("AGORA_MCP_BRIDGE_INTEGRATION_FILE")
	}
	if integrationFile != "" {
		if cfg.Agora == nil {
			cfg.Agora = &mcpagora.Config{}
		}
		cfg.Agora.IntegrationFile = integrationFile
	}

	return nil
}

func main() {
	dl.Init(dl.DefaultOptions().SetTrimPrefix("github.com/openziti/"))
	if err := rootCmd.Execute(); err != nil {
		dl.Fatalf(err)
	}
}
