package usbmedia

import (
	"bytes"
	"context"
	"encoding/binary"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/JRpersonal/streborn/dlna"
)

// Every fixture here is synthetic: made-up names, no real song, no real tag.

// countingReader counts the bytes read from it, to pin the read budget.
type countingReader struct {
	r     *bytes.Reader
	bytes int
	reads int
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	c.reads++
	c.bytes += len(p)
	return c.r.ReadAt(p, off)
}

func syncsafeBytes(n int) []byte {
	return []byte{byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
}

// id3Frame builds a v2.3 (plain size) or v2.4 (syncsafe size) frame.
func id3Frame(ver byte, id string, body []byte) []byte {
	out := []byte(id)
	if ver == 4 {
		out = append(out, syncsafeBytes(len(body))...)
	} else {
		out = binary.BigEndian.AppendUint32(out, uint32(len(body)))
	}
	out = append(out, 0, 0)
	return append(out, body...)
}

// id3Tag wraps frames in an ID3v2 header, with some padding after them.
func id3Tag(ver, flags byte, frames ...[]byte) []byte {
	body := bytes.Join(frames, nil)
	body = append(body, make([]byte, 64)...)
	out := append([]byte{'I', 'D', '3', ver, 0, flags}, syncsafeBytes(len(body))...)
	return append(out, body...)
}

func latin1Text(s string) []byte { return append([]byte{0}, s...) }
func utf8Text(s string) []byte   { return append([]byte{3}, s...) }

func utf16Text(s string) []byte {
	out := []byte{1, 0xff, 0xfe}
	for _, u := range utf16.Encode([]rune(s)) {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return append(out, 0, 0)
}

func vorbisList(entries ...string) []byte {
	out := binary.LittleEndian.AppendUint32(nil, 4)
	out = append(out, "test"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(entries)))
	for _, e := range entries {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(e)))
		out = append(out, e...)
	}
	return out
}

func flacBlock(typ byte, last bool, body []byte) []byte {
	if last {
		typ |= 0x80
	}
	n := len(body)
	return append([]byte{typ, byte(n >> 16), byte(n >> 8), byte(n)}, body...)
}

// oggPage builds one Ogg page holding payload (lacing for one packet).
func oggPage(serial uint32, seq uint32, payload []byte) []byte {
	var segs []byte
	n := len(payload)
	for n >= 255 {
		segs = append(segs, 255)
		n -= 255
	}
	segs = append(segs, byte(n))
	h := []byte("OggS")
	h = append(h, 0, 0)
	h = append(h, make([]byte, 8)...)
	h = binary.LittleEndian.AppendUint32(h, serial)
	h = binary.LittleEndian.AppendUint32(h, seq)
	h = append(h, 0, 0, 0, 0)
	h = append(h, byte(len(segs)))
	h = append(h, segs...)
	return append(h, payload...)
}

func box(typ string, children ...[]byte) []byte {
	body := bytes.Join(children, nil)
	out := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	out = append(out, typ...)
	return append(out, body...)
}

func ilstItem(typ, text string) []byte {
	data := append([]byte{0, 0, 0, 1, 0, 0, 0, 0}, text...)
	return box(typ, box("data", data))
}

// m4aFile builds ftyp, a large mdat, then moov at the end (a file that was
// not "optimised for streaming").
func m4aFile(items ...[]byte) []byte {
	hdlr := box("hdlr", make([]byte, 25))
	meta := box("meta", append([]byte{0, 0, 0, 0}, append(hdlr, box("ilst", items...)...)...))
	moov := box("moov", box("mvhd", make([]byte, 100)), box("trak", make([]byte, 3000)), box("udta", meta))
	out := box("ftyp", []byte("M4A \x00\x00\x00\x00"))
	out = append(out, box("mdat", make([]byte, 300<<10))...)
	return append(out, moov...)
}

