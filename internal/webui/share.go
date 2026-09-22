package webui

import (
	_ "embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// shareJSON is the phone remote's "Recommend STR" buttons, already resolved per
// remote language. It is generated from the website's share registry and the
// desktop app's texts by desktop-app/frontend/scripts/sync-share-targets.mjs
// (`make share-targets`), so the remote has no list of share targets of its
// own. See docs/social-share.md.
//
//go:embed assets/share.json
var shareJSON []byte

// shareByLang holds one ready response per language: the glyphs plus that
// language's texts and groups. The page asks only when the user opens the
// share card, so the remote itself stays as small as it was.
var shareByLang = splitShareData(shareJSON)

func splitShareData(raw []byte) map[string][]byte {
	var data struct {
		Icons json.RawMessage            `json:"icons"`
		Langs map[string]json.RawMessage `json:"langs"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		slog.Warn("webui: embedded share.json unreadable, share card disabled", "err", err)
		return nil
	}
	out := make(map[string][]byte, len(data.Langs))
	for lang, l := range data.Langs {
		b, err := json.Marshal(struct {
			Icons json.RawMessage `json:"icons"`
			Lang  json.RawMessage `json:"lang"`
		}{data.Icons, l})
		if err != nil {
			continue
		}
		out[lang] = b
	}
	return out
}

// handleShare answers GET /share.json?lang=xx with that language's buttons,
// English when the language is unknown.
func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	body, ok := shareByLang[strings.ToLower(r.URL.Query().Get("lang"))]
	if !ok {
		body, ok = shareByLang["en"]
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	etag := indexETag(string(body))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(body)
}
