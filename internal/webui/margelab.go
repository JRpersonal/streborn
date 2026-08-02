package webui

// Developer escape hatch: point this speaker's cloud emulation at a marge
// stub running on a developer machine.
//
// Finding the response shapes the firmware accepts (the account handshake,
// the source list, ...) is a search, and doing it on the box costs an agent
// build, an OTA and a reboot per attempt. With this endpoint the box's Bose
// hostnames resolve to a developer PC instead of 127.0.0.1, so the same
// search costs a process restart on that PC (see cmd/margelab).
//
// Deliberately NOT persisted: the redirect lives in /etc/hosts only until the
// next boot, when run.sh rewrites the file. A forgotten experiment therefore
// heals itself, and no user box can be left pointing at a stranger's machine.
// The target must be a private LAN address, so this cannot redirect a speaker
// to the internet.

import (
	"encoding/json"
	"net"
	"net/http"

	"github.com/JRpersonal/streborn/internal/hosts"
)

// handleMargeLab points the Bose hostnames at a developer machine (POST with
// {"ip":"192.168.x.y"}) or back at the box itself (empty ip / DELETE).
func (s *Server) handleMargeLab(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "POST or DELETE", http.StatusMethodNotAllowed)
		return
	}
	target := "127.0.0.1"
	if r.Method == http.MethodPost {
		var in struct {
			IP string `json:"ip"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in)
		if in.IP != "" {
			ip := net.ParseIP(in.IP)
			if ip == nil || ip.To4() == nil {
				http.Error(w, "ip must be an IPv4 address", http.StatusBadRequest)
				return
			}
			if !ip.IsPrivate() && !ip.IsLoopback() {
				http.Error(w, "refusing a non-private target", http.StatusBadRequest)
				return
			}
			target = ip.String()
		}
	}
	entries := hosts.DefaultEntries()
	for i := range entries {
		entries[i].IP = target
	}
	mgr := hosts.New("/etc/hosts", s.logger)
	if err := mgr.Apply(entries); err != nil {
		s.logger.Warn("marge lab: rewriting the hosts file failed", "target", target, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logger.Warn("marge lab: Bose hostnames now resolve to a developer machine (until the next boot)", "target", target)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "target": target})
}
