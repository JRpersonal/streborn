package webui

// Developer escape hatch: relay this speaker's cloud conversation to a marge
// stub running on a developer machine.
//
// Finding the response shapes the firmware accepts (the account handshake, the
// source list, ...) is a search, and each on-box attempt costs an agent build,
// an OTA and a reboot. With a lab machine registered here the same search
// costs a process restart on that machine (see cmd/margelab).
//
// The box is unaware: the agent keeps terminating TLS for streaming.bose.com
// with the CA already in the box's trust store and only forwards the decrypted
// request. Nothing is exported and nothing is installed on the box.
//
// Off by default and never persisted, so a forgotten experiment heals itself
// on the next boot; the target must be a private LAN address.

import (
	"encoding/json"
	"net/http"
)

// handleMargeLab registers a developer machine to relay the box's cloud
// conversation to (POST {"target":"192.168.1.5:9080"}), or clears the relay
// (DELETE, or an empty target).
func (s *Server) handleMargeLab(w http.ResponseWriter, r *http.Request) {
	// Redirects the speaker's whole cloud conversation to another machine:
	// never reachable on a user's speaker.
	if !s.requireDevTools(w, r) {
		return
	}
	if s.margeForward == nil {
		http.Error(w, "marge forwarding not wired in this build", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "POST or DELETE", http.StatusMethodNotAllowed)
		return
	}
	target := ""
	if r.Method == http.MethodPost {
		var in struct {
			Target string `json:"target"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in)
		target = in.Target
	}
	if err := s.margeForward(target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "target": target})
}
