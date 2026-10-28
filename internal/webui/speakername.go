// Speaker names the firmware's UPnP renderer cannot survive (#1582).
//
// The renderer (STSCertified on :8091) pastes the speaker's name into its
// device description without XML escaping it. With "&" in the name the
// description is invalid XML, and after the next boot the renderer does not
// come up at all: the description and every SOAP call answer 404. A name the
// firmware stores as invalid UTF-8 does the same. Spotify and the music library
// both play through that renderer, so on such a speaker neither works while
// native radio keeps playing, and nothing STR showed said why: go-librespot
// told the Spotify app it was playing and the speaker stayed silent.
//
// STR's own rename paths no longer write such a name (speakername.Sanitise),
// but the Bose app and the stock web UI still can, so the agent also has to
// recognise one it did not write. Two signals feed this:
//
//   - the name itself, every time the agent reads /info anyway (pollBoxInfo);
//   - a 404 from the renderer, which triggers one probe of the description URL
//     (rate-limited, never a loop) to tell "the renderer is gone" from any
//     other 404.
//
// The version endpoint reports the result, the desktop app and the phone page
// turn it into a sentence, and the one-tap repair (handleBoxNameRepair) renames
// the speaker with the sanitised name and restarts it. Nothing here renames or
// restarts the speaker without the user asking.

package webui

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/speakername"
)

// rendererProbeInterval bounds how often a renderer 404 may trigger the
// description probe. A playing queue or a Spotify retry can produce a 404 per
// second; one probe per window is plenty to name the cause.
const rendererProbeInterval = 10 * time.Minute

// boseDescriptionPrefix is the renderer's device description path, followed by
// the speaker's deviceID and ".xml" (the UDN is BO5EBO5E-F00D-F00D-FEED-<id>).
const boseDescriptionPrefix = "/XD/BO5EBO5E-F00D-F00D-FEED-"

type speakerNameHealth struct {
	mu sync.Mutex
	// unsafe is true while the last name read from the speaker breaks the
	// renderer; notUTF8 says whether invalid UTF-8 was (part of) the reason.
	unsafe  bool
	notUTF8 bool
	// warned is the name the WARN was last logged for, so a name is reported
	// once and not on every 30 s poll.
	warned string
	// rendererMissing is set when a renderer 404 was followed by a 404 from
	// the device description too: the renderer did not come up.
	rendererMissing   bool
	rendererMissingAt time.Time
	lastProbe         time.Time
	probing           bool

	// Test seams; nil means the real speaker.
	readInfo  func(ctx context.Context) (boxapi.Info, error)
	probeDesc func(ctx context.Context, deviceID string) (int, error)
	reboot    func()
}

// NoteBoxName records the name the speaker reports, with whether /info had to
// be read as Latin-1. Cheap and silent unless the verdict changes, so it is
// safe to call on every /info read.
func (s *Server) NoteBoxName(name string, notUTF8 bool) {
	bad := notUTF8 || speakername.Unsafe(name)
	h := &s.speakerName
	h.mu.Lock()
	h.unsafe, h.notUTF8 = bad, notUTF8
	warn := bad && h.warned != name
	if bad {
		h.warned = name
	} else {
		h.warned = ""
		// A good name and a renderer that is still down cannot be blamed on
		// the name; the next 404 probes again.
		h.rendererMissing = false
	}
	h.mu.Unlock()
	if warn {
		s.logger.Warn("speaker name contains a character the speaker's firmware cannot handle: Spotify and the music library cannot play on it until it is renamed and restarted",
			"name", name, "notUTF8", notUTF8, "suggested", speakername.Sanitise(name))
	}
}

// SpeakerNameIssue reports whether the stored name breaks the renderer, and
// whether a probe confirmed the renderer is actually down.
func (s *Server) SpeakerNameIssue() (unsafe, rendererMissing bool) {
	h := &s.speakerName
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.unsafe, h.rendererMissing
}

// NoteRendererNotFound is the renderer's 404 hook (upnp.Renderer.OnNotFound).
// It never blocks the caller: the probe runs in the background, at most once
// per rendererProbeInterval.
func (s *Server) NoteRendererNotFound() {
	h := &s.speakerName
	h.mu.Lock()
	if h.probing || (!h.lastProbe.IsZero() && time.Since(h.lastProbe) < rendererProbeInterval) {
		h.mu.Unlock()
		return
	}
	h.probing = true
	h.lastProbe = time.Now()
	h.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.probeRenderer(ctx)
	}()
}

