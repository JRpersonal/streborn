// Regression tests for the http.Server ErrorLog bridge that keeps the Bose
// firmware's per-minute TLS-handshake probe out of the NAND log at the default
// info level (see httpErrorLogWriter).

package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// levelCapture records the level of the most recent record so a test can assert
// where a given http.Server error line was routed.
type levelCapture struct {
	buf   *bytes.Buffer
	level slog.Level
}

func (c *levelCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *levelCapture) Handle(_ context.Context, r slog.Record) error {
	c.level = r.Level
	c.buf.WriteString(r.Message)
	return nil
}
func (c *levelCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *levelCapture) WithGroup(string) slog.Handler      { return c }

func TestHTTPErrorLogRoutesHandshakeNoiseToDebug(t *testing.T) {
	cases := []struct {
		name string
		line string
		want slog.Level
	}{
		{
			// The exact line net/http emits for the box's :443 connect-and-drop
			// probe; this is the per-minute noise we must keep below info.
			name: "tls handshake eof -> debug",
			line: "http: TLS handshake error from 192.0.2.1:43566: EOF",
			want: slog.LevelDebug,
		},
		{
			name: "tls handshake reset -> debug",
			line: "http: TLS handshake error from 192.0.2.1:5001: read tcp: connection reset by peer",
			want: slog.LevelDebug,
		},
		{
			// Anything that is not a handshake error (a recovered handler panic
			// reaches ErrorLog too) must stay visible in a diagnostic bundle.
			name: "genuine server error -> warn",
			line: "http: panic serving 192.0.2.1: runtime error",
			want: slog.LevelWarn,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cap := &levelCapture{buf: &bytes.Buffer{}}
			lg := newHTTPErrorLog(slog.New(cap), "marge-tls")
			lg.Print(tc.line)
			if cap.level != tc.want {
				t.Fatalf("line %q routed to %v, want %v", tc.line, cap.level, tc.want)
			}
			if !strings.Contains(cap.buf.String(), "http server error") {
				t.Fatalf("expected the bridged message, got %q", cap.buf.String())
			}
		})
	}
}

// TestHTTPErrorLogIgnoresEmptyLines guards against a bare newline (the stdlib
// logger appends one) producing an empty slog record.
func TestHTTPErrorLogIgnoresEmptyLines(t *testing.T) {
	cap := &levelCapture{buf: &bytes.Buffer{}}
	w := httpErrorLogWriter{logger: slog.New(cap), name: "marge-tls"}
	n, err := w.Write([]byte("\n"))
	if err != nil || n != 1 {
		t.Fatalf("Write returned n=%d err=%v, want n=1 err=nil", n, err)
	}
	if cap.buf.Len() != 0 {
		t.Fatalf("empty line produced a record: %q", cap.buf.String())
	}
}
