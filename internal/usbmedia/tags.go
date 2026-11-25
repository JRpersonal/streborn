package usbmedia

import (
	"bytes"
	"encoding/binary"
	"io"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

// The artist and album a song's own tag names (discussion #1136).
//
// The stick has no index, so a song listed from it carried only what its path
// says: the file name as the title and the folder as the album. A NAS media
// server reads the tags, which is why the same song from a NAS showed its
// artist in Now Playing and the one from the stick did not (ST20, 2026-11-25:
// "01 The First Time Ever I Saw Your Face" over "Singles", no artist).
//
// So the stick's listing reads the tag of each song it hands out, lazily: only
// the files of the page a client asked for (a Browse page, a Search page, one
// BrowseMetadata), never during a search walk, never at boot. One read is
// bounded: the first tagHeadRead bytes, then single frame and box headers past
// them, at most tagReadBudget bytes and tagMaxReads reads per file, so a cover
// picture embedded before the text is stepped over, not read. Formats:
//
//   - MP3 and AAC: ID3v2.2, 2.3 and 2.4 at the start, ID3v1 at the end as the
//     fallback (one 128-byte read);
//   - FLAC: the VORBIS_COMMENT block (an ID3v2 tag in front is read too);
//   - Ogg Vorbis and Opus: the comment header in the first pages;
//   - M4A: the iTunes ilst under moov/udta/meta, wherever moov sits.
//
// WAV and WMA are not read; their songs stay labelled by path as before.

// Tags is what a file's tag names. Every field is "" when the tag has none.
type Tags struct {
	Title  string
	Artist string
	Album  string
}

// Empty reports whether the tag named nothing.
func (t Tags) Empty() bool { return t.Title == "" && t.Artist == "" && t.Album == "" }

const (
	// tagHeadRead is read from the start of every file in one go. Most tags
	// name title, artist and album in their first few hundred bytes.
	tagHeadRead = 4 << 10
	// tagReadBudget bounds the bytes read from one file, the head included.
	tagReadBudget = 64 << 10
	// tagMaxReads bounds the reads past the head: frame and box headers and
	// the bodies of the frames that are wanted.
	tagMaxReads = 64
	// tagFrameMax bounds one ID3 text frame or MP4 item that is read.
	tagFrameMax = 4 << 10
	// tagListMax bounds one Vorbis comment list, which holds every field.
	tagListMax = 16 << 10
	// tagFieldMax bounds one field, in runes, the way the agent bounds any
	// track text a client sends.
	tagFieldMax = 200
)

// ReadTags reads the tag of the file named name (only its extension matters)
// from r, whose length is size. A format that is not read, a file without a
// tag and a tag that cannot be parsed all give empty Tags.
func ReadTags(r io.ReaderAt, size int64, name string) Tags {
	s := newTagSource(r, size)
	var t Tags
	switch strings.ToLower(path.Ext(name)) {
	case ".mp3", ".aac":
		id3v2Tags(s, &t)
		if t.Artist == "" || t.Title == "" || t.Album == "" {
			id3v1Tags(s, &t)
		}
	case ".flac":
		var id3 Tags
		off := id3v2Tags(s, &id3)
		flacTags(s, off, &t)
		t.fillFrom(id3)
	case ".ogg", ".oga":
		oggTags(s, &t)
	case ".m4a":
		mp4Tags(s, &t)
	}
	return t.clean()
}

// fillFrom sets every field t lacks from o.
func (t *Tags) fillFrom(o Tags) {
	if t.Title == "" {
		t.Title = o.Title
	}
	if t.Artist == "" {
		t.Artist = o.Artist
	}
	if t.Album == "" {
		t.Album = o.Album
	}
}

// clean bounds every field and drops control characters and invalid UTF-8.
func (t Tags) clean() Tags {
	return Tags{Title: cleanTag(t.Title), Artist: cleanTag(t.Artist), Album: cleanTag(t.Album)}
}

func cleanTag(v string) string {
	v = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(v, "")))
	if r := []rune(v); len(r) > tagFieldMax {
		v = strings.TrimSpace(string(r[:tagFieldMax]))
	}
	return v
}

// addValue appends v to a field that may hold several values (two artists in
// one tag), skipping empties and repeats.
func addValue(field *string, v string) {
	v = strings.TrimSpace(v)
	switch {
	case v == "" || *field == v:
	case *field == "":
		*field = v
	default:
		*field += " / " + v
	}
}

