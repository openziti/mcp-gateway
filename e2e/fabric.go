//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// the suite shells out to the zrok and Agora CLIs for two things only:
// provisioning the persistent (bind-path) fixtures, and asserting afterwards
// that ephemeral resources really were released. everything on the data path
// goes through the project's own binaries.

const fabricCLITimeout = 60 * time.Second

// agoraBin locates the Agora CLI. it is not always on PATH — the dev
// controller is often built into its own GOPATH — so an override is honored.
func agoraBin() string {
	if override := strings.TrimSpace(os.Getenv("MCP_E2E_AGORA_BIN")); override != "" {
		return override
	}
	if path, err := exec.LookPath("agora"); err == nil {
		return path
	}
	return ""
}

// agoraEnvRoot reports the enrolled Agora environment directory.
func agoraEnvRoot() string {
	if root := strings.TrimSpace(os.Getenv("AGORA_ENV_ROOT")); root != "" {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".agora")
}

// runFabricCLI executes a provisioning or inspection command.
func runFabricCLI(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(fabricCLITimeout):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
		return string(out), fmt.Errorf("%s %s timed out after %s", name, strings.Join(args, " "), fabricCLITimeout)
	}
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// guardName refuses to touch any resource this suite did not name.
func guardName(name string) error {
	if !strings.HasPrefix(name, namePrefix) {
		return fmt.Errorf("refusing to manage fabric resource %q: not prefixed %q", name, namePrefix)
	}
	return nil
}

// -----------------------------------------------------------------------------
// zrok
// -----------------------------------------------------------------------------

func zrokAvailable() error {
	if _, err := exec.LookPath("zrok2"); err != nil {
		return fmt.Errorf("zrok2 is not on PATH")
	}
	if out, err := runFabricCLI("zrok2", "status"); err != nil {
		return fmt.Errorf("zrok2 environment is not enabled: %v\n%s", err, out)
	}
	return nil
}

// zrokCreateShare provisions a persistent named private share. the name is the
// share token, which is what makes the bind path reproducible across restarts.
func zrokCreateShare(name string) error {
	if err := guardName(name); err != nil {
		return err
	}
	_, err := runFabricCLI("zrok2", "create", "share", "-s", name)
	return err
}

func zrokDeleteShare(name string) error {
	if err := guardName(name); err != nil {
		return err
	}
	_, err := runFabricCLI("zrok2", "delete", "share", name)
	return err
}

func zrokShareExists(t *testing.T, name string) bool {
	t.Helper()
	out, err := runFabricCLI("zrok2", "list", "shares")
	if err != nil {
		t.Fatalf("zrok2 list shares: %v", err)
	}
	return strings.Contains(out, name)
}

// -----------------------------------------------------------------------------
// agora
// -----------------------------------------------------------------------------

func agoraAvailable() error {
	root := agoraEnvRoot()
	if root == "" {
		return fmt.Errorf("cannot determine the Agora environment root")
	}
	if _, err := os.Stat(filepath.Join(root, "environment.json")); err != nil {
		return fmt.Errorf("no enrolled Agora environment at %s (set AGORA_ENV_ROOT)", root)
	}
	return nil
}

// agoraCreateTunnel provisions a persistent direct TCP tunnel. mcp-gateway and
// mcp-bridge bind an existing tunnel rather than creating one, and leave it
// intact on shutdown.
func agoraCreateTunnel(name string) error {
	if err := guardName(name); err != nil {
		return err
	}
	bin := agoraBin()
	if bin == "" {
		return fmt.Errorf("the agora CLI is not available")
	}
	_, err := runFabricCLI(bin, "tunnel", "create", name, "--mode", "tcp")
	return err
}

func agoraDeleteTunnel(name string) error {
	if err := guardName(name); err != nil {
		return err
	}
	bin := agoraBin()
	if bin == "" {
		return fmt.Errorf("the agora CLI is not available")
	}
	_, err := runFabricCLI(bin, "tunnel", "delete", name)
	return err
}

func agoraTunnelExists(t *testing.T, name string) bool {
	t.Helper()
	bin := agoraBin()
	if bin == "" {
		t.Skip("the agora CLI is not available; set MCP_E2E_AGORA_BIN to assert tunnel lifecycle")
	}
	out, err := runFabricCLI(bin, "tunnel", "list")
	if err != nil {
		t.Fatalf("agora tunnel list: %v", err)
	}
	return strings.Contains(out, name)
}

// -----------------------------------------------------------------------------
// lifecycle assertions
// -----------------------------------------------------------------------------

// awaitZrokShareAbsent asserts an ephemeral share was released on shutdown.
func awaitZrokShareAbsent(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if !zrokShareExists(t, name) {
			t.Logf("ephemeral zrok share %s released", name)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Errorf("ephemeral zrok share %s outlived the process that created it", name)
}

// awaitAgoraTunnelAbsent asserts an ephemeral tunnel was released on shutdown.
func awaitAgoraTunnelAbsent(t *testing.T, name string) {
	t.Helper()
	if agoraBin() == "" {
		t.Logf("skipping tunnel-release assertion for %s: the agora CLI is not available", name)
		return
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if !agoraTunnelExists(t, name) {
			t.Logf("ephemeral agora tunnel %s released", name)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Errorf("ephemeral agora tunnel %s outlived the process that created it", name)
}
