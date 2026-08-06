// Status endpoint, region, index page and peer endpoints.

package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// countryToLanguage returns the default language code for the radio-browser
// "language" filter field from an ISO 3166-1 country code. Fallback is
// "english" if unknown — the world understands that.
var countryToLanguage = map[string]string{
	"DE": "german", "AT": "german", "CH": "german", "LI": "german",
	"GB": "english", "US": "english", "IE": "english", "AU": "english",
	"NZ": "english", "CA": "english", "ZA": "english",
	"FR": "french", "BE": "french", "LU": "french", "MC": "french",
	"IT": "italian", "SM": "italian", "VA": "italian",
	"ES": "spanish", "MX": "spanish", "AR": "spanish", "CO": "spanish",
	"CL": "spanish", "PE": "spanish", "VE": "spanish",
	"PT": "portuguese", "BR": "portuguese",
	"NL": "dutch", "SR": "dutch",
	"DK": "danish", "SE": "swedish", "NO": "norwegian", "FI": "finnish",
	"IS": "icelandic",
	"PL": "polish", "CZ": "czech", "SK": "slovak", "HU": "hungarian",
	"RO": "romanian", "BG": "bulgarian", "HR": "croatian", "SI": "slovenian",
	"GR": "greek", "TR": "turkish",
	"RU": "russian", "UA": "ukrainian", "BY": "belarusian",
	"JP": "japanese", "CN": "chinese", "TW": "chinese", "HK": "chinese",
	"KR": "korean", "IN": "hindi", "ID": "indonesian", "TH": "thai",
	"VN": "vietnamese", "PH": "tagalog", "MY": "malay",
	"IL": "hebrew", "AE": "arabic", "SA": "arabic", "EG": "arabic", "MA": "arabic",
}

func languageForCountry(cc string) string {
	if cc == "" {
		return "english"
	}
	if l, ok := countryToLanguage[strings.ToUpper(cc)]; ok {
		return l
	}
	return "english"
}

