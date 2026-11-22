package usbmedia

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/dlna"
)

// searchStick is a stick laid out the ways people fill one: Artist/Album,
// "Artist - Album", loose files, plus what must never be a hit.
func searchStick(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root,
		"Beatles/Help!/01 Help.mp3", "Beatles/Help!/02 Yesterday.flac", "Beatles/Help!/cover.jpg",
		"Beatles/Help!/help notes.txt",
		"Queen - Innuendo/01 Innuendo.mp3", "Queen - Innuendo/02 Headlong.m4a",
		"loose Help.ogg",
		".hidden/Help.mp3", "System Volume Information/Help.mp3", "._Help.mp3",
	)
	return root
}

func hitIDs(hits []Entry) string {
	ids := make([]string, 0, len(hits))
	for _, e := range hits {
		ids = append(ids, e.ID)
	}
	return strings.Join(ids, "|")
}

func TestSearchMatchesFileAndFolderNames(t *testing.T) {
	lib, err := Open(searchStick(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lib.Close() }()
	ctx := context.Background()
	cases := []struct {
		query, field, want string
	}{
		// Any: the file name, or a folder on the way. Root files come first
		// (they are in the first directory read), then the folders depth first.
		{"help", FieldAny, "loose Help.ogg|Beatles/Help!/01 Help.mp3|Beatles/Help!/02 Yesterday.flac"},
		// The artist exists only as a folder name: the bug of discussion #1136.
		{"BEATLES", FieldArtist, "Beatles/Help!/01 Help.mp3|Beatles/Help!/02 Yesterday.flac"},
		{"queen", FieldArtist, "Queen - Innuendo/01 Innuendo.mp3|Queen - Innuendo/02 Headlong.m4a"},
		{"innuendo", FieldAlbum, "Queen - Innuendo/01 Innuendo.mp3|Queen - Innuendo/02 Headlong.m4a"},
		// Title: the file name only, not the folder it sits in.
		{"help", FieldTitle, "loose Help.ogg|Beatles/Help!/01 Help.mp3"},
		{"innuendo", FieldTitle, "Queen - Innuendo/01 Innuendo.mp3"},
		// An extension is not part of the title.
		{"flac", FieldAny, ""},
		{"nothing like it", FieldAny, ""},
	}
	for _, c := range cases {
		res, err := lib.Search(ctx, "0", TermsFor(c.query, c.field), 40)
		if err != nil {
			t.Fatalf("%s/%s: %v", c.query, c.field, err)
		}
		if got := hitIDs(res.Hits); got != c.want {
			t.Errorf("%s/%s: got %q, want %q", c.query, c.field, got, c.want)
		}
		if res.Partial {
			t.Errorf("%s/%s: a small stick is searched in full", c.query, c.field)
		}
	}

	res, _ := lib.Search(ctx, "0", TermsFor("beatles", FieldAny), 40)
	first := res.Hits[0]
	if first.Path != "Beatles/Help!/01 Help.mp3" || first.MIME != "audio/mpeg" || first.Cover != "Beatles/Help!/cover.jpg" ||
		first.Title != "01 Help" {
		t.Fatalf("a hit is the entry a Browse lists: %+v", first)
	}

	// The hit cap ends the search early.
	if res, _ := lib.Search(ctx, "0", TermsFor("help", FieldAny), 1); hitIDs(res.Hits) != "loose Help.ogg" {
		t.Fatalf("limit: %+v", res.Hits)
	}
	// Below a folder, the folders above it still count as its names.
	if res, _ := lib.Search(ctx, "Beatles/Help!", TermsFor("beatles", FieldArtist), 40); len(res.Hits) != 2 {
		t.Fatalf("from a folder: %+v", res.Hits)
	}
	// No query, no hits and no reads.
	if res, _ := lib.Search(ctx, "0", TermsFor("  ", FieldAny), 40); len(res.Hits) != 0 || res.Folders != 0 {
		t.Fatalf("empty query: %+v", res)
	}
	// Outside the stick or gone: an error, as for Browse.
	if _, err := lib.Search(ctx, "../etc", TermsFor("x", FieldAny), 40); !errors.Is(err, ErrBadID) {
		t.Fatalf("traversal: %v", err)
	}
	// A search whose deadline is gone reads nothing and says so.
	done, cancel := context.WithCancel(ctx)
	cancel()
	if res, _ := lib.Search(done, "0", TermsFor("help", FieldAny), 40); !res.Partial || res.Folders != 0 {
		t.Fatalf("cancelled: %+v", res)
	}
}

func TestParseCriteriaReadsWhatTheClientsSend(t *testing.T) {
	cases := []struct {
		field string
		want  SearchTerm
	}{
		{dlna.SearchAny, SearchTerm{Value: `a "b" c\`, InTitle: true, InFolders: true}},
		{dlna.SearchTitle, SearchTerm{Value: `a "b" c\`, InTitle: true}},
		{dlna.SearchArtist, SearchTerm{Value: `a "b" c\`, InFolders: true}},
		{dlna.SearchAlbum, SearchTerm{Value: `a "b" c\`, InFolders: true}},
	}
	for _, c := range cases {
		terms, err := ParseCriteria(dlna.SearchCriteria(`a "b" c\`, c.field))
		if err != nil || len(terms) != 1 || terms[0] != c.want {
			t.Errorf("%s: %+v %v", c.field, terms, err)
		}
	}
	if terms, err := ParseCriteria(`dc:title contains "x" or dc:creator contains "y"`); err != nil || len(terms) != 2 {
		t.Fatalf("generic client: %+v %v", terms, err)
	}
	for _, bad := range []string{"*", `upnp:class derivedfrom "object.item.audioItem"`, `dc:title contains "`, `dc:date contains "2020"`} {
		if _, err := ParseCriteria(bad); !errors.Is(err, ErrUnsupportedCriteria) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// The desktop app searches another speaker's stick over the LAN with the
// repo's own DLNA client: the Search action must answer it, with the URLs a
// Browse of the same folder hands out.
func TestTheSearchActionAnswersTheDLNAClient(t *testing.T) {
	srv, _ := serveStick(t, searchStick(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ds, err := dlna.DescribeServer(ctx, srv.URL+DescriptionPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := dlna.SearchBy(ctx, ds, "beatles", dlna.SearchArtist, 50)
	if err != nil || len(res.Items) != 2 || res.HasMore() {
		t.Fatalf("artist search: %+v %v", res, err)
	}
	album, err := dlna.Browse(ctx, ds, "Beatles/Help!", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	for i, it := range res.Items {
		b := album.Items[i]
		if it.StreamURL != b.StreamURL || it.AlbumArtURL != b.AlbumArtURL || it.ID != b.ID ||
			it.Album != "Help!" || it.MimeType != b.MimeType || !it.IsAudioItem() {
			t.Fatalf("hit %d %+v is not the browsed track %+v", i, it, b)
		}
	}
	// Paging: a client that asks for fewer is told there is more.
	page, err := dlna.SearchBy(ctx, ds, "help", dlna.SearchAny, 2)
	if err != nil || len(page.Items) != 2 || !page.HasMore() {
		t.Fatalf("paged: %+v %v", page, err)
	}
	all, err := dlna.Search(ctx, ds, "help", 50)
	if err != nil || len(all.Items) != 3 {
		t.Fatalf("all: %+v %v", all, err)
	}
	// A criteria the stick cannot answer is UPnP 708, so a client walks.
	body := `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:Search xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><ContainerID>0</ContainerID><SearchCriteria>*</SearchCriteria><StartingIndex>0</StartingIndex><RequestedCount>10</RequestedCount></u:Search></s:Body></s:Envelope>`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+ControlPath, strings.NewReader(body))
	req.Header.Set("SOAPACTION", `"urn:schemas-upnp-org:service:ContentDirectory:1#Search"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("criteria '*': %d", resp.StatusCode)
	}
}
