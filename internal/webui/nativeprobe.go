package webui

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxcli"
)

// Native preset probe: a development harness for finding the exact TAP CLI
// form the firmware accepts for a native LOCAL_INTERNET_RADIO preset.
//
// It exists because the failure mode is silent. `ws AddPreset` answers a
// rejected command the same way it answers an accepted one, so the only honest
// verdict is to write the slot and read it back - and without this endpoint
// every guess costs an agent OTA plus a box reboot, which is minutes per bit
// of information.
//
// It writes presets, so it restores the slot from the STR store when it
// finishes and refuses to run against a slot the store does not back.

type nativeProbeResult struct {
	Variant  string `json:"variant"`
	Command  string `json:"command"`
	Reply    string `json:"reply"`
	Err      string `json:"err,omitempty"`
	StoredAs string `json:"storedAs,omitempty"`
	Landed   bool   `json:"landed"`
	Native   bool   `json:"native"`
}

// handleNativeProbe answers POST /api/debug/native-preset-probe with
// body {"slot": 6} (slot defaults to 6).
func (s *Server) handleNativeProbe(w http.ResponseWriter, r *http.Request) {
	// Writes and deletes preset slots: never reachable on a user's speaker.
	if !s.requireDevTools(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.presets == nil || s.boxHost == "" {
		http.Error(w, "presets store or box host not configured", http.StatusServiceUnavailable)
		return
	}
	var in struct {
		Slot int `json:"slot"`
		// Commands lets the caller supply its own candidate command lines, so a
		// new hypothesis costs one HTTP call instead of an agent OTA and a box
		// reboot. The placeholders {loc}, {stream}, {name} and {slot} are
		// substituted. Empty means "run the built-in list".
		Commands []string `json:"commands"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.Slot < 1 || in.Slot > 6 {
		in.Slot = 6
	}
	var name, stream string
	for _, p := range s.presets.All() {
		if p.Slot == in.Slot {
			name = p.Name
			stream = boxPresetURL(p.Slot, p.Type == "spotify")
			break
		}
	}
	if stream == "" {
		http.Error(w, "no STR preset in that slot", http.StatusBadRequest)
		return
	}

	loc := OrionStationLocation(stream, name, "")
	full := "http://127.0.0.1:8888/core02/svc-bmx-adapter-orion/prod/orion" + loc
	q := func(v string) string { return `"` + v + `"` }
	slot := strconv.Itoa(in.Slot)

	// Each variant changes ONE property against a neighbour, so a single run
	// says which one the firmware rejects: the type keyword, the empty account
	// argument, the query string in the location, or the source name itself.
	variants := []struct{ name, cmd string }{
		{"stationurl+emptyquotes", `ws AddPreset LOCAL_INTERNET_RADIO stationurl ` + loc + ` ` + q(name) + ` "" ` + slot},
		{"stationurl+noaccount", `ws AddPreset LOCAL_INTERNET_RADIO stationurl ` + loc + ` ` + q(name) + ` ` + slot},
		{"stationurl+quotedloc", `ws AddPreset LOCAL_INTERNET_RADIO stationurl ` + q(loc) + ` ` + q(name) + ` "" ` + slot},
		{"stationurl+fullurl", `ws AddPreset LOCAL_INTERNET_RADIO stationurl ` + full + ` ` + q(name) + ` "" ` + slot},
		{"stationurl+rawstream", `ws AddPreset LOCAL_INTERNET_RADIO stationurl ` + stream + ` ` + q(name) + ` "" ` + slot},
		{"station+emptyquotes", `ws AddPreset LOCAL_INTERNET_RADIO station ` + loc + ` ` + q(name) + ` "" ` + slot},
		{"audio+emptyquotes", `ws AddPreset LOCAL_INTERNET_RADIO audio ` + loc + ` ` + q(name) + ` "" ` + slot},
		{"tunein+stationurl", `ws AddPreset TUNEIN stationurl ` + loc + ` ` + q(name) + ` "" ` + slot},
		{"upnp+baseline", `ws AddPreset UPNP audio ` + stream + ` ` + q(name) + ` UPnPUserName ` + slot},
	}
	if len(in.Commands) > 0 {
		sub := strings.NewReplacer("{loc}", loc, "{stream}", stream, "{name}", name, "{slot}", slot, "{full}", full)
		variants = variants[:0]
		for i, c := range in.Commands {
			variants = append(variants, struct{ name, cmd string }{
				name: "custom-" + strconv.Itoa(i), cmd: sub.Replace(c),
			})
		}
	}

	results := make([]nativeProbeResult, 0, len(variants))
	for _, v := range variants {
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		reply, err := boxcli.Send(ctx, s.boxHost, v.cmd)
		cancel()
		res := nativeProbeResult{Variant: v.name, Command: v.cmd, Reply: strings.TrimSpace(reply)}
		if err != nil {
			res.Err = err.Error()
		}
		time.Sleep(500 * time.Millisecond)
		if got, ok := s.probeReadSlot(r.Context(), in.Slot); ok {
			res.Landed = true
			res.StoredAs = got
			res.Native = strings.Contains(got, "/station?data=") ||
				strings.Contains(got, "/orion/station")
		}
		results = append(results, res)
		// Clear the slot so every variant starts from the same state.
		cctx, ccancel := context.WithTimeout(r.Context(), 4*time.Second)
		_ = boxcli.RemovePreset(cctx, s.boxHost, in.Slot)
		ccancel()
		time.Sleep(300 * time.Millisecond)
	}

	// Leave no dead key behind: put the slot back as the store describes it.
	rctx, rcancel := context.WithTimeout(r.Context(), 6*time.Second)
	_ = boxcli.AddPreset(rctx, s.boxHost, in.Slot, name, stream)
	rcancel()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"slot": in.Slot, "name": name, "stream": stream,
		"location": loc, "results": results,
	})
}

// probeReadSlot returns the ContentItem location the box currently holds in a
// preset slot.
func (s *Server) probeReadSlot(ctx context.Context, slot int) (string, bool) {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, "http://"+s.boxHost+":8090/presets", nil)
	if err != nil {
		return "", false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return "", false
	}
	var doc struct {
		Presets []struct {
			ID          string `xml:"id,attr"`
			ContentItem struct {
				Source   string `xml:"source,attr"`
				Type     string `xml:"type,attr"`
				Location string `xml:"location,attr"`
				Account  string `xml:"sourceAccount,attr"`
			} `xml:"ContentItem"`
		} `xml:"preset"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return "", false
	}
	want := strconv.Itoa(slot)
	for _, p := range doc.Presets {
		if p.ID != want {
			continue
		}
		ci := p.ContentItem
		return ci.Source + " " + ci.Type + " account=" + ci.Account + " " + ci.Location, true
	}
	return "", false
}
