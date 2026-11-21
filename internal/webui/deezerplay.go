package webui

// Deezer links: play a Deezer playlist, album or track on the speaker's own
// Deezer client (docs/streaming/deezer.md, A2 content).
//
// POST /api/deezer/play  {"url":"https://www.deezer.com/en/album/123","name":"..."}
//
// The speaker plays a Deezer item from its id alone: a ContentItem with
// source DEEZER, type track, album or playlist, the id as location and the
// account the speaker lists as sourceAccount (AfterTouch #574 selected new
// content exactly this way). So a link only has to give the kind and the id,
// and the key a hold stores afterwards is an ordinary native preset
// (presets.TypeNative, the Pandora mechanism).
//
// A canonical deezer.com link is read as it is. A short share link
// (link.deezer.com, deezer.page.link, dzr.page.link) is followed by its
// redirects, at most three hops, each only to one of those hosts or to
// deezer.com: the agent never fetches an address a user typed anywhere
// else. The name comes from Deezer's public API (no key needed) when the
// request carries none; without an answer the item is named after its kind.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// DeezerLink is a Deezer item a link names.
type DeezerLink struct {
	// Kind is the ContentItem type: "track", "album" or "playlist".
	Kind string `json:"kind"`
	// ID is the item's Deezer id, the ContentItem location.
	ID string `json:"id"`
}

// deezerPathRe matches the path of a canonical link: an optional language
// segment ("en", "pt-br"), the kind, the numeric id.
var deezerPathRe = regexp.MustCompile(`^/(?:[a-z]{2}(?:-[a-z]{2})?/)?(track|album|playlist)/([0-9]{1,20})/?$`)

// deezerShortHosts are the share-link hosts whose redirect is followed.
var deezerShortHosts = map[string]bool{
	"link.deezer.com":   true,
	"deezer.page.link":  true,
	"dzr.page.link":     true,
	"www.deezer.com":    true,
	"deezer.com":        true,
	"deezer.app.link":   true,
	"deezer.onelink.me": true,
}

// ParseDeezerLink reads a canonical deezer.com link (any language segment,
// any query). ok is false for anything else, a short link included.
func ParseDeezerLink(raw string) (DeezerLink, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return DeezerLink{}, false
	}
	host := strings.ToLower(u.Hostname())
	if host != "www.deezer.com" && host != "deezer.com" {
		return DeezerLink{}, false
	}
	m := deezerPathRe.FindStringSubmatch(strings.ToLower(u.Path))
	if m == nil {
		return DeezerLink{}, false
	}
	return DeezerLink{Kind: m[1], ID: m[2]}, true
}

// isDeezerShortLink reports whether raw is a share link to follow.
func isDeezerShortLink(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return deezerShortHosts[host] && host != "www.deezer.com" && host != "deezer.com"
}

// errDeezerLinkUnresolved means a share link did not lead to a Deezer item.
var errDeezerLinkUnresolved = errors.New("the share link did not lead to a Deezer playlist, album or track")

func (s *Server) deezerClient() *http.Client {
	if s.deezerHTTP != nil {
		return s.deezerHTTP
	}
	return &http.Client{Timeout: 6 * time.Second}
}

// resolveDeezerLink returns the item a canonical or share link names.
func (s *Server) resolveDeezerLink(ctx context.Context, raw string) (DeezerLink, error) {
	if l, ok := ParseDeezerLink(raw); ok {
		return l, nil
	}
	if !isDeezerShortLink(raw) {
		return DeezerLink{}, errDeezerLinkUnresolved
	}
	base := s.deezerClient()
	c := &http.Client{
		Transport: base.Transport,
		Timeout:   base.Timeout,
		// Every hop is checked here, never followed blindly.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	next := strings.TrimSpace(raw)
	for hop := 0; hop < 3; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return DeezerLink{}, errDeezerLinkUnresolved
		}
		resp, err := c.Do(req)
		if err != nil {
			return DeezerLink{}, err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			return DeezerLink{}, errDeezerLinkUnresolved
		}
		target, err := req.URL.Parse(loc)
		if err != nil {
			return DeezerLink{}, errDeezerLinkUnresolved
		}
		if l, ok := ParseDeezerLink(target.String()); ok {
			return l, nil
		}
		if target.Scheme != "https" || !deezerShortHosts[strings.ToLower(target.Hostname())] {
			return DeezerLink{}, errDeezerLinkUnresolved
		}
		next = target.String()
	}
	return DeezerLink{}, errDeezerLinkUnresolved
}

