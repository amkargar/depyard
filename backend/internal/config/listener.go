package config

import (
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
)

// ListenerConfig validates before bootstrapping accounts or touching storage.
// An empty pair is plain HTTP; a partial, unreadable or mismatched pair is an
// error, never an implicit downgrade. LISTEN_ADDR takes priority over BIND_*.
func (c Config) ListenerConfig() (*tls.Config, error) {
	_, port, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen address: %w", err)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("listen port must be between 1 and 65535")
	}
	if c.TLSCertFile == "" && c.TLSKeyFile == "" {
		return nil, nil
	}
	if c.TLSCertFile == "" || c.TLSKeyFile == "" {
		return nil, fmt.Errorf("TLS_CERT_FILE and TLS_KEY_FILE must be set together")
	}
	cert, err := tls.LoadX509KeyPair(c.TLSCertFile, c.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate/key: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}
