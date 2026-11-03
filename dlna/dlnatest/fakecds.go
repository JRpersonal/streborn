// Package dlnatest is a fake UPnP ContentDirectory for tests: a media server
// that can be pointed at without a LAN, with switches for the server quirks the
// library search has to survive. It is shared by the dlna package's own tests
// and the desktop app's (a separate module, which cannot see another package's
// _test.go files).
package dlnatest

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// FakeCDS is an http.Handler answering ContentDirectory Search and Browse.
type FakeCDS struct {
	mu sync.Mutex
	// SearchHits is what the Search action answers with. Zero items with no
	// fault is the FRITZ!Box shape: 200, nothing found, the track still there.
	SearchHits []Track
	// SearchFaults makes Search answer UPnPError 501 "Action Failed", the
	// Synology and Windows Media Player shape (#1705).
	SearchFaults bool
	// BrowseFaults makes every Browse answer a SOAP fault too: a server that
	// cannot be read at all.
	BrowseFaults bool
	// Children maps an object id to its children. Items are paged with the
	// StartingIndex the caller sends.
	Children map[string]Container
	// IgnoreStart models a server that hands back the same first page for
	// every StartingIndex.
	IgnoreStart bool
	// MaxPerPage models a server that silently caps RequestedCount, which many
	// ContentDirectory implementations do. Zero honours what is asked.
	MaxPerPage int

	searches int
	browses  int
}

// Container is one object's children: its folders first, then its tracks.
type Container struct {
	Folders []Folder
	Tracks  []Track
}

// Folder is a child container.
type Folder struct {
	ID, Title string
}

// Track is an audio item.
type Track struct {
	ID, Title, Artist, Album, URL string
	// Class and Mime override object.item.audioItem.musicTrack and
	// audio/mpeg, e.g. for a video.
	Class, Mime string
}

// Counts reports how many Search and Browse calls the fake has answered.
func (f *FakeCDS) Counts() (searches, browses int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.searches, f.browses
}

const faultBody = `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>501</errorCode><errorDescription>Action Failed</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`

func (f *FakeCDS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	action := r.Header.Get("SOAPACTION")
	f.mu.Lock()
	defer f.mu.Unlock()

	if strings.Contains(action, "#Search") {
		f.searches++
		if f.SearchFaults {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, faultBody)
			return
		}
		writeSOAP(w, "Search", didl(nil, f.SearchHits), len(f.SearchHits), len(f.SearchHits))
		return
	}

	f.browses++
	if f.BrowseFaults {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, faultBody)
		return
	}
	id := soapValue(string(body), "ObjectID")
	start, _ := strconv.Atoi(soapValue(string(body), "StartingIndex"))
	count, _ := strconv.Atoi(soapValue(string(body), "RequestedCount"))
	if f.IgnoreStart {
		start = 0
	}
	if f.MaxPerPage > 0 && count > f.MaxPerPage {
		count = f.MaxPerPage
	}
	c := f.Children[id]
	all := len(c.Folders) + len(c.Tracks)
	folders := pageOf(c.Folders, start, count)
	// Tracks come after the folders in the child list, so their offset is
	// whatever of the folder block the page did not consume.
	tStart := start - len(c.Folders)
	if tStart < 0 {
		tStart = 0
	}
	tracks := pageOf(c.Tracks, tStart, count-len(folders))
	writeSOAP(w, "Browse", didl(folders, tracks), all, len(folders)+len(tracks))
}

func pageOf[T any](in []T, start, count int) []T {
	if start >= len(in) || count <= 0 {
		return nil
	}
	end := start + count
	if end > len(in) {
		end = len(in)
	}
	return in[start:end]
}

func soapValue(body, tag string) string {
	openTag, closeTag := "<"+tag+">", "</"+tag+">"
	i := strings.Index(body, openTag)
	if i < 0 {
		return ""
	}
	rest := body[i+len(openTag):]
	j := strings.Index(rest, closeTag)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func didl(folders []Folder, tracks []Track) string {
	var b strings.Builder
	b.WriteString(`<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">`)
	for _, f := range folders {
		fmt.Fprintf(&b, `<container id="%s" parentID="0" childCount="1"><dc:title>%s</dc:title><upnp:class>object.container</upnp:class></container>`, f.ID, f.Title)
	}
	for _, t := range tracks {
		class := t.Class
		if class == "" {
			class = "object.item.audioItem.musicTrack"
		}
		mime := t.Mime
		if mime == "" {
			mime = "audio/mpeg"
		}
		fmt.Fprintf(&b, `<item id="%s" parentID="0"><dc:title>%s</dc:title><upnp:artist>%s</upnp:artist><upnp:album>%s</upnp:album><upnp:class>%s</upnp:class><res protocolInfo="http-get:*:%s:*">%s</res></item>`,
			t.ID, t.Title, t.Artist, t.Album, class, mime, t.URL)
	}
	b.WriteString(`</DIDL-Lite>`)
	return b.String()
}

func writeSOAP(w http.ResponseWriter, action, result string, total, returned int) {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(result)
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:%sResponse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><Result>%s</Result><NumberReturned>%d</NumberReturned><TotalMatches>%d</TotalMatches><UpdateID>1</UpdateID></u:%sResponse></s:Body></s:Envelope>`,
		action, esc, returned, total, action)
}
