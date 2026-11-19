// titlecharset.go: which 8-bit charset a StreamTitle that is not UTF-8 is in.
//
// ICY metadata carries no charset. A station either sends UTF-8 or whatever
// code page its playout PC runs on, and for a long time every title that was
// not UTF-8 was read as Latin-1. That is right for German, French or Spanish
// stations and wrong for Czech, Slovak and Polish ones, whose software sends
// Windows-1250 (or ISO-8859-2): "Příliš žluťoučký kůň" came out as
// "Pøíli\u009a \u009elu\u009douèký kùò", wrong letters plus invisible control
// characters, on the speaker's display and in the apps alike (#1982).
//
// The code page is a property of the station, not of one song, and most of
// its bytes mean the same letter in every candidate (á, é, í, ó, ú, ä, ö, ü).
// So each title is searched for the bytes that do tell the code pages apart,
// the result is added to a small score kept per station, and the score picks
// the reading. One title with clear evidence fixes the ones around it that
// have none ("Večer" alone could as well be "Veèer").
//
// The evidence, in falling strength:
//
//   - a byte Windows-1252 does not define (0x8D, 0x8F, 0x9D: Ť, Ź, ť), or a
//     byte that is a sign in Windows-1252 and sits inside a word (0xB3 "³"
//     between letters is ł, 0xB9 "¹" after a letter is ą or š): Central
//     European, whatever the score said before;
//   - a letter Czech and Slovak write where the west writes another one
//     (č/è, ř/ø, ě/ì, ů/ù, ď/ï, ň/ò) next to á, í, ú, ý, š or ž, which French,
//     Italian and the Nordic languages do not use: Central European;
//   - a letter only the western reading makes a word with (à, ç, ê, î, ñ, å,
//     æ, ...): Western.
//
// Without any of that the station's score decides, and with no score the
// title is read as Windows-1252 as before. Hungarian (ő, ű) and Romanian are
// not told apart from the west here: their titles are read as they always
// were.
package streamproxy

import (
	"sync"
	"unicode/utf8"
)

// The upper halves of the code pages. A byte the code page does not define
// keeps its own value as the code point (a C1 control: valid UTF-8, drawn by
// nothing), which is what the Latin-1 reading gave it before.
var (
	// cp1252High is Windows-1252 from 0x80 to 0x9F; from 0xA0 on it is Latin-1.
	cp1252High = []rune("€\u0081‚ƒ„…†‡ˆ‰Š‹Œ\u008dŽ\u008f\u0090‘’“”•–—˜™š›œ\u009džŸ")
	// cp1250High is Windows-1250 from 0x80 to 0xBF.
	cp1250High = []rune("€\u0081‚\u0083„…†‡\u0088‰Š‹ŚŤŽŹ\u0090‘’“”•–—\u0098™š›śťžź" +
		" ˇ˘Ł¤Ą¦§¨©Ş«¬­®Ż°±˛ł´µ¶·¸ąş»Ľ˝ľż")
	// latin2A0 is ISO-8859-2 from 0xA0 to 0xBF (0x80 to 0x9F are controls).
	latin2A0 = []rune(" Ą˘Ł¤ĽŚ§¨ŠŞŤŹ­ŽŻ°ą˛ł´ľśˇ¸šşťź˝žż")
	// centralC0 is 0xC0 to 0xFF, the same in Windows-1250 and ISO-8859-2.
	centralC0 = []rune("ŔÁÂĂÄĹĆÇČÉĘËĚÍÎĎĐŃŇÓÔŐÖ×ŘŮÚŰÜÝŢßŕáâăäĺćçčéęëěíîďđńňóôőö÷řůúűüýţ˙")
)

// Names of the readings, for the log.
const (
	charsetWestern  = "windows-1252"
	charsetCentral  = "windows-1250"
	charsetCentral2 = "iso-8859-2"
)

// titleEvidence is what one title says about its station's code page.
type titleEvidence struct {
	// central: Central European whatever the station's score is.
	central bool
	// ce is added to the station's Central European score (negative: Western).
	ce int
	// iso is added to the station's ISO-8859-2 score (negative: Windows-1250).
	iso int
	// not8859 says the title has bytes ISO-8859-2 keeps for controls, so
	// this title is Windows-1250 if it is Central European at all.
	not8859 bool
}

// byteSet is a set of byte values.
type byteSet [256]bool

func newByteSet(bs ...byte) (s byteSet) {
	for _, b := range bs {
		s[b] = true
	}
	return s
}