func TestReadTags(t *testing.T) {
	bigPicture := make([]byte, 200<<10) // a cover frame far larger than the read budget
	mp3Audio := make([]byte, 2048)
	id3v1 := func(title, artist, album string) []byte {
		b := make([]byte, 128)
		copy(b, "TAG")
		copy(b[3:33], title)
		copy(b[33:63], artist)
		copy(b[63:93], album)
		return b
	}
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	opusHead := append([]byte("OpusHead"), make([]byte, 11)...)
	vorbisIdent := append([]byte("\x01vorbis"), make([]byte, 23)...)

	cases := []struct {
		name string
		data []byte
		want Tags
	}{
		{"v23.mp3", cat(id3Tag(3, 0,
			id3Frame(3, "TIT2", latin1Text("First Song")),
			id3Frame(3, "TPE1", utf16Text("Ärtist Ünö")),
			id3Frame(3, "TALB", latin1Text("Sample Album"))), mp3Audio),
			Tags{"First Song", "Ärtist Ünö", "Sample Album"}},
		{"picture-first.mp3", cat(id3Tag(3, 0,
			id3Frame(3, "APIC", bigPicture),
			id3Frame(3, "TPE1", latin1Text("After The Picture")),
			id3Frame(3, "TIT2", latin1Text("Late Title"))), mp3Audio),
			Tags{"Late Title", "After The Picture", ""}},
		{"v24-multi.mp3", cat(id3Tag(4, 0,
			id3Frame(4, "TPE1", utf8Text("One\x00Two")),
			id3Frame(4, "TALB", utf8Text("Ünicode Album"))), mp3Audio),
			Tags{"", "One / Two", "Ünicode Album"}},
		{"v22.mp3", cat([]byte{'I', 'D', '3', 2, 0, 0}, syncsafeBytes(40),
			[]byte("TP1"), []byte{0, 0, 8}, latin1Text("Old Tag"),
			[]byte("TT2"), []byte{0, 0, 6}, latin1Text("Short"),
			make([]byte, 40-14-12), mp3Audio),
			Tags{"Short", "Old Tag", ""}},
		{"album-artist-only.mp3", cat(id3Tag(3, 0, id3Frame(3, "TPE2", latin1Text("Band"))), mp3Audio),
			Tags{"", "Band", ""}},
		{"v1-only.mp3", cat(mp3Audio, id3v1("Tail Title", "Tail Artist", "Tail Album")),
			Tags{"Tail Title", "Tail Artist", "Tail Album"}},
		{"v2-and-v1.mp3", cat(id3Tag(3, 0, id3Frame(3, "TIT2", latin1Text("Front"))), mp3Audio, id3v1("Back", "Back Artist", "")),
			Tags{"Front", "Back Artist", ""}},
		// Unsynchronised as a whole: the frame says 4 bytes, the stick holds
		// 5, because 0xFF got a 0x00 put after it.
		{"unsync.mp3", cat(id3Tag(3, 0x80,
			[]byte("TPE1\x00\x00\x00\x04\x00\x00\x00A\xff\x00B"),
			id3Frame(3, "TIT2", latin1Text("After"))), mp3Audio),
			Tags{"After", "AÿB", ""}},
		{"a.flac", cat([]byte("fLaC"), flacBlock(0, false, make([]byte, 34)),
			flacBlock(3, false, make([]byte, 6000)), // a seek table past the head
			flacBlock(6, false, bigPicture[:100<<10]),
			flacBlock(4, true, vorbisList("title=Flac Title", "ARTIST=Flac Artist", "Artist=Second", "ALBUM=Flac Album"))),
			Tags{"Flac Title", "Flac Artist / Second", "Flac Album"}},
		{"albumartist.flac", cat([]byte("fLaC"), flacBlock(0, false, make([]byte, 34)),
			flacBlock(4, true, vorbisList("ALBUMARTIST=Whole Band"))),
			Tags{"", "Whole Band", ""}},
		{"id3-in-front.flac", cat(id3Tag(3, 0, id3Frame(3, "TPE1", latin1Text("Id3 Artist"))),
			[]byte("fLaC"), flacBlock(4, true, vorbisList("TITLE=Comment Title"))),
			Tags{"Comment Title", "Id3 Artist", ""}},
		{"v.ogg", cat(oggPage(7, 0, vorbisIdent),
			oggPage(7, 1, append([]byte("\x03vorbis"), vorbisList("ARTIST=Ogg Artist", "TITLE=Ogg Title")...))),
			Tags{"Ogg Title", "Ogg Artist", ""}},
		{"o.ogg", cat(oggPage(9, 0, opusHead),
			oggPage(9, 1, append([]byte("OpusTags"), vorbisList("album=Opus Album", "artist=Opus Artist")...))),
			Tags{"", "Opus Artist", "Opus Album"}},
		{"moov-at-end.m4a", m4aFile(ilstItem("\xa9nam", "Mp4 Title"), ilstItem("covr", string(make([]byte, 5000))),
			ilstItem("\xa9ART", "Mp4 Artist"), ilstItem("\xa9alb", "Mp4 Album")),
			Tags{"Mp4 Title", "Mp4 Artist", "Mp4 Album"}},
		{"aart.m4a", m4aFile(ilstItem("aART", "Album Artist Only")), Tags{"", "Album Artist Only", ""}},
		{"no-tag.mp3", mp3Audio, Tags{}},
		{"tagged.wav", cat([]byte("RIFF"), make([]byte, 100)), Tags{}},
		{"x.wma", id3Tag(3, 0, id3Frame(3, "TPE1", latin1Text("Not Read"))), Tags{}},
		{"control.mp3", cat(id3Tag(3, 0, id3Frame(3, "TPE1", latin1Text("Bad\x01\x02Text  "))), mp3Audio),
			Tags{"", "Bad  Text", ""}},
	}
	for _, c := range cases {
		cr := &countingReader{r: bytes.NewReader(c.data)}
		got := ReadTags(cr, int64(len(c.data)), c.name)
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if cr.bytes > tagReadBudget+tagHeadRead || cr.reads > tagMaxReads+1 {
			t.Errorf("%s: read %d bytes in %d reads, over the budget", c.name, cr.bytes, cr.reads)
		}
	}
}

