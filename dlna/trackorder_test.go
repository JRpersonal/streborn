package dlna

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func track(title string, disc, n int) Item {
	return Item{ID: title, Title: title, Class: "object.item.audioItem.musicTrack",
		StreamURL: "http://192.0.2.1/" + title, DiscNumber: disc, TrackNumber: n}
}

func titles(items []Item) string {
	var s []string
	for _, it := range items {
		s = append(s, it.Title)
	}
	return strings.Join(s, ",")
}

func TestAlbumTrackOrder(t *testing.T) {
	cover := Item{ID: "cover", Title: "cover", Class: "object.item.imageItem.photo", MimeType: "image/jpeg"}
	cases := []struct {
		name  string
		in    []Item
		want  string
		moved bool
	}{
		{"alphabetical album is put in track order",
			[]Item{track("Angel", 0, 3), track("Black", 0, 1), track("Come", 0, 2)},
			"Black,Come,Angel", true},
		{"multi-disc album orders disc first",
			[]Item{track("A", 2, 1), track("B", 1, 2), track("C", 2, 2), track("D", 1, 1)},
			"D,B,A,C", true},
		{"same track number on two discs is not a duplicate",
			[]Item{track("d2t1", 2, 1), track("d1t1", 1, 1)},
			"d1t1,d2t1", true},
		{"mixed folder (one track without a number) keeps server order",
			[]Item{track("B", 0, 2), track("A", 0, 0), track("C", 0, 1)},
			"B,A,C", false},
		{"folder without numbers keeps server order",
			[]Item{track("B", 0, 0), track("A", 0, 0)},
			"B,A", false},
		{"duplicate numbers keep server order",
			[]Item{track("B", 0, 2), track("A", 0, 1), track("C", 0, 1)},
			"B,A,C", false},
		{"already in order is reported unmoved",
			[]Item{track("A", 0, 1), track("B", 0, 2)},
			"A,B", false},
		{"non-audio items keep their slots",
			[]Item{cover, track("B", 0, 2), track("A", 0, 1)},
			"cover,A,B", true},
		{"single track", []Item{track("A", 0, 5)}, "A", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := titles(c.in)
			got, moved := AlbumTrackOrder(c.in)
			if titles(got) != c.want || moved != c.moved {
				t.Errorf("got %s moved=%v, want %s moved=%v", titles(got), moved, c.want, c.moved)
			}
			if titles(c.in) != before {
				t.Errorf("input slice was modified: %s", titles(c.in))
			}
		})
	}
}

func TestOrderContainerItemsKeepsPlaylists(t *testing.T) {
	old := containerClassFn
	defer func() { containerClassFn = old }()
	in := []Item{track("B", 0, 2), track("A", 0, 1)}

	asked := 0
	containerClassFn = func(context.Context, Server, string) (string, error) {
		asked++
		return "object.container.playlistContainer", nil
	}
	if got := OrderContainerItems(context.Background(), Server{}, "pl", in); titles(got) != "B,A" {
		t.Errorf("playlist reordered: %s", titles(got))
	}
	containerClassFn = func(context.Context, Server, string) (string, error) {
		return "", errors.New("no answer")
	}
	if got := OrderContainerItems(context.Background(), Server{}, "x", in); titles(got) != "B,A" {
		t.Errorf("unknown class reordered: %s", titles(got))
	}
	containerClassFn = func(context.Context, Server, string) (string, error) {
		return "object.container.album.musicAlbum", nil
	}
	if got := OrderContainerItems(context.Background(), Server{}, "al", in); titles(got) != "A,B" {
		t.Errorf("album not ordered: %s", titles(got))
	}
	// No lookup when nothing would move.
	asked = 0
	containerClassFn = func(context.Context, Server, string) (string, error) {
		asked++
		return "object.container", nil
	}
	OrderContainerItems(context.Background(), Server{}, "al", []Item{track("A", 0, 1), track("B", 0, 2)})
	if asked != 0 {
		t.Errorf("class looked up %d times for an ordered folder", asked)
	}
}

func TestOrderBrowsePageOnlyWholeContainer(t *testing.T) {
	old := containerClassFn
	defer func() { containerClassFn = old }()
	containerClassFn = func(context.Context, Server, string) (string, error) { return "object.container", nil }
	res := BrowseResult{
		Containers:   []Container{{ID: "sub", Title: "Bonus"}},
		Items:        []Item{track("B", 0, 2), track("A", 0, 1)},
		TotalMatches: 3,
	}
	got := OrderBrowsePage(context.Background(), Server{}, "al", 0, res)
	if titles(got.Items) != "A,B" || got.Containers[0].ID != "sub" {
		t.Errorf("whole page: %s %+v", titles(got.Items), got.Containers)
	}
	partial := res
	partial.TotalMatches = 300
	if got := OrderBrowsePage(context.Background(), Server{}, "al", 0, partial); titles(got.Items) != "B,A" {
		t.Errorf("first page of a longer folder was ordered: %s", titles(got.Items))
	}
	if got := OrderBrowsePage(context.Background(), Server{}, "al", 200, res); titles(got.Items) != "B,A" {
		t.Errorf("later page was ordered: %s", titles(got.Items))
	}
}

