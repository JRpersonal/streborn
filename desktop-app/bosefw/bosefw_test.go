package bosefw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

const base = "https://downloads.bose.com/ced/soundtouch/downloads_stockholm/"

func fixture(t *testing.T) []Entry {
	t.Helper()
	data, err := os.ReadFile("testdata/index.xml")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := ParseIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestParseIndexKeepsSpeakerImagesOnBoseOnly(t *testing.T) {
	entries := fixture(t)
	for _, e := range entries {
		if !strings.HasSuffix(e.FileName, ".stu") {
			t.Errorf("%s: only .stu speaker images belong in the list", e.FileName)
		}
		if !strings.HasPrefix(e.URL, base) {
			t.Errorf("%s: URL %q is not on Bose's download host", e.FileName, e.URL)
		}
		if e.DeviceID == "0x9999" {
			t.Errorf("an entry with a foreign host was kept")
		}
	}
	if len(entries) != 10 {
		t.Errorf("got %d entries, want 10", len(entries))
	}
	var st10 Entry
	for _, e := range entries {
		if e.DeviceID == "0x0939" {
			st10 = e
		}
	}
	if st10.ProductName != "SoundTouch 10" || st10.Release != LatestRelease ||
		st10.URL != base+"Update_ti_27.0.6.46330.5043500.rhino.sm2.stu" {
		t.Errorf("SoundTouch 10 entry parsed wrong: %+v", st10)
	}
}

func TestParseIndexRejectsGarbage(t *testing.T) {
	if _, err := ParseIndex([]byte("<html>not the index</html>")); err == nil {
		t.Error("a page without DEVICE entries must not parse")
	}
	if _, err := ParseIndex([]byte("{")); err == nil {
		t.Error("non-XML must not parse")
	}
}

func names(files []File) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Name)
	}
	return out
}

func TestMatch(t *testing.T) {
	entries := fixture(t)
	cases := []struct {
		name                       string
		model, moduleType, variant string
		want                       []string
	}{
		{"ST20 Series III", "SoundTouch 20", "sm2", "spotty", []string{"Update_ti_27.0.6.46330.5043500.sm2.stu"}},
		{"ST20 older hardware", "SoundTouch 20", "scm", "spotty", []string{"Update_ti_27.0.6.46330.5043500.scm.stu"}},
		{"ST20 generation unknown: both, older first", "SoundTouch 20", "", "",
			[]string{"Update_ti_27.0.6.46330.5043500.scm.stu", "Update_ti_27.0.6.46330.5043500.sm2.stu"}},
		{"ST10", "SoundTouch 10", "sm2", "rhino", []string{"Update_ti_27.0.6.46330.5043500.rhino.sm2.stu"}},
		{"Portable", "SoundTouch Portable", "scm", "taigan", []string{"Update_ti_27.0.6.46330.5043500.scm.stu"}},
		{"case does not matter", "soundtouch portable", "SCM", "", []string{"Update_ti_27.0.6.46330.5043500.scm.stu"}},
		{"ST300 is not the ST30", "SoundTouch 300", "sm2", "ginger", []string{"Update_ti_27.0.6.46330.5043500.ginger.sm2.stu"}},
		{"Wave with a suffix", "Wave SoundTouch music system IV", "scm", "lisa", []string{"Update_ti_27.0.6.46330.5043500.nelson.scm.stu"}},
		{"Lifestyle narrowed by variant", "Lifestyle", "sm2", "bardeen", []string{"Update_ti_27.0.6.46330.5043500.bardeen.sm2.stu"}},
		{"Lifestyle without a deciding variant: no guess", "Lifestyle", "sm2", "lisa", nil},
		{"model not in the catalogue", "CineMate 520", "sm2", "lisa", nil},
		{"ST30 only on a foreign host in this fixture", "SoundTouch 30", "sm2", "mojo", nil},
		{"empty model", "", "sm2", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := names(Match(entries, c.model, c.moduleType, c.variant))
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestMatchReportsGeneration(t *testing.T) {
	files := Match(fixture(t), "SoundTouch 20", "", "")
	if len(files) != 2 || files[0].Generation != "scm" || files[1].Generation != "sm2" {
		t.Fatalf("got %+v", files)
	}
	if files[0].URL != base+files[0].Name {
		t.Errorf("URL %q", files[0].URL)
	}
}

// The built-in table must give the same answer as Bose's live catalogue for the
// speakers in the fixture, or the fallback would send people to a different file.
func TestBuiltinAgreesWithIndex(t *testing.T) {
	entries := fixture(t)
	for _, m := range []struct{ model, mt string }{
		{"SoundTouch 10", "sm2"}, {"SoundTouch 20", "sm2"}, {"SoundTouch 20", "scm"},
		{"SoundTouch Portable", "scm"}, {"SoundTouch 300", "sm2"}, {"Wave SoundTouch", "scm"},
		{"Lifestyle", "sm2"},
	} {
		live := names(Match(entries, m.model, m.mt, "bardeen"))
		fb := names(Match(Builtin, m.model, m.mt, "bardeen"))
		if strings.Join(live, ",") != strings.Join(fb, ",") || len(live) != 1 {
			t.Errorf("%s/%s: index %v, builtin %v", m.model, m.mt, live, fb)
		}
	}
	for _, e := range Builtin {
		if e.URL != base+e.FileName {
			t.Errorf("builtin %s: URL %q", e.FileName, e.URL)
		}
	}
}

func TestCatalogueFetchesOnceAndFallsBack(t *testing.T) {
	data, err := os.ReadFile("testdata/index.xml")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	c := &Catalogue{Client: srv.Client(), URL: srv.URL}
	for i := 0; i < 3; i++ {
		entries, live := c.Entries(context.Background())
		if !live || len(entries) != 10 {
			t.Fatalf("live=%v len=%d", live, len(entries))
		}
	}
	if hits.Load() != 1 {
		t.Errorf("index fetched %d times, want once per session", hits.Load())
	}

	var downHits atomic.Int32
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downHits.Add(1)
		http.Error(w, "gone", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	c2 := &Catalogue{Client: down.Client(), URL: down.URL}
	for i := 0; i < 3; i++ {
		entries, live := c2.Entries(context.Background())
		if live || len(entries) != len(Builtin) {
			t.Fatalf("live=%v len=%d, want the built-in table", live, len(entries))
		}
	}
	if downHits.Load() != 1 {
		t.Errorf("a failed fetch was retried %d times right away", downHits.Load())
	}
}

// TestLiveBoseIndex is opt-in: it checks the real catalogue still parses and
// still lists every built-in file. Run with STR_LIVE_BOSE=1.
func TestLiveBoseIndex(t *testing.T) {
	if os.Getenv("STR_LIVE_BOSE") == "" {
		t.Skip("set STR_LIVE_BOSE=1 to check Bose's live index")
	}
	c := &Catalogue{Client: &http.Client{}}
	entries, live := c.Entries(context.Background())
	if !live {
		t.Fatal("Bose index not reachable or not parseable")
	}
	have := map[string]bool{}
	for _, e := range entries {
		have[e.DeviceID+" "+e.FileName] = true
	}
	for _, b := range Builtin {
		if !have[b.DeviceID+" "+b.FileName] {
			t.Errorf("built-in %s %s is no longer in Bose's index", b.DeviceID, b.FileName)
		}
	}
}
