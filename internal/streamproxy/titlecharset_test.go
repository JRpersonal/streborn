package streamproxy

import (
	"testing"
	"unicode/utf8"
)

// encodeTitle writes s in an 8-bit code page, the way a station's playout
// software does; a character the code page lacks fails the test.
func encodeTitle(t *testing.T, charset, s string) string {
	t.Helper()
	back := map[rune]byte{}
	for c := 0x80; c <= 0xff; c++ {
		back[[]rune(decodeTitle([]byte{byte(c)}, charset))[0]] = byte(c)
	}
	var out []byte
	for _, r := range s {
		switch b, ok := back[r]; {
		case r < 0x80:
			out = append(out, byte(r))
		case ok:
			out = append(out, b)
		default:
			t.Fatalf("%s has no %q (for %q)", charset, r, s)
		}
	}
	if utf8.Valid(out) {
		t.Fatalf("%q in %s is valid UTF-8 too: the test says nothing", s, charset)
	}
	return string(out)
}

func TestCodePageTablesAreComplete(t *testing.T) {
	for name, c := range map[string]struct {
		table []rune
		want  int
	}{
		"cp1252High": {cp1252High, 32},
		"cp1250High": {cp1250High, 64},
		"latin2A0":   {latin2A0, 32},
		"centralC0":  {centralC0, 64},
	} {
		if len(c.table) != c.want {
			t.Errorf("%s has %d entries, want %d", name, len(c.table), c.want)
		}
	}
	// spot checks against the published code pages
	for _, c := range []struct {
		charset string
		b       byte
		want    rune
	}{
		{charsetCentral, 0x8a, 'Š'}, {charsetCentral, 0x9d, 'ť'}, {charsetCentral, 0x9e, 'ž'},
		{charsetCentral, 0xb9, 'ą'}, {charsetCentral, 0xbe, 'ľ'}, {charsetCentral, 0xc8, 'Č'},
		{charsetCentral, 0xe8, 'č'}, {charsetCentral, 0xec, 'ě'}, {charsetCentral, 0xf8, 'ř'},
		{charsetCentral, 0xf9, 'ů'}, {charsetCentral, 0xef, 'ď'}, {charsetCentral, 0xf2, 'ň'},
		{charsetCentral, 0xb3, 'ł'}, {charsetCentral, 0xff, '˙'},
		{charsetCentral2, 0xa9, 'Š'}, {charsetCentral2, 0xb9, 'š'}, {charsetCentral2, 0xbb, 'ť'},
		{charsetCentral2, 0xbe, 'ž'}, {charsetCentral2, 0xae, 'Ž'}, {charsetCentral2, 0xb1, 'ą'},
		{charsetCentral2, 0xe8, 'č'}, {charsetCentral2, 0xd8, 'Ř'},
		{charsetWestern, 0x92, '’'}, {charsetWestern, 0x9a, 'š'}, {charsetWestern, 0x80, '€'},
		{charsetWestern, 0xe8, 'è'}, {charsetWestern, 0xfc, 'ü'}, {charsetWestern, 0xf8, 'ø'},
	} {
		if got := []rune(decodeTitle([]byte{c.b}, c.charset))[0]; got != c.want {
			t.Errorf("%s 0x%02X = %q, want %q", c.charset, c.b, got, c.want)
		}
	}
}

// Issue #1982: Czech song titles from a station that sends Windows-1250 (or
// ISO-8859-2) came out as other letters and control characters, because every
// title that was not UTF-8 was read as Latin-1.
func TestCzechTitlesAreReadInTheirCodePage(t *testing.T) {
	const pangram = "Příliš žluťoučký kůň úpěl ďábelské ódy"
	titles := []string{
		pangram,
		"PŘÍLIŠ ŽLUŤOUČKÝ KŮŇ ÚPĚL ĎÁBELSKÉ ÓDY",
		"Karel Gott - Lady Carneval / Když muž se ženou snídá",
		"Hana Zagorová - Černý páv",
		"Lucie Bílá - Láska je láska",
		"Olympic - Želva",
		"Kabát - Dole v dole (Šaman)",
		"Jaromír Nohavica - Těšínská",
		"Elán - Vymyslená (Ľúbim ťa)",
	}
	// Windows-1250: every title on its own, on a station nothing is known of
	for _, title := range titles {
		var c titleCharsets
		if got, used := c.toUTF8("http://192.0.2.1/cz", encodeTitle(t, charsetCentral, title)); got != title {
			t.Errorf("got %q (read as %s), want %q", got, used, title)
		}
	}
	// ISO-8859-2 has "ž" where Windows-1250 has "ľ", so a title with no
	// other sign of it needs the station's earlier titles: one station here
	var c titleCharsets
	for _, title := range titles {
		if got, used := c.toUTF8("http://192.0.2.1/cz2", encodeTitle(t, charsetCentral2, title)); got != title {
			t.Errorf("iso-8859-2: got %q (read as %s), want %q", got, used, title)
		}
	}
	// what the Latin-1 reading made of it, so the test documents the defect
	if got := decodeTitle([]byte(encodeTitle(t, charsetCentral, pangram)), "latin-1"); got == pangram {
		t.Error("the Latin-1 reading is not supposed to be right")
	}
}

