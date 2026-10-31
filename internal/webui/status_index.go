// Status endpoint, region, index page and peer endpoints.

package webui

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/region"
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
	"XK": "albanian",
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

// regionInfo is the effective region: the resolver when wired (the wizard
// region, else the firmware's countryCode), otherwise the wizard region alone.
func (s *Server) regionInfo() region.Info {
	if s.regionRes != nil {
		return s.regionRes.Info()
	}
	s.regionMu.RLock()
	cc := s.region
	s.regionMu.RUnlock()
	return region.Resolve(cc, "")
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
		// The US music services follow the region (internal/region).
		s.regionRes.SetSTR(cc)
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

// mdnsHostName is the name this speaker answers address queries for, set at
// startup once the responder is actually running. Empty until then, and empty
// forever on a box whose responder could not start, which is the only correct
// answer there: a name nothing answers for is worse than no name.
var mdnsHostName string

// SetMDNSHostName records the name from internal/mdnshost so the desktop app can
// read it back off /api/agent/version.
//
// The app needs it to put a NAME on the phone-remote QR code instead of an
// address, because an address is the thing that changes: a new DHCP lease and
// the page somebody has on their home screen points at nothing. The app cannot
// derive the name itself (mdnshost is under internal/, and it is a separate
// module), and a second copy of that derivation is exactly the drift this
// codebase has paid for elsewhere. So the speaker says it.
func SetMDNSHostName(n string) {
	mdnsHostNameMu.Lock()
	mdnsHostName = n
	mdnsHostNameMu.Unlock()
}

// mdnsHostNameMu guards mdnsHostName: a wired speaker sets it once its first
// lease arrives, after the HTTP surface is already serving.
var mdnsHostNameMu sync.Mutex

func currentMDNSHostName() string {
	mdnsHostNameMu.Lock()
	defer mdnsHostNameMu.Unlock()
	return mdnsHostName
}

// agentIdentityFn reports the ids this speaker answers under: the deviceID its
// own announcement carries, and the firmware's SoundTouch id once the box has
// answered /info. Nil until main.go wires it.
var agentIdentityFn func() (deviceID, boxDeviceID string)

// SetAgentIdentityFn wires the speaker's identity into /api/agent/version.
//
// The desktop app needs it to tell this speaker from another one at the same
// address. A router that hands out fresh leases on every reboot can give this
// speaker's address to another speaker while both restart after an update; an
// address-only check then confirmed the update on the wrong speaker and sent
// the Spotify engine there (field case 2026-10-26, six speakers on one mesh
// Wi-Fi). mdnsName already carries a per-speaker name, but only once the name
// responder runs; the id is known from the first answer after a boot.
func SetAgentIdentityFn(fn func() (deviceID, boxDeviceID string)) { agentIdentityFn = fn }

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
		s.setPlayingKeyHeader(w, body)
		w.WriteHeader(code)
		_, _ = w.Write(s.statusForClients(body))
		return
	}
	s.statusMu.Unlock()

	// A bounded read: a BoseApp that accepts the connection but never answers
	// (the documented Portable freeze) would otherwise hang every /api/status
	// poll forever. readBoxNow records the verdict in the outage tracker and
	// caches a 200 body; a firmware error reply comes back as a BoxError and
	// is served through below, uncached, so the next poll retries the box.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	np, err := s.readBoxNow(ctx)
	var boxErr *boxapi.BoxError
	if errors.As(err, &boxErr) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		s.setPlayingKeyHeader(w, []byte(boxErr.Body))
		w.WriteHeader(boxErr.Status)
		_, _ = w.Write(s.statusForClients([]byte(boxErr.Body)))
		return
	}
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
			s.setPlayingKeyHeader(w, body)
			w.WriteHeader(code)
			_, _ = w.Write(s.statusForClients(body))
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	body := np.Raw
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	s.setPlayingKeyHeader(w, body)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.statusForClients(body))
}

// statusForClients is the now_playing body as /api/status serves it: the
// speaker's own XML with the station name STR started (withStationName) and a
// quiet wake's leftover pause reported as stopped (wokenStop, #1629).
func (s *Server) statusForClients(body []byte) []byte {
	return s.observeWokenStop(s.withStationName(body))
}

// withStationName puts the station STR started back into a now_playing body
// whose station name the speaker no longer reports (#1170).
//
// "Show the current track on the display" works by re-sending the stream with
// the song as its title (setDisplayText), and from then on the speaker reports
// that song as itemName and leaves stationName empty. Both apps took itemName
// as the station, so the phone showed the song as the big title and hid the
// song line as a duplicate. The agent still knows what it started, in
// lastPlay, so when the speaker is playing exactly that stream and has no
// station name of its own, the empty <stationName> gets lastPlay's title. The
// body stays the speaker's own XML otherwise: clients regex-parse it.
func (s *Server) withStationName(body []byte) []byte {
	var url, title string
	if lp := s.play.current(); lp != nil {
		url, title = lp.boxURL, lp.title
	}
	return annotateStationName(body, url, title)
}

