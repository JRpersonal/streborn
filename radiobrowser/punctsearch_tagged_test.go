package radiobrowser

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A user searched for "Gold 90.5", the directory lists the station as
// "Gold 905". The name search finds nothing, but "gold" is a tag on many
// stations, so the tag search did find something, and the punctuation
// fallback used to run only when everything came back empty. The station the
// user typed the name of never showed up.
func TestSearchSmartFindsDottedFrequencyDespiteTagHits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case q.Get("tagList") != "":
			io.WriteString(w, `[{"stationuuid":"t1","name":"Golden Oldies","url":"http://radio.example/oldies","lastcheckok":1}]`)
		case q.Get("name") == "gold":
			io.WriteString(w, `[
				{"stationuuid":"g1","name":"Gold 905 Radio","url":"http://radio.example/gold905","lastcheckok":1},
				{"stationuuid":"g2","name":"Gold FM","url":"http://radio.example/goldfm","lastcheckok":1}
			]`)
		default:
			io.WriteString(w, `[]`)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{HTTP: srv.Client(), ipHTTP: srv.Client(), mirrors: []string{srv.URL + "/json"}}

	got, err := c.SearchSmart(context.Background(), SearchOpts{Name: "Gold 90.5", Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Name != "Gold 905 Radio" {
		names := []string{}
		for _, s := range got {
			names = append(names, s.Name)
		}
		t.Fatalf("want \"Gold 905 Radio\" first, got %v", names)
	}
	for _, s := range got {
		if s.Name == "Gold FM" {
			t.Errorf("\"Gold FM\" does not match \"Gold 90.5\" and must be filtered out")
		}
	}
	found := false
	for _, s := range got {
		if s.Name == "Golden Oldies" {
			found = true
		}
	}
	if !found {
		t.Errorf("the tag hits must still be listed after the name match")
	}
}