func TestPolishTitlesAreReadInTheirCodePage(t *testing.T) {
	for _, charset := range []string{charsetCentral, charsetCentral2} {
		var c titleCharsets // one station per code page
		for _, title := range []string{
			"Zażółć gęślą jaźń",
			"Maryla Rodowicz - Małgośka",
			"Czesław Niemen - Dziwny jest ten świat",
		} {
			if got, used := c.toUTF8("http://192.0.2.1/pl", encodeTitle(t, charset, title)); got != title {
				t.Errorf("%s: got %q (read as %s), want %q", charset, got, used, title)
			}
		}
	}
}

// Western stations keep their letters: nothing here may turn into č, ř or ě,
// and the Windows-1252 quotes and dashes are no longer control characters.
func TestWesternTitlesStayWestern(t *testing.T) {
	for _, title := range []string{
		"STS - Fürstenfeld",
		"Böhse Onkelz - Auf gute Freunde",
		"Mylène Farmer - Désenchantée",
		"Crème brûlée",
		"Françoise Hardy - Tous les garçons et les filles de mon âge",
		"Stromae - Où t’es, papa où t’es ?",
		"Mozart - Così fan tutte",
		"Eros Ramazzotti - Più bella cosa",
		"Jovanotti - Baciami ancora (perché sì)",
		"Bjørn Eidsvåg - Eg ser",
		"Røde Mor - Søndag",
		"Eivør Pálsdóttir - Trøllabundin",
		"Juanes - ¿Qué pasa?",
		"Maná - Mariposa traicionera (versión acústica)",
		"Caetano Veloso - Coração vagabundo",
		"Zaz – «Je veux»",
		"Prince - Sign “☮” the Times™",
	} {
		enc := make([]byte, 0, len(title))
		for _, r := range title {
			if r == '☮' {
				continue // not in any code page
			}
			enc = append(enc, encodeTitle(t, charsetWestern, "é"+string(r))[1:]...)
		}
		want := title
		if title == "Prince - Sign “☮” the Times™" {
			want = "Prince - Sign “” the Times™"
		}
		var c titleCharsets
		if got, used := c.toUTF8("http://192.0.2.1/west", string(enc)); got != want || used != charsetWestern {
			t.Errorf("got %q (read as %s), want %q", got, used, want)
		}
	}
}

// A title that is UTF-8 passes byte for byte and says nothing about the
// station's code page.
func TestUTF8TitlesPassUntouched(t *testing.T) {
	var c titleCharsets
	for _, title := range []string{"Über den Wolken", "Příliš žluťoučký kůň", "Океан Ельзи", ""} {
		if got, used := c.toUTF8("http://192.0.2.1/a", title); got != title || used != "" {
			t.Errorf("toUTF8(%q) = %q, %q", title, got, used)
		}
	}
	if len(c.m) != 0 {
		t.Errorf("UTF-8 titles left %d stations behind", len(c.m))
	}
}

