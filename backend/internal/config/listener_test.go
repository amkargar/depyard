package config_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/amkargar/depyard/backend/internal/config"
)

func TestBindAddressPrecedence(t *testing.T) {
	for _, k := range []string{"LISTEN_ADDR", "BIND_HOST", "BIND_PORT", "TLS_CERT_FILE", "TLS_KEY_FILE"} {
		t.Setenv(k, "")
	}
	if c := config.Load(); c.ListenAddr != "0.0.0.0:8080" {
		t.Fatal(c.ListenAddr)
	}
	t.Setenv("BIND_HOST", "::1")
	t.Setenv("BIND_PORT", "8443")
	if c := config.Load(); c.ListenAddr != "[::1]:8443" {
		t.Fatal(c.ListenAddr)
	}
	t.Setenv("BIND_PORT", "65536")
	if _, err := config.Load().ListenerConfig(); err == nil {
		t.Fatal("invalid bind port accepted")
	}
	t.Setenv("LISTEN_ADDR", ":8090")
	c := config.Load()
	if v, err := c.ListenerConfig(); err != nil || v != nil || c.ListenAddr != ":8090" {
		t.Fatal(c.ListenAddr, err)
	}
}

func TestListenerLoadsKeyPairOrFailsClosed(t *testing.T) {
	s := httptest.NewTLSServer(nil)
	defer s.Close()
	cert := s.TLS.Certificates[0]
	der, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	c := config.Config{ListenAddr: ":8443", TLSCertFile: filepath.Join(dir, "cert.pem")}
	if err := os.WriteFile(c.TLSCertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListenerConfig(); err == nil {
		t.Fatal("accepted certificate without a key")
	}
	c.TLSKeyFile = filepath.Join(dir, "key.pem")
	if _, err := c.ListenerConfig(); err == nil {
		t.Fatal("accepted missing key")
	}
	if err := os.WriteFile(c.TLSKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := c.ListenerConfig()
	if err != nil || cfg.MinVersion < tls.VersionTLS12 || len(cfg.Certificates) != 1 {
		t.Fatal(cfg, err)
	}
	// Exercise a real TLS handshake with trust verification and the loaded key.
	endpoint := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	endpoint.TLS = cfg
	endpoint.StartTLS()
	defer endpoint.Close()
	resp, err := endpoint.Client().Get(endpoint.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.TLS == nil || resp.StatusCode != 204 {
		t.Fatal("missing HTTPS response")
	}
}
