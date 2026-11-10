package webui

// The wire of the stereo-pair form (POST /api/box/zone with "stereo":true) and
// of the dissolve (DELETE /api/box/zone, with and without ?stereo=1 and
// ?forget=1), pinned byte for byte before the handlers were turned into
// service functions.
//
// The desktop app (FormZone, DissolveZone, the undo-pair action), the phone
// remote and the groupkeys client all read these bodies. Every case here is
// one that is reachable with no speaker behind the box host: the host is one
// boxapi refuses to build a URL for (deadhost_test.go), so each firmware call
// fails at once and the answer is what the handler decides on its own. The
// exits that need a firmware answer (a formed pair, an incomplete pair, a
// refused /addGroup, a confirmed firmware pair on dissolve) cannot be reached
// from a unit test, because boxapi pins port 8090.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// wireCase is one request and the exact answer it must get.
type wireCase struct {
	name   string
	status int
	want   string // the exact body, or a prefix when prefix is set
	prefix bool
}

func checkWire(t *testing.T, c wireCase, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != c.status {
		t.Errorf("status = %d, want %d (body %s)", w.Code, c.status, w.Body.String())
	}
	got := w.Body.String()
	if c.prefix && !strings.HasPrefix(got, c.want) {
		t.Errorf("body = %q, want prefix %q", got, c.want)
	}
	if !c.prefix && got != c.want {
		t.Errorf("body = %q, want %q", got, c.want)
	}
}

func TestStereoPairFormWire(t *testing.T) {
	t.Parallel()
	// The master address in the body is a placeholder: the handler talks to
	// its own box host, which is the dead one.
	const master = `"master":{"deviceID":"AABBCCDDEEFF","ip":"192.0.2.10"}`
	onePartner := `{` + master + `,"slaves":[{"deviceID":"112233445566","ip":""}],"stereo":true,"name":"Living room"}`
	permanentGroup := zones.Zone{
		Master: "AABBCCDDEEFF", MasterIP: "192.0.2.10", Permanent: true,
		Slaves: []zones.Member{{DeviceID: "DEV-A", IP: "192.0.2.30"}},
	}
	cases := []struct {
		wireCase
		body   string
		stored *zones.Zone
		// live is what the box answers when asked for its zone; nil leaves
		// the read failing, which the pair guard treats as "go ahead".
		live *boxapi.Zone
	}{
		{wireCase{"two partners", http.StatusBadRequest, "a stereo pair needs exactly one partner speaker\n", false},
			`{` + master + `,"slaves":[{"deviceID":"AAA","ip":""},{"deviceID":"BBB","ip":""}],"stereo":true}`, nil, nil},
		// A loopback partner: an IP address, so it passes the member check,
		// and not a LAN peer. Nothing leaves this machine for it.
		{wireCase{"partner not on the local network", http.StatusBadRequest, "the partner speaker must be on the local network\n", false},
			`{` + master + `,"slaves":[{"deviceID":"112233445566","ip":"127.0.0.9"}],"stereo":true}`, nil, nil},
		{wireCase{"this speaker is in a live multiroom group", http.StatusOK,
			`{"error":"a speaker that is in a multiroom group cannot become half of a stereo pair; dissolve the group first, then create the pair","inZone":["` + deadBoxHost + `"],"ok":false,"stereo":true}` + "\n", false},
			onePartner, nil, &boxapi.Zone{Master: "AABBCCDDEEFF", Members: []boxapi.ZoneMember{{DeviceID: "DEV-A", IP: "192.0.2.30"}}}},
		{wireCase{"this speaker keeps a saved permanent group", http.StatusOK,
			`{"error":"this speaker keeps a saved multiroom group; pairing it would delete that group. Dissolve the group first, then create the pair","inZone":["` + deadBoxHost + `"],"ok":false,"stereo":true,"storedGroup":true}` + "\n", false},
			onePartner, &permanentGroup, &boxapi.Zone{}},
		{wireCase{"the speaker is not answering", http.StatusBadGateway, "the speaker is not answering: ", true},
			onePartner, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var s *Server
			if c.stored != nil {
				s, _ = staleDocServer(t, deadBoxHost, *c.stored, true)
			} else {
				s, _ = staleDocServer(t, deadBoxHost, zones.Zone{}, false)
			}
			if c.live != nil {
				withSpeakers(s, map[string]boxapi.Zone{deadBoxHost: *c.live}, nil)
			}
			r := httptest.NewRequest(http.MethodPost, "/api/box/zone", strings.NewReader(c.body))
			w := httptest.NewRecorder()
			s.handleBoxZone(w, r)
			checkWire(t, c.wireCase, w)
			if c.stored != nil {
				if z, ok := s.zones.Get(); !ok || z.Stereo || !z.Permanent {
					t.Errorf("the refused pairing changed the stored group: %+v (present=%v)", z, ok)
				}
			}
		})
	}
}

