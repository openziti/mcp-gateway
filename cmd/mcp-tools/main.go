package main

import (
	"os"

	"github.com/michaelquigley/df/dl"
	"github.com/openziti/mcp-gateway/build"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:     "mcp-tools",
	Short:   "Access MCP tooling through MCP Gateway",
	Version: build.String(),
}

func main() {
	dl.Init(dl.DefaultOptions().SetTrimPrefix("github.com/openziti/"))
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
