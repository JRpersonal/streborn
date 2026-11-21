package usbmedia

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/dlna"
)

// serveStick runs the stick's ContentDirectory the way the agent mounts it,
// so the repo's own DLNA client (the one the desktop app and the phone page
// browse with) is the test oracle for the wire shapes.
func serveStick(t *testing.T, root string) (*httptest.Server, Info) {
	t.Helper()
	lib, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lib.Close() })
	info := Info{UDN: "uuid:str-usb-test", FriendlyName: "USB stick (Kitchen)", UpdateID: 3}
	mux := http.NewServeMux()
	mux.HandleFunc(DescriptionPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write(DeviceDescription(info))
	})
	mux.HandleFunc(SCPDPath, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, SCPD) })
	mux.HandleFunc(ControlPath, func(w http.ResponseWriter, r *http.Request) { lib.Control(w, r, "http://"+r.Host, info) })
	mux.HandleFunc(FilePrefix, func(w http.ResponseWriter, r *http.Request) { lib.ServeFile(w, r, FilePrefix) })
	mux.HandleFunc(ArtPrefix, func(w http.ResponseWriter, r *http.Request) { lib.ServeFile(w, r, ArtPrefix) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, info
}

func musicStick(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root,
		"Album 1/01 Ärger.mp3", "Album 1/2 Test.flac", "Album 1/10 Last.m4a", "Album 1/cover.jpg",
		"Album 1/notes.txt", "loose.ogg", "Zebra/x.wav",
	)
	return root
}