var emptyStationNameRe = regexp.MustCompile(`<stationName\s*/>|<stationName>\s*</stationName>`)

func annotateStationName(body []byte, boxURL, title string) []byte {
	if boxURL == "" || strings.TrimSpace(title) == "" {
		return body
	}
	m := nowPlayingLocationRe.FindSubmatch(body)
	if m == nil || html.UnescapeString(string(m[1])) != boxURL {
		return body
	}
	loc := emptyStationNameRe.FindIndex(body)
	if loc == nil {
		return body
	}
	var esc strings.Builder
	_ = xml.EscapeText(&esc, []byte(title))
	out := make([]byte, 0, len(body)+len(title)+32)
	out = append(out, body[:loc[0]]...)
	out = append(out, "<stationName>"+esc.String()+"</stationName>"...)
	return append(out, body[loc[1]:]...)
}

// boseappOutage is one closed episode of the firmware webserver (:8090) not
// answering, as observed by the /api/status fetch path.
type boseappOutage struct {
	Start   string `json:"start"`
	End     string `json:"end"`
	Seconds int    `json:"seconds"`
}

// boseappOutageKeep bounds the episode ring; the freeze family produces a
// handful of long episodes, not thousands, so a short ring is plenty.
const boseappOutageKeep = 10

// noteBoseAppFetch feeds the :8090 outage tracker with one fetch verdict.
func (s *Server) noteBoseAppFetch(ok bool) {
	now := time.Now()
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if ok {
		if !s.boseappDownSince.IsZero() {
			s.boseappOutages = append(s.boseappOutages, boseappOutage{
				Start:   s.boseappDownSince.Format(time.RFC3339),
				End:     now.Format(time.RFC3339),
				Seconds: int(now.Sub(s.boseappDownSince).Seconds()),
			})
			if len(s.boseappOutages) > boseappOutageKeep {
				s.boseappOutages = s.boseappOutages[len(s.boseappOutages)-boseappOutageKeep:]
			}
			s.boseappDownSince = time.Time{}
		}
		return
	}
	if s.boseappDownSince.IsZero() {
		s.boseappDownSince = now
	}
}

