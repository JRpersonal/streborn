// #1190 bycatch: a library track saved on a preset as a radio-type stream never
// played. The box resumed mid-file with `Range: bytes=250894-`, the #844 fix
// forwarded that range, the media server answered 206 as it should, and the
// proxy turned the 206 into a 502 because it only accepted 200. Every retry
// asked for the same offset and got the same 502 until the box gave up with
// AUDIO_ERROR_BAD_URL.

package streamproxy

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// rangeFile is a finite "media server" file that honours offset ranges the way
// MiniDLNA and friends do. With always206 set it answers 206 even to a plain
// request, to model a broken upstream.
func rangeFile(t *testing.T, body []byte, always206 bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/flac")
		w.Header().Set("Accept-Ranges", "bytes")
		rng := r.Header.Get("Range")
		if rng == "" && !always206 {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
		start := 0
		if rng != "" {
			if _, err := fmt.Sscanf(rng, "bytes=%d-", &start); err != nil {
				t.Errorf("upstream got an unparsable Range %q", rng)
			}
		}
		if start >= len(body) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(body)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(body)-1, len(body)))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)-start))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[start:])
	}))
}

func rangeProxy(t *testing.T) *httptest.Server {
	t.Helper()
	s := New(presets.New(), silentLogger())
	s.client = &http.Client{} // bypass the SSRF guard for loopback
	mux := http.NewServeMux()
	s.Register(mux)
	return httptest.NewServer(mux)
}

func getVia(t *testing.T, proxy, upstream, rng string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet,
		proxy+"/stream/raw?u="+base64.RawURLEncoding.EncodeToString([]byte(upstream)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func rangeTestBody() []byte {
	b := make([]byte, 64*1024)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func TestOffsetRangePassesThe206ThroughToTheSpeaker(t *testing.T) {
	body := rangeTestBody()
	up := rangeFile(t, body, false)
	defer up.Close()
	proxy := rangeProxy(t)
	defer proxy.Close()

	const off = 25089
	resp := getVia(t, proxy.URL, up.URL, fmt.Sprintf("bytes=%d-", off))
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206 (the speaker resumed mid-file and the upstream honoured it)", resp.StatusCode)
	}
	wantCR := fmt.Sprintf("bytes %d-%d/%d", off, len(body)-1, len(body))
	if got := resp.Header.Get("Content-Range"); got != wantCR {
		t.Errorf("Content-Range = %q, want %q", got, wantCR)
	}
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(body)-off) {
		t.Errorf("Content-Length = %q, want %d", got, len(body)-off)
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", got)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(got) != string(body[off:]) {
		t.Fatalf("body: got %d bytes, want the %d bytes from the offset on", len(got), len(body)-off)
	}
}

func TestPlainRequestStillGetsTheWholeFileWith200(t *testing.T) {
	body := rangeTestBody()
	up := rangeFile(t, body, false)
	defer up.Close()
	proxy := rangeProxy(t)
	defer proxy.Close()

	// "bytes=0-" is not forwarded (see offsetRange), so this is a plain fetch.
	resp := getVia(t, proxy.URL, up.URL, "bytes=0-")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		t.Errorf("Content-Range leaked onto a 200: %q", cr)
	}
	got, _ := io.ReadAll(resp.Body)
	if len(got) != len(body) {
		t.Fatalf("body = %d bytes, want %d", len(got), len(body))
	}
}

func TestUnasked206IsStillRejected(t *testing.T) {
	up := rangeFile(t, rangeTestBody(), true)
	defer up.Close()
	proxy := rangeProxy(t)
	defer proxy.Close()

	resp := getVia(t, proxy.URL, up.URL, "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for a 206 nobody asked for", resp.StatusCode)
	}
}

func TestOffsetPastTheEndAnswers416(t *testing.T) {
	body := rangeTestBody()
	up := rangeFile(t, body, false)
	defer up.Close()
	proxy := rangeProxy(t)
	defer proxy.Close()

	resp := getVia(t, proxy.URL, up.URL, fmt.Sprintf("bytes=%d-", len(body)+10))
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416 passed through, not a 502", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Content-Range"), fmt.Sprintf("bytes */%d", len(body)); got != want {
		t.Errorf("Content-Range = %q, want %q", got, want)
	}
}
