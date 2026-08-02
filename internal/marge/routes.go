// Routes: the catchall dispatcher matching known Bose cloud paths.

package marge

import (
	"net/http"
	"strings"
)

// handleCatchall responds to everything that is not served by a concrete
// handler. Pattern matching on known path schemes, otherwise a
// generic 200 OK with XML.
func (s *Server) handleCatchall(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// The TuneIn partner subdomain is redirected to 127.0.0.1 in /etc/hosts
	// in case STSCertified ever calls the endpoint. Currently this
	// does not happen in this FW (see internal/marge/tunein.go).
	// If the box does connect there, the request falls into the catchall
	// default with a generic 200 OK <ack/>.

	// Real Bose cloud endpoints from captured traffic
	switch {
	case strings.HasPrefix(path, "/streaming/support/power_on"):
		s.respondPowerOn(w, r)
		return
	case strings.HasPrefix(path, "/streaming/support/"):
		s.respondStreamingSupport(w, r)
		return
	case strings.HasPrefix(path, "/streaming/sourceproviders"):
		s.respondSourceProviders(w, r)
		return
	// Stereo-pair group CRUD (#166). During /addGroup the ST10 firmware creates
	// the L/R group record "on marge" via POST /streaming/account/<acct>/group/,
	// polls it via GET /streaming/account/<acct>/device/<dev>/group/, and drops
	// it on /removeGroup. Without a handler the POST fell through to the generic
	// account response below, so the box could not parse a group back and failed
	// with GROUP_CREATE_GROUP_ON_MARGE_ERROR (5580) -> /addGroup HTTP 500. Must
	// sit before the /device and generic /streaming/account cases, since the poll
	// path contains "/device" too.
	case strings.HasPrefix(path, "/streaming/account/") && strings.Contains(path, "/group"):
		s.handleMargeGroup(w, r)
		return
	// AddDevice sync: /streaming/account/<accountId>/device/ POST
	// The box calls this after POST /setMargeAccount on the box itself.
	// The response must be adddeviceresponse XML with a margetoken element.
	case strings.HasPrefix(path, "/streaming/account/") && strings.Contains(path, "/device") && r.Method == http.MethodPost:
		s.respondAddDevice(w, r)
		return
	case strings.HasPrefix(path, "/streaming/account") || strings.HasPrefix(path, "/streaming/auth"):
		s.respondMargeAccountFull(w, r)
		return
	case strings.HasPrefix(path, "/bmx/registry/"):
		s.respondBmxRegistry(w, r)
		return
	case strings.HasPrefix(path, "/bmx/"):
		s.respondBmxGeneric(w, r)
		return
	}

	// Fallback pattern matching (legacy)
	switch {
	case strings.Contains(path, "preset"):
		s.respondPresets(w)
	case strings.Contains(path, "recent"):
		s.respondRecents(w)
	case strings.Contains(path, "service") && strings.Contains(path, "avail"):
		s.respondServiceAvailability(w)
	case strings.Contains(path, "source"):
		s.respondSources(w)
	case strings.Contains(path, "account") || strings.Contains(path, "auth"):
		s.respondAccount(w)
	case strings.Contains(path, "config"):
		s.respondConfigStatus(w)
	default:
		// Generic 200 OK so the box does not go into retry loops.
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ack/>`))
	}
}
