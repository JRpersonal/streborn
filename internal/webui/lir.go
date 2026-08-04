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
		"streamUrl": streamURL, "name": name, "imageUrl": firstArtURL(art),
		"streamType": "liveRadio", "isRealtime": true,
	})
	return "/station?data=" + base64.RawURLEncoding.EncodeToString(payload)
}

// firstArtURL takes the first entry of STR's pipe-separated art fallback chain.
// Presets store several candidate logo URLs separated by "|" so the app can try
// the next when one 404s; the speaker understands exactly one.
func firstArtURL(art string) string {
	if i := strings.IndexByte(art, '|'); i >= 0 {
		return art[:i]
	}
	return art
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
