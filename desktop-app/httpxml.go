package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Small HTTP and XML helpers shared by the discovery probes. Split out of app_discovery.go.

// httpGetSmall fetches a small document with a hard timeout. It returns the
// underlying error (client.Do, status, read) instead of a bare bool so probe
// callers can log WHAT failed; a swallowed error made a macOS local-network
// privacy denial indistinguishable from a plain timeout in diagnostics (#420).
// probeDialTimeout is how long a probe waits to CONNECT, as opposed to how long
// it then waits for an answer. The two are separate on purpose.
//
// A host that accepts the connection is there; only its reply is slow. A host
// that is absent, asleep or on another network refuses or drops the SYN, and
// that verdict arrives in milliseconds on a LAN. Sharing one budget between the
// two meant the generous half had to be paid on every dead address in a sweep,
// so the budget was kept small, so a speaker that was merely BUSY got written
// off as gone.
//
// That is not hypothetical. The budget was already raised once, from 1.2 s to
// 3 s, because a speaker under load answered too slowly and a missed probe
// relabelled a flashed speaker as "needs install". On 2026-08-23 a healthy ST10
// answered its version endpoint in 3.3 seconds, so the same fault came back at
// the new threshold. Chasing the number is the wrong move; splitting the two
// timeouts removes the trade-off instead.
const probeDialTimeout = 1200 * time.Millisecond

// probeAnswerBudget is how long a probe waits for a speaker that HAS answered
// the connection to produce its reply. Generous, because it is only ever spent
// on a host that is demonstrably present.
const probeAnswerBudget = 8 * time.Second

// probeTransport is shared by every discovery probe so the connection pool is
// reused across a sweep instead of one transport per address.
var probeTransport = &http.Transport{
	DialContext:         (&net.Dialer{Timeout: probeDialTimeout, KeepAlive: 30 * time.Second}).DialContext,
	MaxIdleConns:        64,
	IdleConnTimeout:     30 * time.Second,
	TLSHandshakeTimeout: probeDialTimeout,
	DisableCompression:  true,
}

func httpGetSmall(ctx context.Context, url string, timeout time.Duration, max int64) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: timeout, Transport: probeTransport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max))
	if err != nil {
		return nil, err
	}
	return b, nil
}

func extractAttr(xml, key string) string {
	needle := key + "=\""
	i := strings.Index(xml, needle)
	if i < 0 {
		return ""
	}
	j := strings.Index(xml[i+len(needle):], "\"")
	if j < 0 {
		return ""
	}
	return xml[i+len(needle) : i+len(needle)+j]
}

// toValidUTF8 returns s unchanged when it is already valid UTF-8, otherwise it
// reinterprets the bytes as Latin-1 (ISO-8859-1) and re-encodes them as UTF-8.
// The Bose /info XML labels itself UTF-8 but reports an umlaut box name as a
// lone Latin-1 byte ("ü" = 0xFC); left raw that JSON-marshals to U+FFFD and
// shows as garbled "K�che" (#70, Albrecht). Latin-1 maps 1:1 to the first 256
// code points, so ASCII is untouched and only the high bytes are widened.
func toValidUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		b.WriteRune(rune(s[i]))
	}
	return b.String()
}

func extractTag(xml, tag string) string {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	i := strings.Index(xml, open)
	if i < 0 {
		return ""
	}
	j := strings.Index(xml[i+len(open):], close)
	if j < 0 {
		return ""
	}
	return xml[i+len(open) : i+len(open)+j]
}
