package aggregator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type pathPolicyRule struct {
	argument string
	roots    []string
}

// CallPolicy is the resolved, immutable policy for one backend connection.
// rules are keyed by backend-original tool name.
type CallPolicy struct {
	base  string
	paths map[string][]pathPolicyRule
}

// NewCallPolicy resolves policy roots once, before the backend is exposed.
func NewCallPolicy(config PolicyConfig, workingDir string) (*CallPolicy, error) {
	policy := &CallPolicy{paths: map[string][]pathPolicyRule{}}
	if len(config.Paths) == 0 {
		return policy, nil
	}

	base := workingDir
	if base == "" {
		var err error
		base, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("call policy working directory: %w", err)
		}
	}
	resolvedBase, err := resolveExistingDirectory(base)
	if err != nil {
		return nil, fmt.Errorf("call policy working directory: %w", err)
	}
	policy.base = resolvedBase

	for _, configured := range config.Paths {
		rule := pathPolicyRule{argument: configured.Argument}
		for _, root := range configured.Roots {
			resolved, err := resolveExistingDirectory(root)
			if err != nil {
				return nil, fmt.Errorf("call policy tool %q argument %q root %q: %w", configured.Tool, configured.Argument, root, err)
			}
			rule.roots = append(rule.roots, resolved)
		}
		policy.paths[configured.Tool] = append(policy.paths[configured.Tool], rule)
	}
	return policy, nil
}

// Enforce refuses a configured tool call whose governed argument is missing,
// malformed, unresolvable, or outside every allowed root. Tools without a path
// rule retain the gateway's existing behavior; callers such as Sterling close
// their advertised method surface before starting the gateway.
func (p *CallPolicy) Enforce(tool string, args any) error {
	if !p.governs(tool) {
		return nil
	}
	_, err := p.Prepare(tool, args)
	return err
}

// Prepare leaves ungoverned calls unchanged. For governed calls it settles
// arguments once, applies policy, and returns the exact map to dispatch.
func (p *CallPolicy) Prepare(tool string, args any) (any, error) {
	rules := p.rules(tool)
	if len(rules) == 0 {
		return args, nil
	}
	arguments, err := settleArgumentObject(args)
	if err != nil {
		return nil, p.argumentError(tool, rules, err)
	}
	if arguments == nil {
		return nil, p.argumentError(tool, rules, fmt.Errorf("got null"))
	}
	for _, rule := range rules {
		value, ok := arguments[rule.argument]
		if !ok {
			return arguments, fmt.Errorf("call policy tool %q: required path argument %q is missing", tool, rule.argument)
		}
		pathValue, ok := value.(string)
		if !ok || pathValue == "" {
			return arguments, fmt.Errorf("call policy tool %q: path argument %q must be a non-empty string", tool, rule.argument)
		}
		resolved, err := p.resolveCandidate(pathValue)
		if err != nil {
			return arguments, fmt.Errorf("call policy tool %q argument %q: %w", tool, rule.argument, err)
		}
		allowed := false
		for _, root := range rule.roots {
			if pathWithin(root, resolved) {
				allowed = true
				break
			}
		}
		if !allowed {
			return arguments, fmt.Errorf("call policy tool %q argument %q: path %q resolves outside allowed roots", tool, rule.argument, pathValue)
		}
	}
	return arguments, nil
}

func (p *CallPolicy) governs(tool string) bool { return len(p.rules(tool)) != 0 }

func (p *CallPolicy) rules(tool string) []pathPolicyRule {
	if p == nil {
		return nil
	}
	return p.paths[tool]
}

func (p *CallPolicy) argumentError(tool string, rules []pathPolicyRule, err error) error {
	names := make([]string, 0, len(rules))
	for _, rule := range rules {
		names = append(names, fmt.Sprintf("%q", rule.argument))
	}
	return fmt.Errorf("call policy tool %q argument %s: arguments must be a JSON object: %w", tool, strings.Join(names, ", "), err)
}

// ValidateTools fails when a configured policy tool was not advertised by the
// backend at startup. An explicit policy typo must never degrade to no policy.
func (p *CallPolicy) ValidateTools(tools []*mcp.Tool) error {
	if p == nil || len(p.paths) == 0 {
		return nil
	}
	advertised := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		advertised[tool.Name] = struct{}{}
	}
	configured := make([]string, 0, len(p.paths))
	for tool := range p.paths {
		configured = append(configured, tool)
	}
	sort.Strings(configured)
	for _, tool := range configured {
		if _, ok := advertised[tool]; !ok {
			return fmt.Errorf("call policy tool %q is not advertised by the backend", tool)
		}
	}
	return nil
}

// WorkingDir returns the directory resolved when this policy was created.
// an empty result means the backend has no path rules to govern.
func (p *CallPolicy) WorkingDir() string {
	if p == nil {
		return ""
	}
	return p.base
}

func settleArgumentObject(args any) (map[string]any, error) {
	if args == nil {
		return nil, nil
	}
	data, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeUniqueJSON(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("got %T", value)
	}
	return object, nil
}

func decodeUniqueJSON(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		object := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("object key has type %T", keyToken)
			}
			if _, duplicate := object[key]; duplicate {
				return nil, fmt.Errorf("duplicate JSON object key %q", key)
			}
			value, err := decodeUniqueJSON(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return nil, fmt.Errorf("close JSON object: %v", err)
		}
		return object, nil
	case '[':
		var array []any
		for decoder.More() {
			value, err := decodeUniqueJSON(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return nil, fmt.Errorf("close JSON array: %v", err)
		}
		return array, nil
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

func resolveExistingDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return filepath.Clean(resolved), nil
}

func (p *CallPolicy) resolveCandidate(path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.base, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, resolveErr := filepath.EvalSymlinks(absolute)
	if resolveErr == nil {
		return filepath.Clean(resolved), nil
	}

	// parent-only resolution is valid only for a genuinely absent leaf. an
	// existing but unresolvable leaf may be a dangling symlink whose target
	// escapes the roots, and must fail closed.
	if _, err := os.Lstat(absolute); err == nil {
		return "", fmt.Errorf("path cannot be resolved: %w", resolveErr)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect unresolved path: %w", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", fmt.Errorf("path and its parent cannot be resolved: %w", err)
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// PolicyDeniedResult returns a tool-level refusal without invoking the backend,
// allowing the caller/model to observe and recover from the denied operation.
func PolicyDeniedResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "gateway policy denied tool call: " + err.Error()}},
		IsError: true,
	}
}
