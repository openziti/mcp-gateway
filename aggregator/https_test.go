package aggregator

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	client, err := BuildHTTPSClient(TransportConfig{
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

	_, err := BuildHTTPSClient(TransportConfig{
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
	_, err := BuildHTTPSClient(TransportConfig{
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

	client, err := BuildHTTPSClient(TransportConfig{
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

	client, err := BuildHTTPSClient(TransportConfig{
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
