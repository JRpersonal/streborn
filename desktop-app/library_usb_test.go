package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// fakeUSBAgent answers /api/library/servers the way a speaker's agent does, with
// the stick entry built from the Host the app used.
func fakeUSBAgent(t *testing.T, entry string) BoxInfo {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/library/servers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		e := entry
		if e == "served" {
			e = fmt.Sprintf(`{"udn":"uuid:str-usb-aabbccddeeff","name":"USB stick (Kitchen)","kind":"usb","location":"http://%s/usb/device.xml"}`, r.Host)
		}
		list := `[{"udn":"uuid:nas","name":"NAS"}`
		if e != "" {
			list += "," + e
		}
		_, _ = fmt.Fprint(w, list+"]")
	})
	mux.HandleFunc("/usb/device.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device>`+
			`<deviceType>urn:schemas-upnp-org:device:MediaServer:1</deviceType><friendlyName>USB stick (Kitchen)</friendlyName>`+
			`<UDN>uuid:str-usb-aabbccddeeff</UDN><serviceList><service><serviceType>urn:schemas-upnp-org:service:ContentDirectory:1</serviceType>`+
			`<controlURL>/usb/control</controlURL></service></serviceList></device></root>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	return BoxInfo{Name: "Kitchen", Host: host, Port: p}
}

func TestASpeakersMusicStickJoinsTheLibrary(t *testing.T) {
	box := fakeUSBAgent(t, "served")
	sticks, notices := fetchSpeakerUSB(context.Background(), http.DefaultClient, box)
	if len(notices) != 0 || len(sticks) != 1 {
		t.Fatalf("sticks %+v notices %+v", sticks, notices)
	}
	st := sticks[0]
	want := fmt.Sprintf("http://%s:%d/usb/control", box.Host, box.Port)
	if st.Speaker != "Kitchen" || st.Server.UDN != "uuid:str-usb-aabbccddeeff" || st.Server.CDSControlURL != want {
		t.Fatalf("stick: %+v", st)
	}
}

func TestARefusedStickBecomesANotice(t *testing.T) {
	box := fakeUSBAgent(t, `{"udn":"","name":"USB stick (Kitchen)","kind":"usb","refusal":"not-fat32"}`)
	sticks, notices := fetchSpeakerUSB(context.Background(), http.DefaultClient, box)
	if len(sticks) != 0 || len(notices) != 1 || notices[0] != (LibraryUSBNotice{Speaker: "Kitchen", Code: "not-fat32"}) {
		t.Fatalf("sticks %+v notices %+v", sticks, notices)
	}
}

// An agent older than the feature lists only its registered servers.
func TestAnOlderAgentListsNoStick(t *testing.T) {
	box := fakeUSBAgent(t, "")
	sticks, notices := fetchSpeakerUSB(context.Background(), http.DefaultClient, box)
	if len(sticks) != 0 || len(notices) != 0 {
		t.Fatalf("sticks %+v notices %+v", sticks, notices)
	}
	// And a speaker that is off costs nothing but its timeout.
	off := BoxInfo{Name: "Off", Host: "127.0.0.1", Port: 1}
	if s, n := fetchSpeakerUSB(context.Background(), http.DefaultClient, off); len(s) != 0 || len(n) != 0 {
		t.Fatalf("off: %+v %+v", s, n)
	}
}
