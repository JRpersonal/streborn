package boxapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func stationTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return &Client{Host: "192.0.2.1", HTTP: &http.Client{Timeout: 2 * time.Second, Transport: &rewriteTransport{to: u}}}
}

// pandoraItem is one station the way the firmware lists it (shape from the
// Bose app's own browse, thlucas1/bosesoundtouchapi).
func pandoraItem(i int) string {
	return fmt.Sprintf(`<item Playable="1"><name>Station %d</name><type>stationurl</type>`+
		`<ContentItem source="PANDORA" type="stationurl" location="1267%d" sourceAccount="user@example.com" isPresetable="true">`+
		`<itemName>Station %d &amp; Friends</itemName><containerArt>http://img.example.com/%d.jpg</containerArt></ContentItem></item>`, i, i, i, i)
}

func TestListServiceStationsPagesAndKeepsItem(t *testing.T) {
	const total = 130
	var bodies []string
	startRe := regexp.MustCompile(`<startItem>(\d+)</startItem><numItems>(\d+)</numItems>`)
	c := stationTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		m := startRe.FindStringSubmatch(string(b))
		start, _ := strconv.Atoi(m[1])
		n, _ := strconv.Atoi(m[2])
		var items strings.Builder
		for i := start; i < start+n && i <= total; i++ {
			items.WriteString(pandoraItem(i))
		}
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8" ?><navigateResponse source="PANDORA" sourceAccount="user@example.com"><totalItems>%d</totalItems><items>%s</items></navigateResponse>`, total, items.String())
	})
	got, err := c.ListServiceStations(context.Background(), "PANDORA", "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != total || len(bodies) != 2 {
		t.Fatalf("got %d stations in %d calls, want %d in 2", len(got), len(bodies), total)
	}
	want := `<navigate source="PANDORA" sourceAccount="user@example.com" menu="radioStations" sort="dateCreated"><startItem>1</startItem><numItems>100</numItems></navigate>`
	if bodies[0] != want {
		t.Errorf("first request\n got %s\nwant %s", bodies[0], want)
	}
	if !strings.Contains(bodies[1], "<startItem>101</startItem>") {
		t.Errorf("second page request %s", bodies[1])
	}
	st := got[0]
	if st.Name != "Station 1" || st.Location != "12671" || st.Type != "stationurl" || st.Source != "PANDORA" ||
		st.ItemName != "Station 1 & Friends" || st.ContainerArt != "http://img.example.com/1.jpg" || !st.Presetable {
		t.Fatalf("station = %+v", st)
	}
	wantItem := `<ContentItem source="PANDORA" type="stationurl" location="12671" sourceAccount="user@example.com" isPresetable="true">` +
		`<itemName>Station 1 &amp; Friends</itemName><containerArt>http://img.example.com/1.jpg</containerArt></ContentItem>`
	if st.ContentItem != wantItem {
		t.Fatalf("ContentItem not kept as sent\n got %s\nwant %s", st.ContentItem, wantItem)
	}
}

func TestListServiceStationsErrorDocument(t *testing.T) {
	c := stationTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<errors deviceID="AABBCCDDEEFF"><error value="1005" name="UNKNOWN_SOURCE_ERROR" severity="Unknown">1005</error></errors>`))
	})
	_, err := c.ListServiceStations(context.Background(), "PANDORA", "user@example.com")
	if err == nil || !strings.Contains(err.Error(), "UNKNOWN_SOURCE_ERROR") {
		t.Fatalf("err = %v", err)
	}
}

func TestListServiceStationsEmpty(t *testing.T) {
	c := stationTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<navigateResponse source="PANDORA"><totalItems>0</totalItems><items /></navigateResponse>`))
	})
	got, err := c.ListServiceStations(context.Background(), "PANDORA", "user@example.com")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestSelectContentItem(t *testing.T) {
	var body string
	reply := `<status>/select</status>`
	c := stationTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = w.Write([]byte(reply))
	})
	item := `<ContentItem source="PANDORA" location="1"><itemName>x</itemName></ContentItem>`
	if err := c.SelectContentItem(context.Background(), item); err != nil || body != item {
		t.Fatalf("err %v, body %q", err, body)
	}
	reply = `<errors deviceID="AABBCCDDEEFF"><error value="1005" name="UNKNOWN_SOURCE_ERROR" severity="Unknown">1005</error></errors>`
	if err := c.SelectContentItem(context.Background(), item); err == nil || !strings.Contains(err.Error(), "1005") {
		t.Fatalf("refusal not reported: %v", err)
	}
}
