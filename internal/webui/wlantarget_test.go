package webui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/anonymise"
)

// useTempWlanTarget points the intent record at a temp file for one test.
func useTempWlanTarget(t *testing.T) string {
	t.Helper()
	prev := wlanTargetPath
	p := filepath.Join(t.TempDir(), "wlan-target")
	wlanTargetPath = p
	t.Cleanup(func() { wlanTargetPath = prev })
	return p
}

// The record has to survive a power cycle intact, which is the entire point of
// it: the one-shot boot marker it replaces was consumed on read and therefore
// never protected a SECOND power cycle (#479).
func TestWlanTargetRoundTrip(t *testing.T) {
	useTempWlanTarget(t)

	in := wlanTarget{SSID: "HomeNet", PSK: "supersecret", Hidden: true, Verify: "live", Gen: 3, SetAt: 1700000000}
	if err := writeWlanTarget(in); err != nil {
		t.Fatalf("writeWlanTarget: %v", err)
	}
	got, ok := readWlanTarget()
	if !ok {
		t.Fatal("readWlanTarget reported no record right after writing one")
	}
	if got != in {
		t.Errorf("round trip changed the record:\ngot  %+v\nwant %+v", got, in)
	}
}

// Absent, empty, corrupt, or SSID-less all mean the same thing: no intent. The
// guard must stay silent rather than act on a record it does not understand.
func TestWlanTargetTolerance(t *testing.T) {
	p := useTempWlanTarget(t)

	if _, ok := readWlanTarget(); ok {
		t.Error("a missing record must read as no intent")
	}

	for name, body := range map[string]string{
		"empty file":     "",
		"truncated json": `{"ssid":"HomeNet","psk":"super`,
		"not json":       "SSID=HomeNet\nPASS=supersecret\n",
		"wrong types":    `{"ssid":42,"gen":"three"}`,
		"no ssid":        `{"psk":"supersecret","gen":2}`,
	} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("%s: seeding: %v", name, err)
		}
		got, ok := readWlanTarget()
		if ok {
			t.Errorf("%s: read as a valid record: %+v", name, got)
		}
		if got != (wlanTarget{}) {
			t.Errorf("%s: returned a non-zero record: %+v", name, got)
		}
	}
}

// The failure budget only moves for the right reasons: a matching boot clears
// it, a failed correction burns one, and a stand-down burns nothing. That last
// rule is what keeps a speaker whose intended network is simply switched off
// from spending its whole budget waiting for it to come back.
func TestWlanTargetVerdictMovesTheBudget(t *testing.T) {
	useTempWlanTarget(t)

	if err := writeWlanTarget(wlanTarget{SSID: "HomeNet", PSK: "supersecret", Gen: 1}); err != nil {
		t.Fatalf("writeWlanTarget: %v", err)
	}

	noteWlanTargetVerdict("stood-down:"+guardReasonNotInRange, budgetHold)
	got, _ := readWlanTarget()
	if got.BootsFailed != 0 {
		t.Errorf("a stand-down burned an attempt: bootsFailed = %d, want 0", got.BootsFailed)
	}
	if got.LastVerdict != "stood-down:"+guardReasonNotInRange {
		t.Errorf("verdict not recorded: %q", got.LastVerdict)
	}

	noteWlanTargetVerdict("failed", budgetBurn)
	noteWlanTargetVerdict("failed", budgetBurn)
	got, _ = readWlanTarget()
	if got.BootsFailed != 2 {
		t.Errorf("bootsFailed = %d after two failed corrections, want 2", got.BootsFailed)
	}

	noteWlanTargetVerdict(guardReasonOnTarget, budgetReset)
	got, _ = readWlanTarget()
	if got.BootsFailed != 0 {
		t.Errorf("a boot on the intended network must clear the budget, got %d", got.BootsFailed)
	}
	// The credentials must survive every verdict: the record is the standing
	// intent, not a scratch pad.
	if got.SSID != "HomeNet" || got.PSK != "supersecret" || got.Gen != 1 {
		t.Errorf("a verdict damaged the record: %+v", got)
	}

	// No record, nothing to note, and nothing created behind the user's back.
	if err := clearWlanTarget(); err != nil {
		t.Fatalf("clearWlanTarget: %v", err)
	}
	noteWlanTargetVerdict("failed", budgetBurn)
	if _, ok := readWlanTarget(); ok {
		t.Error("noting a verdict resurrected a cleared record")
	}
}

