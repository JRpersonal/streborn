package bosefw

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// These tests pin the app's ladder against docs/FIRMWARE-UPDATE.md by reading
// the tables back out of the doc. When someone changes a route in the doc, the
// app must follow in the same change, and the other way round.

var docVersionRE = regexp.MustCompile(`\b\d{1,2}\.\d{1,2}\.\d{1,2}\b`)

func readFirmwareDoc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "FIRMWARE-UPDATE.md"))
	if err != nil {
		t.Skipf("docs/FIRMWARE-UPDATE.md not found next to the module: %v", err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// docSection returns the text from the line `from` up to the next line that
// starts with `to`.
func docSection(t *testing.T, doc, from, to string) string {
	t.Helper()
	i := strings.Index(doc, from)
	if i < 0 {
		t.Fatalf("the doc has no %q", from)
	}
	rest := doc[i+len(from):]
	if j := strings.Index(rest, to); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// tableRows splits the markdown table rows of a section into cells, header
// and separator rows dropped.
func tableRows(section string) [][]string {
	var rows [][]string
	header := true
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			header = true
			continue
		}
		if header {
			header = false
			continue
		}
		if strings.HasPrefix(line, "|---") {
			continue
		}
		var cells []string
		for _, c := range strings.Split(strings.Trim(line, "|"), "|") {
			cells = append(cells, strings.TrimSpace(c))
		}
		rows = append(rows, cells)
	}
	return rows
}

func routeByFamily(t *testing.T, family string) route {
	t.Helper()
	for _, r := range routes {
		if r.Family == family {
			return r
		}
	}
	t.Fatalf("no route %s", family)
	return route{}
}

func withFinal(vs []string) string {
	return strings.Join(append(append([]string{}, vs...), LatestShort), " ")
}

// Section 7, "SoundTouch 20, 30 and Portable, first generation (no
// Bluetooth)", table "Which steps your speaker needs".
func TestCheckedLadderMatchesDocTable(t *testing.T) {
	doc := readFirmwareDoc(t)
	sec := docSection(t, doc, "**Which steps your speaker needs:**", "The ladder skips 15.00.20")
	samples := map[string][]string{
		"5.x":               {"5.2.0"},
		"6.x":               {"6.0.39", "6.2.1"},
		"7.x up to 14.0.15": {"7.0.37", "10.0.11", "14.0.15"},
		"14.0.33":           {"14.0.33"},
		"15.x or newer":     {"15.0.20", "16.0.9"},
	}
	seen := 0
	for _, row := range tableRows(sec) {
		from, cell := row[0], row[1]
		vs, ok := samples[from]
		if !ok {
			if strings.HasPrefix(from, "2.x") {
				// "On 2.x? Start with 06.02.01" (below the tables).
				if p := Plan("SoundTouch 20", "scm", "", "2.1.51", nil); !p.OldStart || p.Steps[0].Version != "06.02.01" {
					t.Errorf("2.x: %+v", p)
				}
				continue
			}
			t.Errorf("a new row in the doc table: %q. Add it to this test and to the ladder.", from)
			continue
		}
		seen++
		main := cell
		if i := strings.Index(main, "("); i >= 0 {
			main = main[:i]
		}
		want := strings.Join(docVersionRE.FindAllString(main, -1), " ")
		for _, v := range vs {
			for _, model := range []string{"SoundTouch 20", "SoundTouch 30", "SoundTouch Portable"} {
				p := Plan(model, "scm", "", v, nil)
				if got := versions(p.Steps); got != want {
					t.Errorf("%s on %s (doc row %q): steps %q, the doc says %q", model, v, from, got, want)
				}
			}
		}
		if strings.Contains(cell, "20.00.06 first") {
			for _, v := range vs {
				if got := versions(FallbackFor(FamilySTScm+"-27.0.6", v)); got != "20.00.06 27.0.6" {
					t.Errorf("%s: the doc offers 20.00.06 when 27.0.6 stalls, the app %q", v, got)
				}
			}
		}
	}
	if seen != len(samples) {
		t.Errorf("found %d of the %d doc rows", seen, len(samples))
	}
}

// Section 7, "Other models: no checked ladder yet".
func TestRoutesMatchDocTable(t *testing.T) {
	doc := readFirmwareDoc(t)
	sec := docSection(t, doc, "### Other models: no checked ladder yet", "## 8.")
	rows := tableRows(sec)
	if len(rows) != 4 {
		t.Fatalf("the doc table has %d rows, the test knows 4", len(rows))
	}
	knownRE := regexp.MustCompile(`(\d{1,2}\.\d{1,2}\.\d{1,2}) inside: ([\d ]+) / ` + "`" + `([0-9a-f]{8})` + "`")
	boseRE := regexp.MustCompile(`Bose 27\.0\.6: ([\d ]+) / ` + "`" + `([0-9a-f]{8})` + "`")
	for _, row := range rows {
		speaker, routeCell, known, never := row[0], row[1], row[2], row[3]
		var fam string
		switch {
		case strings.HasPrefix(speaker, "Wave SoundTouch, first generation"):
			fam = FamilyWaveScm
		case strings.HasPrefix(speaker, "SA-4, Stereo JC"):
			fam = FamilySA4Scm
		case strings.HasPrefix(speaker, "CineMate adapter"):
			fam = FamilyCinemateScm
		case strings.HasPrefix(speaker, "Bluetooth chassis"):
			checkBluetoothRow(t, routeCell, never)
			continue
		default:
			t.Errorf("a new row in the doc table: %q", speaker)
			continue
		}
		r := routeByFamily(t, fam)
		if got, want := withFinal(r.Versions), strings.Join(docVersionRE.FindAllString(routeCell, -1), " "); got != want {
			t.Errorf("%s: route %q, the doc says %q", fam, got, want)
		}
		if strings.Contains(routeCell, "Below 14: no source known") != (r.NoSourceBelow == "14.0.0") {
			t.Errorf("%s: NoSourceBelow %q against the doc's %q", fam, r.NoSourceBelow, routeCell)
		}
		if m := regexp.MustCompile("folder `([^`]+)`").FindStringSubmatch(speaker); m != nil && m[1] != r.Folder {
			t.Errorf("%s: folder %q, the doc says %q", fam, r.Folder, m[1])
		}
		for _, m := range knownRE.FindAllStringSubmatch(known, -1) {
			fp, ok := r.Known[m[1]]
			if !ok || fp.Size != docBytes(t, m[2]) || fp.CRC != docCRC(t, m[3]) {
				t.Errorf("%s %s: known %+v, the doc says %s / %s", fam, m[1], fp, m[2], m[3])
			}
		}
		if m := boseRE.FindStringSubmatch(known); m != nil {
			fin := routeFinal(fam, docModelFor(fam), "scm")
			if fin.InnerSize != docBytes(t, m[1]) || fin.InnerCRC32 != docCRC(t, m[2]) {
				t.Errorf("%s: Bose's file %d / %08x, the doc says %s / %s", fam, fin.InnerSize, fin.InnerCRC32, m[1], m[2])
			}
		}
		checkAvoid(t, fam, r.Avoid, never)
	}
}

func checkBluetoothRow(t *testing.T, routeCell, never string) {
	t.Helper()
	parts := strings.Split(routeCell, "SoundTouch 10 below 15:")
	if len(parts) != 2 {
		t.Fatalf("the Bluetooth chassis row changed shape: %q", routeCell)
	}
	bt, st10 := parts[0], parts[1]
	wave := ""
	if i := strings.Index(st10, "Wave IV below"); i >= 0 {
		st10, wave = st10[:i], st10[i:]
	}
	if got, want := withFinal(routeByFamily(t, FamilyBTSm2).Versions), strings.Join(docVersionRE.FindAllString(bt, -1), " "); got != want {
		t.Errorf("bt-sm2: route %q, the doc says %q", got, want)
	}
	if got, want := withFinal(routeByFamily(t, FamilyST10Sm2).Versions), strings.Join(docVersionRE.FindAllString(st10, -1), " "); got != want {
		t.Errorf("st10-sm2: route %q, the doc says %q", got, want)
	}
	w := routeByFamily(t, FamilyWaveSm2)
	if withFinal(w.Versions) != withFinal(routeByFamily(t, FamilyBTSm2).Versions) {
		t.Errorf("wave-sm2 is the Bluetooth chassis route: %v", w.Versions)
	}
	m := regexp.MustCompile(`Wave IV below (\d+\.\d+\.\d+): no source known`).FindStringSubmatch(wave)
	if m == nil || m[1] != w.NoSourceBelow {
		t.Errorf("wave-sm2: NoSourceBelow %q against the doc's %q", w.NoSourceBelow, wave)
	}
	checkAvoid(t, FamilyWaveSm2, w.Avoid, never)
}

// checkAvoid: every file the doc's "Never use" cell names is in the list.
func checkAvoid(t *testing.T, fam string, avoid []string, never string) {
	t.Helper()
	// "... from that folder: they are the SoundTouch 20/30 file": the reason
	// after the colon names models, not files.
	if i := strings.Index(never, ":"); i >= 0 {
		never = never[:i]
	}
	has := map[string]bool{}
	for _, a := range avoid {
		has[a] = true
	}
	for _, v := range docVersionRE.FindAllString(never, -1) {
		if !has[v] {
			t.Errorf("%s: the doc says never use %s, the app does not list it", fam, v)
		}
	}
	rest := docVersionRE.ReplaceAllString(never, "")
	if m := regexp.MustCompile(`\b(\d{2}) to (\d{2})\b`).FindStringSubmatch(rest); m != nil {
		lo, _ := strconv.Atoi(m[1])
		hi, _ := strconv.Atoi(m[2])
		for n := lo; n <= hi; n++ {
			if !has[strconv.Itoa(n)+".x"] {
				t.Errorf("%s: the doc says never use %d, the app does not list %d.x", fam, n, n)
			}
		}
		rest = strings.Replace(rest, m[0], "", 1)
	}
	for _, n := range regexp.MustCompile(`\b\d{2}\b`).FindAllString(rest, -1) {
		if !has[n+".x"] {
			t.Errorf("%s: the doc says never use %s, the app does not list %s.x", fam, n, n)
		}
	}
	if len(avoid) > 0 && strings.TrimSpace(never) == "" {
		t.Errorf("%s: the app warns about files the doc does not name: %v", fam, avoid)
	}
}

func docBytes(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(strings.ReplaceAll(strings.TrimSpace(s), " ", ""), 10, 64)
	if err != nil {
		t.Fatalf("bytes %q: %v", s, err)
	}
	return n
}

func docCRC(t *testing.T, s string) uint32 {
	t.Helper()
	n, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		t.Fatalf("crc %q: %v", s, err)
	}
	return uint32(n)
}

