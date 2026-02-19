package main

import (
	"github.com/michaelquigley/df/dl"
	"github.com/openziti/mcp-gateway/build"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:     "mcp-gateway",
	Short:   "Aggregate and serve MCP tools via zrok",
	Version: build.String(),
}

func main() {
	dl.Init(dl.DefaultOptions().SetTrimPrefix("github.com/openziti/"))
	if err := rootCmd.Execute(); err != nil {
		dl.Fatalf(err)
	}
}
