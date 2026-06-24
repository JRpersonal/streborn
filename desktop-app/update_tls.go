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

	updateDLClientOnce sync.Once
	updateDLClient     *http.Client
)

// updateCertPool builds the TLS root pool for the update clients: the OS/system
// trust store first, then the embedded Mozilla bundle appended.
//
// Trusting the system store is what lets the update check work behind an
// HTTPS-inspecting security suite (Norton and friends): such a suite terminates
// TLS itself and re-presents a certificate signed by its OWN root CA, which it
// installs into the OS trust store but which is NOT in the embedded Mozilla
// bundle. Pinning to the embedded bundle alone failed the handshake with
// "certificate signed by unknown authority", so the check failed SILENTLY and no
// update banner ever appeared, even though the user's browser (which trusts the
// suite's CA via the OS store) saw the site fine. Seeding from the system store
// makes Go accept the interception cert exactly like the browser does. The
// embedded bundle is still appended as a belt-and-braces anchor (Starfield /
// st-reborn.de) for a sparse system store.
//
// The pool is ALWAYS non-nil, which preserves the #102 fix: a nil RootCAs routes
// macOS verification through the cgo Security.framework path that crashed the app
// at startup; a non-nil pool keeps verification in Go's own chain builder. The
// downloaded binary is still SHA256-verified against the manifest, so trusting
// whoever terminates TLS never weakens the integrity of what gets installed.
func updateCertPool() *x509.CertPool {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	pool.AppendCertsFromPEM(caBundlePEM)
	return pool
}

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
		pool := updateCertPool()
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

// updateDownloadHTTPClient is the pure-Go client for the user-initiated update
// manifest fetch and app download. Unlike updateHTTPClient (a 6 s TOTAL timeout,
// fine for the tiny startup check) it has NO overall Client.Timeout: a ~30 MB app
// download takes far longer than 6 s on a normal link, and reusing the 6 s-capped
// client made the in-app "Update the app now" fail mid-body at whatever percent it
// had reached ("context deadline exceeded ... while reading body"), forcing every
// user back to the website download. A stalled transfer is instead bounded by the
// dial / TLS / response-header timeouts plus the per-download context and stall
// watchdog in DownloadUpdate, which retries rather than aborting.
func updateDownloadHTTPClient() *http.Client {
	updateDLClientOnce.Do(func() {
		pool := updateCertPool()
		dialer := &net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
			Resolver:  &net.Resolver{PreferGo: true},
		}
		updateDLClient = &http.Client{
			// No total Timeout on purpose; see the doc comment above.
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           dialer.DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          2,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				ExpectContinueTimeout: time.Second,
				TLSClientConfig: &tls.Config{
					RootCAs:    pool,
					MinVersion: tls.VersionTLS12,
				},
			},
		}
	})
	return updateDLClient
}
