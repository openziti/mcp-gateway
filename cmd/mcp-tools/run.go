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
	"github.com/openziti/mcp-gateway/tools"
	"github.com/spf13/cobra"
)

const agoraToolsIntegrationFileEnv = "AGORA_MCP_TOOLS_INTEGRATION_FILE"

func init() {
	rootCmd.AddCommand(newRunCommand().cmd)
}

type runCommand struct {
	agoraTunnel          string
	agoraIntegrationFile string
	cmd                  *cobra.Command
}

type toolsClient interface {
	Start(context.Context) error
	Run(context.Context) error
	RunHTTP(context.Context, *tools.HTTPOptions) error
	Stop() error
}

type toolsTarget struct {
	ShareToken  string
	AgoraTunnel string
	AgoraConfig *mcpagora.Config
}

var newToolsClient = func(target toolsTarget) (toolsClient, error) {
	if target.AgoraTunnel != "" {
		return tools.NewAgora(target.AgoraTunnel, target.AgoraConfig)
	}
	return tools.New(target.ShareToken)
}

var resolveAgoraConfig = mcpagora.ResolveConfig

func newRunCommand() *runCommand {
	cmd := &cobra.Command{
		Use:   "run [<shareToken>]",
		Short: "connect to an mcp gateway share",
		Args:  cobra.MaximumNArgs(1),
	}
	command := &runCommand{cmd: cmd}
	cmd.Flags().StringVar(&command.agoraTunnel, "agora", "", "agora tunnel to connect to")
	cmd.Flags().StringVar(&command.agoraIntegrationFile, "agora-integration-file", "", "path to Agora integration file")
	cmd.RunE = command.run
	return command
}

func (cmd *runCommand) run(_ *cobra.Command, args []string) (retErr error) {
	target, err := resolveToolsTarget(args, cmd.agoraTunnel, cmd.agoraIntegrationFile)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
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

	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run failed: %w", err)
	}

	return nil
}

func resolveToolsTarget(args []string, agoraTunnel, integrationFile string) (toolsTarget, error) {
	agoraTunnel = strings.TrimSpace(agoraTunnel)
	if agoraTunnel != "" && len(args) > 0 {
		return toolsTarget{}, fmt.Errorf("--agora is mutually exclusive with positional share token")
	}
	if agoraTunnel == "" {
		if len(args) != 1 {
			return toolsTarget{}, fmt.Errorf("share token is required unless --agora is set")
		}
		return toolsTarget{ShareToken: args[0]}, nil
	}

	if integrationFile == "" {
		integrationFile = os.Getenv(agoraToolsIntegrationFileEnv)
	}
	cfg := &mcpagora.Config{
		Enabled:         true,
		IntegrationFile: integrationFile,
	}
	if err := resolveAgoraConfig(cfg); err != nil {
		return toolsTarget{}, err
	}
	return toolsTarget{
		AgoraTunnel: agoraTunnel,
		AgoraConfig: cfg,
	}, nil
}
