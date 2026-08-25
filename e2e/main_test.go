//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// persistent fixtures provisioned for the whole run and released at the end.
// they exist so the bind path — where a server attaches to a pre-provisioned
// share or tunnel and leaves it intact across restarts — is covered without
// leaving anything behind.
var (
	persistentShare  = resourceName("pshare")
	persistentTunnel = resourceName("ptun")

	haveAgoraCLI bool
)

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nmcp-gateway smoke suite could not start:\n  %v\n\n", err)
		fmt.Fprintf(os.Stderr, "this suite runs against live fabrics. it needs an enabled zrok v2\n"+
			"environment (zrok2 enable <token>) and an enrolled Agora environment.\n"+
			"see docs/current/smoke-suite.md.\n")
		os.Exit(1)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	if err := zrokAvailable(); err != nil {
		return 0, err
	}
	if err := agoraAvailable(); err != nil {
		return 0, err
	}
	haveAgoraCLI = agoraBin() != ""

	root, err := repoRoot()
	if err != nil {
		return 0, err
	}
	fmt.Fprintf(os.Stderr, "building working tree at %s\n", root)
	binDir, err = buildBinaries(root)
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(binDir)

	fmt.Fprintf(os.Stderr, "run id %s; binaries in %s\n", runID, binDir)
	if !haveAgoraCLI {
		fmt.Fprintf(os.Stderr, "note: the agora CLI is not on PATH; tunnel lifecycle assertions and the\n"+
			"      persistent-tunnel scenario will be skipped (set MCP_E2E_AGORA_BIN)\n")
	}

	release := provisionFixtures()
	defer release()

	return m.Run(), nil
}

// provisionFixtures creates the persistent share and tunnel, returning a
// release function that removes whatever was successfully created.
func provisionFixtures() func() {
	var cleanups []func()

	if err := zrokCreateShare(persistentShare); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create persistent zrok share %s: %v\n", persistentShare, err)
		persistentShare = ""
	} else {
		fmt.Fprintf(os.Stderr, "provisioned persistent zrok share %s\n", persistentShare)
		name := persistentShare
		cleanups = append(cleanups, func() {
			if err := zrokDeleteShare(name); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not delete persistent zrok share %s: %v\n", name, err)
				return
			}
			fmt.Fprintf(os.Stderr, "released persistent zrok share %s\n", name)
		})
	}

	if haveAgoraCLI {
		if err := agoraCreateTunnel(persistentTunnel); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not create persistent agora tunnel %s: %v\n", persistentTunnel, err)
			persistentTunnel = ""
		} else {
			fmt.Fprintf(os.Stderr, "provisioned persistent agora tunnel %s\n", persistentTunnel)
			name := persistentTunnel
			cleanups = append(cleanups, func() {
				if err := agoraDeleteTunnel(name); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not delete persistent agora tunnel %s: %v\n", name, err)
					return
				}
				fmt.Fprintf(os.Stderr, "released persistent agora tunnel %s\n", name)
			})
		}
	} else {
		persistentTunnel = ""
	}

	return func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}
}

// repoRoot walks up from the test package to the module root.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find go.mod above %s", dir)
		}
		dir = parent
	}
}
