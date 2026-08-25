package aggregator

import "testing"

func TestValidateAcceptsHTTPSTransportWithHTTPSEndpoint(t *testing.T) {
	cfg := &Config{
		Backends: []BackendConfig{{
			ID: "remote",
			Transport: TransportConfig{
				Type:     "https",
				Endpoint: "https://mcp.example.com/sse",
			},
		}},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestValidateRejectsHTTPSEndpointForHTTPSTransport(t *testing.T) {
	cfg := &Config{
		Backends: []BackendConfig{{
			ID: "remote",
			Transport: TransportConfig{
				Type:     "https",
				Endpoint: "http://mcp.example.com/sse",
			},
		}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}

	configErr, ok := err.(*ConfigError)
	if !ok {
		t.Fatalf("expected ConfigError, got %T", err)
	}
	if configErr.Field != "backends[0].transport.endpoint" {
		t.Fatalf("expected endpoint field, got %s", configErr.Field)
	}
}

func TestValidateAcceptsHTTPTransportWithHTTPSEndpoint(t *testing.T) {
	cfg := &Config{
		Backends: []BackendConfig{{
			ID: "remote",
			Transport: TransportConfig{
				Type:     "http",
				Endpoint: "https://mcp.example.com/sse",
			},
		}},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestValidateRejectsHTTPTransportWithoutAllowInsecure(t *testing.T) {
	cfg := &Config{
		Backends: []BackendConfig{{
			ID: "remote",
			Transport: TransportConfig{
				Type:     "http",
				Endpoint: "http://localhost:8080/sse",
			},
		}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}

	configErr, ok := err.(*ConfigError)
	if !ok {
		t.Fatalf("expected ConfigError, got %T", err)
	}
	if configErr.Field != "backends[0].transport.allow_insecure" {
		t.Fatalf("expected allow_insecure field, got %s", configErr.Field)
	}
}

func TestValidateAcceptsHTTPTransportWithAllowInsecure(t *testing.T) {
	cfg := &Config{
		Backends: []BackendConfig{{
			ID: "remote",
			Transport: TransportConfig{
				Type:          "http",
				Endpoint:      "http://localhost:8080/sse",
				AllowInsecure: true,
			},
		}},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestValidateRejectsTLSForPlainHTTPTransport(t *testing.T) {
	cfg := &Config{
		Backends: []BackendConfig{{
			ID: "remote",
			Transport: TransportConfig{
				Type:          "http",
				Endpoint:      "http://localhost:8080/sse",
				AllowInsecure: true,
				TLS:           &TLSConfig{},
			},
		}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}

	configErr, ok := err.(*ConfigError)
	if !ok {
		t.Fatalf("expected ConfigError, got %T", err)
	}
	if configErr.Field != "backends[0].transport.tls" {
		t.Fatalf("expected tls field, got %s", configErr.Field)
	}
}

func TestValidateRejectsMalformedHTTPTransportEndpoint(t *testing.T) {
	cfg := &Config{
		Backends: []BackendConfig{{
			ID: "remote",
			Transport: TransportConfig{
				Type:     "http",
				Endpoint: "://bad-url",
			},
		}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}

	configErr, ok := err.(*ConfigError)
	if !ok {
		t.Fatalf("expected ConfigError, got %T", err)
	}
	if configErr.Field != "backends[0].transport.endpoint" {
		t.Fatalf("expected endpoint field, got %s", configErr.Field)
	}
}

func TestValidateRejectsHTTPBehaviorOptInsOnNonHTTPTransports(t *testing.T) {
	tests := []struct {
		name      string
		transport TransportConfig
		field     string
	}{
		{
			name: "stdio environment proxy",
			transport: TransportConfig{
				Type:                  "stdio",
				Command:               "backend",
				AllowEnvironmentProxy: true,
			},
			field: "backends[0].transport.allow_environment_proxy",
		},
		{
			name: "stdio redirects",
			transport: TransportConfig{
				Type:           "stdio",
				Command:        "backend",
				AllowRedirects: true,
			},
			field: "backends[0].transport.allow_redirects",
		},
		{
			name: "zrok environment proxy",
			transport: TransportConfig{
				Type:                  "zrok",
				ShareToken:            "share-token",
				AllowEnvironmentProxy: true,
			},
			field: "backends[0].transport.allow_environment_proxy",
		},
		{
			name: "zrok redirects",
			transport: TransportConfig{
				Type:           "zrok",
				ShareToken:     "share-token",
				AllowRedirects: true,
			},
			field: "backends[0].transport.allow_redirects",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Backends: []BackendConfig{{ID: "remote", Transport: tt.transport}}}
			err := cfg.Validate()
			configErr, ok := err.(*ConfigError)
			if !ok {
				t.Fatalf("expected ConfigError, got %T (%v)", err, err)
			}
			if configErr.Field != tt.field {
				t.Fatalf("field = %q, want %q", configErr.Field, tt.field)
			}
		})
	}
}

func TestValidateAcceptsAgoraTransport(t *testing.T) {
	cfg := &Config{
		Backends: []BackendConfig{{
			ID: "remote",
			Transport: TransportConfig{
				Type:        "agora",
				AgoraTunnel: "filesystem-relay",
			},
		}},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestValidateAcceptsOverlayProtocols(t *testing.T) {
	tests := []TransportConfig{
		{Type: "zrok", ShareToken: "share-token", Protocol: "streamable"},
		{Type: "zrok", ShareToken: "share-token", Protocol: "sse"},
		{Type: "agora", AgoraTunnel: "filesystem-relay", Protocol: "streamable"},
		{Type: "agora", AgoraTunnel: "filesystem-relay", Protocol: "sse"},
	}

	for _, transport := range tests {
		t.Run(transport.Type+"-"+transport.Protocol, func(t *testing.T) {
			cfg := &Config{Backends: []BackendConfig{{ID: "remote", Transport: transport}}}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("expected success, got %v", err)
			}
		})
	}
}

func TestValidateRejectsUnknownOverlayProtocols(t *testing.T) {
	tests := []TransportConfig{
		{Type: "zrok", ShareToken: "share-token", Protocol: "unknown"},
		{Type: "agora", AgoraTunnel: "filesystem-relay", Protocol: "unknown"},
	}

	for _, transport := range tests {
		t.Run(transport.Type, func(t *testing.T) {
			cfg := &Config{Backends: []BackendConfig{{ID: "remote", Transport: transport}}}
			err := cfg.Validate()
			configErr, ok := err.(*ConfigError)
			if !ok {
				t.Fatalf("expected ConfigError, got %T (%v)", err, err)
			}
			if configErr.Field != "backends[0].transport.protocol" {
				t.Fatalf("field = %q, want protocol field", configErr.Field)
			}
		})
	}
}

func TestValidateRejectsAgoraTransportWithoutTunnel(t *testing.T) {
	cfg := &Config{
		Backends: []BackendConfig{{
			ID: "remote",
			Transport: TransportConfig{
				Type: "agora",
			},
		}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}

	configErr, ok := err.(*ConfigError)
	if !ok {
		t.Fatalf("expected ConfigError, got %T", err)
	}
	if configErr.Field != "backends[0].transport.agora_tunnel" {
		t.Fatalf("expected agora_tunnel field, got %s", configErr.Field)
	}
}

func TestValidateRejectsAgoraTransportWithOtherTransportFields(t *testing.T) {
	cfg := &Config{
		Backends: []BackendConfig{{
			ID: "remote",
			Transport: TransportConfig{
				Type:        "agora",
				AgoraTunnel: "filesystem-relay",
				ShareToken:  "zrok-share",
			},
		}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}

	configErr, ok := err.(*ConfigError)
	if !ok {
		t.Fatalf("expected ConfigError, got %T", err)
	}
	if configErr.Field != "backends[0].transport" {
		t.Fatalf("expected transport field, got %s", configErr.Field)
	}
}
