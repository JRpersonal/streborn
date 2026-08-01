package main

// Live stereo-pair + credential-copy harness. Drives the SAME bound App
// methods the Multi-Room and Speaker Settings buttons call, against two real
// SoundTouch 10s, with no GUI and no manual SSH/curl (which would test a
// different path than users take). Env-gated so CI never touches hardware:
//
//	STR_LIVE_STEREO_LEFT=192.168.178.x STR_LIVE_STEREO_RIGHT=192.168.178.y \
//	  go test -run LiveStereoPair -v -timeout 10m
//	STR_LIVE_COPY_SRC=192.168.178.x:17008 STR_LIVE_COPY_DST=192.168.178.y:8888 \
//	  go test -run LiveSpotifyCredentialCopy -v -timeout 5m
//
// The stereo test forms a REAL pair (audible relay clicks are normal), so set
// both speakers to a low volume first. It always dissolves the pair again,
// runs a second form/dissolve round (the old code left the partner's stale
// group behind, so exactly the SECOND round used to fail with 5510
// GROUP_ALREADY_EXISTS), and asserts both marges carry the identical
// canonical pair document while paired (the RIGHT box storing ITSELF as
// master is the field bug this guards against).

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func liveDeviceID(t *testing.T, host string) string {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://%s:8090/info", host))
	if err != nil {
		t.Fatalf("%s /info: %v", host, err)
	}
	defer resp.Body.Close()
	var info struct {
		DeviceID string `xml:"deviceID,attr"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err := xml.Unmarshal(body, &info); err != nil || info.DeviceID == "" {
		t.Fatalf("%s /info parse: err=%v id=%q", host, err, info.DeviceID)
	}
	return info.DeviceID
}

// liveMargeGroup reads a box's marge pair record via the agent relay endpoint.
func liveMargeGroup(t *testing.T, host string) (status int, xmlDoc string, canonical bool) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://%s:8888/api/marge/group", host))
	if err != nil {
		t.Fatalf("%s /api/marge/group: %v", host, err)
	}
	defer resp.Body.Close()
	var out struct {
		XML       string `json:"xml"`
		Canonical bool   `json:"canonical"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.XML, out.Canonical
}

func liveFirmwareGroupMembers(t *testing.T, host string) int {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://%s:8090/getGroup", host))
	if err != nil {
		t.Fatalf("%s /getGroup: %v", host, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return strings.Count(string(body), "<groupRole>")
}

func TestLiveStereoPairRoundTrip(t *testing.T) {
	left := os.Getenv("STR_LIVE_STEREO_LEFT")
	right := os.Getenv("STR_LIVE_STEREO_RIGHT")
	if left == "" || right == "" {
		t.Skip("set STR_LIVE_STEREO_LEFT and STR_LIVE_STEREO_RIGHT to run against real speakers")
	}
	a := NewApp()
	leftID := liveDeviceID(t, left)
	rightID := liveDeviceID(t, right)
	t.Logf("left=%s (%s) right=%s (%s)", left, leftID, right, rightID)

	for round := 1; round <= 2; round++ {
		t.Logf("=== round %d: form pair", round)
		out, err := a.FormZone(left, 8888, ZoneSpec{
			Master: ZoneMember{DeviceID: leftID, IP: left},
			Slaves: []ZoneMember{{DeviceID: rightID, IP: right}},
			Stereo: true,
		})
		if err != nil {
			t.Fatalf("round %d FormZone: %v", round, err)
		}
		t.Logf("round %d FormZone result: %v", round, out)
		if ok, _ := out["ok"].(bool); !ok {
			t.Fatalf("round %d: pair did not form: %v", round, out)
		}
		// The relay (direct agent push, or the app fallback FormZone runs)
		// must have landed the canonical document on the partner.
		if synced, _ := out["partnerMargeSynced"].(bool); !synced {
			t.Fatalf("round %d: canonical document never reached the partner marge: %v", round, out)
		}

		// Both marges must carry the identical canonical record naming LEFT as
		// master (the field bug: the RIGHT marge stored RIGHT as master).
		stL, docL, canL := liveMargeGroup(t, left)
		stR, docR, canR := liveMargeGroup(t, right)
		if stL != http.StatusOK || stR != http.StatusOK {
			t.Fatalf("round %d: marge records missing while paired: left=%d right=%d", round, stL, stR)
		}
		if !canL || !canR {
			t.Errorf("round %d: record not canonical: left=%v right=%v", round, canL, canR)
		}
		if docL != docR {
			t.Errorf("round %d: pair documents DIVERGED:\nleft:  %s\nright: %s", round, docL, docR)
		}
		if !strings.Contains(docR, "<masterDeviceId>"+leftID+"</masterDeviceId>") {
			t.Errorf("round %d: right marge does not name LEFT as master: %s", round, docR)
		}
		if n := liveFirmwareGroupMembers(t, left); n != 2 {
			t.Errorf("round %d: left firmware reports %d group members, want 2", round, n)
		}

		t.Logf("=== round %d: dissolve pair", round)
		// Round 1 dissolves via the plain endpoint (persisted-store path, what
		// older UI flows call); round 2 via the stereo-intent endpoint the
		// undo-pair button uses (also covers the ?stereo=1 escalation wiring).
		var derr error
		if round == 1 {
			derr = a.DissolveZone(left, 8888)
		} else {
			derr = a.DissolveStereoPair(left, 8888)
		}
		if derr != nil {
			t.Fatalf("round %d dissolve: %v", round, derr)
		}
		// Both firmwares and both marges must let go (poll: teardown settles
		// asynchronously on the boxes).
		deadline := time.Now().Add(30 * time.Second)
		for {
			nL := liveFirmwareGroupMembers(t, left)
			nR := liveFirmwareGroupMembers(t, right)
			stL, _, _ := liveMargeGroup(t, left)
			stR, _, _ := liveMargeGroup(t, right)
			if nL == 0 && nR == 0 && stL == http.StatusNotFound && stR == http.StatusNotFound {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("round %d: dissolve incomplete: fwLeft=%d fwRight=%d margeLeft=%d margeRight=%d",
					round, nL, nR, stL, stR)
			}
			time.Sleep(2 * time.Second)
		}
		t.Logf("round %d: dissolved clean on both speakers", round)
	}
}

func TestLiveSpotifyCredentialCopy(t *testing.T) {
	src := os.Getenv("STR_LIVE_COPY_SRC")
	dst := os.Getenv("STR_LIVE_COPY_DST")
	if src == "" || dst == "" {
		t.Skip("set STR_LIVE_COPY_SRC and STR_LIVE_COPY_DST (host:port) to run against real speakers")
	}
	parse := func(v string) (string, int) {
		host, port, ok := strings.Cut(v, ":")
		if !ok {
			return v, 8888
		}
		var p int
		fmt.Sscanf(port, "%d", &p)
		return host, p
	}
	srcHost, srcPort := parse(src)
	dstHost, dstPort := parse(dst)

	credStatus := func(host string, port int) int {
		resp, err := http.Get(fmt.Sprintf("http://%s:%d/spotify/credential", host, port))
		if err != nil {
			t.Fatalf("%s credential read: %v", host, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	before := credStatus(dstHost, dstPort)
	t.Logf("target credential before copy: %d", before)

	a := NewApp()
	copied, err := a.CopyPresetsAcrossBoxes(srcHost, srcPort, dstHost, dstPort)
	if err != nil {
		t.Logf("per-slot errors (non-fatal): %v", err)
	}
	t.Logf("presets copied: %d", copied)
	if copied == 0 {
		t.Fatal("no presets copied")
	}

	// The point of the fix: the login must now be ON the target.
	if after := credStatus(dstHost, dstPort); after != http.StatusOK {
		t.Fatalf("target credential after copy: %d, want 200 (login was not transferred)", after)
	}
	t.Log("Spotify login arrived with the presets")
}