// tagSource reads one file within the budget.
type tagSource struct {
	r     io.ReaderAt
	size  int64
	head  []byte
	spent int
	reads int
}

func newTagSource(r io.ReaderAt, size int64) *tagSource {
	n := tagHeadRead
	if size >= 0 && size < int64(n) {
		n = int(size)
	}
	b := make([]byte, n)
	got, _ := r.ReadAt(b, 0)
	return &tagSource{r: r, size: size, head: b[:got], spent: n}
}

// at returns the n bytes at off: from the head when they lie in it, else from
// one more read, as long as the budget lasts.
func (s *tagSource) at(off int64, n int) ([]byte, bool) {
	if off < 0 || n < 0 || (s.size >= 0 && off+int64(n) > s.size) {
		return nil, false
	}
	if off+int64(n) <= int64(len(s.head)) {
		return s.head[off : off+int64(n)], true
	}
	if s.reads >= tagMaxReads || s.spent+n > tagReadBudget {
		return nil, false
	}
	s.reads++
	s.spent += n
	b := make([]byte, n)
	if got, _ := s.r.ReadAt(b, off); got < n {
		return nil, false
	}
	return b, true
}

// upTo is at for a body that may be cut short: at most n bytes, fewer when
// the file or the budget ends sooner.
func (s *tagSource) upTo(off int64, n int) []byte {
	if s.size >= 0 && off+int64(n) > s.size {
		n = int(max(0, s.size-off))
	}
	if off+int64(n) > int64(len(s.head)) {
		n = min(n, tagReadBudget-s.spent)
	}
	if n <= 0 {
		return nil
	}
	b, _ := s.at(off, n)
	return b
}

// syncsafe reads a 28-bit ID3v2 size, 7 bits per byte.
func syncsafe(b []byte) int64 {
	return int64(b[0]&0x7f)<<21 | int64(b[1]&0x7f)<<14 | int64(b[2]&0x7f)<<7 | int64(b[3]&0x7f)
}

// unsync undoes ID3v2 unsynchronisation: every 0xFF 0x00 becomes 0xFF.
func unsync(b []byte) []byte {
	if !bytes.Contains(b, []byte{0xff, 0x00}) {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		out = append(out, b[i])
		if b[i] == 0xff && i+1 < len(b) && b[i+1] == 0x00 {
			i++
		}
	}
	return out
}

// id3v2Tags reads an ID3v2 tag at the start of the file into t and returns
// the offset just past it, 0 when there is none.
func id3v2Tags(s *tagSource, t *Tags) int64 {
	h, ok := s.at(0, 10)
	if !ok || string(h[:3]) != "ID3" {
		return 0
	}
	ver, flags := h[3], h[5]
	size := syncsafe(h[6:10])
	end := 10 + size
	if ver == 4 && flags&0x10 != 0 {
		end += 10 // footer
	}
	if ver < 2 || ver > 4 || ver == 2 && flags&0x40 != 0 {
		// An unknown version, or a v2.2 tag with compression: skipped.
		return end
	}
	// get reads tag bytes by their offset after the header. A tag
	// unsynchronised as a whole (v2.2 and v2.3) is read up front and
	// undone; any other is walked frame by frame, so a picture frame is
	// stepped over rather than read.
	get := func(off int64, n int) ([]byte, bool) { return s.at(10+off, n) }
	if flags&0x80 != 0 && ver < 4 {
		body := unsync(s.upTo(10, int(min(size, tagReadBudget))))
		get = func(off int64, n int) ([]byte, bool) {
			if off < 0 || off+int64(n) > int64(len(body)) {
				return nil, false
			}
			return body[off : off+int64(n)], true
		}
		size = int64(len(body))
	}
	var pos int64
	if flags&0x40 != 0 && ver >= 3 {
		eh, ok := get(0, 4)
		if !ok {
			return end
		}
		if ver == 3 {
			pos = 4 + int64(binary.BigEndian.Uint32(eh))
		} else {
			pos = syncsafe(eh)
		}
	}
	hdr, idLen := 10, 4
	if ver == 2 {
		hdr, idLen = 6, 3
	}
	var albumArtist string
	for i := 0; i < 256 && pos+int64(hdr) <= size; i++ {
		fh, ok := get(pos, hdr)
		if !ok || fh[0] == 0 {
			break // padding, or past the budget
		}
		id := string(fh[:idLen])
		var n int64
		switch ver {
		case 2:
			n = int64(fh[3])<<16 | int64(fh[4])<<8 | int64(fh[5])
		case 3:
			n = int64(binary.BigEndian.Uint32(fh[4:8]))
		default:
			n = syncsafe(fh[4:8])
		}
		if n <= 0 || pos+int64(hdr)+n > size {
			break
		}
		var field *string
		switch id {
		case "TIT2", "TT2":
			field = &t.Title
		case "TPE1", "TP1":
			field = &t.Artist
		case "TPE2", "TP2":
			field = &albumArtist
		case "TALB", "TAL":
			field = &t.Album
		}
		if field != nil && *field == "" {
			if body, ok := get(pos+int64(hdr), int(min(n, tagFrameMax))); ok {
				if text, ok := id3FrameBody(ver, fh, body); ok {
					*field = id3Text(text)
				}
			}
		}
		if t.Title != "" && t.Artist != "" && t.Album != "" {
			break
		}
		pos += int64(hdr) + n
	}
	if t.Artist == "" {
		t.Artist = albumArtist
	}
	return end
}