// handleRegion returns the region saved by the setup wizard together
// with the derived default language, or sets it anew via PUT.
func (s *Server) handleRegion(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.regionMu.RLock()
		cc := s.region
		s.regionMu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]string{
			"country":  cc,
			"language": languageForCountry(cc),
		})
	case http.MethodPut:
		var req struct {
			Country string `json:"country"`
		}
		if !decodeJSONRequest(w, r, 256, &req) {
			return
		}
		cc := strings.ToUpper(strings.TrimSpace(req.Country))
		if len(cc) != 2 {
			http.Error(w, "country must be ISO 3166-1 alpha-2", http.StatusBadRequest)
			return
		}
		s.regionMu.Lock()
		s.region = cc
		path := s.regionFile
		s.regionMu.Unlock()
		if path != "" {
			if err := os.WriteFile(path, []byte(cc+"\n"), 0o644); err != nil {
				s.logger.Warn("region.txt write failed", "err", err)
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"country":  cc,
			"language": languageForCountry(cc),
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// agentVersion and agentBuild are supplied via setters from main.go.
var (
	agentVersion = func() string { return "1.0.0" }
	agentBuild   = func() string { return "dev" }
)

// SetAgentVersion allows main.go to set the semver version at startup.
func SetAgentVersion(v string) { agentVersion = func() string { return v } }

// SetAgentBuild sets the build stamp (date/commit) as additional info.
func SetAgentBuild(b string) { agentBuild = func() string { return b } }

// debugSections holds extra named providers merged into the /api/debug/state
// JSON. main.go registers agent-side forensics here (the marge request trail,
// the boot clock verdict) without webui needing to know their types; each fn is
// called fresh per request. Guarded: registrations race the first HTTP request.
var (
	debugSectionsMu sync.Mutex
	debugSections   = map[string]func() any{}
)

// RegisterDebugSection adds (or replaces) a named section in /api/debug/state.
// Keys collide with the built-in state map last-write-wins; pick prefixed names
// (e.g. "marge_recent_requests") that cannot shadow a built-in.
func RegisterDebugSection(key string, fn func() any) {
	debugSectionsMu.Lock()
	defer debugSectionsMu.Unlock()
	debugSections[key] = fn
}

// handleStatus proxies the box's now_playing XML, with a short
// micro-cache (statusCacheTTL) in front. Multiple or too-rapidly polling
// clients thus share the same box roundtrip instead of hitting the
// fragile BoseApp (:8090) anew on every request.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if s.boxHost == "" {
		http.Error(w, "box host not configured", http.StatusServiceUnavailable)
		return
	}

	// Serve from cache if a recent good body exists.
	s.statusMu.Lock()
	if s.statusBody != nil && time.Since(s.statusAt) < statusCacheTTL {
		body, code := s.statusBody, s.statusCode
		s.statusMu.Unlock()
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(code)
		_, _ = w.Write(body)
		return
	}
	s.statusMu.Unlock()

	// A bounded client, NOT http.Get: a BoseApp that accepts the connection
	// but never answers (the documented Portable freeze) would otherwise hang
	// every /api/status poll forever.
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get(fmt.Sprintf("http://%s:8090/now_playing", s.boxHost))
	if err != nil {
		// Fall back to the last cached body on a box error so a brief BoseApp
		// hiccup does not blank the now-playing display. The body must stay
		// the box's own XML (clients regex-parse it), so once the outage is no
		// longer brief the staleness is signalled OUT OF BAND: response
		// headers carry the age, and one WARN marks the transition. Without
		// this, a box whose BoseApp died kept showing hours-old "playing"
		// state on every client with nothing in the log.
		s.statusMu.Lock()
		body, code, have := s.statusBody, s.statusCode, s.statusBody != nil
		age := time.Since(s.statusAt)
		stale := have && age >= statusStaleAfter
		warn := stale && !s.statusStaleWarned
		if warn {
			s.statusStaleWarned = true
		}
		s.statusMu.Unlock()
		if have {
			if warn {
				s.logger.Warn("box now_playing unreachable; /api/status keeps serving the last cached body, now marked stale",
					"ageSec", int(age.Seconds()), "err", err)
			}
			w.Header().Set("X-STR-Status-Age", strconv.Itoa(int(age.Seconds())))
			if stale {
				w.Header().Set("X-STR-Status-Stale", "1")
			}
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(code)
			_, _ = w.Write(body)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	// Cache only successful bodies; an error status is served through but
	// not memoised, so the next poll retries the box.
	if resp.StatusCode == http.StatusOK {
		s.statusMu.Lock()
		s.statusBody = body
		s.statusCode = resp.StatusCode
		s.statusAt = time.Now()
		s.statusStaleWarned = false
		s.statusMu.Unlock()
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// ---- Helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- Index Page (minimal HTML for direct browser use) ----

// remoteDisplayName is the speaker's friendly name for the phone remote's
// identity (page title, iOS home-screen label, PWA manifest). Empty when the
// box has not told us a name (yet).
func (s *Server) remoteDisplayName() string {
	if s.boxNameFn == nil {
		return ""
	}
	name, _ := s.boxNameFn()
	return strings.TrimSpace(name)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := indexHTML
	// Stamp the speaker's name into the page identity so "Add to Home Screen"
	// saves one distinguishable app PER SPEAKER: iOS takes its label from the
	// apple-mobile-web-app-title meta (and the title tag) at save time; each
	// speaker is its own origin, so the phone keeps them apart as separate
	// apps. The generic "ST Reborn" is only the fallback for a box whose name
	// is not known yet.
	if name := s.remoteDisplayName(); name != "" {
		esc := html.EscapeString(name)
		page = strings.Replace(page, "<title>ST Reborn</title>", "<title>"+esc+"</title>", 1)
		page = strings.Replace(page,
			`<meta name="apple-mobile-web-app-title" content="ST Reborn">`,
			`<meta name="apple-mobile-web-app-title" content="`+esc+`">`, 1)
	}
	// The page carried NO caching instruction at all: no Cache-Control, no
	// ETag, no Last-Modified. A browser then applies its own heuristic and may
	// hold the page for a long time, so an agent update that changes the remote
	// stays invisible until someone knows to force a reload. Caught live on
	// 2026-08-06: the box was already serving a corrected page while the
	// browser kept showing the old one. On a page saved to the home screen that
	// is worse, because there is no reload button to reach for.
	//
	// no-cache, not no-store: the browser must revalidate every time, but a
	// speaker that has not changed answers 304 with no body, so the common case
	// stays as cheap as a cache hit. The tag covers the name substitution above,
	// since two speakers serve different bytes from the same build.
	etag := indexETag(page)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = fmt.Fprint(w, page)
}

// indexETag is a strong validator for the exact bytes served. Hashing a ~110 KB
// string per request is cheap next to the transfer it can save, and it keeps
// the tag honest when the page is rewritten per speaker.
func indexETag(page string) string {
	sum := sha256.Sum256([]byte(page))
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// handlePeers lists the other STR speakers on the LAN so the page can offer
// links to hop between them. Returns [] when no resolver is wired.
func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	if s.peersFn == nil {
		writeJSON(w, http.StatusOK, []PeerLink{})
		return
	}
	peers := s.peersFn(r.Context())
	if peers == nil {
		peers = []PeerLink{}
	}
	writeJSON(w, http.StatusOK, peers)
}

// handlePeerSeed accepts the desktop app's known-speaker list (POST, JSON
// array of PeerSeed) and merges it into the agent's peer store, so speakers
// the local mDNS never saw still show up in the on-box picker. Body capped;
// list capped to keep a stray client from ballooning the NAND store.
func (s *Server) handlePeerSeed(w http.ResponseWriter, r *http.Request) {
	if s.peerSeedFn == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var seeds []PeerSeed
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&seeds); err != nil {
		http.Error(w, "bad seed list", http.StatusBadRequest)
		return
	}
	if len(seeds) > 32 {
		seeds = seeds[:32]
	}
	s.peerSeedFn(seeds)
	w.WriteHeader(http.StatusNoContent)
}

// handlePeerForget removes one entry from the sticky picker (POST, JSON
// {"host":"..."}). 204 when removed, 404 when the host was not listed.
func (s *Server) handlePeerForget(w http.ResponseWriter, r *http.Request) {
	if s.peerForgetFn == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Host string `json:"host"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil || req.Host == "" {
		http.Error(w, "bad forget request", http.StatusBadRequest)
		return
	}
	if !s.peerForgetFn(req.Host) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleManifest serves the PWA manifest so a phone can install the controller
// page as a standalone home-screen app. The app name is the SPEAKER's name, so
// a user with several speakers saves several distinguishable apps (one per
// origin); the generic branding is only the fallback while the box name is
// unknown. Cache short: a rename should reach the next home-screen save.
func (s *Server) handleManifest(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	name, short := "ST Reborn", "STR"
	if n := s.remoteDisplayName(); n != "" {
		name = n
		// short_name shows under the icon; Android truncates around 12-15
		// characters itself, but a deliberate cap keeps the label readable.
		short = n
		if r := []rune(short); len(r) > 14 {
			short = string(r[:14])
		}
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":             name,
		"short_name":       short,
		"description":      "Control your Bose SoundTouch speaker",
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"orientation":      "portrait",
		"background_color": "#1a1a1a",
		"theme_color":      "#1a1a1a",
		"icons": []map[string]string{
			{"src": "/icon.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "/icon.png", "sizes": "192x192", "type": "image/png", "purpose": "maskable"},
			// A >= 512px icon is required for Android to offer an installable
			// home-screen app rather than only a bookmark; the 1024x1024 covers it.
			{"src": "/icon-large.png", "sizes": "1024x1024", "type": "image/png", "purpose": "any"},
			{"src": "/icon-large.png", "sizes": "1024x1024", "type": "image/png", "purpose": "maskable"},
		},
	})
}

// handleIcon serves the embedded STR app icon (favicon, iOS apple-touch-icon and
// the PWA manifest icon). Tiny (a few KB) and cached hard to spare the NAND-bound
// box repeat reads.
func (s *Server) handleIcon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	_, _ = w.Write(iconPNG)
}

// handleIconLarge serves the 1024x1024 icon for the PWA manifest's >= 512px slot.
func (s *Server) handleIconLarge(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	_, _ = w.Write(iconLargePNG)
}
