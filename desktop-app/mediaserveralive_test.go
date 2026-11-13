package main

import (
	"testing"
	"time"
)

// #1890: a server repeats its alive for every device and service; the app
// hears it once per server per gap, and every server on its own.
func TestAliveGateOncePerServerPerGap(t *testing.T) {
	g := &aliveGate{}
	t0 := time.Date(2026, 11, 13, 13, 0, 0, 0, time.UTC)
	if !g.pass("uuid:a", t0) {
		t.Fatal("the first alive was held back")
	}
	if g.pass("uuid:a", t0.Add(time.Second)) {
		t.Error("the repeat of the same burst went through")
	}
	if !g.pass("uuid:b", t0.Add(time.Second)) {
		t.Error("another server was held back by the first one's gap")
	}
	if !g.pass("uuid:a", t0.Add(mediaServerAliveGap+time.Second)) {
		t.Error("the alive after the gap was held back")
	}
}

func TestMediaServerAlivePayload(t *testing.T) {
	p := mediaServerAlivePayload("http://192.0.2.20:50001/desc.xml", "uuid:abc::urn:schemas-upnp-org:device:MediaServer:1")
	if p["uuid"] != "uuid:abc" || p["location"] != "http://192.0.2.20:50001/desc.xml" {
		t.Errorf("payload %+v", p)
	}
	if usnUUID("uuid:only") != "uuid:only" {
		t.Error("a USN without a type part is its own uuid")
	}
}