func TestZoneDissolveWire(t *testing.T) {
	t.Parallel()
	pair := zones.Zone{
		Master: "DEV-MASTER", MasterIP: deadBoxHost, Stereo: true, Name: "Living room",
		Slaves: []zones.Member{{DeviceID: "DEV-SLAVE", IP: deadMemberA, Role: "RIGHT"}},
	}
	// A pair whose partner address was never learned: nothing is dialled for
	// the partner and the answer carries the empty address.
	pairNoPartnerIP := zones.Zone{
		Master: "DEV-MASTER", MasterIP: deadBoxHost, Stereo: true,
		Slaves: []zones.Member{{DeviceID: "DEV-SLAVE", Role: "RIGHT"}},
	}
	group := multiroomDoc(false)
	// What a dissolve answers when the speaker takes no call at all: nothing
	// was removed and nothing could be read back, and it says both.
	const unreadable = `{"error":"the speaker still reports members in the group","ok":false,"remaining":2,"unverified":"Get \"\": unsupported protocol scheme \"\""}` + "\n"
	saved := multiroomDoc(true)
	cases := []struct {
		wireCase
		query  string
		stored *zones.Zone
		// kept says whether the stored document must still be there afterwards.
		kept bool
	}{
		{wireCase{"plain ungroup leaves a persisted pair alone", http.StatusOK,
			`{"nothing":true,"ok":true,"stereoPairKept":true}` + "\n", false}, "", &pair, true},
		{wireCase{"undo pair dissolves a persisted pair", http.StatusOK,
			`{"ok":true,"partnerDeviceID":"DEV-SLAVE","partnerIP":"` + deadMemberA + `","partnerMargeCleared":false,"stereo":true}` + "\n", false},
			"?stereo=1", &pair, false},
		{wireCase{"undo pair with no partner address", http.StatusOK,
			`{"ok":true,"partnerDeviceID":"DEV-SLAVE","partnerIP":"","partnerMargeCleared":false,"stereo":true}` + "\n", false},
			"?stereo=1", &pairNoPartnerIP, false},
		{wireCase{"undo pair refuses a multiroom group", http.StatusOK,
			`{"inZone":true,"noPair":true,"nothing":true,"ok":false,"pairReadable":false}` + "\n", false},
			"?stereo=1", &group, true},
		{wireCase{"undo pair on a speaker holding nothing", http.StatusOK,
			`{"firmwarePair":false,"nothing":true,"ok":true}` + "\n", false}, "?stereo=1", nil, false},
		{wireCase{"plain ungroup on a speaker holding nothing", http.StatusOK,
			`{"firmwarePair":false,"nothing":true,"ok":true}` + "\n", false}, "", nil, false},
		{wireCase{"plain ungroup whose result cannot be read", http.StatusOK,
			unreadable, false}, "", &group, false},
		{wireCase{"plain ungroup keeps a saved group", http.StatusOK,
			unreadable, false}, "", &saved, true},
		{wireCase{"ungroup and forget deletes a saved group", http.StatusOK,
			unreadable, false}, "?forget=1", &saved, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var s *Server
			var st *zones.Store
			if c.stored != nil {
				s, st = staleDocServer(t, deadBoxHost, *c.stored, true)
			} else {
				s, st = staleDocServer(t, deadBoxHost, zones.Zone{}, false)
			}
			r := httptest.NewRequest(http.MethodDelete, "/api/box/zone"+c.query, nil)
			w := httptest.NewRecorder()
			s.handleBoxZone(w, r)
			checkWire(t, c.wireCase, w)
			if got := storedGroup(t, st); got != c.kept {
				t.Errorf("stored document present = %v, want %v", got, c.kept)
			}
		})
	}
}

// The method switch in front of both handlers.
func TestBoxZoneRefusesOtherMethods(t *testing.T) {
	t.Parallel()
	s := quietServer(deadBoxHost)
	w := httptest.NewRecorder()
	s.handleBoxZone(w, httptest.NewRequest(http.MethodPut, "/api/box/zone", nil))
	if w.Code != http.StatusMethodNotAllowed || w.Body.String() != "method not allowed\n" {
		t.Errorf("PUT answered %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Allow"); got != "GET, POST, DELETE" {
		t.Errorf("Allow = %q", got)
	}
}
