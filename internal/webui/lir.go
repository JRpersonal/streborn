package webui

// LOCAL_INTERNET_RADIO station descriptor.
//
// Bose's own LIR adapter was a stateless shim: the box fetched a small JSON
// document and played the streamUrl inside it, with no account and no login.
// That is the one native radio path whose ContentItem carries no
// sourceAccount, so it cannot fail the not-logged-in check that breaks our
// UPNP presets (1036). This endpoint serves that document for a preset slot,
// pointing at the slot's own stream-proxy URL, so a preset stored as
//
//	<ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl"
//	             location="http://127.0.0.1:8888/lir/<slot>.json" ...>
//
// can be activated by the BOX itself.
//
// Experimental: on firmware 27.0.6 the LIR source is missing from the box's
// source registry (POST /select answers 1005 UNKNOWN_SOURCE_ERROR), so this
// only pays off if the box's own preset activation takes a different path
// than /select, or on a chassis that still has the source. Harmless
// regardless: an unused read-only endpoint.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/JRpersonal/streborn/internal/boxurl"
)

// lirDescriptor is the station document shape the firmware consumes.
type lirDescriptor struct {
	Audio struct {
		HasPlaylist bool   `json:"hasPlaylist"`
		IsRealtime  bool   `json:"isRealtime"`
		StreamURL   string `json:"streamUrl"`
	} `json:"audio"`
	ImageURL   string `json:"imageUrl"`
	Name       string `json:"name"`
	StreamType string `json:"streamType"`
}

// handleLIRStation serves /lir/<slot>.json for a stored preset slot.
func (s *Server) handleLIRStation(w http.ResponseWriter, r *http.Request) {
	if s.presets == nil {
		http.Error(w, "presets store not initialized", http.StatusServiceUnavailable)
		return
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/lir/"), ".json")
	slot, err := strconv.Atoi(raw)
	if err != nil || slot < 1 || slot > 6 {
		http.Error(w, "slot 1..6 required", http.StatusBadRequest)
		return
	}
	var name string
	for _, p := range s.presets.All() {
		if p.Slot == slot {
			name = p.Name
			break
		}
	}
	if name == "" {
		http.Error(w, "slot empty", http.StatusNotFound)
		return
	}
	var d lirDescriptor
	d.Audio.IsRealtime = true
	d.Audio.StreamURL = boxurl.StreamSlot(slot)
	d.Name = name
	d.StreamType = "liveRadio"
	// The firmware fetches this over plain HTTP from the loopback proxy; keep
	// it uncached so a re-saved preset is picked up without a box restart.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	s.logger.Info("lir: station descriptor served", "slot", slot, "name", name)
	_ = json.NewEncoder(w).Encode(d)
}

// handleOrionStation serves the Bose ORION adapter path
// /core02/svc-bmx-adapter-orion/prod/orion/station?data=<base64url JSON>.
//
// This is the location shape a LOCAL_INTERNET_RADIO ContentItem must carry:
// community projects measured that a RAW stream URL in the location is
// "echoed but not played" (the box shows the name and never reaches
// PLAY_STATE) - byte for byte the symptom measured here on 2026-08-02 - while
// the ORION envelope plays. The payload is the same descriptor
// handleLIRStation returns, so both paths share one shape.
func (s *Server) handleOrionStation(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("data")
	if raw == "" {
		http.Error(w, "data parameter required", http.StatusBadRequest)
		return
	}
	// The box sends standard base64 with URL escaping; accept both alphabets
	// and tolerate missing padding.
	dec := func(s string) ([]byte, bool) {
		s = strings.TrimSpace(s)
		if b, err := base64.StdEncoding.DecodeString(s); err == nil {
			return b, true
		}
		if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
			return b, true
		}
		if b, err := base64.URLEncoding.DecodeString(s); err == nil {
			return b, true
		}
		b, err := base64.RawURLEncoding.DecodeString(s)
		return b, err == nil
	}
	payload, ok := dec(raw)
	if !ok {
		http.Error(w, "data is not base64", http.StatusBadRequest)
		return
	}
	var in struct {
		StreamURL string `json:"streamUrl"`
		Name      string `json:"name"`
		ImageURL  string `json:"imageUrl"`
	}
	if err := json.Unmarshal(payload, &in); err != nil || in.StreamURL == "" {
		http.Error(w, "data must carry a streamUrl", http.StatusBadRequest)
		return
	}
	var d lirDescriptor
	d.Audio.IsRealtime = true
	d.Audio.StreamURL = in.StreamURL
	d.ImageURL = in.ImageURL
	d.Name = in.Name
	d.StreamType = "liveRadio"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	s.logger.Info("lir: orion station resolved", "name", in.Name, "stream", in.StreamURL)
	_ = json.NewEncoder(w).Encode(d)
}

// OrionStationLocation builds the ContentItem location for a stream URL, the
// way the firmware expects it: base64 JSON in a data parameter, on a path
// RELATIVE to the BMX service baseUrl.
//
// Two things here are load-bearing and both were learned the hard way.
//
// The path must stay relative. The service entry already declares
// baseUrl ".../core02/svc-bmx-adapter-orion/prod/orion", so a location
// carrying that prefix again makes the firmware request the concatenation and
// resolve nothing.
//
// The payload uses the unpadded URL-safe alphabet. The location travels to the
// box as one positional argument of a TAP CLI line ("ws AddPreset ..."), and
// the standard alphabet's "+" and "/" plus percent-escaping made the CLI
// accept the command and store NOTHING - it reported success and left the slot
// empty, which is far worse than an error. Unpadded URL-safe base64 is
// alphanumeric plus "-" and "_", so it survives tokenisation intact.
// handleOrionStation accepts every alphabet, so this stays compatible with
// locations written by older builds.
// art is the station logo the speaker shows next to the name. Passing it
// matters: the UPnP path always carried the artwork, and leaving it empty here
// silently cost users the station logo on the speaker's display when their
// presets converted to the native form (reported on a SoundTouch 20, 2026-08-04:
// "the logo that was there before does not appear anymore"). Empty is still
// accepted, for stations that simply have no image.
func OrionStationLocation(streamURL, name, art string) string {
	payload, _ := json.Marshal(map[string]any{
		// Through the art proxy: the speaker fetches this image itself and
		// cannot do https, which is why the audio goes through a proxy too.
		"streamUrl": streamURL, "name": name,
		"imageUrl":   stationImageURL(art),
		"streamType": "liveRadio", "isRealtime": true,
	})
	return "/station?data=" + base64.RawURLEncoding.EncodeToString(payload)
}

