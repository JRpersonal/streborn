// Library sort order A to Z (#1257).
//
// Folders and songs come in the order the media server sends them, and a
// server on Linux puts a name starting with "_" at the bottom, where nobody
// looks for the folder they named that way to keep it on top. "A to Z" puts
// names starting with "_" first, then those starting with a digit, then the
// rest, case and accents ignored and numbers compared as numbers ("2" before
// "10"). The same rule lives in the desktop app (src/librarysort.js) and on
// the phone page; tests on both sides pin the same cases so the three do
// not drift apart.

package webui

import (
	"sort"
	"strings"

	"github.com/JRpersonal/streborn/dlna"
)

// libraryNameRank is the group a name sorts in: "_" first, digits next.
func libraryNameRank(name string) int {
	name = strings.TrimSpace(name)
	if name == "" {
		return 2
	}
	r := []rune(name)[0]
	switch {
	case r == '_':
		return 0
	case isASCIIDigit(r):
		return 1
	}
	return 2
}

// libraryFoldRunes maps the letters whose accent is not a separate letter for
// sorting onto their base. The desktop app does the same with Unicode
// decomposition; this table covers the languages STR speaks.
var libraryFoldRunes = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'ā': "a", 'ą': "a", 'æ': "ae",
	'ç': "c", 'ć': "c", 'č': "c",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ė': "e", 'ę': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ī': "i", 'į': "i", 'ı': "i",
	'ñ': "n", 'ń': "n", 'ņ': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ū': "u", 'ų': "u",
	'ý': "y", 'ÿ': "y",
	'ß': "ss", 'ś': "s", 'š': "s", 'ş': "s",
	'ź': "z", 'ż': "z", 'ž': "z",
	'ğ': "g", 'ģ': "g", 'ķ': "k", 'ļ': "l", 'ł': "l",
	'й': "и", 'ё': "е", 'ї': "і",
}

// libraryFoldName is the name as A to Z compares it: trimmed, lower case,
// accents folded.
func libraryFoldName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if f, ok := libraryFoldRunes[r]; ok {
			b.WriteString(f)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// libraryNameLess orders two names A to Z.
func libraryNameLess(a, b string) bool {
	if ra, rb := libraryNameRank(a), libraryNameRank(b); ra != rb {
		return ra < rb
	}
	return naturalLess(libraryFoldName(a), libraryFoldName(b))
}

// naturalLess compares runs of digits by their number and everything else by
// code point, so "track 2" comes before "track 10".
func naturalLess(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	i, j := 0, 0
	for i < len(ra) && j < len(rb) {
		if isASCIIDigit(ra[i]) && isASCIIDigit(rb[j]) {
			si := i
			for i < len(ra) && isASCIIDigit(ra[i]) {
				i++
			}
			sj := j
			for j < len(rb) && isASCIIDigit(rb[j]) {
				j++
			}
			na := strings.TrimLeft(string(ra[si:i]), "0")
			nb := strings.TrimLeft(string(rb[sj:j]), "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if ra[i] != rb[j] {
			return ra[i] < rb[j]
		}
		i++
		j++
	}
	return len(ra)-i < len(rb)-j
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

// sortLibraryFolders returns the folders A to Z; the input is left alone.
func sortLibraryFolders(in []dlna.Container) []dlna.Container {
	out := append([]dlna.Container(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return libraryNameLess(out[i].Title, out[j].Title) })
	return out
}

// libraryKeepsTrackOrder reports whether a folder's songs keep their order
// under A to Z: an album (by its class, or because every song carries its own
// track number) plays in track order, a playlist in the order its owner gave
// it.
func libraryKeepsTrackOrder(items []dlna.Item, class string) bool {
	c := strings.ToLower(strings.TrimSpace(class))
	if strings.HasPrefix(c, "object.container.album") || dlna.IsPlaylistClass(class) {
		return true
	}
	if len(items) < 2 {
		return true
	}
	type disc struct{ d, t int }
	seen := make(map[disc]bool, len(items))
	for _, it := range items {
		if it.TrackNumber <= 0 {
			return false
		}
		k := disc{it.DiscNumber, it.TrackNumber}
		if seen[k] {
			return false
		}
		seen[k] = true
	}
	return true
}

// sortLibraryTracks returns a folder's songs A to Z by title, unless the
// folder keeps its order (libraryKeepsTrackOrder). The input is left alone.
func sortLibraryTracks(in []dlna.Item, class string) []dlna.Item {
	if libraryKeepsTrackOrder(in, class) {
		return in
	}
	out := append([]dlna.Item(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return libraryNameLess(out[i].Title, out[j].Title) })
	return out
}