// BoseAppHealth is the /api/debug/state section with the :8090 outage record:
// whether the firmware webserver is answering right now, for how long it has
// been dead if not, and the recent closed outage episodes. Observation is
// demand-driven (the app's status poll), so gaps with no polling show no data
// rather than false uptime.
func (s *Server) BoseAppHealth() any {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	out := map[string]any{
		"currentlyDown": !s.boseappDownSince.IsZero(),
		"episodes":      append([]boseappOutage(nil), s.boseappOutages...),
	}
	if !s.boseappDownSince.IsZero() {
		out["downSince"] = s.boseappDownSince.Format(time.RFC3339)
		out["downForSec"] = int(time.Since(s.boseappDownSince).Seconds())
	}
	return out
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

// indexPage is the phone remote rendered for one speaker name: the bytes in
// both encodings the page is served in, each with its own strong ETag. Built
// once per name and kept, because the page is ~425 KB and the box CPU is slow:
// hashing it on every open was measurable, compressing it on every open would
// not be affordable at all.
type indexPage struct {
	plain, gzipped    []byte
	etagPlain, etagGz string
}

// indexPageCap bounds the per-name cache. A speaker has one name and is renamed
// rarely, so a handful covers every rename an agent sees between restarts; when
// the cap is hit the whole map is dropped rather than tracking recency.
const indexPageCap = 4

// renderIndexPage stamps the speaker's name into the page identity so "Add to
// Home Screen" saves one distinguishable app PER SPEAKER: iOS takes its label
// from the apple-mobile-web-app-title meta (and the title tag) at save time;
// each speaker is its own origin, so the phone keeps them apart as separate
// apps. The generic "ST Reborn" is only the fallback for a box whose name is
// not known yet.
func renderIndexPage(name string) *indexPage {
	page := indexHTML
	if name != "" {
		esc := html.EscapeString(name)
		page = strings.Replace(page, "<title>ST Reborn</title>", "<title>"+esc+"</title>", 1)
		page = strings.Replace(page,
			`<meta name="apple-mobile-web-app-title" content="ST Reborn">`,
			`<meta name="apple-mobile-web-app-title" content="`+esc+`">`, 1)
	}
	p := &indexPage{plain: []byte(page), etagPlain: indexETag(page)}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(p.plain)
	_ = zw.Close()
	p.gzipped = buf.Bytes()
	// A different representation gets a different strong tag (RFC 9110
	// section 8.8.3.1): a cache holding the compressed page must never have
	// its tag accepted for a client that cannot decompress it.
	p.etagGz = strings.TrimSuffix(p.etagPlain, `"`) + `-gz"`
	return p
}

// indexPageFor returns the rendered page for the speaker's current name,
// building and remembering it on first use.
func (s *Server) indexPageFor(name string) *indexPage {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	if p := s.indexPages[name]; p != nil {
		return p
	}
	if s.indexPages == nil || len(s.indexPages) >= indexPageCap {
		s.indexPages = make(map[string]*indexPage, indexPageCap)
	}
	p := renderIndexPage(name)
	s.indexPages[name] = p
	return p
}

// acceptsGzip reports whether an Accept-Encoding header lets the server send
// gzip. Every phone browser does, so the plain path is for curl and the tests.
func acceptsGzip(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		coding := strings.ToLower(strings.TrimSpace(fields[0]))
		if coding != "gzip" && coding != "*" {
			continue
		}
		for _, param := range fields[1:] {
			if q := strings.TrimSpace(param); strings.HasPrefix(q, "q=") {
				if v, err := strconv.ParseFloat(strings.TrimPrefix(q, "q="), 64); err == nil && v == 0 {
					return false
				}
			}
		}
		return true
	}
	return false
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.noteStrayBoxRequest(r)
	s.notePhoneLanguage(r.Header.Get("Accept-Language"))
	p := s.indexPageFor(s.remoteDisplayName())
	body, etag := p.plain, p.etagPlain
	gz := acceptsGzip(r.Header.Get("Accept-Encoding"))
	if gz {
		body, etag = p.gzipped, p.etagGz
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
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
	// stays as cheap as a cache hit. The tag covers the name substitution, since
	// two speakers serve different bytes from the same build, and the encoding,
	// since the compressed and the plain page are different bytes too. Vary is
	// set on every answer, the 304 included, so a cache keys the page on the
	// encoding it asked for and a home-screen app never gets the other one.
	h.Set("ETag", etag)
	h.Set("Cache-Control", "no-cache")
	h.Set("Vary", "Accept-Encoding")
	if gz {
		h.Set("Content-Encoding", "gzip")
	}
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

// indexETag is a strong validator for the exact bytes served. It is computed
// once per rendered page (see indexPage), and it keeps the tag honest when the
// page is rewritten per speaker.
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
		"name":        name,
		"short_name":  short,
		"description": "Control your Bose SoundTouch speaker",
		"start_url":   "/",
		"scope":       "/",
		"display":     "standalone",
		// "any", not "portrait": the lock only binds the INSTALLED home-screen
		// app, and it locked out the one setup that needs the other rotation.
		// A user mounted a tablet on the wall in landscape, added the remote to
		// its home screen, and the page turned itself upright on a screen that
		// cannot turn with it (reported by mail, 2026-09-20). The layout does
		// not care: body is capped at 620px and centred, so a wide screen just
		// gets margins. Leaving rotation to the device is also what a phone
		// user gets from every other page they open.
		"orientation":      "any",
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

// notePhoneLanguage remembers what the browser asked for, so a diagnostic can
// answer a question that was otherwise only ever argued about: can the phone
// be trusted to say which language it wants?
//
// The remote decides its language from navigator.languages, and that value is
// flattened to en-US by browsers configured against fingerprinting. How often
// that happens to real users is not something to reason about, so the header
// the same request already carries is recorded instead. Accept-Language is
// sent without any JavaScript and is the independent second opinion.
//
// Nothing reads this to make a decision. It exists so the next report about a
// remote in the wrong language arrives with evidence attached.
func (s *Server) notePhoneLanguage(header string) {
	header = strings.TrimSpace(header)
	if header == "" {
		header = "(none sent)"
	}
	s.phoneLangMu.Lock()
	defer s.phoneLangMu.Unlock()
	if s.phoneLangSeen == nil {
		s.phoneLangSeen = map[string]int{}
	}
	// Bounded: a page reload must not turn this into a log of every visit, and
	// a household has a handful of phones, not hundreds.
	if len(s.phoneLangSeen) >= 12 {
		if _, known := s.phoneLangSeen[header]; !known {
			return
		}
	}
	s.phoneLangSeen[header]++
}

// PhoneLanguages reports what the browsers that opened the remote asked for,
// with a count each.
func (s *Server) PhoneLanguages() map[string]int {
	s.phoneLangMu.Lock()
	defer s.phoneLangMu.Unlock()
	out := make(map[string]int, len(s.phoneLangSeen))
	for k, v := range s.phoneLangSeen {
		out[k] = v
	}
	return out
}