var (
	// czechLetters read as another letter in the west: č ř ě ů ď ň (è ø ì ù ï ò).
	czechLetters = newByteSet(0xe8, 0xf8, 0xec, 0xf9, 0xef, 0xf2, 0xc8, 0xd8, 0xcc, 0xd9, 0xcf, 0xd2)
	// czechOnly are the ones Polish and Slovak-with-ľ texts do not have: ě ř ů.
	czechOnly = newByteSet(0xec, 0xf8, 0xf9, 0xcc, 0xd8, 0xd9)
	// czechCompanions are the same letter in every reading and frequent in
	// Czech and Slovak, while French, Italian and the Nordic languages do
	// not write them: á í ú ý š ž.
	czechCompanions = newByteSet(0xe1, 0xed, 0xfa, 0xfd, 0xc1, 0xcd, 0xda, 0xdd, 0x9a, 0x9e, 0x8a, 0x8e)
	// westLetters make a word only in the western reading: à â ã å æ ç ê ë î
	// ñ õ û (Czech and Slovak have none of them; as Windows-1250 they would be
	// ŕ â ă ĺ ć ç ę ë î ń ő ű).
	westLetters = newByteSet(0xe0, 0xe2, 0xe3, 0xe5, 0xe6, 0xe7, 0xea, 0xeb, 0xee, 0xf1, 0xf5, 0xfb,
		0xc0, 0xc2, 0xc3, 0xc5, 0xc6, 0xc7, 0xca, 0xcb, 0xce, 0xd1, 0xd5, 0xdb)
	// polishLetters: ł ę ń ć ż. A title with them has ą at 0xB9, not š.
	polishLetters = newByteSet(0xb3, 0xa3, 0xea, 0xca, 0xf1, 0xd1, 0xe6, 0xc6, 0xbf, 0xaf)
	// signAfterLetter are signs in Windows-1252 and small letters in a Central
	// European code page: after a letter they are that letter (ł ą/š ľ/ž ż, and
	// ISO-8859-2's ą ľ ś). "¿" opens a Spanish word and never follows a letter.
	signAfterLetter = newByteSet(0xb3, 0xb9, 0xbe, 0xbf, 0xb1, 0xb5, 0xb6)
	// signByLetter are capitals there (Ł Ą/Ľ Ż Ľ/ź Ś Ź): a letter on either
	// side is enough, no western title puts "£", "¥", "¯", "¼", "¦" or "¬"
	// against one.
	signByLetter = newByteSet(0xa3, 0xa5, 0xaf, 0xbc, 0xa6, 0xac)
	// signInWord need a letter on both sides: "¡" and "«" open a western word
	// and "»" closes one (ISO-8859-2: Ą Ť ť).
	signInWord = newByteSet(0xa1, 0xab, 0xbb)
	// latin2Letters are letters in ISO-8859-2 and signs in Windows-1250.
	latin2Letters = newByteSet(0xa1, 0xa6, 0xa9, 0xab, 0xac, 0xae, 0xb1, 0xb5, 0xb6, 0xbb)
	// vowels, for telling ř from ø: plain ones and the accented ones that are
	// vowels in every reading.
	vowels = newByteSet('a', 'e', 'i', 'o', 'u', 'y', 'A', 'E', 'I', 'O', 'U', 'Y',
		0xe1, 0xe9, 0xed, 0xf3, 0xfa, 0xfd, 0xec, 0xf9, 0xe4, 0xf6, 0xfc, 0xf4,
		0xc1, 0xc9, 0xcd, 0xd3, 0xda, 0xdd, 0xcc, 0xd9, 0xc4, 0xd6, 0xdc, 0xd4)
)

// letterByte reports whether c is a letter in every candidate code page:
// ASCII letters, 0xC0 and up without the two arithmetic signs, and š ž Š Ž.
func letterByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	case c >= 0xc0:
		return c != 0xd7 && c != 0xf7
	}
	return c == 0x9a || c == 0x9e || c == 0x8a || c == 0x8e
}

func lowerLetterByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 0xe0 && c != 0xf7) || c == 0x9a || c == 0x9e
}

