package streamproxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"testing"
)

// serveOnce accepts one connection, discards the request, writes raw, closes.
func serveOnce(t *testing.T, raw string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		// Read (and drop) the request line so the client's write completes.
		_, _ = bufio.NewReader(c).ReadString('\n')
		_, _ = io.WriteString(c, raw)
	}()
	return ln
}

// TestICYConnRewritesShoutcastStatusLine is the regression guard for legacy
// SHOUTcast stations (e.g. Radio Studio D) that answer with "ICY 200 OK" instead
// of an HTTP status line: without the rewrite, http.ReadResponse rejects it and
// the stream never plays.
func TestICYConnRewritesShoutcastStatusLine(t *testing.T) {
	ln := serveOnce(t, "ICY 200 OK\r\nContent-Type: audio/mpeg\r\nicy-br:128\r\n\r\nHELLOAUDIO")
	defer ln.Close()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_, _ = io.WriteString(conn, "GET /; HTTP/1.1\r\nHost: x\r\n\r\n")

	ic := &icyConn{Conn: conn, br: bufio.NewReader(conn)}
	resp, err := http.ReadResponse(bufio.NewReader(ic), nil)
	if err != nil {
		t.Fatalf("ReadResponse on an ICY response: %v (the rewrite did not take)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/mpeg" {
		t.Fatalf("Content-Type = %q, want audio/mpeg (headers after the status line must survive)", ct)
	}
	if br := resp.Header.Get("icy-br"); br != "128" {
		t.Fatalf("icy-br = %q, want 128", br)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "HELLOAUDIO" {
		t.Fatalf("body = %q, want HELLOAUDIO (audio after the headers must survive)", string(body))
	}
}

// TestICYConnLeavesHTTPUntouched confirms a normal HTTP response is passed
// through byte-for-byte (the rewrite only triggers on a leading "ICY ").
func TestICYConnLeavesHTTPUntouched(t *testing.T) {
	ln := serveOnce(t, "HTTP/1.1 200 OK\r\nContent-Type: audio/aacp\r\n\r\nAACDATA")
	defer ln.Close()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")

	ic := &icyConn{Conn: conn, br: bufio.NewReader(conn)}
	resp, err := http.ReadResponse(bufio.NewReader(ic), nil)
	if err != nil {
		t.Fatalf("ReadResponse on plain HTTP: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "audio/aacp" {
		t.Fatalf("plain HTTP response was altered: status=%d ct=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "AACDATA" {
		t.Fatalf("body = %q, want AACDATA", string(body))
	}
}