// deezerAPIBase is Deezer's public API; a variable for the tests.
var deezerAPIBase = "https://api.deezer.com"

// deezerTitle asks Deezer's public API for an item's title (and a track's
// artist), "" when it does not answer. The id is digits only (the link
// parser's rule), so the request can only ever go to this one address.
func (s *Server) deezerTitle(ctx context.Context, l DeezerLink) string {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, deezerAPIBase+"/"+l.Kind+"/"+l.ID, nil)
	if err != nil {
		return ""
	}
	resp, err := s.deezerClient().Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var doc struct {
		Title  string `json:"title"`
		Artist struct {
			Name string `json:"name"`
		} `json:"artist"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&doc) != nil {
		return ""
	}
	title := strings.TrimSpace(doc.Title)
	if title != "" && l.Kind == "track" && strings.TrimSpace(doc.Artist.Name) != "" {
		title = strings.TrimSpace(doc.Artist.Name) + " - " + title
	}
	return title
}

// deezerFallbackName names an item the API did not name.
func deezerFallbackName(l DeezerLink) string {
	switch l.Kind {
	case "album":
		return "Deezer album"
	case "track":
		return "Deezer track"
	}
	return "Deezer playlist"
}

func (s *Server) handleDeezerPlay(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "music service settings only allowed from LAN", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if !decodeNativeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}
	if ref := s.pairPlayRefusal(); ref != nil {
		writePlayRefusal(w, ref)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	link, err := s.resolveDeezerLink(ctx, req.URL)
	if err != nil {
		s.logger.Info("deezer: link not resolved", "err", err)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "This is not a Deezer playlist, album or track link. Open it in a browser and copy the address from the address bar.",
			"code":  "deezer-link-unresolved",
		})
		return
	}
	srcs, err := s.nativeBox().GetSources(ctx)
	if err != nil {
		s.logger.Warn("deezer: could not read the speaker's sources before a play", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "The speaker's source list could not be read (" + speakerErrorReason(err) + ").",
			"code":  "sources-unreadable",
		})
		return
	}
	src := pickDeezerSource(srcs)
	if !src.HasAccount {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "No Deezer account is linked to this speaker.",
			"code":  "deezer-not-linked",
		})
		return
	}
	if src.Status != "READY" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "The speaker lists Deezer as " + src.Status + ": its Deezer login does not work right now.",
			"code":  nativeServiceUnavailableCode,
		})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = s.deezerTitle(ctx, link)
	}
	if name == "" {
		name = deezerFallbackName(link)
	}
	item := presets.NativeItem{Source: deezerSource, SourceAccount: src.account, Location: link.ID, ItemType: link.Kind}
	if err := s.nativeBox().SelectContentItem(ctx, nativeContentItemXML(item, name)); err != nil {
		s.logger.Warn("deezer: the speaker refused the item", "kind", link.Kind, "id", link.ID,
			"err", maskAccountIn(err.Error(), src.account))
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "The speaker did not start it (" + speakerErrorReason(err) + ").",
			"code":  "select-failed",
		})
		return
	}
	s.stopQueueIfActive("a Deezer item was started instead")
	s.logger.Info("deezer: item handed to the speaker", "kind", link.Kind, "id", link.ID, "name", name,
		"from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "kind": link.Kind, "id": link.ID, "name": name})
}
