package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/dlna"
)

// albumCDS answers Browse for one container with three tracks listed
// alphabetically, each carrying upnp:originalTrackNumber, and BrowseMetadata
// with the container class it was given. total is what TotalMatches claims.
func albumCDS(t *testing.T, class string, total int) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var didl string
		if strings.Contains(string(body), "<BrowseFlag>BrowseMetadata</BrowseFlag>") {
			didl = `<DIDL-Lite xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/"><container id="al" parentID="0"><upnp:class>` +
				class + `</upnp:class></container></DIDL-Lite>`
		} else {
			var b strings.Builder
			b.WriteString(`<DIDL-Lite xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">`)
			b.WriteString(`<container id="bonus" parentID="al"><dc:title>Bonus</dc:title><upnp:class>object.container</upnp:class></container>`)
			for _, tr := range []struct {
				title string
				no    int
			}{{"Angel", 3}, {"Black", 1}, {"Come", 2}} {
				fmt.Fprintf(&b, `<item id="%s" parentID="al"><dc:title>%s</dc:title><upnp:class>object.item.audioItem.musicTrack</upnp:class>`+
					`<upnp:originalTrackNumber>%d</upnp:originalTrackNumber><res protocolInfo="http-get:*:audio/mpeg:*">http://192.0.2.9/%s.mp3</res></item>`,
					tr.title, tr.title, tr.no, tr.title)
			}
			b.WriteString(`</DIDL-Lite>`)
			didl = b.String()
		}
		esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(didl)
		fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:BrowseResponse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><Result>%s</Result><NumberReturned>4</NumberReturned><TotalMatches>%d</TotalMatches></u:BrowseResponse></s:Body></s:Envelope>`, esc, total)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func libraryTitles(items []LibraryItem) string {
	var s []string
	for _, it := range items {
		s = append(s, it.Title)
	}
	return strings.Join(s, ",")
}

// The Library lists, plays and saves an album folder in track order (#1487):
// everything downstream (Play folder, the folder key) takes the page's item
// order, so the order has to be right here.
func TestBrowseLibraryPutsAnAlbumInTrackOrder(t *testing.T) {
	for _, c := range []struct {
		name, class, want string
	}{
		{"album", "object.container.album.musicAlbum", "Black,Come,Angel"},
		{"playlist keeps the server order", "object.container.playlistContainer", "Angel,Black,Come"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ts := albumCDS(t, c.class, 4)
			a := &App{libraryServers: map[string]dlna.Server{"uuid:x": {UDN: "uuid:x", CDSControlURL: ts.URL}}}
			page, err := a.BrowseLibrary("uuid:x", "al", 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			if got := libraryTitles(page.Items); got != c.want {
				t.Errorf("items = %s, want %s", got, c.want)
			}
			if len(page.Containers) != 1 || page.Containers[0].ID != "bonus" {
				t.Errorf("sub-folder lost: %+v", page.Containers)
			}
			if page.Items[0].TrackNumber == 0 {
				t.Errorf("track number not handed to the frontend: %+v", page.Items[0])
			}
			if page.ContainerClass != "" {
				t.Errorf("a single-page folder needs no class for the frontend, got %q", page.ContainerClass)
			}
		})
	}
}

// The first page of a longer folder is left in server order (it is only part
// of the album) and carries the folder's class, so the frontend can order the
// whole folder once it holds every page, and leave a playlist alone.
func TestBrowseLibraryFirstPageOfALongFolderCarriesTheClass(t *testing.T) {
	ts := albumCDS(t, "object.container.playlistContainer", 400)
	a := &App{libraryServers: map[string]dlna.Server{"uuid:x": {UDN: "uuid:x", CDSControlURL: ts.URL}}}
	page, err := a.BrowseLibrary("uuid:x", "al", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got := libraryTitles(page.Items); got != "Angel,Black,Come" {
		t.Errorf("a partial page was re-ordered: %s", got)
	}
	if page.ContainerClass != "object.container.playlistContainer" {
		t.Errorf("containerClass = %q", page.ContainerClass)
	}
}
