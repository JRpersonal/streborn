package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The phone remote's share card is drawn from /share.json, which is generated
// from the website registry (docs/social-share.md). These tests pin the parts a
// markup or data change could silently break.

func TestShareJSONServesEveryRemoteLanguage(t *testing.T) {
	page := readIndexHTML(t)
	langs := regexp.MustCompile(`(?m)^ {2}([a-z]{2}(?:-[A-Za-z]+)?):\{langAuto:`).FindAllStringSubmatch(page, -1)
	if len(langs) == 0 {
		t.Fatal("no remote languages found in index.html")
	}
	for _, m := range langs {
		if _, ok := shareByLang[m[1]]; !ok {
			t.Errorf("share.json has no buttons for remote language %q; run make share-targets", m[1])
		}
	}
}

func TestHandleShareAnswersPerLanguage(t *testing.T) {
	s := &Server{}
	get := func(q string) (int, map[string]json.RawMessage) {
		rec := httptest.NewRecorder()
		s.handleShare(rec, httptest.NewRequest(http.MethodGet, "/share.json"+q, nil))
		var body map[string]json.RawMessage
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s: body is not JSON: %v", q, err)
			}
		}
		return rec.Code, body
	}
	code, de := get("?lang=de")
	if code != http.StatusOK || de["icons"] == nil || de["lang"] == nil {
		t.Fatalf("?lang=de: code %d, keys %v", code, de)
	}
	if !strings.Contains(string(de["lang"]), "Auf Reddit teilen") {
		t.Error("German answer does not carry the German labels")
	}
	// Reddit stays text-free and English from the German remote too.
	if strings.Contains(string(de["lang"]), "reddit.com/submit?url=https%3A%2F%2Fst-reborn.de%2Fde") ||
		strings.Contains(string(de["lang"]), "reddit.com/submit?url=https%3A%2F%2Fst-reborn.de%2F&title=SoundTouch%20Reborn%3A%20Bose%20SoundTouch%20Lautsprecher") {
		t.Error("Reddit link from the German remote is not the English page and title")
	}
	code, unknown := get("?lang=xx")
	if code != http.StatusOK || !strings.Contains(string(unknown["lang"]), "Share on Reddit") {
		t.Errorf("unknown language should fall back to English, got code %d", code)
	}
}

// No share target may be written into the page itself: the only list is the
// registry behind share.json.
func TestRemoteHasNoShareTargetList(t *testing.T) {
	page := readIndexHTML(t)
	intents := regexp.MustCompile(`wa\.me/|sharer\.php|bsky\.app/intent|t\.me/share|reddit\.com/submit|share-offsite`)
	if m := intents.FindString(page); m != "" {
		t.Errorf("index.html names a share intent (%q); share targets belong in the registry", m)
	}
	if !strings.Contains(page, "fetch('/share.json?lang='") {
		t.Error("the share card no longer loads /share.json")
	}
	if tag := elementTag(t, page, "shareCard"); !strings.Contains(tag, " hidden") {
		t.Errorf("shareCard must start hidden until its buttons arrive: %s", tag)
	}
}
