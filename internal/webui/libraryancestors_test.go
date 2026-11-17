package webui

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
)

// forgetfulWMP is Windows media sharing in the shape of #1857: after its
// service restarted it answers 701 No such object for a deep folder id, and
// the folder's files do not serve, until the folder above it was browsed
// again. Then the same id lists the same files at the same addresses.
type forgetfulWMP struct {
	ts     *httptest.Server
	mu     sync.Mutex
	known  bool
	parent string // browsing this folder makes the server know the key's folder
	folder string
	calls  []string
}

func newForgetfulWMP(t *testing.T, parent, folder string) *forgetfulWMP {
	t.Helper()
	f := &forgetfulWMP{parent: parent, folder: folder}
	f.ts = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.ts.Close)
	return f
}

func (f *forgetfulWMP) fileURL(name string) string { return f.ts.URL + "/WMPNSSv4/1/" + name + ".mp3" }

func (f *forgetfulWMP) srv() dlna.Server {
	return dlna.Server{UDN: "uuid:PC-MEDIA", FriendlyName: "PC: Media", CDSControlURL: f.ts.URL + "/cds"}
}

func (f *forgetfulWMP) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *forgetfulWMP) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/WMPNSSv4/") {
		f.calls = append(f.calls, "GET")
		if !f.known {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0})
		return
	}
	b, _ := io.ReadAll(r.Body)
	id := soapValue(string(b), "ObjectID")
	f.calls = append(f.calls, "Browse "+id)
	var didl string
	switch id {
	case f.parent:
		f.known = true
		didl = `<container id="` + f.folder + `" parentID="` + id + `"><dc:title>Top Gun</dc:title><upnp:class>object.container.storageFolder</upnp:class></container>`
	case f.folder:
		if !f.known {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault>` +
				`<detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>701</errorCode></UPnPError></detail>` +
				`</s:Fault></s:Body></s:Envelope>`))
			return
		}
		for _, n := range []string{"x1", "x2"} {
			didl += fmt.Sprintf(`<item id="%s" parentID="%s"><dc:title>%s</dc:title><upnp:class>object.item.audioItem.musicTrack</upnp:class>`+
				`<res protocolInfo="http-get:*:audio/mpeg:*">%s</res></item>`, n, id, n, f.fileURL(n))
		}
	}
	n := strings.Count(didl, "<item ") + strings.Count(didl, "<container ")
	result := `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">` +
		didl + `</DIDL-Lite>`
	_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>`+
		`<u:BrowseResponse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><Result>%s</Result>`+
		`<NumberReturned>%d</NumberReturned><TotalMatches>%d</TotalMatches></u:BrowseResponse></s:Body></s:Envelope>`,
		html.EscapeString(result), n, n)
}

func forgetfulWMPServer(t *testing.T, f *forgetfulWMP) *Server {
	t.Helper()
	s := libHealServer(t, mediaservers.Server{ID: "uuid:PC-MEDIA", Name: "PC: Media",
		Location: f.ts.URL + "/description.xml"})
	s.queueHostAnswers = func(context.Context, string) bool { return true }
	s.libraryServerResolve = func(context.Context, string) (dlna.Server, bool) { return f.srv(), true }
	return s
}

func forgetfulFolderKey(f *forgetfulWMP) presets.Preset {
	return presets.Preset{Slot: 6, Type: "queue", Name: "Top Gun", Source: "PC: Media",
		Folder: &presets.FolderRef{ServerUDN: "uuid:PC-MEDIA", ContainerID: f.folder},
		Items: []presets.PresetItem{
			{URL: f.fileURL("x1"), Title: "x1", ObjectID: "x1"},
			{URL: f.fileURL("x2"), Title: "x2", ObjectID: "x2"},
		}}
}

// The reporter's key (#1857): a folder on Windows media sharing whose deep id
// the server forgot. The press walks down to it the way the library browser
// does, the server knows the folder again, and the key is not called gone.
func TestFolderKeyTheServerForgotIsFoundByWalkingDownToIt(t *testing.T) {
	f := newForgetfulWMP(t, "14_1ce3_67e8_ad69", "14_1ce3_67e8_ad69_21da")
	s := forgetfulWMPServer(t, f)
	key := forgetfulFolderKey(f)
	if v := s.healQueuePresetIfMoved(context.Background(), 6, &key); v != nil {
		t.Fatalf("key called unplayable %+v; server saw %v", v, f.log())
	}
	want := "Browse 0|Browse 14|Browse 14_1ce3|Browse 14_1ce3_67e8|Browse 14_1ce3_67e8_ad69|Browse 14_1ce3_67e8_ad69_21da"
	if got := strings.Join(f.log(), "|"); !strings.Contains(got, want) {
		t.Fatalf("server saw %v, want the walk %s", got, want)
	}
	if !s.libraryFileServes(context.Background(), key.Items[0].URL) {
		t.Fatalf("the key's first track does not serve after the walk")
	}
}

// A folder id without that chain shape is not walked: the 701 stays the
// server's word that the folder is gone.
func TestFolderKeyWithAFlatIDIsNotWalked(t *testing.T) {
	f := newForgetfulWMP(t, "never", "c-9")
	s := forgetfulWMPServer(t, f)
	key := forgetfulFolderKey(f)
	v := s.healQueuePresetIfMoved(context.Background(), 6, &key)
	if v == nil || !v.FileGone {
		t.Fatalf("verdict = %+v, want the music gone", v)
	}
	for _, c := range f.log() {
		if c == "Browse 0" {
			t.Fatalf("a flat id was walked: %v", f.log())
		}
	}
}

func TestLibraryContainerAncestors(t *testing.T) {
	for id, want := range map[string]string{
		"14_a_b":            "0|14|14_a",
		"14_a":              "0|14",
		"c-1":               "",
		"14__b":             "",
		"_a":                "",
		"1_2_3_4_5_6_7_8_9": "",
	} {
		if got := strings.Join(libraryContainerAncestors(id), "|"); got != want {
			t.Errorf("ancestors(%q) = %q, want %q", id, got, want)
		}
	}
}
