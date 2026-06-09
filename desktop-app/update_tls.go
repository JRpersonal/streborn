package main

import (
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"net"
	"net/http"
	"sync"
	"time"
)

// caBundlePEM is the Mozilla CA root bundle (from https://curl.se/ca/cacert.pem),
// embedded so the update check can verify TLS with a pure-Go path instead of
// the platform trust store. Refresh occasionally; it includes the Starfield
// Root CA that st-reborn.de currently chains to, plus all common roots so a
// future hosting/CA change keeps working.
//
//go:embed cacert.pem
var caBundlePEM []byte

var (
	updateClientOnce sync.Once
	updateClient     *http.Client
)

// updateHTTPClient returns the http.Client used for the external update-check
// request, configured so its entire network path is pure Go with no cgo:
//
//   - DNS: net.DefaultResolver.PreferGo is set in main(), and we also pin a
//     PreferGo resolver on the dialer here, so name resolution never touches
//     the C getaddrinfo path.
//   - TLS cert verification: we supply an explicit RootCAs pool from the
//     embedded Mozilla bundle. This matters on macOS: with RootCAs left nil,
//     crypto/x509 verifies the server certificate against the system trust
//     store through cgo (Security.framework), which SIGSEGV'd on an old Mac
//     during the startup update check and took the whole app down before it
//     could draw a window (issue #102 — kill-switch A/B confirmed the
//     crash is in this call). A non-nil RootCAs switches verification to Go's
//     own chain builder, removing the last cgo path.
//
// The rest of the app still links cgo for the WebKit webview; this only
// affects the one outbound HTTPS request. A dedicated client also keeps the
// shared httpClient (used for plain-HTTP box LAN calls) untouched.
func updateHTTPClient() *http.Client {
	updateClientOnce.Do(func() {
		pool := x509.NewCertPool()
		// AppendCertsFromPEM returns false only if no certs parsed; the
		// embedded bundle is fixed and known-good, so we proceed regardless
		// (a worst case of an empty pool just fails the check = no banner,
		// never a crash).
		pool.AppendCertsFromPEM(caBundlePEM)
		dialer := &net.Dialer{
			Timeout:   6 * time.Second,
			KeepAlive: 30 * time.Second,
			Resolver:  &net.Resolver{PreferGo: true},
		}
		updateClient = &http.Client{
			Timeout: 6 * time.Second,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           dialer.DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          2,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   6 * time.Second,
				ExpectContinueTimeout: time.Second,
				TLSClientConfig: &tls.Config{
					RootCAs:    pool,
					MinVersion: tls.VersionTLS12,
				},
			},
		}
	})
	return updateClient
}
