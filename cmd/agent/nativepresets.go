package main

import (
	"context"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/webui"
)

// Native presets: storing a slot as a LOCAL_INTERNET_RADIO station rather than
// a UPnP stream, so the speaker can activate its own hardware key.
//
// Measured on an ST10 (FW 27.0.6, 2026-08-02): a UPNP preset makes the box
// answer its OWN key press with 1036 UNABLE_TO_PROCESS_NOT_LOGGED_IN /
// UpnpRcvdContentItemInWrongState, then flap UPNP -> INVALID_SOURCE -> UPNP,
// and only STR's recovery (clear the transport, push again, verify) gets audio
// out, about eight seconds after the press. The same station stored as a
// native radio item plays in about two seconds and logs nothing, because the
// firmware accepts it.
//
// The cause is not a login. UPNP is the box's local MediaRenderer and reports
// status="UNAVAILABLE" in GET /sources even WHILE it is the actively playing
// source, so the firmware's availability check can never pass. Registering
// UPNP in the emulated account does not change that (tried, no effect) and is
// actively wrong: it is a device-local slot the firmware answers with
// INVALID_SOURCE.
//
// Every preset STR stores is already an HTTP URL on our own stream proxy -
// radio, playlist queues and Spotify alike - so the conversion is uniform and
// needs no per-type handling: the same URL simply travels inside an orion
// station descriptor instead of a UPnP ContentItem.
//
// This migrates the installed base without asking anything of anyone: the next
// preset sync after an agent update rewrites the slots in place. It is gated on
// the box actually reporting the radio source as registered, and falls back to
// the UPnP form otherwise, so a speaker where the emulated account did not take
// keeps exactly the behaviour it has today.

const nativeReadyTTL = 2 * time.Minute

var nativeReady struct {
	sync.Mutex
	checked time.Time
	ok      bool
	// disabled latches native presets off for the rest of this agent run once
	// the box has proven it will not store them. It is deliberately sticky:
	// the failure mode is silent (the CLI reports success and the slot stays
	// empty), so retrying forever would leave every hardware key dead while
	// the log claimed six successful syncs.
	disabled bool
	why      string
}

// disableNativePresets latches the native preset form off after the box was
// measured to ignore it, so the next sweep restores the UPnP form and the
// hardware keys work again.
func disableNativePresets(reason string) {
	nativeReady.Lock()
	already := nativeReady.disabled
	nativeReady.disabled = true
	nativeReady.why = reason
	nativeReady.Unlock()
	if l := nativeReadyLogger; l != nil && !already {
		l.Warn("native presets: the box accepted the command but stored nothing, falling back to UPnP presets for this run so the hardware keys keep working",
			"reason", reason)
	}
}

// nativePresetsDisabled reports the latch state, for diagnostics.
func nativePresetsDisabled() (bool, string) {
	nativeReady.Lock()
	defer nativeReady.Unlock()
	return nativeReady.disabled, nativeReady.why
}

// nativeRadioReady reports whether the box has LOCAL_INTERNET_RADIO registered
// and READY, which is the precondition for storing native presets. The answer
// is cached: this is consulted once per preset slot in a sync sweep, and the
// box only changes it at boot or re-association.
func nativeRadioReady(ctx context.Context, boxHost string) bool {
	nativeReady.Lock()
	defer nativeReady.Unlock()
	if nativeReady.disabled {
		return false
	}
	if !nativeReady.checked.IsZero() && time.Since(nativeReady.checked) < nativeReadyTTL {
		return nativeReady.ok
	}
	was, first := nativeReady.ok, nativeReady.checked.IsZero()
	nativeReady.checked = time.Now()
	nativeReady.ok = probeNativeRadioReady(ctx, boxHost)
	// A silent verdict here is unauditable from a diagnostic bundle: it decides
	// whether every hardware key on this box costs a recovery round or not, and
	// a bundle that only shows UPnP presets cannot say whether the box refused
	// the radio source or the agent never asked. Log every change, and the
	// first answer either way.
	if l := nativeReadyLogger; l != nil && (first || was != nativeReady.ok) {
		if nativeReady.ok {
			l.Info("native presets: the box has LOCAL_INTERNET_RADIO registered, storing hardware presets natively")
		} else {
			l.Warn("native presets: the box does NOT report LOCAL_INTERNET_RADIO as READY, keeping the UPnP preset form (hardware keys stay on the recovery path)")
		}
	}
	return nativeReady.ok
}

// nativeReadyLogger is set once at agent start. A package-level logger keeps
// nativePresetLocation callable from the three preset-sync sites without
// threading a logger through each of them.
var nativeReadyLogger *slog.Logger

// SetNativeReadyLogger wires the logger used for the native-preset verdict.
func setNativeReadyLogger(l *slog.Logger) { nativeReadyLogger = l }

// invalidateNativeRadioReady drops the cached verdict so the next preset sync
// re-probes. Called when the box re-associates, since that is when the source
// registration changes.
func invalidateNativeRadioReady() {
	nativeReady.Lock()
	nativeReady.checked = time.Time{}
	nativeReady.Unlock()
}

func probeNativeRadioReady(ctx context.Context, boxHost string) bool {
	if boxHost == "" {
		return false
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, "http://"+boxHost+":8090/sources", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return false
	}
	var doc struct {
		Items []struct {
			Source string `xml:"source,attr"`
			Status string `xml:"status,attr"`
		} `xml:"sourceItem"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return false
	}
	for _, it := range doc.Items {
		if strings.EqualFold(it.Source, "LOCAL_INTERNET_RADIO") &&
			strings.EqualFold(it.Status, "READY") {
			return true
		}
	}
	return false
}

// wroteNative reports whether a sync batch contained at least one native
// slot, i.e. whether the readback below is worth an HTTP call.
func wroteNative(specs []boxcli.PresetSpec) bool {
	for _, s := range specs {
		if s.NativeLocation != "" {
			return true
		}
	}
	return false
}

// nativePresetLocation returns the orion station location for a preset, or ""
// when the slot must keep the UPnP form. The location is relative to the BMX
// service baseUrl on purpose: a full URL makes the firmware concatenate the
// two and resolve nothing.
func nativePresetLocation(ctx context.Context, boxHost string, p presets.Preset) string {
	if !nativeStorable(p) {
		return ""
	}
	if !nativeRadioReady(ctx, boxHost) {
		return ""
	}
	stream := boxPresetURL(p)
	if stream == "" {
		return ""
	}
	return webui.OrionStationLocation(stream, p.Name)
}

// nativeStorable reports whether a preset may be stored on the native radio
// source.
//
// Spotify presets may NOT, and the reason is the very property that makes the
// native form good for radio: the box activates it entirely by itself, so STR
// stands back and does not run its recall path. A radio stream needs nothing
// more than that. A Spotify preset does: something has to tell the local
// Spotify engine WHICH playlist to load, and that only happens on STR's recall
// path. Stored natively, the box would faithfully play the Spotify proxy URL
// and get whatever the engine happened to be playing before - or silence.
//
// Everything else STR stores (radio stations and play queues) is a plain
// stream from the box's point of view and converts cleanly.
func nativeStorable(p presets.Preset) bool {
	return !strings.EqualFold(strings.TrimSpace(p.Type), "spotify")
}
