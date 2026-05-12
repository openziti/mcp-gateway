package agora

// Config holds Agora integration settings shared by the mcp-gateway binaries.
type Config struct {
	Enabled         bool
	IntegrationFile string

	APIEndpoint string
	EnvRoot     string

	InstanceName string
	Description  string
	TunnelMode   string // tcp, http, or udp unless constrained by Defaults

	Advertisement *AdvertisementConfig
	Serve         *ServeConfig
}

// AdvertisementConfig controls Agora catalog publication.
type AdvertisementConfig struct {
	Publish      *bool
	WorkgroupIDs []string `dd:"workgroup_ids"`
	ContractID   string
	Capabilities []string
}

// ServeConfig controls Agora Layer 1 serve behavior.
type ServeConfig struct {
	Enabled bool
	Grants  []string
}

// IntegrationFile is the demo-bootstrap handoff file shape.
type IntegrationFile struct {
	APIEndpoint   string
	EnvRoot       string
	Advertisement *IntegrationAdvertisement
}

// IntegrationAdvertisement holds catalog identifiers from demo-bootstrap.
type IntegrationAdvertisement struct {
	WorkgroupIDs []string `dd:"workgroup_ids"`
	ContractID   string
}

// ServeEnabled reports whether Agora serving is enabled.
func ServeEnabled(cfg *Config) bool {
	return cfg != nil && cfg.Serve != nil && cfg.Serve.Enabled
}

// AdvertisementPublish reports whether catalog publication is enabled.
func AdvertisementPublish(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	if cfg.Advertisement != nil && cfg.Advertisement.Publish != nil {
		return *cfg.Advertisement.Publish
	}
	return true
}