// id3FrameBody undoes what a frame's format flags did to its body: grouping
// and a data length indicator are skipped, v2.4 unsynchronisation undone. A
// compressed or encrypted frame is not read.
func id3FrameBody(ver byte, fh, body []byte) ([]byte, bool) {
	switch ver {
	case 3:
		f := fh[9]
		if f&0xc0 != 0 {
			return nil, false
		}
		if f&0x20 != 0 && len(body) > 0 {
			body = body[1:]
		}
	case 4:
		f := fh[9]
		if f&0x0c != 0 {
			return nil, false
		}
		if f&0x40 != 0 && len(body) > 0 {
			body = body[1:]
		}
		if f&0x02 != 0 {
			body = unsync(body)
		}
		if f&0x01 != 0 {
			if len(body) < 4 {
				return nil, false
			}
			body = body[4:]
		}
	}
	return body, true
}

// id3Text decodes a text frame: an encoding byte, then one or more values
// ending in NUL. Several values (ID3v2.4) are joined.
func id3Text(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	enc, b := b[0], b[1:]
	var out string
	switch enc {
	case 1, 2:
		bigEndian := enc == 2
		for len(b) >= 2 {
			// Each value may carry its own byte order mark.
			if b[0] == 0xff && b[1] == 0xfe {
				bigEndian, b = false, b[2:]
			} else if b[0] == 0xfe && b[1] == 0xff {
				bigEndian, b = true, b[2:]
			}
			var units []uint16
			for len(b) >= 2 {
				var u uint16
				if bigEndian {
					u = binary.BigEndian.Uint16(b)
				} else {
					u = binary.LittleEndian.Uint16(b)
				}
				b = b[2:]
				if u == 0 {
					break
				}
				units = append(units, u)
			}
			addValue(&out, string(utf16.Decode(units)))
		}
	case 3:
		for _, v := range bytes.Split(b, []byte{0}) {
			addValue(&out, string(v))
		}
	default:
		for _, v := range bytes.Split(b, []byte{0}) {
			addValue(&out, latin1(v))
		}
	}
	return out
}

// latin1 decodes ISO-8859-1, which maps each byte to the rune of its value.
func latin1(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}

// id3v1Tags fills what t lacks from an ID3v1 tag, the last 128 bytes.
func id3v1Tags(s *tagSource, t *Tags) {
	if s.size < 128 {
		return
	}
	b, ok := s.at(s.size-128, 128)
	if !ok || string(b[:3]) != "TAG" {
		return
	}
	field := func(f []byte) string {
		if i := bytes.IndexByte(f, 0); i >= 0 {
			f = f[:i]
		}
		return strings.TrimSpace(latin1(f))
	}
	t.fillFrom(Tags{Title: field(b[3:33]), Artist: field(b[33:63]), Album: field(b[63:93])})
}

