package webui

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
)

type phoneSearchReply struct {
	Results []struct {
		Title, Album, URL, Art, Mime, Server string
	} `json:"results"`
	Registered int  `json:"registered"`
	Partial    bool `json:"partial"`
}

func phoneSearch(t *testing.T, s *Server, q, field string) phoneSearchReply {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/library/search?q="+url.QueryEscape(q)+"&field="+field, nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "192.0.2.10:8888"
	s.handleLibrarySearch(rr, req)
	var out phoneSearchReply
	if rr.Code != 200 {
		t.Fatalf("search %q: %d %s", q, rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("search %q: %s", q, rr.Body.String())
	}
	return out
}

// Discussion #1136, 2026-11-22: the phone page's search only asked the
// registered servers, the stick is never one, and so it found nothing on the
// stick and, with no other server, said none was set up.
func TestThePhoneSearchFindsSongsOnTheStick(t *testing.T) {
	f := newFakeStick(t, "rhino")
	media := t.TempDir()
	f.put(media, "Beatles/Help!/01 Help.mp3", "Beatles/Help!/02 Yesterday.flac", "Beatles/Help!/cover.jpg",
		"Queen/Innuendo/01 Innuendo.mp3", "loose.ogg")
	f.plug(fatSector("fat32", 64))
	f.firmwareMount(media, "vfat")
	s, _ := usbTestServer(t)
	srv := startUSBAgent(t, s)

	got := phoneSearch(t, s, "beatles", "artist")
	if got.Registered != 1 || len(got.Results) != 2 || got.Partial {
		t.Fatalf("artist search: %+v", got)
	}
	hit := got.Results[0]
	if hit.Title != "01 Help" || hit.Album != "Help!" || hit.Mime != "audio/mpeg" || hit.Server != "USB stick (Kitchen)" {
		t.Fatalf("hit: %+v", hit)
	}

	// The hit plays like the browsed song: the same URL, the same cover.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/library/browse?udn="+s.usbUDN()+"&id="+url.QueryEscape("Beatles/Help!"), nil)
	req.Host = "192.0.2.10:8888"
	s.handleLibraryBrowse(rr, req)
	var page struct {
		Tracks []struct{ URL, Art, Mime string } `json:"tracks"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil || len(page.Tracks) != 2 {
		t.Fatalf("browse: %s", rr.Body.String())
	}
	if hit.URL != page.Tracks[0].URL || hit.Art != page.Tracks[0].Art || hit.Mime != page.Tracks[0].Mime {
		t.Fatalf("search hit %+v is not the browsed track %+v", hit, page.Tracks[0])
	}
	if hit.URL != srv.URL+"/usb/file/Beatles/Help%21/01%20Help.mp3" && hit.URL != srv.URL+"/usb/file/Beatles/Help!/01%20Help.mp3" {
		t.Fatalf("loopback URL: %s", hit.URL)
	}
	// A key saved from the hit knows the track's object id.
	if ref, ok := s.libRefs.get(hit.URL); !ok || ref.ObjectID != "Beatles/Help!/01 Help.mp3" || ref.ServerUDN != s.usbUDN() {
		t.Fatalf("ref: %+v %v", ref, ok)
	}

	if got := phoneSearch(t, s, "innuendo", "title"); len(got.Results) != 1 {
		t.Fatalf("title search: %+v", got)
	}
	if got := phoneSearch(t, s, "help", "any"); len(got.Results) != 2 {
		t.Fatalf("any search: %+v", got)
	}
	if got := phoneSearch(t, s, "nothing like it", "any"); got.Registered != 1 || len(got.Results) != 0 {
		t.Fatalf("a miss still counts the stick as searched: %+v", got)
	}

	// No stick: nothing to search and nothing registered, as before.
	f.unplug()
	if got := phoneSearch(t, s, "beatles", "any"); got.Registered != 0 || len(got.Results) != 0 {
		t.Fatalf("no stick: %+v", got)
	}
}
