// mcp-filesystem is a sandboxed filesystem MCP server. it exposes read_file,
// write_file, and list_directory tools that are restricted to the allowed root
// directories specified on the command line.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/michaelquigley/df/dl"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openziti/mcp-gateway/build"
	"github.com/spf13/cobra"
)

type readFileParams struct {
	Path string `json:"path" jsonschema:"path to the file to read"`
}

type writeFileParams struct {
	Path    string `json:"path" jsonschema:"path to the file to write"`
	Content string `json:"content" jsonschema:"content to write to the file"`
}

type listDirectoryParams struct {
	Path string `json:"path" jsonschema:"path to the directory to list"`
}

var rootCmd = &cobra.Command{
	Use:     "mcp-filesystem <dir> [dir...]",
	Short:   "sandboxed filesystem MCP server",
	Version: build.String(),
	Args:    cobra.MinimumNArgs(1),
	Run:     run,
}

func run(_ *cobra.Command, args []string) {
	fsys, err := newFS(args)
	if err != nil {
		dl.Fatalf("failed to initialize: %v", err)
	}

	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "mcp-filesystem",
			Version: build.String(),
		},
		nil,
	)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_file",
		Description: "read and return the contents of a file",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args readFileParams) (*mcp.CallToolResult, any, error) {
		content, err := fsys.readFile(args.Path)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("error: %v", err)}},
				IsError: true,
			}, nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: content}},
		}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "write_file",
		Description: "create or overwrite a file with the given content",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args writeFileParams) (*mcp.CallToolResult, any, error) {
		if err := fsys.writeFile(args.Path, args.Content); err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("error: %v", err)}},
				IsError: true,
			}, nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("wrote '%s'", args.Path)}},
		}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_directory",
		Description: "list entries in a directory with type indicators",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args listDirectoryParams) (*mcp.CallToolResult, any, error) {
		result, err := fsys.listDirectory(args.Path)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("error: %v", err)}},
				IsError: true,
			}, nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: result}},
		}, nil, nil
	})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func main() {
	dl.Init(dl.DefaultOptions().SetTrimPrefix("github.com/openziti/"))
	if err := rootCmd.Execute(); err != nil {
		dl.Fatalf(err)
	}
}
