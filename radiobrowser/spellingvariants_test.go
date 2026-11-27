package radiobrowser

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// variantServer answers like the directory does: a plain substring match on
// the name, case-insensitive, nothing cleverer.
func variantServer(t *testing.T, requests *[]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if requests != nil {
			*requests = append(*requests, q.Get("name")+"|"+q.Get("offset"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch strings.ToLower(q.Get("name")) {
		case "star fm":
			io.WriteString(w, `[
				{"stationuuid":"s1","name":"Star FM 101.9","url":"http://radio.example/gd","lastcheckok":1},
				{"stationuuid":"s2","name":"Star FM Eesti","url":"http://radio.example/ee","lastcheckok":1}
			]`)
		case "starfm":
			io.WriteString(w, `[
				{"stationuuid":"b1","name":"StarFM Berlin","url":"http://radio.example/berlin","lastcheckok":1},
				{"stationuuid":"s2","name":"Star FM Eesti","url":"http://radio.example/ee","lastcheckok":1}
			]`)
		case "berlin":
			io.WriteString(w, `[
				{"stationuuid":"x1","name":"Radio Berlin","url":"http://radio.example/rb","lastcheckok":1},
				{"stationuuid":"b1","name":"StarFM Berlin","url":"http://radio.example/berlin","lastcheckok":1}
			]`)
		default:
			io.WriteString(w, `[]`)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{HTTP: srv.Client(), ipHTTP: srv.Client(), mirrors: []string{srv.URL + "/json"}}
}

func stationNames(st []Station) []string {
	out := make([]string, 0, len(st))
	for _, s := range st {
		out = append(out, s.Name)
	}
	return out
}

// Discussion #2140: a user asked for "STAR FM Berlin" to be added to the
// search. The directory lists it as "StarFM Berlin", so the typed spelling
// found other countries' "Star FM" stations and never this one.
func TestSearchWithVariantsFindsTheWordsWrittenTogether(t *testing.T) {
	c := variantServer(t, nil)
	res, err := c.SearchWithVariants(context.Background(), SearchOpts{Name: "Star FM", Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	got := stationNames(res.Stations)
	want := []string{"StarFM Berlin", "Star FM Eesti", "Star FM 101.9"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v (other spelling first, no station twice)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// The full name as a person writes it matches no station name as a substring;
// the punctuation-blind comparison has to find it.
func TestSearchWithVariantsFindsTheFullNameSpelledApart(t *testing.T) {
	c := variantServer(t, nil)
	res, err := c.SearchWithVariants(context.Background(), SearchOpts{Name: "STAR FM Berlin", Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	got := stationNames(res.Stations)
	if len(got) != 1 || got[0] != "StarFM Berlin" {
		t.Fatalf("got %v, want only StarFM Berlin (Radio Berlin does not carry the typed words)", got)
	}
}

// One word has no other spelling, and a later page belongs to the plain name
// search alone, so neither may cost an extra request.
func TestSearchWithVariantsLeavesSingleWordsAndLaterPagesAlone(t *testing.T) {
	var requests []string
	c := variantServer(t, &requests)
	if _, err := c.SearchWithVariants(context.Background(), SearchOpts{Name: "starfm", Limit: 30}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Errorf("a single word asked the directory %d times, want 1: %v", len(requests), requests)
	}
	requests = nil
	if _, err := c.SearchWithVariants(context.Background(), SearchOpts{Name: "star fm", Limit: 30, Offset: 30}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Errorf("page two asked the directory %d times, want 1: %v", len(requests), requests)
	}
}

// The other spellings are an addition in front of the list, never the list.
func TestSpellingVariantsAreCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("name") != "topforty" {
			io.WriteString(w, `[]`)
			return
		}
		io.WriteString(w, `[`)
		for i := 0; i < 20; i++ {
			if i > 0 {
				io.WriteString(w, `,`)
			}
			io.WriteString(w, `{"stationuuid":"v`+string(rune('a'+i))+`","name":"TopForty `+string(rune('A'+i))+`","url":"http://radio.example/`+string(rune('a'+i))+`","lastcheckok":1}`)
		}
		io.WriteString(w, `]`)
	}))
	t.Cleanup(srv.Close)
	c := &Client{HTTP: srv.Client(), ipHTTP: srv.Client(), mirrors: []string{srv.URL + "/json"}}
	got := c.spellingVariants(context.Background(), SearchOpts{Name: "top forty", Limit: 30}, []string{"top", "forty"}, 5)
	if len(got) != variantLimit {
		t.Errorf("got %d other-spelling hits, want the cap of %d", len(got), variantLimit)
	}
}
