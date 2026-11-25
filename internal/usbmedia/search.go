package usbmedia

import (
	"context"
	"errors"
	"path"
	"strings"
)

// Searching the stick (discussion #1136, 2026-11-22: "searching music servers
// does not display any results for the USB stick").
//
// The stick has no index: a song's title is its file name and the only
// grouping a walk can see is the folders it sits in (tags are read for the
// hits handed out, tags.go, never during the walk). So a search reads the directories,
// the same one-directory reads a Browse makes, and matches the file names and
// the folder names on the way to each file. No file is opened or stat'ed, and
// nothing is kept between searches: a stick that nobody searches costs nothing.
//
// Folder names stand in for the artist and the album. A stick laid out as
// Artist/Album/01 Song.mp3, or as "Artist - Album"/01 Song.mp3, then answers
// an artist search with that artist's songs. Before, the clients' browse walk
// matched only the file name and the folder the file sits in, never an artist
// (the DIDL carries none), so a search for an artist found nothing at all.

// Bounds of one search. A stick is local to the agent, so the cost is the
// directory reads alone; these keep a stick with a pathological tree from
// pinning the speaker's CPU.
const (
	// SearchMaxFolders caps the directories one search reads.
	SearchMaxFolders = 3000
	// SearchMaxDepth caps how far below the starting folder a search goes.
	SearchMaxDepth = 16
	// SearchMaxHits caps the hits one search collects, whatever is asked.
	SearchMaxHits = 2000
)

// SearchTerm is one "contains" test of a search, case-insensitive. A file
// matches the term when Value is in its title (InTitle) or in the name of any
// folder on its way from the stick's root (InFolders). Terms are alternatives:
// a file matching any of them is a hit.
type SearchTerm struct {
	Value     string
	InTitle   bool
	InFolders bool
}

// The fields a library search can be narrowed to, as the STR clients name them
// (dlna.SearchAny and friends).
const (
	FieldAny    = "any"
	FieldTitle  = "title"
	FieldArtist = "artist"
	FieldAlbum  = "album"
)

// TermsFor is the search for query narrowed to field: the title is the file
// name, artist and album are the folder names, any is both. Empty for an empty
// query.
func TermsFor(query, field string) []SearchTerm {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	switch field {
	case FieldTitle:
		return []SearchTerm{{Value: q, InTitle: true}}
	case FieldArtist, FieldAlbum:
		return []SearchTerm{{Value: q, InFolders: true}}
	}
	return []SearchTerm{{Value: q, InTitle: true, InFolders: true}}
}

// SearchResult is what one search found.
type SearchResult struct {
	// Hits are the matching playable files, in the order a user browsing the
	// stick meets them (folders depth first, each in natural order).
	Hits []Entry
	// Partial says the search did not cover the whole tree: it stopped at a
	// bound, the deadline, or a folder it could not read. Stopping because
	// limit hits were found is not partial; the caller knows its own limit.
	Partial bool
	// Folders is how many directories were read.
	Folders int
}

// Search looks for the playable files under the folder from ("0" or "" for the
// whole stick) that match any of terms, and stops once limit hits are found.
// An error is returned only when from itself cannot be read.
func (l *Library) Search(ctx context.Context, from string, terms []SearchTerm, limit int) (SearchResult, error) {
	var out SearchResult
	terms = cleanTerms(terms)
	if len(terms) == 0 {
		return out, nil
	}
	if limit <= 0 || limit > SearchMaxHits {
		limit = SearchMaxHits
	}
	if from == "" {
		from = "0"
	}
	if _, err := relPath(from); err != nil {
		return out, err
	}
	type node struct {
		id    string
		depth int
		// folderHit: a folder on the way here already matched a term that
		// looks at folder names.
		folderHit bool
	}
	start := node{id: from}
	if from != "0" {
		for _, name := range strings.Split(from, "/") {
			if folderMatches(terms, name) {
				start.folderHit = true
				break
			}
		}
	}
	// Depth first, children pushed in reverse so they are read in natural
	// order: the hits of one album come out together and in track order.
	stack := []node{start}
	for len(stack) > 0 {
		if out.Folders >= SearchMaxFolders || ctx.Err() != nil {
			out.Partial = true
			break
		}
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, truncated, err := l.list(n.id)
		out.Folders++
		if err != nil {
			if n.id == from {
				return out, err
			}
			// A folder that cannot be read is a hole in the coverage.
			out.Partial = true
			continue
		}
		if truncated {
			out.Partial = true
		}
		var sub []node
		for _, e := range entries {
			if e.Folder {
				if n.depth+1 > SearchMaxDepth {
					out.Partial = true
					continue
				}
				sub = append(sub, node{id: e.ID, depth: n.depth + 1, folderHit: n.folderHit || folderMatches(terms, e.Title)})
				continue
			}
			if n.folderHit || titleMatches(terms, e.Title) {
				out.Hits = append(out.Hits, e)
				if len(out.Hits) >= limit {
					return out, nil
				}
			}
		}
		for i := len(sub) - 1; i >= 0; i-- {
			stack = append(stack, sub[i])
		}
	}
	return out, nil
}

