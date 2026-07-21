package aggregator

import (
	"context"
	"fmt"

	"github.com/michaelquigley/df/dl"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server wraps an MCP server with aggregation capabilities.
type Server struct {
	mcpServer *mcp.Server
	namespace *Namespace
	backends  *BackendManager
	config    *Config
}

// NewServer creates a new aggregator MCP server.
func NewServer(cfg *Config, namespace *Namespace, backends *BackendManager) *Server {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{
			Name:    cfg.Aggregator.Name,
			Version: cfg.Aggregator.Version,
		},
		nil,
	)

	s := &Server{
		mcpServer: mcpServer,
		namespace: namespace,
		backends:  backends,
		config:    cfg,
	}

	// register all aggregated tools with routing handlers
	s.registerTools()

	return s
}

// registerTools registers all namespaced tools with the MCP server.
func (s *Server) registerTools() {
	tools := s.namespace.AllTools()
	for _, tool := range tools {
		// capture tool in closure
		t := tool
		s.mcpServer.AddTool(&t, s.createToolHandler(t.Name))
	}
	dl.Log().With("count", len(tools)).Info("registered aggregated tools")
}

// createToolHandler creates a handler for a specific namespaced tool.
func (s *Server) createToolHandler(namespacedName string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		dl.Log().With("tool", namespacedName).Debug("routing tool call")

		// lookup the tool to get routing information
		nsTool, ok := s.namespace.GetTool(namespacedName)
		if !ok {
			return nil, &ToolNotFoundError{Name: namespacedName}
		}

		// get the backend
		backend, ok := s.backends.GetBackend(nsTool.BackendID)
		if !ok {
			return nil, &BackendError{
				BackendID: nsTool.BackendID,
				Op:        "lookup",
				Err:       fmt.Errorf("backend not found"),
			}
		}
		// apply call timeout from config
		ctx, cancel := context.WithTimeout(ctx, s.config.Aggregator.Connection.CallTimeout)
		defer cancel()

		// forward the call to the backend with original tool name
		result, err := backend.CallTool(ctx, nsTool.OriginalName, req.Params.Arguments)
		if err != nil {
			dl.Log().With("tool", namespacedName).With("backend", nsTool.BackendID).With("error", err).Error("tool call failed")
			return nil, &BackendError{
				BackendID: nsTool.BackendID,
				Op:        "call_tool",
				Err:       err,
			}
		}

		dl.Log().With("tool", namespacedName).With("backend", nsTool.BackendID).Debug("tool call completed")
		return result, nil
	}
}

// Run starts the MCP server on the given transport.
func (s *Server) Run(ctx context.Context, transport mcp.Transport) error {
	return s.mcpServer.Run(ctx, transport)
}

// MCPServer returns the underlying MCP server for advanced usage.
func (s *Server) MCPServer() *mcp.Server {
	return s.mcpServer
}

// CallTool invokes a tool by its namespaced name.
func (s *Server) CallTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	toolName := req.Params.Name
	handler := s.createToolHandler(toolName)
	return handler(ctx, req)
}
