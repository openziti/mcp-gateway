package agora

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadIntegrationFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agora.yaml")
	if err := os.WriteFile(path, []byte(`
api_endpoint: "http://127.0.0.1:8080"
env_root: "/tmp/agora"
advertisement:
  workgroup_ids:
    - wg_abcdefghijkl
  contract_id: con_abcdefghijkl
`), 0o600); err != nil {
		t.Fatalf("write integration file: %v", err)
	}

	file, err := loadIntegrationFile(path)
	if err != nil {
		t.Fatalf("loadIntegrationFile returned error: %v", err)
	}
	if file.APIEndpoint != "http://127.0.0.1:8080" || file.EnvRoot != "/tmp/agora" {
		t.Fatalf("unexpected file: %#v", file)
	}
	if got := file.Advertisement.WorkgroupIDs; len(got) != 1 || got[0] != "wg_abcdefghijkl" {
		t.Fatalf("unexpected workgroup IDs: %#v", got)
	}
}

func TestLoadIntegrationFileMissing(t *testing.T) {
	if _, err := loadIntegrationFile(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected missing file error")
	}
}

func TestMergeIntegrationFilePreservesExplicitConfig(t *testing.T) {
	cfg := &Config{
		APIEndpoint: "http://inline.example",
		Advertisement: &AdvertisementConfig{
			ContractID: "con_inline1234",
		},
	}
	file := &IntegrationFile{
		APIEndpoint: "http://file.example",
		EnvRoot:     "/tmp/file",
		Advertisement: &IntegrationAdvertisement{
			WorkgroupIDs: []string{"wg_abcdefghijkl"},
			ContractID:   "con_abcdefghijkl",
		},
	}

	mergeIntegrationFile(cfg, file)

	if cfg.APIEndpoint != "http://inline.example" {
		t.Fatalf("inline API endpoint was overwritten: %q", cfg.APIEndpoint)
	}
	if cfg.EnvRoot != "/tmp/file" {
		t.Fatalf("env root = %q", cfg.EnvRoot)
	}
	if cfg.Advertisement.ContractID != "con_inline1234" {
		t.Fatalf("inline contract was overwritten: %q", cfg.Advertisement.ContractID)
	}
	if got := cfg.Advertisement.WorkgroupIDs; len(got) != 1 || got[0] != "wg_abcdefghijkl" {
		t.Fatalf("workgroup IDs were not merged: %#v", got)
	}
}

func TestResolveConfigExpandsEnvAndMergesIntegrationFile(t *testing.T) {
	t.Setenv("AGORA_ROOT", "/tmp/agora-root")
	t.Setenv("AGORA_WG", "wg_abcdefghijkl")

	path := filepath.Join(t.TempDir(), "agora.yaml")
	if err := os.WriteFile(path, []byte(`
api_endpoint: "http://controller.example"
env_root: "${AGORA_ROOT}"
advertisement:
  workgroup_ids:
    - "${AGORA_WG}"
  contract_id: con_abcdefghijkl
`), 0o600); err != nil {
		t.Fatalf("write integration file: %v", err)
	}

	cfg := &Config{
		Enabled:         true,
		IntegrationFile: path,
		InstanceName:    "${AGORA_INSTANCE}",
		Advertisement:   &AdvertisementConfig{Capabilities: []string{"${AGORA_CAPABILITY}"}},
	}
	t.Setenv("AGORA_INSTANCE", "engineering")
	t.Setenv("AGORA_CAPABILITY", "mcp-tools")

	if err := ResolveConfig(cfg); err != nil {
		t.Fatalf("ResolveConfig returned error: %v", err)
	}
	if cfg.APIEndpoint != "http://controller.example" {
		t.Fatalf("api endpoint = %q", cfg.APIEndpoint)
	}
	if cfg.EnvRoot != "/tmp/agora-root" {
		t.Fatalf("env root = %q", cfg.EnvRoot)
	}
	if cfg.InstanceName != "engineering" {
		t.Fatalf("instance name = %q", cfg.InstanceName)
	}
	if got := cfg.Advertisement.WorkgroupIDs; len(got) != 1 || got[0] != "wg_abcdefghijkl" {
		t.Fatalf("workgroup IDs = %#v", got)
	}
	if got := cfg.Advertisement.Capabilities; len(got) != 1 || got[0] != "mcp-tools" {
		t.Fatalf("capabilities = %#v", got)
	}
}

func TestAdvertisementPublishDefaultAndOverride(t *testing.T) {
	if !AdvertisementPublish(&Config{}) {
		t.Fatal("expected default publish to be true")
	}
	publish := false
	if AdvertisementPublish(&Config{Advertisement: &AdvertisementConfig{Publish: &publish}}) {
		t.Fatal("expected explicit false to disable publishing")
	}
}
