package webui

// The stations of the Pandora account linked to the speaker, and starting one
// (docs/streaming/us-services.md, "Starting a station without the Bose app").
//
// GET  /api/pandora/stations  the account's stations, read from the speaker's
//                             own /navigate (name, art, location, type)
// POST /api/pandora/play      {"location":"...","name":"...","type":"..."}
//                             starts one through the speaker's /select
//
// The speaker's own Pandora client plays the station; STR only hands it the
// item the speaker listed. Both calls are LAN only and need the service
// enabled (a US speaker, or the opt-in). The account name is never logged in
// clear and never leaves the agent: the list carries no account, and play
// fills it in from the speaker's /sources.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/presets"
)

const pandoraSource = "PANDORA"

// errPandoraNotLinked means the speaker lists no Pandora account.
var errPandoraNotLinked = errors.New("no Pandora account on the speaker")

// pandoraAccount returns the account and status the speaker lists for
// PANDORA in /sources.
func (s *Server) pandoraAccount(ctx context.Context) (account, status string, err error) {
	srcs, err := s.nativeBox().GetSources(ctx)
	if err != nil {
		return "", "", err
	}
	for _, it := range srcs {
		if strings.EqualFold(it.Source, pandoraSource) && strings.TrimSpace(it.SourceAccount) != "" {
			return strings.TrimSpace(it.SourceAccount), it.Status, nil
		}
	}
	return "", "", errPandoraNotLinked
}

// maskAccountIn replaces the account wherever it appears in a message (a
// firmware error can echo the request).
func maskAccountIn(msg, account string) string {
	if account == "" {
		return msg
	}
	return strings.ReplaceAll(msg, account, maskForLog(account))
}

// pandoraGate answers the requests every Pandora call refuses alike and
// reports whether the handler may go on.
func (s *Server) pandoraGate(w http.ResponseWriter, r *http.Request, method string) bool {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "music service calls only allowed from LAN", http.StatusForbidden)
		return false
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if s.native == nil {
		http.Error(w, "not available on this build", http.StatusNotImplemented)
		return false
	}
	if !s.native.NativeServiceEnabled(pandoraSource) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "Pandora is offered on US speakers; elsewhere switch the test on first.",
			"code":  "service-off",
		})
		return false
	}
	return true
}

// writePandoraAccountError turns a failed /sources read into the answer.
func writePandoraAccountError(w http.ResponseWriter, err error) {
	if errors.Is(err, errPandoraNotLinked) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "No Pandora account is linked to this speaker.",
			"code":  "not-linked",
		})
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]any{
		"error": "The speaker's source list could not be read: " + err.Error(),
		"code":  "sources-unreadable",
	})
}

func (s *Server) handlePandoraStations(w http.ResponseWriter, r *http.Request) {
	if !s.pandoraGate(w, r, http.MethodGet) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	account, status, err := s.pandoraAccount(ctx)
	if err != nil {
		writePandoraAccountError(w, err)
		return
	}
	stations, err := s.nativeBox().ListServiceStations(ctx, pandoraSource, account)
	if err != nil {
		msg := maskAccountIn(err.Error(), account)
		s.logger.Warn("pandora: the speaker did not list the stations", "account", maskForLog(account), "status", status, "err", msg)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "The speaker could not list the Pandora stations: " + msg,
			"code":  "navigate-failed",
		})
		return
	}
	s.pandoraMu.Lock()
	s.pandoraStations = stations
	s.pandoraMu.Unlock()
	s.logger.Info("pandora: station list read from the speaker", "account", maskForLog(account), "status", status, "stations", len(stations))
	if stations == nil {
		stations = []boxapi.ServiceStation{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "stations": stations})
}

// listedPandoraItem returns the ContentItem the speaker listed for location,
// from the last station list.
func (s *Server) listedPandoraItem(location string) (string, bool) {
	s.pandoraMu.Lock()
	defer s.pandoraMu.Unlock()
	for _, st := range s.pandoraStations {
		if st.Location == location && st.ContentItem != "" {
			return st.ContentItem, true
		}
	}
	return "", false
}

func (s *Server) handlePandoraPlay(w http.ResponseWriter, r *http.Request) {
	if !s.pandoraGate(w, r, http.MethodPost) {
		return
	}
	var req struct {
		Location string `json:"location"`
		Name     string `json:"name"`
		Type     string `json:"type"`
	}
	if !decodeNativeJSON(w, r, &req) {
		return
	}
	req.Location = strings.TrimSpace(req.Location)
	if req.Location == "" {
		http.Error(w, "location is required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	// The account is read even when the listed item is replayed, so a speaker
	// that lost the account answers "not linked" rather than a firmware error.
	account, _, err := s.pandoraAccount(ctx)
	if err != nil {
		writePandoraAccountError(w, err)
		return
	}
	item, listed := s.listedPandoraItem(req.Location)
	if !listed {
		// Not in the last list (the agent restarted, or the list was never
		// read): rebuild the item in the form the speaker lists it.
		typ := strings.TrimSpace(req.Type)
		if typ == "" {
			typ = "stationurl"
		}
		item = nativeContentItemXML(presets.NativeItem{
			Source: pandoraSource, SourceAccount: account, Location: req.Location, ItemType: typ,
		}, strings.TrimSpace(req.Name))
	}
	if err := s.nativeBox().SelectContentItem(ctx, item); err != nil {
		msg := maskAccountIn(err.Error(), account)
		s.logger.Warn("pandora: the speaker refused the station", "location", req.Location, "listed", listed, "err", msg)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "The speaker did not start the station: " + msg,
			"code":  "select-failed",
		})
		return
	}
	s.logger.Info("pandora: station handed to the speaker", "location", req.Location, "name", req.Name, "listed", listed,
		"from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
