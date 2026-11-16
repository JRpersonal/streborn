package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A stereo pair joins a group as ONE member, named by its pair master, the way
// the Bose app forms it (a SoundTouch 30 leading a pair of SoundTouch 10s,
// bundle 2026-11-15), and never leads one (#792).

const pairDocJSON = `{"id":"str-grp-L","masterDeviceID":"LEFTID","name":"Kitchen","status":"GROUP_OK",` +
	`"members":[{"deviceID":"LEFTID","ip":"192.0.2.19","role":"LEFT"},{"deviceID":"RIGHTID","ip":"192.0.2.11","role":"RIGHT"}]}`

func testPairDoc(t *testing.T) *stereoPairDoc {
	t.Helper()
	var d stereoPairDoc
	if err := json.Unmarshal([]byte(pairDocJSON), &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

func TestPairUnitSlavesTakesAPairInByItsPairMaster(t *testing.T) {
	doc := testPairDoc(t)
	pairOf := func(ip string) *stereoPairDoc {
		if ip == "192.0.2.19" || ip == "192.0.2.11" {
			return doc
		}
		return nil
	}
	ips := func(ms []ZoneMember) string {
		out := make([]string, 0, len(ms))
		for _, m := range ms {
			out = append(out, m.DeviceID+"@"+m.IP)
		}
		return strings.Join(out, ",")
	}

	// The pair master as a member: kept as it is.
	got, mapped, _ := pairUnitSlaves([]ZoneMember{{DeviceID: "LEFTID", IP: "192.0.2.19"}, {DeviceID: "OTHER", IP: "192.0.2.30"}}, pairOf)
	if ips(got) != "LEFTID@192.0.2.19,OTHER@192.0.2.30" || len(mapped) != 0 {
		t.Fatalf("got %s mapped %v", ips(got), mapped)
	}
	// The other half alone means the pair.
	got, mapped, _ = pairUnitSlaves([]ZoneMember{{DeviceID: "RIGHTID", IP: "192.0.2.11"}}, pairOf)
	if ips(got) != "LEFTID@192.0.2.19" || len(mapped) != 1 {
		t.Fatalf("got %s mapped %v, want the pair master", ips(got), mapped)
	}
	// Both halves: one member.
	got, _, _ = pairUnitSlaves([]ZoneMember{{DeviceID: "RIGHTID", IP: "192.0.2.11"}, {DeviceID: "LEFTID", IP: "192.0.2.19"}}, pairOf)
	if ips(got) != "LEFTID@192.0.2.19" {
		t.Fatalf("got %s, want the pair once", ips(got))
	}
	// A pair the app cannot address by its master is left out.
	got, _, bad := pairUnitSlaves([]ZoneMember{{DeviceID: "X", IP: "192.0.2.40"}}, func(string) *stereoPairDoc { return &stereoPairDoc{} })
	if len(got) != 0 || len(bad) != 1 {
		t.Fatalf("got %s bad %v, want the unaddressable half dropped", ips(got), bad)
	}
}

// A pair half as the main speaker is refused before anything is sent.
func TestFormZoneRefusesAPairHalfAsTheMainSpeaker(t *testing.T) {
	var posts atomic.Int32
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		_, _ = w.Write([]byte(`{"members":[],"stereo":` + pairDocJSON + `}`))
	}))
	defer agent.Close()
	a := newTestApp()
	a.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	res, err := a.FormZone("127.0.0.1", listenPort(t, agent), ZoneSpec{
		Master: ZoneMember{DeviceID: "LEFTID"},
		Slaves: []ZoneMember{{DeviceID: "OTHER"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res["ok"] != false || res["code"] != "pair-cannot-lead" {
		t.Fatalf("result = %v, want the pair-cannot-lead refusal", res)
	}
	if posts.Load() != 0 {
		t.Fatal("the refused group was sent to the speaker anyway")
	}
}

// The agent's own refusal (409, pair-cannot-lead) reaches the view as a
// refusal with its reason, not as a transport failure.
func TestFormZonePassesTheAgentsPairRefusalOn(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"ok":false,"code":"pair-cannot-lead","inPair":["192.0.2.19"],"error":"half of a pair"}`))
			return
		}
		_, _ = w.Write([]byte(`{"members":[]}`))
	}))
	defer agent.Close()
	a := newTestApp()
	a.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	res, err := a.FormZone("127.0.0.1", listenPort(t, agent), ZoneSpec{
		Master: ZoneMember{DeviceID: "M"},
		Slaves: []ZoneMember{{DeviceID: "OTHER"}},
	})
	if err != nil {
		t.Fatalf("a coded refusal came back as an error: %v", err)
	}
	if res["ok"] != false || res["code"] != "pair-cannot-lead" {
		t.Fatalf("result = %v", res)
	}
}
