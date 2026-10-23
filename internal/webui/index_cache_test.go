package webui

import (
	"bytes"
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func newIndexServer() *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// The page must never be cached without revalidation. It carried no caching
// instruction at all, so browsers applied their own heuristic and could hold a
// stale remote across an agent update: caught live on 2026-08-06 with the box
// already serving a corrected page while the browser showed the old one. On a
// page saved to the home screen there is not even a reload button to reach for.
func TestIndexRevalidatesAndOffersAnETag(t *testing.T) {
	s := newIndexServer()
	w := httptest.NewRecorder()
	s.handleIndex(w, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-cache")
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag; without one every revalidation costs a full page")
	}
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Errorf("ETag %q is not quoted", etag)
	}
	if w.Body.Len() == 0 {
		t.Error("first response carried no page")
	}
}

// A speaker that has not changed must answer 304 with no body, so revalidating
// on every open stays as cheap as a cache hit on hardware this old.
func TestIndexAnswers304ForAKnownETag(t *testing.T) {
	s := newIndexServer()
	first := httptest.NewRecorder()
	s.handleIndex(first, httptest.NewRequest(http.MethodGet, "/", nil))
	etag := first.Header().Get("ETag")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	s.handleIndex(second, req)

	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 carried %d bytes of body, want none", second.Body.Len())
	}
}

// A stale tag must get the new page, not a 304. This is the case that decides
// whether an agent update reaches the user at all.
func TestIndexServesThePageForAStaleETag(t *testing.T) {
	s := newIndexServer()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", `"0000000000000000"`)
	w := httptest.NewRecorder()
	s.handleIndex(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a stale tag", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Error("stale tag got an empty body")
	}
}

// Two speakers serve different bytes from the same build, because the page
// carries the speaker's name. Their tags must differ, or one speaker's cached
// page would be served as valid for the other.
func TestIndexETagCoversTheSpeakerName(t *testing.T) {
	a := indexETag("<title>Kitchen</title>")
	b := indexETag("<title>Bathroom</title>")
	if a == b {
		t.Error("two different pages produced the same ETag")
	}
}

func getIndex(t *testing.T, s *Server, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.handleIndex(w, req)
	return w
}

func gunzipBody(t *testing.T, w *httptest.ResponseRecorder) []byte {
	t.Helper()
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip body did not decode: %v", err)
	}
	return out
}

// The page is ~425 KB of hand-written HTML and JavaScript, and a phone on
// Wi-Fi in the next room fetches it on every open. Every phone browser accepts
// gzip, so that is what it gets, and the whole page has to come back out of it.
func TestIndexIsCompressedWhenThePhoneAcceptsGzip(t *testing.T) {
	s := newIndexServer()
	plain := getIndex(t, s, nil)
	gz := getIndex(t, s, map[string]string{"Accept-Encoding": "gzip, deflate, br"})

	if got := gz.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	t.Logf("page: %d bytes plain, %d bytes gzip", plain.Body.Len(), gz.Body.Len())
	if gz.Body.Len() >= plain.Body.Len()/3 {
		t.Errorf("gzip body is %d bytes against %d plain; that is not compressed", gz.Body.Len(), plain.Body.Len())
	}
	if got := gz.Header().Get("Content-Length"); got != strconv.Itoa(gz.Body.Len()) {
		t.Errorf("Content-Length = %q, body is %d bytes", got, gz.Body.Len())
	}
	if !bytes.Equal(gunzipBody(t, gz), plain.Body.Bytes()) {
		t.Error("the compressed page is not the plain page")
	}
	for _, w := range []*httptest.ResponseRecorder{plain, gz} {
		if got := w.Header().Get("Vary"); got != "Accept-Encoding" {
			t.Errorf("Vary = %q, want Accept-Encoding on both encodings", got)
		}
		if got := w.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("Cache-Control = %q, want no-cache", got)
		}
		if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Errorf("Content-Type = %q", got)
		}
	}
	if plain.Header().Get("ETag") == gz.Header().Get("ETag") {
		t.Error("the plain and the compressed page share an ETag; a cache could hand one out for the other")
	}
}

