package webui

// The phone drives "update all speakers" (selfupdate_fleet.go).
//
// The first version let the speaker the phone talks to drive the run. On real
// hardware that speaker often cannot reach its peers: ST10s on the rhino
// chassis are isolated from each other ("no route to host" box to box), while
// the phone in the owner's hand reaches every one of them. So the phone page
// now walks the run itself and talks to each peer's /api/agent/self-update
// directly. Two things on the agent make that possible and keep it safe:
//
//   - CORS on exactly one endpoint. The page is served by speaker A and calls
//     speaker B, a cross-origin request. B answers it only for an Origin that
//     is itself a plain LAN address (a page served by another speaker), only
//     for /api/agent/self-update, only for the methods and the one header that
//     endpoint needs. No wildcard, no public origin, no "null". The caller's
//     own address still has to pass the LAN gate, exactly as before.
//   - A run token. The phone claims the run on the speaker that serves the page
//     before it starts, renews it while it walks, and passes it on the install
//     of that speaker at the very end. A second phone cannot claim a run that
//     is going, and an install without the token is turned away (409), so two
//     drivers never reboot the same speaker in each other's run.
//
// When the phone cannot reach a peer (an older agent without CORS, a browser
// that refuses), it hands that one peer to the speaker-driven path: the same
// install-all action, narrowed to the given peers and without the speaker's
// own update.

import (
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// selfUpdateAPIPath is the one path that answers cross-origin requests from
// another speaker's page.
const selfUpdateAPIPath = "/api/agent/self-update"

// selfUpdateOriginAllowed reports whether a page at origin may call this
// speaker's self-update endpoint from the browser: an http origin whose host
// is a LAN address (private, or on one of this speaker's own subnets, the same
// rule the caller gate applies) or a bare local name (single label or .local).
// Exactly scheme://host[:port], nothing else. Never loopback, never "null",
// never a name with dots in it that a public DNS could answer.
func selfUpdateOriginAllowed(origin string) bool {
	if origin == "" || origin == "null" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Opaque != "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	// The serialised origin never carries a trailing slash; refuse anything
	// that is not exactly what a browser sends.
	if strings.HasSuffix(origin, "/") {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if p := u.Port(); p != "" {
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
			return false
		}
		return isLANPeer(host) || sameSubnetAsAnInterface(ip)
	}
	return bareLocalHost(host)
}

var bareHostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// bareLocalHost accepts a single-label name ("kitchen") or an mDNS name
// ("kitchen.local"). Both resolve only on the local network.
func bareLocalHost(host string) bool {
	h := strings.ToLower(host)
	h = strings.TrimSuffix(h, ".local")
	if h == "localhost" || strings.Contains(h, ".") {
		return false
	}
	return bareHostLabel.MatchString(h)
}

// selfUpdateCORS answers the cross-origin side of /api/agent/self-update. It
// runs in corsMiddleware for that path only. True when it answered the
// request itself (a preflight).
func selfUpdateCORS(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" && allowedCORSOrigins[origin] {
		return false // the desktop app keeps its general grant
	}
	ok := selfUpdateOriginAllowed(origin) && isLocalLAN(r.RemoteAddr)
	if ok {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Add("Vary", "Origin")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type")
		h.Set("Access-Control-Max-Age", "600")
	}
	if r.Method != http.MethodOptions {
		return false
	}
	if !ok {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return true
	}
	// Chrome's Private Network Access asks before a page reaches a private
	// address; the answer is yes for exactly the origins above.
	if strings.EqualFold(r.Header.Get("Access-Control-Request-Private-Network"), "true") {
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}

// selfUpdateForeignOrigin reports a browser request from a page this endpoint
// does not serve: neither the speaker's own page, nor another speaker's page,
// nor the desktop app. Such a request is refused before it changes anything,
// because a missing CORS header only stops the page from READING the answer,
// not the speaker from acting on a simple POST.
func selfUpdateForeignOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false // not a browser cross-origin request (curl, Go, desktop)
	}
	if origin == "http://"+r.Host || origin == "https://"+r.Host {
		return false
	}
	return !allowedCORSOrigins[origin] && !selfUpdateOriginAllowed(origin)
}

// phoneRunTTL is how long a claimed phone run holds without a renewal. Longer
// than one speaker's longest wait, so a phone that sat locked through one
// speaker's update still finds its run on return.
const phoneRunTTL = 20 * time.Minute

var phoneRunTokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// phoneRun is the claim of the phone that drives a run.
type phoneRun struct {
	mu    sync.Mutex
	token string
	since time.Time
	until time.Time
}

// phoneRunView is what the status shows of it: that a run is going, never the
// token.
type phoneRunView struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}

func (p *phoneRun) activeLocked(now time.Time) bool {
	return p.token != "" && now.Before(p.until)
}

// claim takes or renews the run for token. False when another phone holds it.
func (p *phoneRun) claim(token string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeLocked(now) && p.token != token {
		return false
	}
	if p.token != token || !p.activeLocked(now) {
		p.since = now
	}
	p.token, p.until = token, now.Add(phoneRunTTL)
	return true
}

// release ends the run if token holds it.
func (p *phoneRun) release(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token == token {
		p.token = ""
	}
}

// heldByOther reports a live run claimed by somebody other than token.
func (p *phoneRun) heldByOther(token string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activeLocked(now) && p.token != token
}

func (p *phoneRun) view(now time.Time) *phoneRunView {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.activeLocked(now) {
		return nil
	}
	return &phoneRunView{Since: p.since, Until: p.until}
}
