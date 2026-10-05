// TuneIn API stub. Kept as infrastructure for a possible
// future path via Bose's TuneIn worker, currently NOT active in the code
// path since STSCertified does not start the TuneIn worker in the final FW version
// (see docs/findings-pair-flow.md "Service Discovery Update").
//
// If Bose ever reactivates the service or flashes an older FW version
// onto the box, we could offer a TuneIn-compatible OPML/JSON
// endpoint here, backed by Radio-Browser.info. The hostname
// 7f5055e9ff15f2a5035a488b81ec10f4.api.radiotime.com is already redirected via
// /etc/hosts to 127.0.0.1 (internal/hosts.DefaultEntries).
//
// Known paths that the BMXTuneInClient calls (from STSCertified binary
// strings):
//
//	/profiles/<id>/nowPlaying?partnerId=Bose&serial=<deviceSerial>
//	/now-playing/...
//	/Browse.ashx?id=s<station>      (standard TuneIn OPML)
//	/Tune.ashx?id=s<station>
//	/Describe.ashx?id=s<station>
//
// If this is ever reactivated, build a handler here that
// converts Radio-Browser API stations into the TuneIn OPML format.
//
// Not the only TuneIn path any more: with TUNEIN reflected as a source, the
// firmware's BMX TuneIn adapter asks the remote port for
// /bmx/tunein/v1/playback/station/<id>, served by internal/webui/bmxtunein.go
// (#500). That route is experimental and untested on hardware; this stub
// covers the partner-hash radiotime host only.
package marge

import (
	"net/http"
	"strings"
)

// isTuneInRequest detects whether the request goes to the Bose TuneIn partner
// subdomain. The spy middleware logs such a request at INFO, since none has
// ever been seen on 27.0.6 and one would answer #500 (does the firmware fetch
// station titles itself).
func isTuneInRequest(r *http.Request) bool {
	host := r.Host
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	return strings.HasSuffix(host, "api.radiotime.com")
}
