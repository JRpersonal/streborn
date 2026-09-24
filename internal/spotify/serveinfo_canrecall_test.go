package spotify

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
)

// /spotify/info has to say whether a Spotify preset key on this speaker could
// play at all, because the apps cannot work it out from anything else they are
// given. premiumRequired is not that answer: it is deliberately conservative
// and reports false on "unknown", and "unknown" is exactly a speaker that was
// never picked in Spotify, where the key is equally unusable. The notice that
// told a reporter twice to press such a key had nothing better to read (#973).
func TestSpotifyInfoSaysWhetherAKeyCouldPlay(t *testing.T) {
	m, _, cleanup := mockLibrespot(t)
	defer cleanup()

	w := httptest.NewRecorder()
	m.ServeInfo(w, httptest.NewRequest("GET", "/spotify/info", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, w.Body.String())
	}
	if _, ok := got["canRecall"]; !ok {
		t.Fatal("canRecall missing: the app then cannot tell a speaker with no Spotify login from one that can recall")
	}
	// The mock answers /status with a username, so there is a live session and
	// this speaker genuinely could recall.
	if got["canRecall"] != true {
		t.Errorf("canRecall = %v on a speaker with a live session, want true", got["canRecall"])
	}
	// It must agree with the predicate the recall gate itself uses, or the app
	// would promise something the box then refuses.
	if want := m.CanRecall(t.Context()); got["canRecall"] != want {
		t.Errorf("canRecall = %v but CanRecall() = %v; the two must not diverge", got["canRecall"], want)
	}
}

// A speaker that has never been picked in Spotify holds no session and no
// credential. It reports premiumRequired=false (nothing positive to judge), so
// that flag alone would let the notice through; canRecall is what stops it.
func TestSpotifyInfoOnASpeakerThatWasNeverPickedInSpotify(t *testing.T) {
	// No mock engine at all: every probe fails, which is what an agent with no
	// live go-librespot session sees.
	m := New("", t.TempDir(), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.apiAddr = "127.0.0.1:1" // nothing listens

	w := httptest.NewRecorder()
	m.ServeInfo(w, httptest.NewRequest("GET", "/spotify/info", nil))
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if got["canRecall"] != false {
		t.Errorf("canRecall = %v with no session and no credential, want false", got["canRecall"])
	}
	if got["premiumRequired"] != false {
		t.Errorf("premiumRequired = %v, want false; this is precisely why it cannot be the gate on its own", got["premiumRequired"])
	}
}
