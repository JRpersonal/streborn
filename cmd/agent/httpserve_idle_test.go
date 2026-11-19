package main

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"
)

// TestStubHTTPServerTimeouts pins the timeouts that keep the cloud-stub
// listeners (marge on :80, bmx, marge-tls) from leaking idle keep-alive
// connections into process-wide fd exhaustion (field report 2026-11-19).
func TestStubHTTPServerTimeouts(t *testing.T) {
	h := http.NewServeMux()
	plain := stubHTTPServer(":80", h, nil, nil, "marge")
	if plain.IdleTimeout != stubServerIdleTimeout {
		t.Errorf("IdleTimeout = %v, want %v", plain.IdleTimeout, stubServerIdleTimeout)
	}
	if plain.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", plain.ReadHeaderTimeout)
	}
	// No Read/Write timeout: the marge forward streams a bounded upstream body
	// and a blanket write deadline could cut it.
	if plain.ReadTimeout != 0 || plain.WriteTimeout != 0 {
		t.Errorf("Read/WriteTimeout should stay unset, got %v/%v", plain.ReadTimeout, plain.WriteTimeout)
	}
	if plain.TLSConfig != nil {
		t.Errorf("plain server must have no TLSConfig")
	}
	tlsSrv := stubHTTPServer(":443", h, &tls.Config{}, nil, "marge-tls")
	if tlsSrv.TLSConfig == nil {
		t.Errorf("TLS server must carry the supplied TLSConfig")
	}
	if tlsSrv.IdleTimeout != stubServerIdleTimeout {
		t.Errorf("TLS IdleTimeout = %v, want %v", tlsSrv.IdleTimeout, stubServerIdleTimeout)
	}
}