// cleanTerms drops the terms that test nothing and lower-cases the rest.
func cleanTerms(terms []SearchTerm) []SearchTerm {
	out := make([]SearchTerm, 0, len(terms))
	for _, t := range terms {
		v := strings.ToLower(strings.TrimSpace(t.Value))
		if v == "" || (!t.InTitle && !t.InFolders) {
			continue
		}
		t.Value = v
		out = append(out, t)
	}
	return out
}

func titleMatches(terms []SearchTerm, title string) bool {
	lower := strings.ToLower(title)
	for _, t := range terms {
		if t.InTitle && strings.Contains(lower, t.Value) {
			return true
		}
	}
	return false
}

func folderMatches(terms []SearchTerm, name string) bool {
	lower := strings.ToLower(name)
	for _, t := range terms {
		if t.InFolders && strings.Contains(lower, t.Value) {
			return true
		}
	}
	return false
}

// ErrUnsupportedCriteria is a SearchCriteria the stick cannot answer: one
// without a "contains" test on the title, an artist or an album.
var ErrUnsupportedCriteria = errors.New("usbmedia: unsupported search criteria")

// ParseCriteria reads the "contains" tests of a ContentDirectory
// SearchCriteria into terms. It understands what the STR clients send
// (dlna.SearchCriteria) and the plain forms generic clients send:
// dc:title contains "x" is a title test; upnp:artist, dc:creator, upnp:album
// and upnp:genre contains "x" are folder tests, the folders being all the
// stick has for them. Tests are taken as alternatives. The class clause is not
// needed: only audio files are ever hits.
func ParseCriteria(criteria string) ([]SearchTerm, error) {
	var terms []SearchTerm
	s := criteria
	for {
		i := strings.Index(s, " contains ")
		if i < 0 {
			break
		}
		prop := lastWord(s[:i])
		rest := strings.TrimLeft(s[i+len(" contains "):], " ")
		val, tail, ok := quoted(rest)
		if !ok {
			return nil, ErrUnsupportedCriteria
		}
		s = tail
		t := SearchTerm{Value: val}
		switch strings.ToLower(prop) {
		case "dc:title":
			t.InTitle = true
		case "upnp:artist", "dc:creator", "upnp:album", "upnp:genre":
			t.InFolders = true
		default:
			continue
		}
		terms = mergeTerm(terms, t)
	}
	if len(cleanTerms(terms)) == 0 {
		return nil, ErrUnsupportedCriteria
	}
	return terms, nil
}

// mergeTerm folds a test of the same value into the term already holding it,
// so "title or artist or album contains x" is one term.
func mergeTerm(terms []SearchTerm, t SearchTerm) []SearchTerm {
	for i := range terms {
		if terms[i].Value == t.Value {
			terms[i].InTitle = terms[i].InTitle || t.InTitle
			terms[i].InFolders = terms[i].InFolders || t.InFolders
			return terms
		}
	}
	return append(terms, t)
}

// lastWord is the property name in front of an operator, without the
// parenthesis a grouped criteria puts before it.
func lastWord(s string) string {
	s = strings.TrimRight(s, " ")
	if i := strings.LastIndexAny(s, " ("); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// quoted reads a criteria string value: a double-quoted string in which a
// backslash escapes the next character. It returns the value and what follows.
func quoted(s string) (string, string, bool) {
	if !strings.HasPrefix(s, `"`) {
		return "", s, false
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '"':
			return b.String(), s[i+1:], true
		default:
			b.WriteByte(c)
		}
	}
	return "", "", false
}

// FileAlbum is the album a file is listed with: the folder it sits in, "" for
// a file in the stick's root. What a Browse and a Search both put in the DIDL.
func FileAlbum(id string) string {
	parent := Parent(id)
	if parent == "0" {
		return ""
	}
	return path.Base(parent)
}