func docModelFor(fam string) string {
	switch fam {
	case FamilyWaveScm:
		return "Wave SoundTouch"
	case FamilySA4Scm:
		return "SoundTouch SA-4"
	case FamilyCinemateScm:
		return "Cinemate"
	}
	return ""
}

// Section 2, "Pick your route": 15.x or newer goes in one step, 14.x or older
// never does.
func TestDirectJumpOK(t *testing.T) {
	for v, want := range map[string]bool{
		"5.2.0": false, "14.0.15": false, "14.0.33": false, "15.0.20": true, "27.0.3": true, "": true,
	} {
		if got := DirectJumpOK(v); got != want {
			t.Errorf("DirectJumpOK(%q) = %v", v, got)
		}
	}
}

func TestPlanRoutes(t *testing.T) {
	cases := []struct {
		name, model, mt, version string
		bt                       *bool
		status, reason, family   string
		steps                    string
	}{
		{"first generation Wave on 5.x", "Wave SoundTouch", "scm", "5.0.3", nil, StatusRoute, "", FamilyWaveScm, "06.02.01 07.00.37 14.00.33 15.00.20 27.0.6"},
		{"Wave on 14.0.33 still needs 15.00.20", "Wave SoundTouch", "scm", "14.0.33", nil, StatusRoute, "", FamilyWaveScm, "15.00.20 27.0.6"},
		{"Wave without moduleType below 9 is the first generation", "Wave SoundTouch", "", "7.0.37", nil, StatusRoute, "", FamilyWaveScm, "14.00.33 15.00.20 27.0.6"},
		{"Wave without moduleType from 9 on cannot be told apart", "Wave SoundTouch", "", "10.0.11", nil, StatusUnsupported, "generation", "", ""},
		{"Wave IV on 14.0.33", "Wave SoundTouch music system IV", "sm2", "14.0.33", nil, StatusRoute, "", FamilyWaveSm2, "15.00.20 27.0.6"},
		{"Wave IV below 14.0.33: no usable 14.00.33 known", "Wave SoundTouch", "sm2", "10.0.11", nil, StatusUnsupported, "no-source", FamilyWaveSm2, ""},
		{"SA-4 below 14: no source", "SoundTouch SA-4", "scm", "10.0.11", nil, StatusUnsupported, "no-source", FamilySA4Scm, ""},
		{"SA-4 on 14.0.15", "SoundTouch SA-4", "scm", "14.0.15", nil, StatusRoute, "", FamilySA4Scm, "14.0.33 15.0.20 27.0.6"},
		{"Stereo JC on 14.0.33", "SoundTouch Stereo JC", "scm", "14.0.33", nil, StatusRoute, "", FamilySA4Scm, "15.0.20 27.0.6"},
		{"SA-4 on the newer hardware: no route in the doc", "SoundTouch SA-4", "sm2", "14.0.15", nil, StatusUnsupported, "model", "", ""},
		{"CineMate on 6.x", "Cinemate", "scm", "6.0.20", nil, StatusRoute, "", FamilyCinemateScm, "06.00.91 07.00.37 14.00.33 15.00.20 27.0.6"},
		{"SA-5 without moduleType is sm2", "SoundTouch SA-5", "", "12.0.1", nil, StatusRoute, "", FamilyBTSm2, "14.00.33 15.00.20 27.0.6"},
		{"SoundTouch 300", "SoundTouch 300", "sm2", "14.0.15", nil, StatusRoute, "", FamilyBTSm2, "14.00.33 15.00.20 27.0.6"},
		{"Wireless Link adapter", "SoundTouch Wireless Link Adapter", "", "13.0.5", nil, StatusRoute, "", FamilyBTSm2, "14.00.33 15.00.20 27.0.6"},
		{"SoundTouch 10 without moduleType", "SoundTouch 10", "", "10.0.11", nil, StatusRoute, "", FamilyST10Sm2, "15.00.20 27.0.6"},
		{"Lifestyle: no known path", "Lifestyle", "sm2", "10.0.11", nil, StatusUnsupported, "model", "", ""},
		{"no model at all", "", "", "7.0.37", nil, StatusUnsupported, "model", "", ""},
		{"from 15 every model gets the normal guide", "Wave SoundTouch", "scm", "15.0.20", nil, StatusFinal, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Plan(c.model, c.mt, "", c.version, c.bt)
			if p.Status != c.status || p.Reason != c.reason || p.Family != c.family {
				t.Fatalf("status %q reason %q family %q, want %q %q %q", p.Status, p.Reason, p.Family, c.status, c.reason, c.family)
			}
			if got := versions(p.Steps); got != c.steps {
				t.Fatalf("steps %q, want %q", got, c.steps)
			}
		})
	}
}

