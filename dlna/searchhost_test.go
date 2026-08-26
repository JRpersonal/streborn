package dlna

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSearchHost stands in a UDP responder for the unicast M-SEARCH (#726):
// the probe must reach the given host directly, follow the LOCATION it
// answers with, and come back with a resolved server. No multicast involved.
func TestSearchHost(t *testing.T) {
	const descXML = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <deviceType>urn:schemas-upnp-org:device:MediaServer:1</deviceType>
    <friendlyName>Unicast Test Server</friendlyName>
    <UDN>uuid:unicast-42</UDN>
    <serviceList>
      <service>
        <serviceType>urn:schemas-upnp-org:service:ContentDirectory:1</serviceType>
        <controlURL>/ctl</controlURL>
      </service>
    </serviceList>
  </device>
</root>`
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(descXML))
	}))
	defer web.Close()

	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("responder listen: %v", err)
	}
	defer udp.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, raddr, rerr := udp.ReadFromUDP(buf)
			if rerr != nil {
				return
			}
			if !strings.Contains(string(buf[:n]), "M-SEARCH") {
				continue
			}
			resp := "HTTP/1.1 200 OK\r\n" +
				"ST: urn:schemas-upnp-org:device:MediaServer:1\r\n" +
				"LOCATION: " + web.URL + "/desc.xml\r\n\r\n"
			_, _ = udp.WriteToUDP([]byte(resp), raddr)
		}
	}()

	oldPort := searchHostPort
	searchHostPort = udp.LocalAddr().(*net.UDPAddr).Port
	defer func() { searchHostPort = oldPort }()

	got, err := SearchHost(context.Background(), "127.0.0.1", 2*time.Second)
	if err != nil {
		t.Fatalf("SearchHost: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 server, got %d", len(got))
	}
	if got[0].UDN != "uuid:unicast-42" || got[0].CDSControlURL == "" {
		t.Errorf("server = %+v, want UDN uuid:unicast-42 with a control URL", got[0])
	}
}

// TestSearchHostRejectsNonIP: the host comes from the firmware's own list and
// must already be an address; anything else is a caller bug, not a probe.
func TestSearchHostRejectsNonIP(t *testing.T) {
	if _, err := SearchHost(context.Background(), "fritz.box", time.Second); err == nil {
		t.Error("hostname: want error, got nil")
	}
}
