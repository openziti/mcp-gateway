package agora

import (
	"fmt"
	"os"

	"github.com/michaelquigley/df/dd"
)

func loadIntegrationFile(path string) (*IntegrationFile, error) {
	file := &IntegrationFile{}
	if err := dd.MergeYAMLFile(file, path); err != nil {
		return nil, fmt.Errorf("load agora integration file '%s': %w", path, err)
	}
	return file, nil
}

func mergeIntegrationFile(cfg *Config, file *IntegrationFile) {
	if cfg == nil || file == nil {
		return
	}

	if cfg.APIEndpoint == "" {
		cfg.APIEndpoint = file.APIEndpoint
	}
	if cfg.EnvRoot == "" {
		cfg.EnvRoot = file.EnvRoot
	}
	if file.Advertisement == nil {
		return
	}
	if cfg.Advertisement == nil {
		cfg.Advertisement = &AdvertisementConfig{}
	}
	if len(cfg.Advertisement.WorkgroupIDs) == 0 {
		cfg.Advertisement.WorkgroupIDs = append([]string(nil), file.Advertisement.WorkgroupIDs...)
	}
	if cfg.Advertisement.ContractID == "" {
		cfg.Advertisement.ContractID = file.Advertisement.ContractID
	}
}

// ResolveConfig expands environment variables and merges the integration file.
func ResolveConfig(cfg *Config) error {
	if cfg == nil || !cfg.Enabled {
		return nil
	}

	expandStrings(cfg)
	if cfg.IntegrationFile != "" {
		file, err := loadIntegrationFile(cfg.IntegrationFile)
		if err != nil {
			return err
		}
		mergeIntegrationFile(cfg, file)
		expandStrings(cfg)
	}

	return nil
}

func expandStrings(cfg *Config) {
	cfg.IntegrationFile = os.ExpandEnv(cfg.IntegrationFile)
	cfg.APIEndpoint = os.ExpandEnv(cfg.APIEndpoint)
	cfg.EnvRoot = os.ExpandEnv(cfg.EnvRoot)
	cfg.InstanceName = os.ExpandEnv(cfg.InstanceName)
	cfg.Description = os.ExpandEnv(cfg.Description)

	if cfg.Advertisement != nil {
		cfg.Advertisement.ContractID = os.ExpandEnv(cfg.Advertisement.ContractID)
		for i := range cfg.Advertisement.WorkgroupIDs {
			cfg.Advertisement.WorkgroupIDs[i] = os.ExpandEnv(cfg.Advertisement.WorkgroupIDs[i])
		}
		for i := range cfg.Advertisement.Capabilities {
			cfg.Advertisement.Capabilities[i] = os.ExpandEnv(cfg.Advertisement.Capabilities[i])
		}
	}
	if cfg.Serve != nil {
		cfg.Serve.Tunnel = os.ExpandEnv(cfg.Serve.Tunnel)
		for i := range cfg.Serve.Grants {
			cfg.Serve.Grants[i] = os.ExpandEnv(cfg.Serve.Grants[i])
		}
	}
}