// A route step is a list entry, never a file the app fetches or writes: no
// stick preparation can find it, and only Bose's last step names a URL.
func TestRouteStepsAreNeverPrepared(t *testing.T) {
	for _, r := range routes {
		p := Plan(docModel(r), r.ModuleType, "", "14.0.33", nil)
		if r.NoSourceBelow != "" && CompareVersions("14.0.33", r.NoSourceBelow) < 0 {
			continue
		}
		if p.Status != StatusRoute || len(p.Steps) == 0 {
			t.Fatalf("%s: %+v", r.Family, p)
		}
		last := p.Steps[len(p.Steps)-1]
		if last.Source != SourceBose || last.Version != LatestShort {
			t.Errorf("%s: the last step must be Bose's own %s, got %+v", r.Family, LatestShort, last)
		}
		if last.URL != "" && !strings.HasPrefix(last.URL, DownloadBase) {
			t.Errorf("%s: Bose's step must come from Bose's host: %s", r.Family, last.URL)
		}
		for _, s := range p.Steps {
			if _, ok := StepByID(s.ID); ok {
				t.Errorf("%s: a stick could be prepared for the unchecked step %s", r.Family, s.ID)
			}
			if s.Source == SourceArchive && (s.URL != "" || s.ZipSHA1 != "" || s.LiveVerified || s.NetworkOK) {
				t.Errorf("%s: an unchecked archive step names a file or claims a test: %+v", r.Family, s)
			}
		}
	}
}

func docModel(r route) string {
	switch r.Models[0] {
	case "wave soundtouch":
		return "Wave SoundTouch"
	case "wireless link":
		return "SoundTouch Wireless Link adapter"
	case "cinemate":
		return "Cinemate"
	}
	// "soundtouch 20" -> "SoundTouch 20"
	return "SoundTouch" + strings.TrimPrefix(r.Models[0], "soundtouch")
}
