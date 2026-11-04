package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The phone remote's group members get a minus and a plus beside their
// slider, and the Play tab gets the same rows folded under the group volume
// (asked for by a group owner, 2026-11-04). These tests pin the parts that
// make that safe on a phone: the steppers send the member write the slider
// sends, the folded list keeps its own row map instead of rebuilding on every
// poll, and it only exists for a group that is not a stereo pair.

func TestPhoneRemoteMemberSteppersSendTheMemberWrite(t *testing.T) {
	line := jsFunc(t, "memberSlideLine")
	for _, want := range []string{
		// Same send as the slider, clamped to the speaker's range.
		"Math.max(0, Math.min(100, (parseInt(sl.value, 10) || 0) + d))",
		"memberVol(m.ip, v);",
		// The main rockers' hold-to-repeat, not a second implementation.
		"bindHoldStep(b, function(){",
		"stepper('−', -1, T.memDownAria",
		"stepper('+', 1, T.memUpAria",
		// The slider keeps its scroll guard and its drag guard.
		"if (!sliderBlocked(sl)) memberVol(m.ip, sl.value);",
		"memberDragging = m.ip;",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("memberSlideLine is missing %q", want)
		}
	}
	if !strings.Contains(jsFunc(t, "renderMembers"), "memberSlideLine(m, val)") {
		t.Error("the Speakers tab member rows must be built with memberSlideLine")
	}
	if !strings.Contains(jsFunc(t, "bindVolHold"), "bindHoldStep(") {
		t.Error("the main rockers and the member steppers must share bindHoldStep")
	}
	// The write itself is unchanged: one POST per member to the zone volume.
	if !strings.Contains(jsFunc(t, "memberVol"), "api('/api/box/zone/volume', 'POST', { ip: ip, value: +v })") {
		t.Error("memberVol must keep posting {ip, value} to /api/box/zone/volume")
	}
	if !strings.Contains(indexHTML, ".member .mstep {") {
		t.Error("the member steppers need their own size rule")
	}
}

func TestPhoneRemotePlayTabListsTheGroupsSpeakers(t *testing.T) {
	if !regexp.MustCompile(`<details class="grpeach" id="grpEach" hidden>`).MatchString(indexHTML) {
		t.Fatal("the Play tab needs the folded per-speaker list, hidden (and closed) by default")
	}
	// It sits in the volume card, under the group volume.
	vol := strings.Index(indexHTML, `id="volScopeHint"`)
	det := strings.Index(indexHTML, `id="grpEach"`)
	play := strings.Index(indexHTML, `id="lblPlayback"`)
	if vol < 0 || det < vol || det > play {
		t.Error("the per-speaker list must sit under the group volume, before the playback card")
	}
	r := jsFunc(t, "renderGrpEach")
	for _, want := range []string{
		// Only for a real group: never alone, never for a stereo pair.
		"var show = !!(zone.grouped && !zone.stereo && zone.members.length > 1);",
		"box.hidden = !show;",
		// Built only when the membership changes, into its own row map.
		"if (list.getAttribute('data-ips') === want) return;",
		"grpEachRows[m.ip] =",
		"memberSlideLine(m, val)",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("renderGrpEach is missing %q", want)
		}
	}
	// A dissolve must take the list away too, not only the group card.
	if !strings.Contains(jsFunc(t, "loadZone"), "renderGrpEach();") {
		t.Error("loadZone must hide the per-speaker list when the group is gone")
	}
	// Values are written in place into both maps, never by a rebuild.
	p := jsFunc(t, "paintMemberLevels")
	if !strings.Contains(p, "[memberRows[m.ip], grpEachRows[m.ip]]") || !strings.Contains(p, "if (memberDragging === m.ip) return;") {
		t.Error("paintMemberLevels must update both row maps and leave a dragged slider alone")
	}
	// The open state is a per-phone preference and survives a blocked storage.
	if !strings.Contains(indexHTML, "try { if (localStorage.getItem(GRP_EACH_KEY) === '1') d.open = true; } catch (e) {}") {
		t.Error("the folded list must read its open state from localStorage inside try/catch")
	}
}

func TestPhoneRemoteLocalesCarryTheMemberStepKeys(t *testing.T) {
	bundles := strings.Count(indexHTML, "now:\"")
	if bundles == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	for _, key := range []string{"memDownAria", "memUpAria", "grpEachHead"} {
		got := len(regexp.MustCompile(key+`:"`).FindAllString(indexHTML, -1))
		if got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
	for _, key := range []string{"memDownAria", "memUpAria"} {
		got := len(regexp.MustCompile(key+`:"[^"]*\{\{name\}\}[^"]*"`).FindAllString(indexHTML, -1))
		if got != bundles {
			t.Errorf("%s: every locale must name the speaker with {{name}}, %d of %d do", key, got, bundles)
		}
	}
	if !strings.Contains(indexHTML, `de:{memDownAria:"{{name}} leiser",memUpAria:"{{name}} lauter",grpEachHead:"Lautstärke pro Lautsprecher"`) {
		t.Error("the German strings for the member steppers are missing or changed")
	}
}

// jsFunc returns the source of the top-level function name in indexHTML, from
// its declaration up to the next top-level declaration.
func jsFunc(t *testing.T, name string) string {
	t.Helper()
	start := strings.Index(indexHTML, "\nfunction "+name+"(")
	if start < 0 {
		start = strings.Index(indexHTML, "\nasync function "+name+"(")
	}
	if start < 0 {
		t.Fatalf("function %s not found in indexHTML", name)
	}
	rest := indexHTML[start+1:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatalf("end of function %s not found", name)
	}
	return rest[:end+2]
}
