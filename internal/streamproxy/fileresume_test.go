package streamproxy

// #830: a finite file whose upstream connection broke mid-way was refetched
// from byte 0, so the speaker got the start of the recording again into its
// running decode. The reconnect now asks for the rest of the file.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// breakingFile serves body like a NAS does (size, byte ranges) but aborts the
// FIRST plain request after cut bytes, the way a dropped connection ends. With
// ignoreRange set it answers every request with the whole file, like a server
// that does not do ranges on a GET. It records each request's Range header.
type breakingFile struct {
	body        []byte
	cut         int
	ignoreRange bool

	mu     sync.Mutex
	ranges []string
}

func (f *breakingFile) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	first := len(f.ranges) == 0
	f.ranges = append(f.ranges, r.Header.Get("Range"))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Accept-Ranges", "bytes")
	start := 0
	if rng := r.Header.Get("Range"); rng != "" && !f.ignoreRange {
		if _, err := fmt.Sscanf(rng, "bytes=%d-", &start); err != nil {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(f.body)-1, len(f.body)))
		w.Header().Set("Content-Length", strconv.Itoa(len(f.body)-start))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(f.body[start:])
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(f.body)))
	w.WriteHeader(http.StatusOK)
	if first {
		_, _ = w.Write(f.body[:f.cut])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	_, _ = w.Write(f.body)
}

func (f *breakingFile) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ranges...)
}

func readWithin(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		b := make([]byte, n)
		_, err := io.ReadFull(r, b)
		ch <- res{b, err}
	}()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("reading %d bytes via the proxy: %v", n, got.err)
		}
		return got.b
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out reading %d bytes via the proxy", n)
	}
	return nil
}

func TestFileReconnectContinuesAtTheDeliveredOffset(t *testing.T) {
	body := rangeTestBody()
	f := &breakingFile{body: body, cut: 20000}
	up := httptest.NewServer(f)
	defer up.Close()
	proxy := rangeProxy(t)
	defer proxy.Close()

	resp := getVia(t, proxy.URL, up.URL, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := readWithin(t, resp.Body, len(body))
	if string(got) != string(body) {
		t.Fatal("the speaker did not get the file exactly once in order: the reconnect replayed or skipped bytes")
	}
	ranges := f.seen()
	if len(ranges) != 2 || ranges[0] != "" || ranges[1] != "bytes=20000-" {
		t.Fatalf("upstream Range headers = %q, want [\"\" \"bytes=20000-\"]", ranges)
	}
}

func TestFileReconnectSkipsAheadWhenTheServerIgnoresTheRange(t *testing.T) {
	body := rangeTestBody()
	f := &breakingFile{body: body, cut: 30000, ignoreRange: true}
	up := httptest.NewServer(f)
	defer up.Close()
	proxy := rangeProxy(t)
	defer proxy.Close()

	resp := getVia(t, proxy.URL, up.URL, "")
	got := readWithin(t, resp.Body, len(body))
	if string(got) != string(body) {
		t.Fatal("a server that resent the whole file was not read past the delivered offset")
	}
	if ranges := f.seen(); len(ranges) != 2 || ranges[1] != "bytes=30000-" {
		t.Fatalf("upstream Range headers = %q, want the reconnect to ask for bytes=30000-", ranges)
	}
}

// A live station has no size and no ranges: its reconnect stays a plain GET,
// exactly as before.
func TestStationReconnectSendsNoRange(t *testing.T) {
	var mu sync.Mutex
	var ranges []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		mu.Unlock()
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("icy-name", "Test FM")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 8192)) // then EOF, like a CDN token expiry
	}))
	defer up.Close()
	proxy := rangeProxy(t)
	defer proxy.Close()

	resp := getVia(t, proxy.URL, up.URL+"/ndr2.mp3", "")
	readWithin(t, resp.Body, 8192*2+1)
	resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(ranges) < 2 {
		t.Fatalf("expected a reconnect, upstream saw %d requests", len(ranges))
	}
	for i, r := range ranges {
		if r != "" {
			t.Errorf("request %d carried Range %q; a station must reconnect with a plain GET", i, r)
		}
	}
}

func TestParseContentRange(t *testing.T) {
	cases := []struct {
		in           string
		start, total int64
		ok           bool
	}{
		{"bytes 100-199/1000", 100, 1000, true},
		{"bytes 0-0/*", 0, 0, true},
		{"Bytes 5-9/10", 5, 10, true},
		{"bytes */1000", 0, 0, false},
		{"", 0, 0, false},
		{"items 1-2/3", 0, 0, false},
	}
	for _, c := range cases {
		start, total, ok := parseContentRange(c.in)
		if start != c.start || total != c.total || ok != c.ok {
			t.Errorf("parseContentRange(%q) = %d, %d, %v; want %d, %d, %v",
				c.in, start, total, ok, c.start, c.total, c.ok)
		}
	}
}