// A field is bounded, and a broken or hostile file never panics or reads
// past the budget.
func TestReadTagsIsBoundedOnBrokenFiles(t *testing.T) {
	long := strings.Repeat("x", 5000)
	data := append(id3Tag(3, 0, id3Frame(3, "TPE1", latin1Text(long))), make([]byte, 100)...)
	if got := ReadTags(bytes.NewReader(data), int64(len(data)), "a.mp3"); len([]rune(got.Artist)) != tagFieldMax {
		t.Fatalf("artist of %d runes, want %d", len([]rune(got.Artist)), tagFieldMax)
	}
	rng := rand.New(rand.NewSource(1136))
	seeds := [][]byte{
		id3Tag(4, 0x40, id3Frame(4, "TPE1", utf8Text("x"))),
		append([]byte("fLaC"), flacBlock(4, true, vorbisList("ARTIST=y"))...),
		oggPage(1, 0, []byte("\x03vorbis")),
		m4aFile(ilstItem("\xa9ART", "z")),
	}
	for i := 0; i < 2000; i++ {
		seed := seeds[i%len(seeds)]
		b := append([]byte(nil), seed[:rng.Intn(len(seed)+1)]...)
		for j := 0; j < 1+rng.Intn(8) && len(b) > 0; j++ {
			b[rng.Intn(len(b))] = byte(rng.Intn(256))
		}
		for _, ext := range []string{".mp3", ".flac", ".ogg", ".m4a"} {
			cr := &countingReader{r: bytes.NewReader(b)}
			got := ReadTags(cr, int64(len(b)), "f"+ext)
			if cr.bytes > tagReadBudget+tagHeadRead || cr.reads > tagMaxReads+1 {
				t.Fatalf("case %d%s: read %d bytes in %d reads", i, ext, cr.bytes, cr.reads)
			}
			for _, f := range []string{got.Title, got.Artist, got.Album} {
				if strings.ContainsRune(f, 0) || !utf8.ValidString(f) || len([]rune(f)) > tagFieldMax {
					t.Fatalf("case %d%s: field %q", i, ext, f)
				}
			}
		}
	}
}