// A client that does not say it takes gzip (curl, a script, an old proxy) gets
// the page as it is, with no Content-Encoding to trip over.
func TestIndexStaysPlainWithoutAcceptEncoding(t *testing.T) {
	s := newIndexServer()
	for _, accept := range []string{"", "identity", "br", "gzip;q=0", "*;q=0"} {
		w := getIndex(t, s, map[string]string{"Accept-Encoding": accept})
		if got := w.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("Accept-Encoding %q: Content-Encoding = %q, want none", accept, got)
		}
		if !strings.HasPrefix(w.Body.String(), "<!") && !strings.HasPrefix(w.Body.String(), "<html") {
			t.Errorf("Accept-Encoding %q: body does not start as HTML: %.20q", accept, w.Body.String())
		}
		if got := w.Header().Get("Content-Length"); got != strconv.Itoa(w.Body.Len()) {
			t.Errorf("Content-Length = %q, body is %d bytes", got, w.Body.Len())
		}
	}
	for _, accept := range []string{"gzip", "GZIP", "*", "deflate, gzip;q=0.5", "br;q=1.0, gzip;q=0.8, *;q=0.1"} {
		w := getIndex(t, s, map[string]string{"Accept-Encoding": accept})
		if got := w.Header().Get("Content-Encoding"); got != "gzip" {
			t.Errorf("Accept-Encoding %q: Content-Encoding = %q, want gzip", accept, got)
		}
	}
}

// The 304 path has to work in both encodings, each against its own tag: a
// home-screen app that cached the compressed page revalidates with that tag
// and the gzip header, and gets its 304. The tag of the other encoding must
// NOT be accepted, or a cache would keep bytes the client cannot read.
func TestIndexAnswers304InBothEncodings(t *testing.T) {
	s := newIndexServer()
	plainTag := getIndex(t, s, nil).Header().Get("ETag")
	gzTag := getIndex(t, s, map[string]string{"Accept-Encoding": "gzip"}).Header().Get("ETag")

	cases := []struct {
		name, accept, match string
		want                int
	}{
		{"plain client, plain tag", "", plainTag, http.StatusNotModified},
		{"gzip client, gzip tag", "gzip", gzTag, http.StatusNotModified},
		{"gzip client, list with gzip tag", "gzip", `"0000000000000000", ` + gzTag, http.StatusNotModified},
		{"plain client, gzip tag", "", gzTag, http.StatusOK},
		{"gzip client, plain tag", "gzip", plainTag, http.StatusOK},
	}
	for _, c := range cases {
		w := getIndex(t, s, map[string]string{"Accept-Encoding": c.accept, "If-None-Match": c.match})
		if w.Code != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, w.Code, c.want)
		}
		if w.Code == http.StatusNotModified {
			if w.Body.Len() != 0 {
				t.Errorf("%s: 304 carried %d bytes of body", c.name, w.Body.Len())
			}
			if got := w.Header().Get("Vary"); got != "Accept-Encoding" {
				t.Errorf("%s: 304 Vary = %q, want Accept-Encoding", c.name, got)
			}
		}
	}
}

// The rendered page is cached per speaker name. A rename must produce a new
// page with a new tag, and the old name's page must not be served for it.
func TestIndexCacheFollowsASpeakerRename(t *testing.T) {
	name := "Kitchen"
	s := newIndexServer()
	s.boxNameFn = func() (string, string) { return name, "rhino" }

	first := getIndex(t, s, map[string]string{"Accept-Encoding": "gzip"})
	if !bytes.Contains(gunzipBody(t, first), []byte("<title>Kitchen</title>")) {
		t.Fatal("the page does not carry the speaker's name")
	}
	again := getIndex(t, s, map[string]string{"Accept-Encoding": "gzip"})
	if again.Header().Get("ETag") != first.Header().Get("ETag") {
		t.Error("the same name produced a different tag on the second open")
	}
	if n := len(s.indexPages); n != 1 {
		t.Errorf("cache holds %d pages after two opens of one name, want 1", n)
	}

	name = "Bathroom"
	renamed := getIndex(t, s, map[string]string{"Accept-Encoding": "gzip"})
	body := gunzipBody(t, renamed)
	if !bytes.Contains(body, []byte("<title>Bathroom</title>")) || bytes.Contains(body, []byte("Kitchen")) {
		t.Error("the page still carries the old name after a rename")
	}
	if renamed.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Error("a renamed speaker kept the old ETag; the phone would keep the old title")
	}
	stale := getIndex(t, s, map[string]string{"Accept-Encoding": "gzip", "If-None-Match": first.Header().Get("ETag")})
	if stale.Code != http.StatusOK {
		t.Errorf("old tag after a rename: status = %d, want 200", stale.Code)
	}

	// The cache is bounded: a box renamed many times does not grow it forever.
	for i := 0; i < 3*indexPageCap; i++ {
		name = "Room " + strconv.Itoa(i)
		getIndex(t, s, nil)
	}
	if n := len(s.indexPages); n > indexPageCap {
		t.Errorf("cache holds %d pages, cap is %d", n, indexPageCap)
	}
}

// A page with no name is rendered as it ships and the generic title stays, so
// the fallback is the same page a box without a name always served.
func TestIndexWithoutANameKeepsTheGenericTitle(t *testing.T) {
	w := getIndex(t, newIndexServer(), nil)
	if !bytes.Contains(w.Body.Bytes(), []byte("<title>ST Reborn</title>")) {
		t.Error("the unnamed page lost its generic title")
	}
}
