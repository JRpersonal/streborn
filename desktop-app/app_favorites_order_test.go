package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPushFavoritesAtCarriesTheOrderTime pins #1519: the push names how new the
// app's own order is, so a speaker can tell it from an order set on a phone.
// The plain push of older callers sends no time at all.
func TestPushFavoritesAtCarriesTheOrderTime(t *testing.T) {
	var gotMethod, gotPath, gotQuery, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotMethod, gotPath, gotQuery, gotBody = r.Method, r.URL.Path, r.URL.RawQuery, string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	a := newTestApp()
	port := listenPort(t, srv)

	if err := a.PushFavoritesAt("127.0.0.1", port, `[{"name":"x"}]`, 1730000000000); err != nil {
		t.Fatalf("PushFavoritesAt: %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/favorites" || gotQuery != "orderAt=1730000000000" || gotBody != `[{"name":"x"}]` {
		t.Errorf("request = %s %s?%s %s", gotMethod, gotPath, gotQuery, gotBody)
	}

	if err := a.PushFavorites("127.0.0.1", port, " "); err != nil {
		t.Fatalf("PushFavorites: %v", err)
	}
	if gotQuery != "" || gotBody != "[]" {
		t.Errorf("plain push = ?%s %s, want no orderAt and an empty list", gotQuery, gotBody)
	}
}
