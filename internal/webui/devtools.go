package webui

import (
	"net/http"
	"os"
	"strings"
)

// Development-only endpoints.
//
// Most of /api/debug is read-only and belongs on every speaker: it is what a
// diagnostic bundle is built from. Two endpoints are not read-only and must not
// be reachable on a user's speaker:
//
//   - /api/debug/native-preset-probe REMOVES and rewrites preset slots while it
//     works out which command shape the firmware accepts. It restores them
//     afterwards, but a call that is interrupted leaves a hardware key dead.
//   - /api/debug/marge-lab redirects the speaker's whole cloud conversation to
//     another machine on the LAN.
//
// Both are harnesses built for one test bench. On port 8888 they are reachable
// from anywhere on the owner's network, so they are off unless deliberately
// switched on, and they answer 404 rather than 403 so a scan cannot even tell
// they exist.
//
// Enable with the environment variable STR_DEV_TOOLS=1 (the agent is launched
// from the on-NAND run script, so this is a deliberate act), or by placing a
// marker file at /mnt/nv/streborn/devtools - the same opt-in-by-marker shape
// the SSH access uses.
const devToolsMarker = "/mnt/nv/streborn/devtools"

func devToolsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("STR_DEV_TOOLS"))) {
	case "1", "true", "yes", "on":
		return true
	}
	if _, err := os.Stat(devToolsMarker); err == nil {
		return true
	}
	return false
}

// requireDevTools reports whether the request may proceed, answering 404 when
// the development endpoints are off.
func (s *Server) requireDevTools(w http.ResponseWriter, r *http.Request) bool {
	if devToolsEnabled() {
		return true
	}
	s.logger.Warn("refused a development endpoint: it is off on this speaker",
		"path", r.URL.Path)
	http.NotFound(w, r)
	return false
}