func TestParseDIDLTrackAndDiscNumbers(t *testing.T) {
	didl := `&lt;DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" ` +
		`xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/"&gt;` +
		`&lt;item id="t1" parentID="al"&gt;&lt;dc:title&gt;Angel&lt;/dc:title&gt;` +
		`&lt;upnp:class&gt;object.item.audioItem.musicTrack&lt;/upnp:class&gt;` +
		`&lt;upnp:originalTrackNumber&gt; 03 &lt;/upnp:originalTrackNumber&gt;` +
		`&lt;upnp:originalDiscNumber&gt;2&lt;/upnp:originalDiscNumber&gt;` +
		`&lt;res protocolInfo="http-get:*:audio/flac:*"&gt;http://192.0.2.1/a.flac&lt;/res&gt;&lt;/item&gt;` +
		`&lt;item id="t2" parentID="al"&gt;&lt;dc:title&gt;Black&lt;/dc:title&gt;` +
		`&lt;upnp:class&gt;object.item.audioItem.musicTrack&lt;/upnp:class&gt;` +
		`&lt;upnp:originalTrackNumber&gt;7/12&lt;/upnp:originalTrackNumber&gt;` +
		`&lt;res protocolInfo="http-get:*:audio/flac:*"&gt;http://192.0.2.1/b.flac&lt;/res&gt;&lt;/item&gt;` +
		`&lt;item id="t3" parentID="al"&gt;&lt;dc:title&gt;Come&lt;/dc:title&gt;` +
		`&lt;upnp:class&gt;object.item.audioItem.musicTrack&lt;/upnp:class&gt;` +
		`&lt;res protocolInfo="http-get:*:audio/flac:*"&gt;http://192.0.2.1/c.flac&lt;/res&gt;&lt;/item&gt;` +
		`&lt;container id="pl" parentID="al"&gt;&lt;dc:title&gt;Mix&lt;/dc:title&gt;` +
		`&lt;upnp:class&gt;object.container.playlistContainer&lt;/upnp:class&gt;&lt;/container&gt;` +
		`&lt;/DIDL-Lite&gt;`
	soap := "<?xml version=\"1.0\"?>\n<Envelope><Body><BrowseResponse><Result>" + didl +
		"</Result><NumberReturned>4</NumberReturned><TotalMatches>4</TotalMatches></BrowseResponse></Body></Envelope>"
	res, err := parseBrowseResponse([]byte(soap))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 3 {
		t.Fatalf("want 3 items, got %d", len(res.Items))
	}
	if it := res.Items[0]; it.TrackNumber != 3 || it.DiscNumber != 2 {
		t.Errorf("item 1: track %d disc %d, want 3 / 2", it.TrackNumber, it.DiscNumber)
	}
	if it := res.Items[1]; it.TrackNumber != 7 || it.DiscNumber != 0 {
		t.Errorf("item 2: track %d disc %d, want 7 / 0", it.TrackNumber, it.DiscNumber)
	}
	if it := res.Items[2]; it.TrackNumber != 0 {
		t.Errorf("item 3 without a number: track %d", it.TrackNumber)
	}
	if len(res.Containers) != 1 || !IsPlaylistClass(res.Containers[0].Class) {
		t.Errorf("playlist container class lost: %+v", res.Containers)
	}
}

func TestContainerClassAsksBrowseMetadata(t *testing.T) {
	var flag string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		body := string(buf[:n])
		if i := strings.Index(body, "<BrowseFlag>"); i >= 0 {
			flag = body[i+len("<BrowseFlag>"):]
			flag = flag[:strings.Index(flag, "<")]
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>` +
			`<u:BrowseResponse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><Result>` +
			`&lt;DIDL-Lite xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/"&gt;&lt;container id="pl" parentID="0"&gt;` +
			`&lt;upnp:class&gt;object.container.playlistContainer&lt;/upnp:class&gt;&lt;/container&gt;&lt;/DIDL-Lite&gt;` +
			`</Result><NumberReturned>1</NumberReturned><TotalMatches>1</TotalMatches></u:BrowseResponse></s:Body></s:Envelope>`))
	}))
	defer ts.Close()
	class, err := ContainerClass(context.Background(), Server{CDSControlURL: ts.URL}, "pl")
	if err != nil {
		t.Fatal(err)
	}
	if flag != "BrowseMetadata" {
		t.Errorf("flag = %q, want BrowseMetadata", flag)
	}
	if !IsPlaylistClass(class) {
		t.Errorf("class = %q", class)
	}
}
