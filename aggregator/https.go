package aggregator

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var systemCertPool = x509.SystemCertPool

// BuildHTTPClient creates an http.Client configured with TLS and header injection
// for connecting to HTTP(S) MCP backends.
func BuildHTTPClient(cfg TransportConfig) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	endpoint, err := validateEndpointForClient(cfg)
	if err != nil {
		return nil, err
	}

	if endpoint.Scheme == "https" && cfg.TLS != nil {
		tlsConfig := &tls.Config{}
		if cfg.TLS.InsecureSkipVerify {
			tlsConfig.InsecureSkipVerify = true
		}
		if cfg.TLS.CACertFile != "" {
			rootCAs, err := loadRootCAs(cfg.TLS.CACertFile)
			if err != nil {
				return nil, err
			}
			tlsConfig.RootCAs = rootCAs
		}
		transport.TLSClientConfig = tlsConfig
	}

	var rt http.RoundTripper = transport
	if len(cfg.Headers) > 0 {
		rt = &headerRoundTripper{
			base:    transport,
			headers: cfg.Headers,
		}
	}

	return &http.Client{Transport: rt}, nil
}

func loadRootCAs(caCertFile string) (*x509.CertPool, error) {
	caCert, err := os.ReadFile(caCertFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read ca cert file '%s': %w", caCertFile, err)
	}

	rootCAs, err := systemCertPool()
	if err != nil {
		return nil, fmt.Errorf("failed to load system ca pool: %w", err)
	}
	if rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}
	if !rootCAs.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse ca cert from '%s'", caCertFile)
	}

	return rootCAs, nil
}

func validateEndpointForClient(cfg TransportConfig) (*url.URL, error) {
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, fmt.Errorf("invalid endpoint url for '%s' transport", cfg.Type)
	}

	switch cfg.Type {
	case "https":
		if endpoint.Scheme != "https" {
			return nil, fmt.Errorf("endpoint scheme must be 'https' for https transport")
		}
	case "http":
		if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
			return nil, fmt.Errorf("endpoint scheme must be 'http' or 'https' for http transport")
		}
		if endpoint.Scheme == "http" && !cfg.AllowInsecure {
			return nil, fmt.Errorf("allow_insecure must be true for http endpoints")
		}
		if endpoint.Scheme == "http" && cfg.TLS != nil {
			return nil, fmt.Errorf("tls configuration is only valid for https endpoints")
		}
	default:
		return nil, fmt.Errorf("unsupported transport type '%s'", cfg.Type)
	}

	return endpoint, nil
}

// BuildMCPTransport creates the appropriate MCP client transport based on the
// protocol setting. Defaults to SSE if protocol is empty.
func BuildMCPTransport(cfg TransportConfig, httpClient *http.Client) (mcp.Transport, error) {
	protocol := cfg.Protocol
	if protocol == "" {
		protocol = "sse"
	}

	switch protocol {
	case "sse":
		return &mcp.SSEClientTransport{
			Endpoint:   cfg.Endpoint,
			HTTPClient: httpClient,
		}, nil
	case "streamable":
		return &mcp.StreamableClientTransport{
			Endpoint:   cfg.Endpoint,
			HTTPClient: httpClient,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported protocol '%s'", protocol)
	}
}

// headerRoundTripper injects custom headers into every HTTP request.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.base.RoundTrip(req)
}
