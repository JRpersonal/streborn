package main

// This file was split out of app.go (wave-1 move-only refactor):
// preset CRUD against the stick agent and cross-box preset copy.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Preset Format passt zu internal/presets.Preset JSON.
type Preset struct {
	Slot      int    `json:"slot"`
	Name      string `json:"name"`
	StreamURL string `json:"stream_url"`
	Type      string `json:"type"`
	Art       string `json:"art,omitempty"`
	Bitrate   int    `json:"bitrate,omitempty"`
	Codec     string `json:"codec,omitempty"`    // radio presets: station codec ("MP3", "AAC+"); recalls label AAC streams audio/aac (#252)
	URI       string `json:"uri,omitempty"`      // Spotify presets: playlist/album URI
	Account   string `json:"account,omitempty"`  // Spotify presets: owning account
	Source    string `json:"source,omitempty"`   // DLNA presets: media server name (cosmetic badge)
	Homepage  string `json:"homepage,omitempty"` // radio presets: station website (recent "website" link)
	// Queue presets (Type=="queue", a saved DLNA folder) carry the shuffle
	// flag and the ordered track list. Items stays raw JSON on purpose: the
	// agent owns that schema (internal/presets PresetItem), and the copy-
	// presets flow must round-trip it VERBATIM. A typed mirror was missing
	// here once already, so GetPresets silently dropped the tracks and the
	// target then rejected the copied preset as an empty folder, aborting the
	// whole transfer.
	Shuffle bool            `json:"shuffle,omitempty"`
	Items   json.RawMessage `json:"items,omitempty"`
}

// presetAPIPath is the agent's preset REST route; the slot is appended for
// per-slot writes and deletes.
const presetAPIPath = "/api/presets"

