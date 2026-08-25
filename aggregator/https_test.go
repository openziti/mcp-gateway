package aggregator

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBuildHTTPSClient_AppendsCustomCAToSystemPool(t *testing.T) {
	origSystemCertPool := systemCertPool
	defer func() {
		systemCertPool = origSystemCertPool
	}()

	systemPEM, systemCert := newTestCert(t, "system")
	customPEM, customCert := newTestCert(t, "custom")

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(systemPEM) {
		t.Fatalf("expected system cert to append")
	}

	systemCertPool = func() (*x509.CertPool, error) {
		return pool, nil
	}

	caPath := writeTempCertFile(t, customPEM)
	client, err := BuildHTTPClient(TransportConfig{
		Type:     "https",
		Endpoint: "https://mcp.example.com/sse",
		TLS: &TLSConfig{
			CACertFile: caPath,
		},
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	rootCAs := transport.TLSClientConfig.RootCAs
	if rootCAs == nil {
		t.Fatalf("expected root CA pool")
	}

	subjects := rootCAs.Subjects()
	if !containsSubject(subjects, systemCert.RawSubject) {
		t.Fatalf("expected system subject to remain trusted")
	}
	if !containsSubject(subjects, customCert.RawSubject) {
		t.Fatalf("expected custom subject to be appended")
	}
}

func TestBuildHTTPSClient_ReturnsSystemCertPoolError(t *testing.T) {
	origSystemCertPool := systemCertPool
	defer func() {
		systemCertPool = origSystemCertPool
	}()

	customPEM, _ := newTestCert(t, "custom")
	caPath := writeTempCertFile(t, customPEM)

	systemCertPool = func() (*x509.CertPool, error) {
		return nil, errors.New("boom")
	}

	_, err := BuildHTTPClient(TransportConfig{
		Type:     "https",
		Endpoint: "https://mcp.example.com/sse",
		TLS: &TLSConfig{
			CACertFile: caPath,
		},
	})
	if err == nil || err.Error() != "failed to load system ca pool: boom" {
		t.Fatalf("expected system pool error, got %v", err)
	}
}

func TestBuildHTTPSClient_ReturnsParseErrorForInvalidPEM(t *testing.T) {
	origSystemCertPool := systemCertPool
	defer func() {
		systemCertPool = origSystemCertPool
	}()

	systemCertPool = func() (*x509.CertPool, error) {
		return x509.NewCertPool(), nil
	}

	caPath := writeTempCertFile(t, []byte("not a cert"))
	_, err := BuildHTTPClient(TransportConfig{
		Type:     "https",
		Endpoint: "https://mcp.example.com/sse",
		TLS: &TLSConfig{
			CACertFile: caPath,
		},
	})
	if err == nil || err.Error() != "failed to parse ca cert from '"+caPath+"'" {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestBuildHTTPSClient_SkipsSystemPoolWithoutCustomCA(t *testing.T) {
	origSystemCertPool := systemCertPool
	defer func() {
		systemCertPool = origSystemCertPool
	}()

	calls := 0
	systemCertPool = func() (*x509.CertPool, error) {
		calls++
		return x509.NewCertPool(), nil
	}

	client, err := BuildHTTPClient(TransportConfig{
		Type:     "https",
		Endpoint: "https://mcp.example.com/sse",
		TLS: &TLSConfig{
			InsecureSkipVerify: true,
		},
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if calls != 0 {
		t.Fatalf("expected system cert pool to remain unused, got %d calls", calls)
	}
	if transport.TLSClientConfig == nil || !transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("expected insecure skip verify to remain enabled")
	}
	if transport.TLSClientConfig.RootCAs != nil {
		t.Fatalf("expected root CAs to remain unset without a custom CA file")
	}
}

func TestBuildHTTPSClient_UsesEmptyPoolWhenSystemPoolIsNil(t *testing.T) {
	origSystemCertPool := systemCertPool
	defer func() {
		systemCertPool = origSystemCertPool
	}()

	customPEM, customCert := newTestCert(t, "custom")
	caPath := writeTempCertFile(t, customPEM)

	systemCertPool = func() (*x509.CertPool, error) {
		return nil, nil
	}

	client, err := BuildHTTPClient(TransportConfig{
		Type:     "https",
		Endpoint: "https://mcp.example.com/sse",
		TLS: &TLSConfig{
			CACertFile: caPath,
		},
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	rootCAs := transport.TLSClientConfig.RootCAs
	if rootCAs == nil {
		t.Fatalf("expected root CA pool")
	}
	if !containsSubject(rootCAs.Subjects(), customCert.RawSubject) {
		t.Fatalf("expected custom subject in fallback pool")
	}
}

func TestBuildHTTPClient_RejectsPlainHTTPWithoutAllowInsecure(t *testing.T) {
	_, err := BuildHTTPClient(TransportConfig{
		Type:     "http",
		Endpoint: "http://localhost:8080/sse",
	})
	if err == nil || err.Error() != "allow_insecure must be true for http endpoints" {
		t.Fatalf("expected allow_insecure error, got %v", err)
	}
}

func TestBuildHTTPClient_AcceptsPlainHTTPWithAllowInsecure(t *testing.T) {
	client, err := BuildHTTPClient(TransportConfig{
		Type:          "http",
		Endpoint:      "http://localhost:8080/sse",
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if transport.TLSClientConfig != nil {
		if transport.TLSClientConfig.InsecureSkipVerify {
			t.Fatalf("expected plain HTTP endpoint to avoid TLS overrides")
		}
		if transport.TLSClientConfig.RootCAs != nil {
			t.Fatalf("expected plain HTTP endpoint to avoid custom root CAs")
		}
	}
}

func TestBuildHTTPClient_RejectsTLSForPlainHTTPEndpoint(t *testing.T) {
	_, err := BuildHTTPClient(TransportConfig{
		Type:          "http",
		Endpoint:      "http://localhost:8080/sse",
		AllowInsecure: true,
		TLS:           &TLSConfig{},
	})
	if err == nil || err.Error() != "tls configuration is only valid for https endpoints" {
		t.Fatalf("expected tls validation error, got %v", err)
	}
}

func TestBuildHTTPClient_DefaultsToClosedNetworkPosture(t *testing.T) {
	client, err := BuildHTTPClient(TransportConfig{
		Type:          "http",
		Endpoint:      "http://mcp.example.com/sse",
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected environment proxy lookup to be disabled")
	}
	if client.CheckRedirect == nil {
		t.Fatal("expected redirects to be refused")
	}
	if err := client.CheckRedirect(&http.Request{}, nil); err == nil {
		t.Fatal("expected redirect refusal error")
	}
}

func TestBuildHTTPClient_AllowsExplicitNetworkOptIns(t *testing.T) {
	client, err := BuildHTTPClient(TransportConfig{
		Type:                  "http",
		Endpoint:              "http://mcp.example.com/sse",
		AllowInsecure:         true,
		AllowEnvironmentProxy: true,
		AllowRedirects:        true,
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if transport.Proxy == nil {
		t.Fatal("expected environment proxy lookup to be enabled")
	}
	if client.CheckRedirect == nil {
		t.Fatal("expected redirect policy")
	}
	redirect, err := http.NewRequest(http.MethodGet, "http://redirected.example.com/sse", nil)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}
	if err := client.CheckRedirect(redirect, []*http.Request{{}}); err != nil {
		t.Fatalf("expected explicitly enabled HTTP redirect, got %v", err)
	}
}

func TestBuildHTTPClient_RejectsRedirectDowngrade(t *testing.T) {
	client, err := BuildHTTPClient(TransportConfig{
		Type:           "https",
		Endpoint:       "https://mcp.example.com/sse",
		AllowRedirects: true,
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	httpsRedirect, err := http.NewRequest(http.MethodGet, "https://redirected.example.com/sse", nil)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}
	if err := client.CheckRedirect(httpsRedirect, []*http.Request{{}}); err != nil {
		t.Fatalf("expected HTTPS redirect, got %v", err)
	}

	httpRedirect, err := http.NewRequest(http.MethodGet, "http://redirected.example.com/sse", nil)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}
	if err := client.CheckRedirect(httpRedirect, []*http.Request{{}}); err == nil {
		t.Fatal("expected plaintext redirect to be refused")
	}
}

func TestBuildHTTPClient_RedirectsPreserveHopLimit(t *testing.T) {
	client, err := BuildHTTPClient(TransportConfig{
		Type:           "https",
		Endpoint:       "https://mcp.example.com/sse",
		AllowRedirects: true,
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	redirect, err := http.NewRequest(http.MethodGet, "https://redirected.example.com/sse", nil)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}
	if err := client.CheckRedirect(redirect, make([]*http.Request, 10)); err == nil {
		t.Fatal("expected ten-redirect limit")
	}
}

func TestBuildMCPTransportDefaultsToStreamable(t *testing.T) {
	httpClient := &http.Client{}
	transport, err := BuildMCPTransport(TransportConfig{}, "http://mcp.example/mcp", httpClient)
	if err != nil {
		t.Fatal(err)
	}

	streamable, ok := transport.(*mcp.StreamableClientTransport)
	if !ok {
		t.Fatalf("transport = %T, want streamable HTTP", transport)
	}
	if streamable.Endpoint != "http://mcp.example/mcp" || streamable.HTTPClient != httpClient {
		t.Fatalf("streamable transport = %#v", streamable)
	}
}

func TestBuildMCPTransportHonorsExplicitSSE(t *testing.T) {
	httpClient := &http.Client{}
	transport, err := BuildMCPTransport(TransportConfig{Protocol: "sse"}, "http://mcp.example/sse", httpClient)
	if err != nil {
		t.Fatal(err)
	}

	sse, ok := transport.(*mcp.SSEClientTransport)
	if !ok {
		t.Fatalf("transport = %T, want SSE", transport)
	}
	if sse.Endpoint != "http://mcp.example/sse" || sse.HTTPClient != httpClient {
		t.Fatalf("SSE transport = %#v", sse)
	}
}

func TestConnectOverlayClientSessionHonorsDefaultAndExplicitProtocols(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "overlay-test", Version: "1.0.0"}, nil)
	server.AddTool(&mcp.Tool{Name: "ping", InputSchema: map[string]any{"type": "object"}}, nil)

	mux := http.NewServeMux()
	mux.Handle("/sse", mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil))
	mux.Handle("/", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)

	target, err := url.Parse(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	httpClient := &http.Client{Transport: rewriteOriginRoundTripper{target: target, base: base}}

	for _, protocol := range []string{"", "streamable", "sse"} {
		name := protocol
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			session, err := ConnectOverlayClientSession(
				context.Background(),
				&mcp.Implementation{Name: "overlay-client", Version: "1.0.0"},
				TransportConfig{Protocol: protocol},
				httpClient,
				5*time.Second,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Session.Close()

			tools, err := session.Session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(tools.Tools) != 1 || tools.Tools[0].Name != "ping" {
				t.Fatalf("tools = %#v", tools.Tools)
			}
		})
	}
}

type rewriteOriginRoundTripper struct {
	target *url.URL
	base   http.RoundTripper
}

func (r rewriteOriginRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	requestURL := *req.URL
	requestURL.Scheme = r.target.Scheme
	requestURL.Host = r.target.Host
	clone.URL = &requestURL
	return r.base.RoundTrip(clone)
}

func newTestCert(t *testing.T, commonName string) ([]byte, *x509.Certificate) {
	t.Helper()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("failed to parse certificate: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert
}

func writeTempCertFile(t *testing.T, certPEM []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, certPEM, 0o600); err != nil {
		t.Fatalf("failed to write cert file: %v", err)
	}

	return path
}

func containsSubject(subjects [][]byte, want []byte) bool {
	for _, subject := range subjects {
		if string(subject) == string(want) {
			return true
		}
	}

	return false
}
