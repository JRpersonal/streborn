package oled

import (
	"os"
	"path/filepath"
	"testing"
)

func meanLevel(buf []byte) float64 {
	s := 0
	for _, v := range buf {
		s += int(v)
	}
	return float64(s) / float64(len(buf))
}

func TestLogoLifecycle(t *testing.T) {
	buf := make([]byte, FrameSize)
	l := NewLogo("UPDATED", 6.5, 1)
	l.Frame(0, buf)
	if m := meanLevel(buf); m > 0.2 {
		t.Fatalf("first frame should be almost empty, mean %.2f", m)
	}
	l.Frame(4, buf)
	mid := meanLevel(buf)
	if mid < 0.3 {
		t.Fatalf("assembled logo too dark, mean %.2f", mid)
	}
	// Burn-in budget: the splash must stay dim on average (the stock standby
	// clock measures 0.53 of 15 on the panel).
	if mid > 2 {
		t.Fatalf("assembled logo too bright, mean %.2f", mid)
	}
	for _, v := range buf {
		if v > 15 {
			t.Fatalf("grey level %d out of range", v)
		}
	}
	if l.Done(6) {
		t.Fatal("done before the end")
	}
	l.Frame(6.49, buf)
	if m := meanLevel(buf); m > 0.3 {
		t.Fatalf("zoom-out should leave the panel nearly dark, mean %.2f", m)
	}
	if !l.Done(6.5) {
		t.Fatal("not done at the end")
	}
}

func TestLogoHoldExits(t *testing.T) {
	buf := make([]byte, FrameSize)
	l := NewLogo("UPDATING", 0, 2)
	l.Hold = true
	for _, ts := range []float64{1, 5, 60} {
		l.Frame(ts, buf)
		if l.Done(ts) {
			t.Fatalf("hold logo done at %.0fs without Exit", ts)
		}
	}
	l.Exit(60)
	if l.Done(61) {
		t.Fatal("done before the zoom finished")
	}
	if !l.Done(62) {
		t.Fatal("not done after the zoom")
	}
}

// The splash's subtitles (and the games' titles under the same mark) fit the
// panel in the large face, and are drawn in it: anti-aliased, on the rows
// under the mark, not in the dot matrix the mark is made of.
func TestSubtitlesFitAndRender(t *testing.T) {
	subs := []string{"UPDATING"}
	for _, k := range []Kind{KindBoot, KindInstalled, KindUpdated} {
		subs = append(subs, k.subtitle())
	}
	for _, g := range Games {
		subs = append(subs, g.Title)
	}
	for _, s := range subs {
		if w := smoothLarge.width(s); w > Width-8 {
			t.Errorf("%q is %d px wide", s, w)
		}
		if sanitizeSmooth(s, trackTextMax) != s {
			t.Errorf("%q is not drawable as is", s)
		}
		buf := make([]byte, FrameSize)
		NewLogo(s, 6.5, 1).Frame(4, buf)
		top := subBase - smoothLarge.capH
		if litIn(buf, 0, top, Width, smoothLarge.capH) < 40 {
			t.Errorf("%q: subtitle not drawn", s)
		}
		if greyIn(buf, 0, top, Width, smoothLarge.capH, subLvl) == 0 {
			t.Errorf("%q: subtitle has no anti-aliased pixels", s)
		}
	}
	// While it zooms out, the subtitle grows with the mark and fades.
	l := NewLogo("UPDATED", 6.5, 1)
	still, zoom := make([]byte, FrameSize), make([]byte, FrameSize)
	l.Frame(4, still)
	l.Frame(5.3, zoom)
	if litIn(zoom, 0, 0, Width, Height) == 0 || meanLevel(zoom) >= meanLevel(still)*4 {
		t.Errorf("zoom frame: mean %.2f against %.2f standing", meanLevel(zoom), meanLevel(still))
	}
}
func TestDecideKind(t *testing.T) {
	dir := t.TempDir()
	SeenPath = filepath.Join(dir, "splash-seen")
	defer func() { SeenPath = "/mnt/nv/streborn/splash-seen" }()

	if k := DecideKind("1.0.2", false, true); k != KindInstalled {
		t.Fatalf("first start: %v", k)
	}
	if k := DecideKind("1.0.2", false, true); k != KindBoot {
		t.Fatalf("plain boot: %v", k)
	}
	if k := DecideKind("1.0.2", false, false); k != KindNone {
		t.Fatalf("agent respawn: %v", k)
	}
	if k := DecideKind("1.0.3", false, false); k != KindUpdated {
		t.Fatalf("version changed: %v", k)
	}
	if k := DecideKind("1.0.3", true, true); k != KindUpdated {
		t.Fatalf("ota marker: %v", k)
	}
	b, _ := os.ReadFile(SeenPath)
	if string(b) != "1.0.3\n" {
		t.Fatalf("seen marker %q", b)
	}
}

func TestEnabledFlag(t *testing.T) {
	dir := t.TempDir()
	FlagPath = filepath.Join(dir, "display-splash")
	defer func() { FlagPath = "/mnt/nv/streborn/display-splash" }()
	if !Enabled() {
		t.Fatal("absent flag must mean on")
	}
	os.WriteFile(FlagPath, []byte("0\n"), 0o644)
	if Enabled() {
		t.Fatal("0 must mean off")
	}
	os.WriteFile(FlagPath, []byte("1"), 0o644)
	if !Enabled() {
		t.Fatal("1 must mean on")
	}
}