// weighTitle collects the evidence in one title that is not UTF-8.
func weighTitle(b []byte) titleEvidence {
	var ev titleEvidence
	var czech, onlyCzech, companion, west, polish, sharedSign bool
	for i, c := range b {
		if c < 0x80 {
			continue
		}
		left := i > 0 && letterByte(b[i-1])
		right := i+1 < len(b) && letterByte(b[i+1])
		if c < 0xa0 {
			ev.not8859 = true
			if c == 0x8d || c == 0x8f || c == 0x9d {
				ev.central = true
			}
		}
		inWord := false
		switch {
		case signAfterLetter[c]:
			inWord = left
		case signByLetter[c]:
			inWord = left || right
		case signInWord[c]:
			inWord = left && right
		case c == 0xa9 || c == 0xae:
			// "©" and "®": Š and Ž in ISO-8859-2 when a word goes on after them
			inWord = (left && right) || (i+1 < len(b) && lowerLetterByte(b[i+1]))
		}
		if inWord {
			ev.central = true
			if latin2Letters[c] {
				ev.iso += 2
			}
		}
		if (c == 0xb9 || c == 0xbe) && (left || right) {
			// ą/ľ in Windows-1250, š/ž in ISO-8859-2
			sharedSign = true
		}
		switch {
		case c == 0xf8 || c == 0xd8:
			// ř stands next to a vowel; "Bjørn" and "Eivør" are not Czech
			if (i > 0 && vowels[b[i-1]]) || (i+1 < len(b) && vowels[b[i+1]]) {
				czech, onlyCzech = true, true
			}
		case czechLetters[c]:
			czech = true
			onlyCzech = onlyCzech || czechOnly[c]
		}
		companion = companion || czechCompanions[c]
		west = west || westLetters[c]
		polish = polish || polishLetters[c]
	}
	switch {
	case ev.central:
		ev.ce = 2
	case czech && companion && !west:
		ev.ce = 1
	case west:
		ev.ce = -1
	}
	if sharedSign && onlyCzech && !polish {
		ev.iso++
	}
	if ev.not8859 {
		ev.iso = -2
	}
	return ev
}

// decodeTitle reads b in the named code page.
func decodeTitle(b []byte, charset string) string {
	out := make([]byte, 0, len(b)+8)
	for _, c := range b {
		r := rune(c)
		switch {
		case c < 0x80:
		case charset == charsetWestern:
			if c < 0xa0 {
				r = cp1252High[c-0x80]
			}
		case c >= 0xc0:
			r = centralC0[c-0xc0]
		case charset == charsetCentral:
			r = cp1250High[c-0x80]
		case c >= 0xa0:
			r = latin2A0[c-0xa0]
		}
		out = utf8.AppendRune(out, r)
	}
	return string(out)
}

const (
	// charsetScoreMax bounds a station's scores, so a run of titles saying
	// one thing is outvoted again by a few saying the other.
	charsetScoreMax = 4
	// charsetStationsMax bounds the stations remembered.
	charsetStationsMax = 128
)

// stationCharset is what the titles of one station have said so far.
type stationCharset struct {
	ce, iso int
	// last is the title weighed last: a station repeats its title in every
	// metadata block, and one song must count once.
	last string
}

// titleCharsets keeps the scores per station. The zero value is ready.
type titleCharsets struct {
	mu sync.Mutex
	m  map[string]*stationCharset
}

func clampScore(v int) int { return max(-charsetScoreMax, min(charsetScoreMax, v)) }

// toUTF8 returns title as valid UTF-8 and, when it had to be decoded, the
// name of the code page it was read in ("" for a title that was UTF-8).
func (c *titleCharsets) toUTF8(station, title string) (string, string) {
	if utf8.ValidString(title) {
		return title, ""
	}
	b := []byte(title)
	ev := weighTitle(b)
	c.mu.Lock()
	st := c.m[station]
	if st == nil {
		if c.m == nil || len(c.m) >= charsetStationsMax {
			c.m = make(map[string]*stationCharset)
		}
		st = &stationCharset{}
		c.m[station] = st
	}
	if st.last != title {
		st.ce, st.iso, st.last = clampScore(st.ce+ev.ce), clampScore(st.iso+ev.iso), title
	}
	central, iso := ev.central || st.ce > 0, st.iso > 0 && !ev.not8859
	c.mu.Unlock()
	charset := charsetWestern
	switch {
	case central && iso:
		charset = charsetCentral2
	case central:
		charset = charsetCentral
	}
	return decodeTitle(b, charset), charset
}

// titleToUTF8 is toUTF8 for one title on its own, with no station behind it.
func titleToUTF8(s string) string {
	var c titleCharsets
	out, _ := c.toUTF8("", s)
	return out
}
