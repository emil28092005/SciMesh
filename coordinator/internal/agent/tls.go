package agent

import (
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

// tlsClient builds an HTTP client whose transport trusts the coordinator's
// TLS certificate:
//
//   - SCIMESH_CA_CERT=/path/to/ca.pem adds a root CA (for self-signed or
//     private-CA coordinators);
//   - SCIMESH_INSECURE_SKIP_VERIFY=1 disables verification entirely — only
//     for trusted LANs where a self-signed certificate was auto-generated.
//
// Both settings are deliberately opt-in and noisy: a coordinator without them
// fails to verify, never silently downgrades.
func tlsClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: tlsTransport(nil)}
}

// tlsTransport configures a transport honouring the trust environment.
func tlsTransport(base *http.Transport) *http.Transport {
	if base == nil {
		base = &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:        100,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		}
	}
	caPath := os.Getenv("SCIMESH_CA_CERT")
	skip := os.Getenv("SCIMESH_INSECURE_SKIP_VERIFY") == "1"
	if caPath == "" && !skip {
		return base
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12} //nolint:gosec // G402: min TLS 1.2 by default
	if caPath != "" {
		//nolint:gosec // G304: SCIMESH_CA_CERT is operator-configured
		pem, err := os.ReadFile(caPath)
		if err != nil {
			slog.Warn("could not read SCIMESH_CA_CERT", "path", caPath, "err", err)
			return base
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			slog.Warn("SCIMESH_CA_CERT contained no usable certificates", "path", caPath)
			return base
		}
		tlsConfig.RootCAs = pool
	}
	if skip {
		// G402 is about production code paths; here the operator explicitly
		// opts into an unverified LAN trust root, so the bypass is intended.
		tlsConfig.InsecureSkipVerify = true //nolint:gosec // G402: operator opt-in for self-signed LAN certs
		slog.Warn("SCIMESH_INSECURE_SKIP_VERIFY=1: TLS certificate verification is disabled")
	}
	base.TLSClientConfig = tlsConfig
	return base
}
