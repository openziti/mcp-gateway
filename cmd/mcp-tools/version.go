package main

import (
	"fmt"

	"github.com/openziti/mcp-gateway/build"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(newVersionCommand())
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), build.String())
			return nil
		},
	}
}