// StationLocationCarriesStandInLogo reports whether an already-stored station
// location tells the display to draw STR's own logo rather than the station's.
//
// It exists so a slot written by an older agent can be recognised and put
// right. A preset the box already holds in native form is never rewritten,
// which is correct (a write wakes the speaker and resets its standby
// countdown, and rewriting on every boot for nothing would be a bug of its
// own). But it means a slot that lost its logo to the old extension check
// keeps STR's stand-in for good, and the owner has no way to tell why one
// station has a picture and the next one does not.
//
// Deliberately answers only this one question. Comparing whole locations would
// rewrite slots for any harmless difference and could ping-pong; "the stored
// one is our stand-in" is a condition that stops being true the moment the
// rewrite lands, so the repair happens once per slot and then never again.
func StationLocationCarriesStandInLogo(loc string) bool {
	const p = "/station?data="
	i := strings.Index(loc, p)
	if i < 0 {
		return false
	}
	raw := loc[i+len(p):]
	dec, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		if dec, err = base64.URLEncoding.DecodeString(raw); err != nil {
			return false // unreadable: leave it alone rather than guess
		}
	}
	var st struct {
		ImageURL string `json:"imageUrl"`
	}
	if err := json.Unmarshal(dec, &st); err != nil {
		return false
	}
	return strings.HasSuffix(st.ImageURL, strLogoPath)
}

// strLogoPath is the STR icon the agent already serves, 192x192 PNG over plain
// http from the speaker itself: raster, right-sized, no external fetch and no
// https the firmware cannot do.
const strLogoPath = "/icon.png"

// stationImageURL is what the speaker's display gets told to draw.
//
// Stations without a logo are common: a live payload from a Portable
// (2026-08-06) carried imageUrl:"" for 1LIVE while WDR 5 on the next slot had
// its own icon. An empty value leaves the display blank next to the station
// name, which reads as something being broken rather than as the station simply
// having no picture. So STR's own logo stands in.
//
// The substitution is deliberately narrow: it happens only when there is NO
// image, or when every candidate carries an extension a display provably cannot
// draw (.svg, .ico). A URL with no extension at all keeps its place, because
// plenty of perfectly drawable logos are served without one and replacing those
// would take a working picture away.
// Which candidate to point at is still an extension question, because choosing
// among URLs without fetching them is all that can be done cheaply. Whether the
// chosen one is actually drawable is NOT: that is settled from the bytes, in
// the art proxy, which is fetching the image anyway (see drawableImage). The
// veto that used to sit here judged by extension and threw away real pictures,
// most visibly the PNGs the fallback icon service serves under a .ico URL.
func stationImageURL(art string) string {
	if u := ArtProxyURL("http://"+boxurl.Authority, firstArtURL(art)); u != "" {
		return u
	}
	return "http://" + boxurl.Authority + strLogoPath
}

// firstArtURL picks ONE logo out of STR's pipe-separated fallback chain, the
// only thing the speaker understands.
//
// Not simply the first one. The chain is ordered by how likely a URL is to
// belong to the station, not by whether anything can draw it, and the front of
// it is very often a vector logo or a 16-pixel favicon: the first entry for
// Energy NRJ is an .svg, for Absolut Relax an .ico. The speaker takes the URL
// (now_playing comes back with artImageStatus="IMAGE_PRESENT"), and then
// nothing appears, because a display cannot render either of those. Reported as
// "the logo above the station name does not show up any more" (2026-08-05).
//
// So: prefer the first entry that looks like an ordinary raster image, and only
// fall back to the head of the chain when none of them does. Extension-based on
// purpose. The alternative is fetching every candidate to sniff its type, which
// would turn saving a preset into a series of network round trips.
func firstArtURL(art string) string {
	parts := strings.Split(art, "|")
	for _, p := range parts {
		if isRasterImageURL(p) {
			return strings.TrimSpace(p)
		}
	}
	return strings.TrimSpace(parts[0])
}

// isRasterImageURL reports whether a URL ends in an extension a speaker display
// can actually draw. Query strings and fragments are ignored, plenty of logo
// URLs carry a cache-busting parameter.
func isRasterImageURL(raw string) bool {
	u := strings.TrimSpace(raw)
	if u == "" {
		return false
	}
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	u = strings.ToLower(u)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp"} {
		if strings.HasSuffix(u, ext) {
			return true
		}
	}
	return false
}

// handleOrionToken answers the token endpoint the BMX service registry points
// at (_links.bmx_token). The firmware fetches it before it will use a service;
// an unanswered token call leaves the source registered but unusable. The real
// adapter needed no credentials for custom stations, so empty tokens are the
// correct answer.
func (s *Server) handleOrionToken(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	s.logger.Info("lir: orion token served")
	_, _ = w.Write([]byte(`{"access_token":"","refresh_token":""}`))
}