// The bug of discussion #1136: a song from the stick reached Now Playing
// without its artist, because the stick's listing named none. The listing now
// carries the tag's artist and album, read once per file.
func TestTheListingNamesTheArtistFromTheTag(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, data []byte) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("Singles/01 Tagged Song.mp3", append(id3Tag(3, 0,
		id3Frame(3, "TIT2", latin1Text("Tagged Song")),
		id3Frame(3, "TPE1", latin1Text("Sample Singer")),
		id3Frame(3, "TALB", latin1Text("Sample Album"))), make([]byte, 512)...))
	mk("Singles/02 Untagged.mp3", make([]byte, 512))
	lib, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := serveLibrary(t, lib)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ds, err := dlna.DescribeServer(ctx, srv.URL+DescriptionPath)
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		res, err := dlna.Browse(ctx, ds, "Singles", 0, 200)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Items) != 2 {
			t.Fatalf("items: %+v", res.Items)
		}
		tagged, plain := res.Items[0], res.Items[1]
		// The title stays the file name: the listing's order and every saved
		// key go by it.
		if tagged.Title != "01 Tagged Song" || tagged.Artist != "Sample Singer" || tagged.Album != "Sample Album" {
			t.Fatalf("tagged song: %+v", tagged)
		}
		if plain.Artist != "" || plain.Album != "Singles" {
			t.Fatalf("untagged song keeps the folder as its album: %+v", plain)
		}
	}
	if n := lib.TagReads(); n != 2 {
		t.Fatalf("tags read %d times for two files over two listings, want 2 (cached)", n)
	}

	// A metadata browse of one song names it too, and a changed file is read
	// again.
	mk("Singles/02 Untagged.mp3", append(id3Tag(4, 0, id3Frame(4, "TPE1", utf8Text("Now Tagged"))), make([]byte, 600)...))
	one, err := dlna.ItemMetadata(ctx, ds, "Singles/02 Untagged.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if one.Artist != "Now Tagged" || one.Album != "Singles" {
		t.Fatalf("metadata: %+v", one)
	}

	// The search page names it as well.
	hits, err := dlna.SearchBy(ctx, ds, "tagged", dlna.SearchTitle, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range hits.Items {
		if it.Title == "01 Tagged Song" {
			found = it.Artist == "Sample Singer"
		}
	}
	if !found {
		t.Fatalf("search hit without the artist: %+v", hits.Items)
	}
}

// A page past the time budget is handed out labelled by path, not held up.
func TestAddTagsStopsAtTheDeadline(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.mp3")
	if err := os.WriteFile(p, append(id3Tag(3, 0, id3Frame(3, "TPE1", latin1Text("X"))), 0), 0o644); err != nil {
		t.Fatal(err)
	}
	lib, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lib.Close() }()
	page, _, err := lib.Browse("0", 0, 10)
	if err != nil || len(page) != 1 {
		t.Fatalf("browse: %v %+v", err, page)
	}
	lib.AddTags(page, time.Now().Add(-time.Second))
	if page[0].Artist != "" || lib.TagReads() != 0 {
		t.Fatalf("read past the deadline: %+v", page[0])
	}
	lib.AddTags(page, time.Now().Add(time.Minute))
	if page[0].Artist != "X" {
		t.Fatalf("artist: %+v", page[0])
	}
}