// flacTags walks the metadata blocks after "fLaC" at off to VORBIS_COMMENT.
// The blocks before it (a seek table, a picture) are stepped over by their
// headers.
func flacTags(s *tagSource, off int64, t *Tags) {
	if m, ok := s.at(off, 4); !ok || string(m) != "fLaC" {
		return
	}
	off += 4
	for i := 0; i < 32; i++ {
		h, ok := s.at(off, 4)
		if !ok {
			return
		}
		n := int64(h[1])<<16 | int64(h[2])<<8 | int64(h[3])
		if h[0]&0x7f == 4 {
			vorbisComments(s.upTo(off+4, int(min(n, tagListMax))), t)
			return
		}
		if h[0]&0x80 != 0 {
			return // the last block
		}
		off += 4 + n
	}
}

// vorbisComments reads a Vorbis comment list (FLAC, Ogg Vorbis, Opus): a
// vendor string, a count, then "KEY=value" entries, all lengths little
// endian. A list cut short by the budget yields what it holds.
func vorbisComments(b []byte, t *Tags) {
	if len(b) < 8 {
		return
	}
	p := 4 + int64(binary.LittleEndian.Uint32(b))
	if p+4 > int64(len(b)) {
		return
	}
	count := binary.LittleEndian.Uint32(b[p:])
	p += 4
	var albumArtist string
	for i := uint32(0); i < count && i < 1024 && p+4 <= int64(len(b)); i++ {
		n := int64(binary.LittleEndian.Uint32(b[p:]))
		p += 4
		if n > int64(len(b))-p {
			break
		}
		k, v, ok := strings.Cut(string(b[p:p+n]), "=")
		p += n
		if !ok {
			continue
		}
		switch strings.ToUpper(k) {
		case "TITLE":
			addValue(&t.Title, v)
		case "ARTIST":
			addValue(&t.Artist, v)
		case "ALBUM":
			addValue(&t.Album, v)
		case "ALBUMARTIST", "ALBUM ARTIST":
			addValue(&albumArtist, v)
		}
	}
	if t.Artist == "" {
		t.Artist = albumArtist
	}
}

// oggTags joins the payloads of the first pages of the first logical stream
// and reads the comment header in them: the second packet, "\x03vorbis" for
// Vorbis, "OpusTags" for Opus.
func oggTags(s *tagSource, t *Tags) {
	var data []byte
	var serial uint32
	off := int64(0)
	for page := 0; page < 16 && len(data) < tagReadBudget; page++ {
		h, ok := s.at(off, 27)
		if !ok || string(h[:4]) != "OggS" {
			break
		}
		segs, ok := s.at(off+27, int(h[26]))
		if !ok {
			break
		}
		n := 0
		for _, l := range segs {
			n += int(l)
		}
		sn := binary.LittleEndian.Uint32(h[14:18])
		if page == 0 {
			serial = sn
		}
		if sn == serial {
			want := min(n, tagListMax)
			body := s.upTo(off+27+int64(len(segs)), want)
			data = append(data, body...)
			if len(body) < want || want < n {
				break
			}
		}
		off += 27 + int64(len(segs)) + int64(n)
		if i := commentStart(data); i >= 0 && page >= 1 {
			// The comment packet has begun; one more page at most is
			// needed for a long list, and the budget stops it anyway.
			if len(data)-i > tagFrameMax {
				break
			}
		}
	}
	if i := commentStart(data); i >= 0 {
		vorbisComments(data[i:], t)
	}
}

// commentStart is the offset of the comment list in joined Ogg payloads, -1
// when the comment header is not in them.
func commentStart(data []byte) int {
	if i := bytes.Index(data, []byte("\x03vorbis")); i >= 0 {
		return i + 7
	}
	if i := bytes.Index(data, []byte("OpusTags")); i >= 0 {
		return i + 8
	}
	return -1
}

// mp4Box is one box: where it starts, where its children or data start, and
// where it ends.
type mp4Box struct{ body, end int64 }

// findBox walks the boxes from off to end and returns the first of type typ.
// Each step reads one header, so a large mdat before moov costs nothing.
func findBox(s *tagSource, off, end int64, typ string) (mp4Box, bool) {
	for i := 0; i < 64 && off+8 <= end; i++ {
		h, ok := s.at(off, 8)
		if !ok {
			return mp4Box{}, false
		}
		size, hl := int64(binary.BigEndian.Uint32(h)), int64(8)
		switch size {
		case 0:
			size = end - off
		case 1:
			x, ok := s.at(off+8, 8)
			if !ok {
				return mp4Box{}, false
			}
			size, hl = int64(binary.BigEndian.Uint64(x)), 16
		}
		if size < hl || off+size > end {
			return mp4Box{}, false
		}
		if string(h[4:8]) == typ {
			return mp4Box{body: off + hl, end: off + size}, true
		}
		off += size
	}
	return mp4Box{}, false
}

