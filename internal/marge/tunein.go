// TuneIn API Stub. Vorgehalten als Infrastruktur fuer einen moeglichen
// kuenftigen Pfad ueber Bose's TuneIn Worker, derzeit NICHT aktiv im Code
// Pfad da STSCertified den TuneIn Worker in der finalen FW Version nicht
// startet (siehe docs/findings-pair-flow.md "Service Discovery Update").
//
// Wenn Bose den Service jemals reaktiviert oder eine aeltere FW Version
// auf der Box flasht, koennten wir hier einen TuneIn-kompatiblen OPML/JSON
// Endpoint anbieten, mit Backend Radio-Browser.info. Der Hostname
// 7f5055e9ff15f2a5035a488b81ec10f4.api.radiotime.com wird bereits via
// /etc/hosts auf 127.0.0.1 umgeleitet (internal/hosts.DefaultEntries).
//
// Bekannte Pfade die der BMXTuneInClient ruft (aus STSCertified Binary
// Strings):
//
//	/profiles/<id>/nowPlaying?partnerId=Bose&serial=<deviceSerial>
//	/now-playing/...
//	/Browse.ashx?id=s<station>      (Standard TuneIn OPML)
//	/Tune.ashx?id=s<station>
//	/Describe.ashx?id=s<station>
//
// Wenn das jemals reaktiviert wird, hier einen Handler bauen der
// Radio-Browser API Stationen in das TuneIn OPML Format konvertiert.
package marge

import (
	"net/http"
	"strings"
)

// isTuneInRequest detected ob der Request an den Bose TuneIn Partner
// Subdomain geht. Wird derzeit im catchall nicht mehr aufgerufen weil
// die Box den Endpoint nicht anspricht.
func isTuneInRequest(r *http.Request) bool {
	host := r.Host
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	return strings.HasSuffix(host, "api.radiotime.com")
}