func (a *App) GetPresets(host string, port int) ([]Preset, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, presetAPIPath, "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out []Preset
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetPreset does PUT /api/presets/<slot>. art is the station logo URL,
// sent to the box as upnp:albumArtURI on play. codec is radio-browser's
// station codec ("MP3", "AAC+"); the agent labels AAC streams audio/aac on
// recall so they do not decode to silence (#252). Routed through
// boxPut so a preset save gets the same :8888<->:17008 port fallback as the
// other box commands.
func (a *App) SetPreset(host string, port int, slot int, name, streamURL, art string, bitrate int, homepage, codec string) error {
	return a.boxPut(host, port, fmt.Sprintf("%s/%d", presetAPIPath, slot),
		Preset{Slot: slot, Name: name, StreamURL: streamURL, Type: "radio", Art: art, Bitrate: bitrate, Homepage: homepage, Codec: codec})
}

// SaveLibraryPreset stores a preset saved from a DLNA media server (the Library
// tab). It plays like a radio preset (a stream URL the box pulls) but carries
// the media server name as Source, so the desktop app can show a small "from"
// badge on the preset. Source is cosmetic and round-trips through the agent.
func (a *App) SaveLibraryPreset(host string, port int, slot int, name, streamURL, art string, bitrate int, source string) error {
	return a.boxPut(host, port, fmt.Sprintf("%s/%d", presetAPIPath, slot),
		Preset{Slot: slot, Name: name, StreamURL: streamURL, Type: "radio", Art: art, Bitrate: bitrate, Source: source})
}

// SaveFolderPreset stores a queue preset (a whole DLNA folder, type=queue) on a
// slot. payloadJSON is the already-built preset object from the Library tab
// ({name, type:"queue", shuffle, items:[{url,title,art,mime,duration_sec}...]});
// it is PUT verbatim to /api/presets/<slot> so the frontend owns the shape and
// the agent reloads it into the play-queue on recall. Routed through boxDo for
// the same :8888<->:17008 port fallback as the other preset saves.
func (a *App) SaveFolderPreset(host string, port int, slot int, payloadJSON string) error {
	resp, err := a.boxDo(host, port, http.MethodPut,
		fmt.Sprintf("%s/%d", presetAPIPath, slot), "application/json", payloadJSON)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}

// SaveSpotifyPreset stores a real Spotify preset (type=spotify with the
// playlist/album URI) on a slot. A long-press while a Spotify playlist plays
// uses this so the saved preset is recallable, shuffled and account-aware,
// instead of a radio link to the raw stream (which showed the album cover, not
// the Spotify logo, and did not recall the playlist). The agent fills the
// account and a stable playlist cover when they are empty.
func (a *App) SaveSpotifyPreset(host string, port int, slot int, name, uri, account string) error {
	return a.boxPut(host, port, fmt.Sprintf("%s/%d", presetAPIPath, slot),
		Preset{Slot: slot, Name: name, Type: "spotify", URI: uri, Account: account})
}

// CopyPresetsAcrossBoxes copies every preset (slots 1-6) from a source speaker
// to a target speaker, preserving radio vs Spotify type and all fields, then
// re-syncs the target's hardware keys so buttons 1-6 reflect the copy. Used by
// the box-to-box preset copy in Speaker Settings so the user does not have to
// re-enter stations on every speaker. Returns the number of presets copied.
func (a *App) CopyPresetsAcrossBoxes(srcHost string, srcPort int, dstHost string, dstPort int) (int, error) {
	if srcHost == "" || dstHost == "" {
		return 0, fmt.Errorf("source and target host are required")
	}
	if srcHost == dstHost {
		return 0, fmt.Errorf("source and target are the same speaker")
	}
	presets, err := a.GetPresets(srcHost, srcPort)
	if err != nil {
		return 0, fmt.Errorf("read source presets: %w", err)
	}
	copied := 0
	var slotErrs []string
	for _, p := range presets {
		if p.Slot < 1 || p.Slot > 6 || p.Name == "" {
			continue
		}
		// PUT the source preset verbatim (via boxPut, so the target's port
		// fallback applies too) so radio, Spotify and queue presets keep all
		// their fields (type, uri, account, art, bitrate, shuffle, items)
		// with no field mapping. A rejected slot is reported but must not
		// abort the transfer: the remaining slots still copy.
		if err := a.boxPut(dstHost, dstPort, fmt.Sprintf("%s/%d", presetAPIPath, p.Slot), p); err != nil {
			a.logger.Warn("copy presets: slot rejected by the target",
				"src", srcHost, "dst", dstHost, "slot", p.Slot, "type", p.Type, "err", err)
			slotErrs = append(slotErrs, fmt.Sprintf("preset %d (%s): %v", p.Slot, p.Name, err))
			continue
		}
		copied++
	}
	// Re-push the target's hardware keys so 1-6 on the speaker match the copy.
	if _, err := a.SyncBoxPresets(dstHost, dstPort); err != nil {
		a.logger.Warn("copy presets: target hardware sync failed", "dst", dstHost, "err", err)
	}
	// A copied Spotify preset is dead on a speaker that lacks the Spotify
	// login: the credential lives per box, so recalls there fail with
	// "speaker not logged into Spotify" until the user taps the target in the
	// Spotify app. Users rightly expect the copy to carry the login along
	// (Jens, 2026-07-10), so transfer the source's credential too, best-effort
	// - a transfer failure must not fail the preset copy (the presets DID
	// land; the recall error still tells the user the manual way).
	if hasSpotifyPreset(presets) {
		a.transferSpotifyCredential(srcHost, srcPort, dstHost, dstPort)
	}
	if len(slotErrs) > 0 {
		return copied, fmt.Errorf("%s", strings.Join(slotErrs, "; "))
	}
	return copied, nil
}

// hasSpotifyPreset reports whether any preset in the set is a Spotify one.
func hasSpotifyPreset(presets []Preset) bool {
	for _, p := range presets {
		if p.Type == "spotify" {
			return true
		}
	}
	return false
}

// transferSpotifyCredential copies the source speaker's active Spotify login
// to the target (the agent's credential endpoints: GET exports the blob, POST
// imports it and restarts the engine so the login is live immediately).
// Best-effort by contract: every failure is logged and swallowed. A target
// that already has the SAME account keeps it (the import is idempotent); a
// target on a different account gets the source's login, which matches the
// user intent of copying that source's presets.
func (a *App) transferSpotifyCredential(srcHost string, srcPort int, dstHost string, dstPort int) {
	// The agent serves this on /spotify/credential (same endpoint
	// SyncSpotifyLogin uses). This function called /api/spotify/credential for
	// weeks, which the agent's catch-all index answered with 200 + HTML: the
	// copy then logged success while transferring nothing, and every copied
	// Spotify preset stayed dead until the user picked the target in the
	// Spotify app once. Newer agents alias both spellings, but use the
	// canonical path so older agents in the field work too.
	resp, err := a.boxDo(srcHost, srcPort, http.MethodGet, "/spotify/credential", "", "")
	if err != nil {
		a.logger.Warn("copy presets: source Spotify credential not readable", "src", srcHost, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 404 = no login stored on the source; nothing to transfer.
		a.logger.Info("copy presets: source has no stored Spotify login, skipping credential transfer", "src", srcHost, "status", resp.StatusCode)
		return
	}
	// Guard against ever falling through to a catch-all HTML page again: the
	// credential endpoint answers application/octet-stream, an index answers
	// text/html. Posting HTML onward would corrupt the target's login.
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/octet-stream") {
		a.logger.Warn("copy presets: source answered the credential read with the wrong content type, not transferring", "src", srcHost, "contentType", ct)
		return
	}
	blob, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil || len(blob) == 0 {
		a.logger.Warn("copy presets: reading the Spotify credential failed", "src", srcHost, "err", err)
		return
	}
	postResp, err := a.boxDo(dstHost, dstPort, http.MethodPost, "/spotify/credential", "application/octet-stream", string(blob))
	if err != nil {
		a.logger.Warn("copy presets: Spotify credential transfer to the target failed", "dst", dstHost, "err", err)
		return
	}
	defer postResp.Body.Close()
	if postResp.StatusCode >= 400 {
		a.logger.Warn("copy presets: target rejected the Spotify credential", "dst", dstHost, "status", postResp.StatusCode)
		return
	}
	a.logger.Info("copy presets: Spotify login transferred with the presets", "src", srcHost, "dst", dstHost)
}

// DeletePreset does DELETE /api/presets/<slot>.
func (a *App) DeletePreset(host string, port int, slot int) error {
	resp, err := a.boxDo(host, port, http.MethodDelete, fmt.Sprintf("/api/presets/%d", slot), "", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}
