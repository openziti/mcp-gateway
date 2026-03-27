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