func TestTheDLNAClientDescribesAndBrowsesTheStick(t *testing.T) {
	srv, info := serveStick(t, musicStick(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ds, err := dlna.DescribeServer(ctx, srv.URL+DescriptionPath)
	if err != nil {
		t.Fatal(err)
	}
	if ds.UDN != info.UDN || ds.FriendlyName != info.FriendlyName || ds.CDSControlURL != srv.URL+ControlPath {
		t.Fatalf("description: %+v", ds)
	}

	root, err := dlna.Browse(ctx, ds, "0", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if root.TotalMatches != 3 || len(root.Containers) != 2 || len(root.Items) != 1 {
		t.Fatalf("root: %+v", root)
	}
	if root.Containers[0].ID != "Album 1" || root.Containers[0].ParentID != "0" || root.Containers[1].Title != "Zebra" {
		t.Fatalf("root folders: %+v", root.Containers)
	}
	loose := root.Items[0]
	if loose.Title != "loose" || loose.MimeType != "audio/ogg" || loose.Album != "" ||
		loose.StreamURL != srv.URL+"/usb/file/loose.ogg" || !loose.IsAudioItem() {
		t.Fatalf("root file: %+v", loose)
	}

	album, err := dlna.Browse(ctx, ds, "Album 1", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, it := range album.Items {
		titles = append(titles, it.Title)
	}
	// Natural order, and the text file is not offered.
	if strings.Join(titles, "|") != "01 Ärger|2 Test|10 Last" {
		t.Fatalf("album order: %v", titles)
	}
	first := album.Items[0]
	if first.Album != "Album 1" || first.ParentID != "Album 1" ||
		first.AlbumArtURL != srv.URL+"/usb/art/Album%201/cover.jpg" ||
		first.StreamURL != srv.URL+"/usb/file/Album%201/01%20%C3%84rger.mp3" {
		t.Fatalf("album track: %+v", first)
	}
	if album.Items[1].MimeType != "audio/flac" || album.Items[2].MimeType != "audio/mp4" {
		t.Fatalf("mime: %+v", album.Items)
	}

	// Paging: TotalMatches stays the folder's size.
	p, err := dlna.Browse(ctx, ds, "Album 1", 1, 1)
	if err != nil || p.TotalMatches != 3 || len(p.Items) != 1 || p.Items[0].Title != "2 Test" {
		t.Fatalf("page: %+v %v", p, err)
	}

	// BrowseMetadata, as the desktop app's folder heading asks for it.
	c, err := dlna.ContainerMetadata(ctx, ds, "Album 1")
	if err != nil || c.Title != "Album 1" || c.ParentID != "0" || c.AlbumArtURL != srv.URL+"/usb/art/Album%201/cover.jpg" {
		t.Fatalf("metadata: %+v %v", c, err)
	}
	it, err := dlna.ItemMetadata(ctx, ds, "Album 1/2 Test.flac")
	if err != nil || it.StreamURL != srv.URL+"/usb/file/Album%201/2%20Test.flac" {
		t.Fatalf("item metadata: %+v %v", it, err)
	}

	// A gone folder is UPnP 701, which the clients read as "gone".
	if _, err := dlna.Browse(ctx, ds, "Removed", 0, 10); !errors.Is(err, dlna.ErrNoSuchObject) {
		t.Fatalf("gone folder: %v", err)
	}
	if _, err := dlna.Browse(ctx, ds, "../etc", 0, 10); !errors.Is(err, dlna.ErrNoSuchObject) {
		t.Fatalf("traversal: %v", err)
	}
	// No Search: a fault, so the clients walk instead.
	if _, err := dlna.Search(ctx, ds, "Test", 10); err == nil {
		t.Fatal("Search must answer a fault")
	}
}

func TestFilesAreServedWithRangeAndTheirOwnType(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.flac"), []byte("fLaC0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, root, "cover.jpg", "notes.txt", "Sub/x.mp3")
	srv, _ := serveStick(t, root)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/usb/file/a.flac", nil)
	req.Header.Set("Range", "bytes=4-7")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != "0123" ||
		resp.Header.Get("Content-Type") != "audio/flac" || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("range: %d %q %v", resp.StatusCode, body, resp.Header)
	}

	for path, want := range map[string]int{
		"/usb/art/cover.jpg":          http.StatusOK,
		"/usb/file/notes.txt":         http.StatusNotFound, // not audio
		"/usb/art/notes.txt":          http.StatusNotFound, // not a cover
		"/usb/file/Sub/x.mp3":         http.StatusOK,
		"/usb/file/../a.flac":         http.StatusNotFound,
		"/usb/file/Sub%2F..%2Fa.flac": http.StatusNotFound,
		"/usb/file/missing.mp3":       http.StatusNotFound,
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: got %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestControlRefusesWhatItDoesNotOffer(t *testing.T) {
	srv, _ := serveStick(t, musicStick(t))
	post := func(action, body string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+ControlPath, strings.NewReader(body))
		if action != "" {
			req.Header.Set("SOAPACTION", `"urn:schemas-upnp-org:service:ContentDirectory:1#`+action+`"`)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(b)
	}
	code, body := post("GetSystemUpdateID", "")
	if code != http.StatusOK || !strings.Contains(body, "<Id>3</Id>") {
		t.Fatalf("update id: %d %s", code, body)
	}
	code, body = post("CreateObject", "")
	if code != http.StatusInternalServerError || !strings.Contains(body, "<errorCode>401</errorCode>") {
		t.Fatalf("unknown action: %d %s", code, body)
	}
	// No header: the action is read from the body.
	code, body = post("", `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:Browse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><ObjectID>0</ObjectID><BrowseFlag>BrowseDirectChildren</BrowseFlag><StartingIndex>0</StartingIndex><RequestedCount>0</RequestedCount></u:Browse></s:Body></s:Envelope>`)
	if code != http.StatusOK || !strings.Contains(body, "<TotalMatches>3</TotalMatches>") {
		t.Fatalf("body action: %d %s", code, body)
	}
	resp, err := http.Get(srv.URL + ControlPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET on control: %d", resp.StatusCode)
	}
}

func TestNaturalOrder(t *testing.T) {
	cases := [][2]string{{"2 a", "10 b"}, {"a", "B"}, {"01 x", "2 a"}, {"track9", "track10"}}
	for _, c := range cases {
		if !naturalLess(c[0], c[1]) || naturalLess(c[1], c[0]) {
			t.Errorf("%q should sort before %q", c[0], c[1])
		}
	}
}
