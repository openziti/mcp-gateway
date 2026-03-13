package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidatePath_WithinRoot(t *testing.T) {
	dir := t.TempDir()
	f, err := newFS([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello"), 0644)

	resolved, err := f.validatePath(path)
	if err != nil {
		t.Fatalf("expected path within root to be valid: %v", err)
	}
	if resolved != path {
		t.Fatalf("expected '%s', got '%s'", path, resolved)
	}
}

func TestValidatePath_OutsideRoot(t *testing.T) {
	dir := t.TempDir()
	f, err := newFS([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.validatePath("/etc/passwd")
	if err == nil {
		t.Fatal("expected error for path outside root")
	}
}

func TestValidatePath_TraversalAttack(t *testing.T) {
	dir := t.TempDir()
	f, err := newFS([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.validatePath(filepath.Join(dir, "..", "..", "etc", "passwd"))
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestValidatePath_SymlinkOutsideRoot(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0644)

	link := filepath.Join(dir, "escape")
	os.Symlink(outside, link)

	f, err := newFS([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.validatePath(filepath.Join(link, "secret.txt"))
	if err == nil {
		t.Fatal("expected error for symlink pointing outside root")
	}
}

func TestValidatePath_MultipleRoots(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	os.WriteFile(filepath.Join(dir1, "a.txt"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(dir2, "b.txt"), []byte("b"), 0644)

	f, err := newFS([]string{dir1, dir2})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.validatePath(filepath.Join(dir1, "a.txt")); err != nil {
		t.Fatalf("expected path in first root to be valid: %v", err)
	}
	if _, err := f.validatePath(filepath.Join(dir2, "b.txt")); err != nil {
		t.Fatalf("expected path in second root to be valid: %v", err)
	}
}

func TestValidatePath_WriteNonExistentFile(t *testing.T) {
	dir := t.TempDir()
	f, err := newFS([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	// parent exists and is within root — should be valid
	path := filepath.Join(dir, "newfile.txt")
	resolved, err := f.validatePath(path)
	if err != nil {
		t.Fatalf("expected non-existent file with valid parent to be valid: %v", err)
	}
	if resolved != path {
		t.Fatalf("expected '%s', got '%s'", path, resolved)
	}
}

func TestValidatePath_WriteNonExistentParent(t *testing.T) {
	dir := t.TempDir()
	f, err := newFS([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	// parent does not exist
	path := filepath.Join(dir, "nodir", "newfile.txt")
	_, err = f.validatePath(path)
	if err == nil {
		t.Fatal("expected error for non-existent parent directory")
	}
}

func TestValidatePath_RootBoundary(t *testing.T) {
	// create /tmp/xxx/user and /tmp/xxx/user2 to test that /tmp/xxx/user
	// root does not match /tmp/xxx/user2/file
	base := t.TempDir()
	root := filepath.Join(base, "user")
	sibling := filepath.Join(base, "user2")
	os.MkdirAll(root, 0755)
	os.MkdirAll(sibling, 0755)
	os.WriteFile(filepath.Join(sibling, "file.txt"), []byte("data"), 0644)

	f, err := newFS([]string{root})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.validatePath(filepath.Join(sibling, "file.txt"))
	if err == nil {
		t.Fatal("expected error: root /user should not match /user2/file")
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	os.WriteFile(path, []byte("hello world"), 0644)

	f, err := newFS([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	content, err := f.readFile(path)
	if err != nil {
		t.Fatalf("readFile failed: %v", err)
	}
	if content != "hello world" {
		t.Fatalf("expected 'hello world', got '%s'", content)
	}
}

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")

	f, err := newFS([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	if err := f.writeFile(path, "written content"); err != nil {
		t.Fatalf("writeFile failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read back written file: %v", err)
	}
	if string(data) != "written content" {
		t.Fatalf("expected 'written content', got '%s'", string(data))
	}
}

func TestListDirectory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte(""), 0644)
	os.MkdirAll(filepath.Join(dir, "subdir"), 0755)

	f, err := newFS([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	result, err := f.listDirectory(dir)
	if err != nil {
		t.Fatalf("listDirectory failed: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty directory listing")
	}
	// check that both entries appear
	if !contains(result, "file.txt") {
		t.Fatalf("expected file.txt in listing: %s", result)
	}
	if !contains(result, "subdir") {
		t.Fatalf("expected subdir in listing: %s", result)
	}
	if !contains(result, "[dir]") {
		t.Fatalf("expected [dir] prefix in listing: %s", result)
	}
	if !contains(result, "[file]") {
		t.Fatalf("expected [file] prefix in listing: %s", result)
	}
}

func TestNewFS_NonExistentRoot(t *testing.T) {
	_, err := newFS([]string{"/nonexistent/path/that/does/not/exist"})
	if err == nil {
		t.Fatal("expected error for non-existent root")
	}
}

func TestNewFS_EmptyRoots(t *testing.T) {
	_, err := newFS([]string{})
	if err == nil {
		t.Fatal("expected error for empty roots")
	}
}

func TestValidatePath_RootItself(t *testing.T) {
	dir := t.TempDir()
	f, err := newFS([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	// the root directory itself should be valid (for list_directory)
	resolved, err := f.validatePath(dir)
	if err != nil {
		t.Fatalf("expected root itself to be valid: %v", err)
	}
	if resolved != dir {
		t.Fatalf("expected '%s', got '%s'", dir, resolved)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