// One title with clear evidence settles the station: the titles after it
// that could be read either way follow, per station, and a single odd title
// does not turn a station round.
func TestTheStationRemembersItsCodePage(t *testing.T) {
	const cz, fr = "http://192.0.2.1/cz", "http://192.0.2.1/fr"
	var c titleCharsets
	vecer := encodeTitle(t, charsetCentral, "Večer")
	if got, _ := c.toUTF8(cz, vecer); got != "Veèer" {
		t.Errorf("with nothing known the western reading stays: got %q", got)
	}
	c.toUTF8(cz, encodeTitle(t, charsetCentral, "Hana Zagorová - Černý páv"))
	if got, _ := c.toUTF8(cz, vecer); got != "Večer" {
		t.Errorf("after a Czech title: got %q, want Večer", got)
	}
	// the same bytes on another station are still French
	creme := encodeTitle(t, charsetWestern, "Crème")
	if got, _ := c.toUTF8(fr, creme); got != "Crème" {
		t.Errorf("another station: got %q, want Crème", got)
	}
	// a French station with a few clearly western titles behind it is not
	// turned round by one title that looks Czech ("ì" next to "á")
	for _, s := range []string{"Françoise Hardy - Message personnel", "Brel - Ne me quitte pas (à l'Olympia)"} {
		c.toUTF8(fr, encodeTitle(t, charsetWestern, s))
	}
	if got, _ := c.toUTF8(fr, encodeTitle(t, charsetWestern, "Maná - Così")); got != "Maná - Così" {
		t.Errorf("one odd title on a French station: got %q", got)
	}
	if got, _ := c.toUTF8(fr, creme); got != "Crème" {
		t.Errorf("after the odd title: got %q, want Crème", got)
	}
	// a station repeats its title in every metadata block: one song is one vote
	var d titleCharsets
	odd := encodeTitle(t, charsetWestern, "Maná - Così")
	for range 20 {
		d.toUTF8(fr, odd)
	}
	if got := d.m[fr].ce; got != 1 {
		t.Errorf("a repeated title voted %d times", got)
	}
}

// An ISO-8859-2 station whose title has only letters that Windows-1250 has
// elsewhere ("š" at 0xB9 is "ą" there) is read right once the station is known.
func TestTheStationRemembersISO88592(t *testing.T) {
	const st = "http://192.0.2.1/iso"
	var c titleCharsets
	if got, used := c.toUTF8(st, encodeTitle(t, charsetCentral2, "Příliš žluťoučký kůň")); got != "Příliš žluťoučký kůň" || used != charsetCentral2 {
		t.Fatalf("got %q read as %s", got, used)
	}
	if got, _ := c.toUTF8(st, encodeTitle(t, charsetCentral2, "Koš")); got != "Koš" {
		t.Errorf("got %q, want Koš", got)
	}
	// and Windows-1250 stays the reading for a station that showed its
	// 0x80..0x9F letters
	var d titleCharsets
	d.toUTF8(st, encodeTitle(t, charsetCentral, "Šaman"))
	d.toUTF8(st, encodeTitle(t, charsetCentral, "Černý páv"))
	if got, _ := d.toUTF8(st, encodeTitle(t, charsetCentral, "Są"+"siad ľudia")); got != "Sąsiad ľudia" {
		t.Errorf("got %q, want Sąsiad ľudia", got)
	}
}

func TestTheStationMemoryIsBounded(t *testing.T) {
	var c titleCharsets
	title := encodeTitle(t, charsetCentral, "Černý páv")
	for i := range 5 * charsetStationsMax {
		c.toUTF8(string(rune('a'+i%26))+string(rune('0'+i/26%10))+string(rune('A'+i/260)), title)
	}
	if len(c.m) > charsetStationsMax {
		t.Errorf("%d stations kept, limit %d", len(c.m), charsetStationsMax)
	}
}

// End to end through the proxy's title path: what the apps read and what the
// speaker's display is handed are the Czech letters.
func TestACzechStationsTitleReachesTheListeners(t *testing.T) {
	const station = "http://192.0.2.1/cz"
	const want = "Lucie Bílá - Příliš žluťoučký kůň"
	s := newTitleServer()
	var pushed []string
	s.SetOnTitle(func(title string) { pushed = append(pushed, title) })
	s.noteStreamStart(station)
	meta := "StreamTitle='" + encodeTitle(t, charsetCentral, want) + "';StreamUrl='';\x00\x00"
	title, ok := parseStreamTitle(meta)
	if !ok {
		t.Fatal("no StreamTitle parsed")
	}
	s.setTitle(station, title)
	if got := s.CurrentTitle(); got != want {
		t.Errorf("CurrentTitle = %q, want %q", got, want)
	}
	if len(pushed) != 1 || pushed[0] != want {
		t.Errorf("pushed %q, want one %q", pushed, want)
	}
}