// Every user-initiated move bumps the generation, which is how a correction
// already in flight notices it is chasing a network the user has since
// replaced and stands down instead of fighting them.
func TestArmWlanTargetBumpsTheGeneration(t *testing.T) {
	useTempWlanTarget(t)
	s := quietServer("")

	s.armWlanTarget("HomeNet", "supersecret", false, "live")
	first, ok := readWlanTarget()
	if !ok {
		t.Fatal("arming wrote no record")
	}
	if first.Gen != 1 || first.Verify != "live" || first.SSID != "HomeNet" {
		t.Fatalf("first arm: %+v", first)
	}
	if first.SetAt == 0 {
		t.Error("first arm left no timestamp")
	}

	// A second move, and this one is the weak kind: requested but never
	// observed, which must never be presented as a verified move.
	s.armWlanTarget("Cellar", "othersecret", true, "weak")
	second, _ := readWlanTarget()
	if second.Gen != 2 {
		t.Errorf("gen = %d after a second move, want 2", second.Gen)
	}
	if second.SSID != "Cellar" || !second.Hidden || second.Verify != "weak" {
		t.Errorf("second arm: %+v", second)
	}
	// A fresh move starts with a fresh budget: the previous network's failures
	// say nothing about this one.
	if second.BootsFailed != 0 {
		t.Errorf("a new move must start with an empty budget, got %d", second.BootsFailed)
	}
}

// Clearing must be idempotent: the rollback path calls it on every failed
// switch, including the ones where no record was ever written.
func TestClearWlanTargetIsIdempotent(t *testing.T) {
	useTempWlanTarget(t)
	if err := clearWlanTarget(); err != nil {
		t.Errorf("clearing a record that was never written: %v", err)
	}
	if err := writeWlanTarget(wlanTarget{SSID: "HomeNet"}); err != nil {
		t.Fatalf("writeWlanTarget: %v", err)
	}
	if err := clearWlanTarget(); err != nil {
		t.Fatalf("clearWlanTarget: %v", err)
	}
	if _, ok := readWlanTarget(); ok {
		t.Error("the record survived clearing")
	}
}

// An agent respawn is not a boot: the firmware did not re-pick a network, so
// there is nothing to correct and no reason to put the radio through a site
// survey. The record must come back untouched.
func TestBootGuardIgnoresAnAgentRespawn(t *testing.T) {
	useTempWlanTarget(t)
	s := quietServer("")
	if err := writeWlanTarget(wlanTarget{SSID: "HomeNet", PSK: "supersecret", Gen: 1}); err != nil {
		t.Fatalf("writeWlanTarget: %v", err)
	}

	s.StartWLANBootGuard(t.Context(), "agent-respawn (box already up 9h03m)")

	got, ok := readWlanTarget()
	if !ok {
		t.Fatal("the record vanished")
	}
	if got.LastVerdict != "" || got.BootsFailed != 0 {
		t.Errorf("an agent respawn recorded a boot verdict: %+v", got)
	}
}

// The tag is what makes a bundle decidable: it separates two networks without
// naming either. Stability across calls is the value, because a log line is
// compared against other lines in the same log.
//
// The contract CHANGED on 2026-09-26 and this test changed with it. It used to
// be an unsalted hash plus the SSID's length, and that made it a household join
// key: 46 of 72 sample bundles carried one, and the most common tag tied 15
// bundles into a single home that anyone downloading the attachments could
// follow (#971). The length narrowed a guess before it began. Both are gone.
func TestSSIDTag(t *testing.T) {
	const ssid = "HomeNet"

	tag := ssidTag(ssid)
	if tag != ssidTag(ssid) {
		t.Error("ssidTag is not stable across calls")
	}
	if !strings.HasPrefix(tag, "ssid:") {
		t.Fatalf("ssidTag(%q) = %q, want an ssid: prefix", ssid, tag)
	}
	hash := strings.TrimPrefix(tag, "ssid:")
	for _, r := range hash {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("hash part of %q is not lowercase hex", tag)
		}
	}
	// The length must NOT be recoverable from the tag.
	if strings.Contains(tag, strconv.Itoa(len(ssid))) && len(hash) < 8 {
		t.Errorf("the tag %q still carries the network name's length", tag)
	}
	// Salted: the same network on another speaker must not produce this tag.
	before := tag
	anonymise.SetSalt([]byte("another-speaker"))
	after := ssidTag(ssid)
	anonymise.SetSalt(nil)
	if before == after {
		t.Error("two speakers produced the same tag for one network, which is the join key this replaced")
	}

	// Different networks must be distinguishable, including two of the same
	// length: four indistinguishable networks in a bundle is exactly the state
	// this replaces.
	if ssidTag("HomeNet") == ssidTag("OldNet1") {
		t.Error("two different SSIDs of the same length share a tag")
	}
	// And it must not be the SSID.
	if strings.Contains(tag, ssid) {
		t.Errorf("ssidTag leaked the network name: %q", tag)
	}

	// "not associated" has to be sayable. It must be non-empty so a log field
	// never reads as missing data.
	if got := ssidTag(""); got != "none" {
		t.Errorf("ssidTag(\"\") = %q, want %q", got, "none")
	}

	// A multi-byte network name is handled like any other and says nothing about
	// its own size. The byte length used to be part of the tag, which is one of
	// the things that made it guessable.
	if got := ssidTag("Küche"); !strings.HasPrefix(got, "ssid:") || strings.Contains(got, "6") && len(got) < 10 {
		t.Errorf("ssidTag(%q) = %q, want a plain salted tag", "Küche", got)
	}
}
