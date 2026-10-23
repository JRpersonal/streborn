package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The NAND-full verdict is one typed error wherever it is reached, and its
// texts stay what the frontend's /insufficient nand|no space|507/ and the
// "status " classifiers match on.
func TestNANDFullErrorIsTypedAndKeepsItsTexts(t *testing.T) {
	gate := fmt.Errorf("sidecar: %w", &nandFullError{free: 1, reclaimable: 2, need: 3})
	if !errors.Is(gate, errInsufficientNAND) {
		t.Error("the gate's error must match errInsufficientNAND")
	}
	if want := "sidecar: insufficient NAND space (free=1 reclaimable=2 need=3)"; gate.Error() != want {
		t.Errorf("gate text %q, want %q", gate.Error(), want)
	}
	var nf *nandFullError
	if !errors.As(gate, &nf) || nf.need != 3 {
		t.Error("errors.As must hand back the figures")
	}

	reply := fmt.Errorf("sidecar upload failed: %w", &nandFullError{status: 507, detail: "insufficient NAND space: need 12000KB"})
	if !errors.Is(reply, errInsufficientNAND) || !engineWillNeverFit(reply) {
		t.Error("a 507 reply must be the NAND-full verdict")
	}
	if want := "sidecar upload failed: status 507: insufficient NAND space: need 12000KB"; reply.Error() != want {
		t.Errorf("507 text %q, want %q", reply.Error(), want)
	}
	if !isCleanHTTPReject(reply) || isConnDropErr(reply) {
		t.Error("a 507 is a clean rejection, never a connection drop")
	}

	df := &nandFullError{need: 18874368, detail: "the speaker's df refused the push of /mnt/nv/streborn/bin/go-librespot"}
	if want := "insufficient NAND space (free=0 reclaimable=0 need=18874368): the speaker's df refused the push of /mnt/nv/streborn/bin/go-librespot"; df.Error() != want {
		t.Errorf("df text %q, want %q", df.Error(), want)
	}

	for _, plain := range []error{errors.New("status 413: too large"), errors.New("connection reset by peer"), errors.New("sidecar push: 507 insufficient NAND")} {
		if errors.Is(plain, errInsufficientNAND) || engineWillNeverFit(plain) {
			t.Errorf("%v must not read as the NAND-full verdict without the type", plain)
		}
	}
}

// The agent's reply is classified once, where it arrives: a 507, or an older
// agent's 5xx naming the condition, becomes the typed verdict; any other
// refusal stays the plain "status N" error.
func TestStreamPostBinaryTypesTheAgentsFullNANDReply(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantFull bool
	}{
		{"507", http.StatusInsufficientStorage, "insufficient NAND space: need 12000KB", true},
		{"old agent 500 with ENOSPC text", http.StatusInternalServerError, "write /mnt/nv/x.new: no space left on device", true},
		{"413", http.StatusRequestEntityTooLarge, "Request Too Large", false},
		{"500 other", http.StatusInternalServerError, "rename failed", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, c.body, c.status)
			}))
			defer srv.Close()
			a := newTestApp()
			_, _, err := a.streamPostBinary("127.0.0.1", listenPort(t, srv), "/api/agent/sidecar", []byte("binary"))
			if err == nil {
				t.Fatal("want an error for a refused upload")
			}
			if got := errors.Is(err, errInsufficientNAND); got != c.wantFull {
				t.Fatalf("errors.Is(errInsufficientNAND) = %v, want %v (err %v)", got, c.wantFull, err)
			}
			if !strings.HasPrefix(err.Error(), fmt.Sprintf("status %d: %s", c.status, c.body)) {
				t.Fatalf("reply text %q lost its status form", err.Error())
			}
			if c.wantFull {
				var nf *nandFullError
				if !errors.As(err, &nf) || nf.status != c.status {
					t.Fatalf("want a nandFullError with status %d, got %v", c.status, err)
				}
			}
		})
	}
}
