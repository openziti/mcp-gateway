package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// fs provides sandboxed filesystem operations within allowed root directories.
type fs struct {
	roots []string
}

// newFS creates a new fs with the given root directories. each root is resolved
// to an absolute path with symlinks evaluated. returns an error if any root
// does not exist or cannot be resolved.
func newFS(roots []string) (*fs, error) {
	if len(roots) == 0 {
		return nil, fmt.Errorf("at least one root directory is required")
	}
	resolved := make([]string, len(roots))
	for i, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve root '%s': %w", root, err)
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("root '%s' does not exist or cannot be resolved: %w", root, err)
		}
		info, err := os.Stat(real)
		if err != nil {
			return nil, fmt.Errorf("root '%s' does not exist: %w", root, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("root '%s' is not a directory", root)
		}
		resolved[i] = real
	}
	return &fs{roots: resolved}, nil
}

// validatePath checks that the given path resolves to a location within one of
// the allowed roots. for paths that don't exist yet (e.g. write targets), the
// parent directory is validated instead.
func (f *fs) validatePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path '%s': %w", path, err)
	}

	// try to resolve symlinks on the full path
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// path doesn't exist — resolve the parent and rejoin basename
		parent := filepath.Dir(abs)
		base := filepath.Base(abs)
		resolvedParent, err := filepath.EvalSymlinks(parent)
		if err != nil {
			return "", fmt.Errorf("parent directory does not exist for '%s': %w", path, err)
		}
		resolved = filepath.Join(resolvedParent, base)
	}

	for _, root := range f.roots {
		if resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path '%s' is outside allowed roots", path)
}

func (f *fs) readFile(path string) (string, error) {
	resolved, err := f.validatePath(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("failed to read '%s': %w", path, err)
	}
	return string(data), nil
}

func (f *fs) writeFile(path, content string) error {
	resolved, err := f.validatePath(path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(resolved, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write '%s': %w", path, err)
	}
	return nil
}

func (f *fs) listDirectory(path string) (string, error) {
	resolved, err := f.validatePath(path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return "", fmt.Errorf("failed to list '%s': %w", path, err)
	}
	var b strings.Builder
	for _, entry := range entries {
		if entry.IsDir() {
			fmt.Fprintf(&b, "[dir]  %s\n", entry.Name())
		} else {
			fmt.Fprintf(&b, "[file] %s\n", entry.Name())
		}
	}
	return b.String(), nil
}