// probeRenderer reads the speaker's name and deviceID, then fetches the
// renderer's device description once. A 404 there means the renderer did not
// come up; with a bad name that is the cause, and the log says so.
func (s *Server) probeRenderer(ctx context.Context) {
	h := &s.speakerName
	defer func() {
		h.mu.Lock()
		h.probing = false
		h.mu.Unlock()
	}()
	readInfo, probeDesc := h.readInfo, h.probeDesc
	if readInfo == nil {
		readInfo = func(ctx context.Context) (boxapi.Info, error) { return boxapi.New(s.boxHost).GetInfo(ctx) }
	}
	if probeDesc == nil {
		probeDesc = s.fetchDescriptionStatus
	}
	info, err := readInfo(ctx)
	if err != nil {
		s.logger.Info("renderer answered 404; could not read the speaker's name to check it", "err", err)
		return
	}
	s.NoteBoxName(info.Name, info.NameNotUTF8)
	if info.DeviceID == "" {
		return
	}
	status, err := probeDesc(ctx, info.DeviceID)
	if err != nil {
		s.logger.Info("renderer answered 404; its device description could not be fetched", "err", err)
		return
	}
	missing := status == http.StatusNotFound
	h.mu.Lock()
	h.rendererMissing = missing
	if missing {
		h.rendererMissingAt = time.Now()
	}
	bad := h.unsafe
	h.mu.Unlock()
	switch {
	case missing && bad:
		s.logger.Warn("UPnP renderer is down because of the speaker name: its device description answers 404. Rename the speaker and restart it",
			"name", info.Name, "notUTF8", info.NameNotUTF8, "suggested", speakername.Sanitise(info.Name))
	case missing:
		s.logger.Warn("UPnP renderer is down: its device description answers 404, and the speaker name looks fine", "status", status)
	default:
		s.logger.Info("renderer answered 404 but its device description is there", "status", status)
	}
}

// fetchDescriptionStatus GETs the renderer's device description and returns
// the HTTP status.
func (s *Server) fetchDescriptionStatus(ctx context.Context, deviceID string) (int, error) {
	if s.boxHost == "" || strings.ContainsAny(deviceID, "/?#:@ ") {
		return 0, fmt.Errorf("no usable box host or deviceID")
	}
	u := "http://" + net.JoinHostPort(s.boxHost, "8091") + boseDescriptionPrefix + deviceID + ".xml"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	resp.Body.Close()
	return resp.StatusCode, nil
}

// SpeakerNameForensics is the "speaker_name" section of /api/debug/state.
func (s *Server) SpeakerNameForensics() any {
	h := &s.speakerName
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.unsafe && !h.rendererMissing && h.lastProbe.IsZero() {
		return nil
	}
	out := map[string]any{
		"unsafe":          h.unsafe,
		"notUTF8":         h.notUTF8,
		"rendererMissing": h.rendererMissing,
	}
	if !h.lastProbe.IsZero() {
		out["lastProbeAgoSec"] = int(time.Since(h.lastProbe).Seconds())
	}
	if !h.rendererMissingAt.IsZero() {
		out["rendererMissingAgoSec"] = int(time.Since(h.rendererMissingAt).Seconds())
	}
	return out
}

// handleBoxNameRepair POST renames the speaker to the sanitised form of its
// current name and restarts it, which is what brings the renderer back. Only
// ever on the user's tap.
func (s *Server) handleBoxNameRepair(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	c := boxapi.New(s.boxHost)
	info, err := c.GetInfo(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	name := speakername.Sanitise(info.Name)
	if name == "" {
		name = "SoundTouch"
	}
	if err := c.SetName(ctx, name); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.logger.Info("speaker name repaired on the user's request, restarting the speaker",
		"old", info.Name, "new", name)
	s.NoteBoxName(name, false)
	writeJSON(w, http.StatusOK, map[string]string{"status": "rebooting", "name": name})
	reboot := s.speakerName.reboot
	if reboot == nil {
		reboot = rebootBox
	}
	// A short head start so BoseApp has written the new name before the
	// restart (rebootBox syncs, and waits a second of its own).
	go func() {
		time.Sleep(2 * time.Second)
		reboot()
	}()
}
