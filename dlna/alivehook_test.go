package dlna

import "testing"

func notifyPkt(nts, nt, loc, usn string) []byte {
	return []byte("NOTIFY * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nNTS: " + nts +
		"\r\nNT: " + nt + "\r\nLOCATION: " + loc + "\r\nUSN: " + usn + "\r\nCACHE-CONTROL: max-age=1800\r\n\r\n")
}

// #1890: only a media server's alive reaches the hook, with its location and
// USN; a speaker or router announcing itself does not.
func TestMediaServerAliveHookHearsOnlyMediaServers(t *testing.T) {
	type heard struct{ loc, usn string }
	var got []heard
	SetMediaServerAliveHook(func(loc, usn string) { got = append(got, heard{loc, usn}) })
	defer SetMediaServerAliveHook(nil)

	const loc = "http://192.0.2.20:50001/desc/device.xml"
	noteMediaServerAlive(notifyPkt("ssdp:alive", "urn:schemas-upnp-org:device:MediaServer:1", loc, "uuid:abc::urn:schemas-upnp-org:device:MediaServer:1"))
	noteMediaServerAlive(notifyPkt("ssdp:alive", "urn:schemas-upnp-org:service:ContentDirectory:1", loc, "uuid:abc::urn:schemas-upnp-org:service:ContentDirectory:1"))
	noteMediaServerAlive(notifyPkt("ssdp:alive", "urn:schemas-upnp-org:device:MediaRenderer:1", "http://192.0.2.30:8091/XD/BO5EBO5E-F00D-F00D-FEED-000000000000.xml", "uuid:spk::urn:schemas-upnp-org:device:MediaRenderer:1"))
	noteMediaServerAlive(notifyPkt("ssdp:alive", "upnp:rootdevice", "http://192.0.2.1:49000/igd.xml", "uuid:rtr::upnp:rootdevice"))
	noteMediaServerAlive(notifyPkt("ssdp:alive", "urn:schemas-upnp-org:device:MediaServer:1", "", "uuid:noloc"))

	if len(got) != 2 {
		t.Fatalf("heard %d alives, want 2: %+v", len(got), got)
	}
	if got[0].loc != loc || got[0].usn != "uuid:abc::urn:schemas-upnp-org:device:MediaServer:1" {
		t.Errorf("first alive = %+v", got[0])
	}

	SetMediaServerAliveHook(nil)
	noteMediaServerAlive(notifyPkt("ssdp:alive", "urn:schemas-upnp-org:device:MediaServer:1", loc, "uuid:abc"))
	if len(got) != 2 {
		t.Error("a removed hook was still called")
	}
}
