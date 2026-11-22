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
		switch e {
		case "served":
			e = fmt.Sprintf(`{"udn":"uuid:str-usb-aabbccddeeff","name":"USB stick (Kitchen)","kind":"usb","location":"http://%s/usb/device.xml"}`, r.Host)
		case "served-label":
			e = fmt.Sprintf(`{"udn":"uuid:str-usb-aabbccddeeff","name":"USB stick MUSIK (Küche)","kind":"usb","location":"http://%s/usb/device.xml","speaker":"Küche","label":"MUSIK"}`, r.Host)
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

// Discussion #1136 (ST20, 2026-11-22): the stick showed as "USB stick
// (str-192.168.1.213)". A speaker found by probing its address is called
// "str-<ip>" in BoxInfo.Name, and that placeholder won over the speaker's
// real name. The name the app shows the speaker by wins now, then the name
// the agent gives itself, and a placeholder never.
func TestTheStickIsListedUnderTheSpeakersNameNotThePlaceholder(t *testing.T) {
	box := fakeUSBAgent(t, "served")
	box.Name, box.FriendlyName = "str-"+box.Host, "Wohnzimmer"
	sticks, _ := fetchSpeakerUSB(context.Background(), http.DefaultClient, box)
	if len(sticks) != 1 || sticks[0].Speaker != "Wohnzimmer" {
		t.Fatalf("sticks %+v", sticks)
	}

	cases := []struct {
		box   BoxInfo
		agent string
		want  string
	}{
		{BoxInfo{Name: "str-192.168.1.213", FriendlyName: "Küche", Host: "192.168.1.213"}, "Kitchen", "Küche"},
		{BoxInfo{Name: "str-192.168.1.213", Host: "192.168.1.213"}, "Küche", "Küche"},
		{BoxInfo{Name: "STR-3E6CE1", Host: "192.168.1.213"}, "", "192.168.1.213"},
		{BoxInfo{Name: "str-192.168.1.213", Host: "192.168.1.213"}, "", "192.168.1.213"},
		{BoxInfo{Name: "str-Kueche", Host: "192.168.1.213"}, "", "str-Kueche"},
		{BoxInfo{Name: "Bad", Host: "192.168.1.213"}, "", "Bad"},
	}
	for _, c := range cases {
		if got := usbSpeakerName(c.box, c.agent); got != c.want {
			t.Errorf("%+v / agent %q: got %q, want %q", c.box, c.agent, got, c.want)
		}
	}
}

// A newer agent names the speaker and the stick's volume label itself; the
// Library carries both.
func TestTheStickCarriesItsVolumeLabel(t *testing.T) {
	box := fakeUSBAgent(t, "served-label")
	box.Name = "str-" + box.Host
	sticks, _ := fetchSpeakerUSB(context.Background(), http.DefaultClient, box)
	if len(sticks) != 1 || sticks[0].Speaker != "Küche" || sticks[0].Label != "MUSIK" {
		t.Fatalf("sticks %+v", sticks)
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
