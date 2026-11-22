package dlna_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/JRpersonal/streborn/dlna"
)

// A NAS that closes a kept-alive connection while the client reuses it made
// reads fail with a bare EOF (#1705: the Synology's root once, then the whole
// NAS was reported as not answering). The read is sent again on a fresh
// connection.
func TestBrowseResendsOnAKeptAliveConnectionTheServerClosed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	const ok = `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:BrowseResponse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><Result></Result><NumberReturned>0</NumberReturned><TotalMatches>0</TotalMatches></u:BrowseResponse></s:Body></s:Envelope>`
	answer := func(c net.Conn, r *bufio.Reader) bool {
		req, err := http.ReadRequest(r)
		if err != nil {
			return false
		}
		io.Copy(io.Discard, req.Body)
		if _, sent := req.Header["X-Idempotency-Key"]; sent {
			t.Error("the idempotency mark must not go on the wire")
		}
		fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: text/xml\r\nContent-Length: %d\r\n\r\n%s", len(ok), ok)
		return true
	}
	go func() {
		// First connection: answer one read, then take the next and drop
		// the connection without a word, as the NAS did.
		c, err := ln.Accept()
		if err != nil {
			return
		}
		r := bufio.NewReader(c)
		answer(c, r)
		if req, err := http.ReadRequest(r); err == nil {
			io.Copy(io.Discard, req.Body)
		}
		c.Close()
		// Second connection: the resent read.
		c, err = ln.Accept()
		if err != nil {
			return
		}
		answer(c, bufio.NewReader(c))
		c.Close()
	}()
	srv := dlna.Server{CDSControlURL: "http://" + ln.Addr().String() + "/ContentDirectory/control"}
	if _, err := dlna.Browse(context.Background(), srv, "0", 0, 10); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if _, err := dlna.Browse(context.Background(), srv, "0", 0, 10); err != nil {
		t.Fatalf("the read on the closed connection was not sent again: %v", err)
	}
}
