package webui

import (
	"errors"
	"testing"
)

// The exact fault a speaker returned on 2026-10-01 when it was a member of a
// group and Spotify tried to drive it.
func TestIsGroupedRejectionOnTheRealSpotifyFault(t *testing.T) {
	err := errors.New(`SetURI: soap SetAVTransportURI status 500: <?xml version="1.0"?><s:Envelope><s:Body><s:Fault><faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>501</errorCode><errorDescription>Can't control member of group</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`)
	if !IsGroupedRejection(err) {
		t.Fatal("the live fault from a grouped speaker was not recognised")
	}
	if IsGroupedRejection(errors.New("SetURI: soap SetAVTransportURI status 500: some other fault")) {
		t.Error("an unrelated 500 was taken as a grouped refusal")
	}
	if IsGroupedRejection(nil) {
		t.Error("nil counted as a refusal")
	}
}
