package streamproxy

import (
	"encoding/base64"
	"testing"
)

func TestUnwrapSelfProxy(t *testing.T) {
	real := "http://stream.srg-ssr.ch/m/la-1ere/mp3_128"
	b64 := base64.RawURLEncoding.EncodeToString([]byte(real))
	wrapped := "http://127.0.0.1:8888/stream/raw?u=" + b64
	doubleWrapped := "http://127.0.0.1:8888/stream/raw?u=" +
		base64.RawURLEncoding.EncodeToString([]byte(wrapped))

	cases := map[string]string{
		wrapped:                        real, // the regression case
		doubleWrapped:                  real, // tolerate multiple wraps
		real:                           real, // a real URL passes through
		"https://example.com/a.mp3":    "https://example.com/a.mp3",
		"http://127.0.0.1:8888/stream/3": "http://127.0.0.1:8888/stream/3", // slot URL, not /stream/raw: unchanged
		"":                             "",
	}
	for in, want := range cases {
		if got := unwrapSelfProxy(in); got != want {
			t.Errorf("unwrapSelfProxy(%q) = %q, want %q", in, got, want)
		}
	}

	// Std-base64 encoded wrapper (belt-and-braces) also unwraps.
	stdWrapped := "http://192.168.1.50:8888/stream/raw?u=" + base64.StdEncoding.EncodeToString([]byte(real))
	if got := unwrapSelfProxy(stdWrapped); got != real {
		t.Errorf("unwrapSelfProxy(std-b64) = %q, want %q", got, real)
	}
}
