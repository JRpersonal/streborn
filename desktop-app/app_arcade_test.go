package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The share buttons point at the real announcement discussion, never at a
// placeholder.
func TestArcadeThreadURL(t *testing.T) {
	if !regexp.MustCompile(`^https://github\.com/JRpersonal/streborn/discussions/[0-9]+$`).MatchString(arcadeThreadURL) {
		t.Fatalf("thread %s", arcadeThreadURL)
	}
}

func arcadeServer(t *testing.T, routes map[string]string) (*App, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.RequestURI()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".png") {
			w.Header().Set("Content-Type", "image/png")
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return newTestApp(), listenPort(t, srv)
}

// Only games somebody played are listed, so the app never names a game
// before it was found; each comes with its own screenshot.
func TestGetArcadeListsPlayedGamesOnly(t *testing.T) {
	a, port := arcadeServer(t, map[string]string{
		"/api/box/arcade": `{"games":[{"id":"blockfall","title":"BLOCKFALL","best":90,"last":40,"lastRows":2,"rounds":3},` +
			`{"id":"other","title":"OTHER","rounds":0}]}`,
		"/api/box/arcade/screenshot.png?game=blockfall": "PNG",
	})
	games, err := a.GetArcade("127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 || games[0]["id"] != "blockfall" || games[0]["best"] != 90 {
		t.Fatalf("games %v", games)
	}
	if games[0]["screenshot"] != "data:image/png;base64,UE5H" {
		t.Fatalf("screenshot %v", games[0]["screenshot"])
	}
}

// An agent from v1.0.5 only answers the Blockfall endpoints.
func TestGetArcadeFallsBackToBlockfallOnlyAgent(t *testing.T) {
	a, port := arcadeServer(t, map[string]string{
		"/api/box/blockfall":                `{"best":70,"last":70,"lastRows":5,"rounds":1}`,
		"/api/box/blockfall/screenshot.png": "PNG",
	})
	games, err := a.GetArcade("127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 || games[0]["id"] != "blockfall" || games[0]["best"] != 70 || games[0]["screenshot"] == nil {
		t.Fatalf("games %v", games)
	}
	// an agent with no game at all is an empty list, not an error
	a2, port2 := arcadeServer(t, map[string]string{})
	if games, err := a2.GetArcade("127.0.0.1", port2); err != nil || len(games) != 0 {
		t.Fatalf("no games: %v %v", games, err)
	}
}