// mp4Tags reads the iTunes metadata list: moov/udta/meta/ilst, whose items
// (©nam, ©ART, aART, ©alb) each hold a data box with the text.
func mp4Tags(s *tagSource, t *Tags) {
	if s.size < 8 {
		return
	}
	moov, ok := findBox(s, 0, s.size, "moov")
	if !ok {
		return
	}
	udta, ok := findBox(s, moov.body, moov.end, "udta")
	if !ok {
		return
	}
	meta, ok := findBox(s, udta.body, udta.end, "meta")
	if !ok {
		return
	}
	// meta is a full box (4 bytes of version and flags before its children);
	// QuickTime writes it without them, so its first child starts at once.
	start := meta.body + 4
	if h, ok := s.at(meta.body+4, 4); ok && string(h) == "hdlr" {
		start = meta.body
	}
	ilst, ok := findBox(s, start, meta.end, "ilst")
	if !ok {
		return
	}
	var albumArtist string
	off := ilst.body
	for i := 0; i < 64 && off+8 <= ilst.end; i++ {
		h, ok := s.at(off, 8)
		if !ok {
			break
		}
		size := int64(binary.BigEndian.Uint32(h))
		if size < 8 || off+size > ilst.end {
			break
		}
		var field *string
		switch string(h[4:8]) {
		case "\xa9nam":
			field = &t.Title
		case "\xa9ART":
			field = &t.Artist
		case "aART":
			field = &albumArtist
		case "\xa9alb":
			field = &t.Album
		}
		if field != nil && *field == "" {
			// The data box: size, "data", a type (1 is UTF-8), a locale,
			// then the text.
			if d := s.upTo(off+8, int(min(size-8, tagFrameMax))); len(d) >= 16 && string(d[4:8]) == "data" {
				n := min(int64(binary.BigEndian.Uint32(d)), int64(len(d)))
				if n >= 16 && binary.BigEndian.Uint32(d[8:12])&0xffffff == 1 {
					*field = string(d[16:n])
				}
			}
		}
		if t.Title != "" && t.Artist != "" && t.Album != "" {
			break
		}
		off += size
	}
	if t.Artist == "" {
		t.Artist = albumArtist
	}
}

// tagCacheMax bounds the tag cache: about 300 bytes an entry, so at most
// about 1 MB on a speaker with about 120 MB in all. A full cache starts over.
const tagCacheMax = 4000

type cachedTags struct {
	size int64
	mod  time.Time
	tags Tags
}

// TagsOf returns the tag of the playable file p (an on-disk path or an object
// id, as for OpenFile). A file read before and not changed since comes from
// memory; ok is false when the file cannot be opened.
func (l *Library) TagsOf(p string) (Tags, bool) {
	f, err := l.OpenFile(p)
	if err != nil {
		return Tags{}, false
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return Tags{}, false
	}
	l.tagMu.Lock()
	c, hit := l.tagCache[p]
	l.tagMu.Unlock()
	if hit && c.size == fi.Size() && c.mod.Equal(fi.ModTime()) {
		return c.tags, true
	}
	tags := ReadTags(f, fi.Size(), p)
	l.tagMu.Lock()
	if l.tagCache == nil || len(l.tagCache) >= tagCacheMax {
		l.tagCache = make(map[string]cachedTags)
	}
	l.tagCache[p] = cachedTags{size: fi.Size(), mod: fi.ModTime(), tags: tags}
	l.tagMu.Unlock()
	l.tagReads.Add(1)
	return tags, true
}

// TagReads is how many files had their tag read from the stick (cache hits
// not counted), for the debug state.
func (l *Library) TagReads() int64 { return l.tagReads.Load() }

// AddTags sets Artist and Album of the songs in entries from their tags, in
// place, until deadline: a song past it keeps the label its path gives. It is
// called with one page of a listing, never with a whole walk.
func (l *Library) AddTags(entries []Entry, deadline time.Time) {
	for i := range entries {
		e := &entries[i]
		if e.Folder || e.MIME == "" {
			continue
		}
		if time.Now().After(deadline) {
			return
		}
		if tags, ok := l.TagsOf(e.Path); ok {
			e.Artist, e.Album = tags.Artist, tags.Album
		}
	}
}
