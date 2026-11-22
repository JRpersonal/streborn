package usbmedia

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/dlna"
)

// liveFirmwareMountOptions is what /proc/mounts lists for a stick the speaker
// firmware mounted itself (ST10 and Portable, FW 27.0.6).
const liveFirmwareMountOptions = "rw,relatime,fmask=0022,dmask=0022,codepage=437,iocharset=iso8859-1,shortname=mixed,errors=remount-ro"

func TestNameCharsetFromMountOptions(t *testing.T) {
	cases := map[string]string{
		liveFirmwareMountOptions:                       CharsetLatin1,
		"rw,relatime,iocharset=utf8,errors=remount-ro": CharsetUTF8,
		"ro,nosuid,nodev,noexec,utf8":                  CharsetUTF8,
		"rw,iocharset=iso8859-1,utf8":                  CharsetUTF8, // the utf8 option wins
		"rw,utf8=1,iocharset=iso8859-1":                CharsetUTF8,
		"rw,utf8=0,iocharset=iso8859-1":                CharsetLatin1,
		"rw,iocharset=ISO8859-15":                      CharsetLatin9,
		"rw,iocharset=cp1251":                          CharsetUnknown,
		"ro,nosuid,nodev,noexec":                       CharsetUnknown,
		"":                                             CharsetUnknown,
	}
	for opts, want := range cases {
		if got := NameCharset(opts); got != want {
			t.Errorf("%q: got %q, want %q", opts, got, want)
		}
	}
}

func TestNameCodecMapsBothWays(t *testing.T) {
	latin1 := nameCodec{charset: CharsetLatin1}
	if got := latin1.decode("01 \xC4rger"); got != "01 Ärger" {
		t.Fatalf("latin1 decode: %q", got)
	}
	if got := latin1.encode("01 Ärger"); len(got) != 1 || got[0] != "01 \xC4rger" {
		t.Fatalf("latin1 encode: %q", got)
	}
	if got := latin1.decode("plain.mp3"); got != "plain.mp3" {
		t.Fatalf("ASCII stays as it is: %q", got)
	}
	// A character Latin-1 cannot hold names no file on that mount.
	if got := latin1.encode("Łódź"); len(got) != 0 {
		t.Fatalf("latin1 encode of a non-Latin-1 name: %q", got)
	}

	latin9 := nameCodec{charset: CharsetLatin9}
	if got := latin9.decode("5 \xA4"); got != "5 €" {
		t.Fatalf("latin9 decode: %q", got)
	}
	if got := latin9.encode("5 €"); len(got) != 1 || got[0] != "5 \xA4" {
		t.Fatalf("latin9 encode: %q", got)
	}
	if got := latin9.encode("¤"); len(got) != 0 {
		t.Fatalf("the currency sign is no ISO 8859-15 character: %q", got)
	}

	utf := nameCodec{charset: CharsetUTF8}
	if got := utf.decode("01 Ärger"); got != "01 Ärger" {
		t.Fatalf("utf8 decode: %q", got)
	}
	if got := utf.encode("01 Ärger"); len(got) != 1 || got[0] != "01 Ärger" {
		t.Fatalf("utf8 encode: %q", got)
	}

	// Unknown charset: valid UTF-8 is kept, anything else read as Latin-1, and
	// a text maps back to both forms, UTF-8 first.
	unknown := nameCodec{}
	if unknown.decode("01 Ärger") != "01 Ärger" || unknown.decode("01 \xC4rger") != "01 Ärger" {
		t.Fatal("unknown charset decode")
	}
	if got := unknown.encode("01 Ärger"); len(got) != 2 || got[0] != "01 Ärger" || got[1] != "01 \xC4rger" {
		t.Fatalf("unknown charset encode: %q", got)
	}
}

// latin1Stick writes a stick whose names are Latin-1 bytes, the way the
// kernel hands them over on an iocharset=iso8859-1 mount. A filesystem that
// cannot hold such names (NTFS, APFS) skips the test; Linux CI runs it.
func latin1Stick(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	album := filepath.Join(root, "B\xFCcher")
	if err := os.Mkdir(album, 0o755); err != nil {
		t.Skipf("this filesystem cannot hold a Latin-1 name: %v", err)
	}
	for _, name := range []string{"01 \xC4rger.mp3", "02 plain.flac", "cover.jpg"} {
		if err := os.WriteFile(filepath.Join(album, name), []byte("x"), 0o644); err != nil {
			t.Skipf("this filesystem cannot hold a Latin-1 name: %v", err)
		}
	}
	des, err := os.ReadDir(root)
	if err != nil || len(des) != 1 || des[0].Name() != "B\xFCcher" {
		t.Skip("this filesystem does not keep a Latin-1 name byte for byte")
	}
	return root
}

func TestLatin1MountedStickShowsUmlautsAndStillServesTheFile(t *testing.T) {
	root := latin1Stick(t)
	lib, err := OpenMounted(root, liveFirmwareMountOptions)
	if err != nil {
		t.Fatal(err)
	}
	if lib.Charset() != CharsetLatin1 {
		t.Fatalf("charset %q", lib.Charset())
	}
	srv, _ := serveLibrary(t, lib)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ds, err := dlna.DescribeServer(ctx, srv.URL+DescriptionPath)
	if err != nil {
		t.Fatal(err)
	}

	top, err := dlna.Browse(ctx, ds, "0", 0, 50)
	if err != nil || len(top.Containers) != 1 {
		t.Fatalf("root: %+v %v", top, err)
	}
	if c := top.Containers[0]; c.ID != "Bücher" || c.Title != "Bücher" {
		t.Fatalf("folder: %+v", c)
	}
	res, err := dlna.Browse(ctx, ds, "Bücher", 0, 50)
	if err != nil || len(res.Items) != 2 {
		t.Fatalf("folder browse: %+v %v", res, err)
	}
	it := res.Items[0]
	if it.Title != "01 Ärger" || it.ID != "Bücher/01 Ärger.mp3" || it.Album != "Bücher" || strings.ContainsRune(it.Title, '�') {
		t.Fatalf("item: %+v", it)
	}
	// The URL carries the bytes the filesystem knows the file by.
	if want := srv.URL + FilePrefix + "B%FCcher/01%20%C4rger.mp3"; it.StreamURL != want {
		t.Fatalf("res %q, want %q", it.StreamURL, want)
	}
	if want := srv.URL + ArtPrefix + "B%FCcher/cover.jpg"; it.AlbumArtURL != want {
		t.Fatalf("cover %q, want %q", it.AlbumArtURL, want)
	}
	for _, u := range []string{
		it.StreamURL,
		// The UTF-8 form of the same path is found too.
		srv.URL + FilePrefix + "B%C3%BCcher/01%20%C3%84rger.mp3",
		it.AlbumArtURL,
	} {
		resp, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d", u, resp.StatusCode)
		}
	}

	// BrowseMetadata of the UTF-8 id answers the same object.
	e, err := lib.Lookup("Bücher/01 Ärger.mp3")
	if err != nil || e.Title != "01 Ärger" || e.Path != "B\xFCcher/01 \xC4rger.mp3" || e.Cover != "B\xFCcher/cover.jpg" {
		t.Fatalf("lookup: %+v %v", e, err)
	}
	// A name Latin-1 cannot hold is no object on this stick.
	if _, err := lib.Lookup("Łódź"); err == nil {
		t.Fatal("a non-Latin-1 id must not resolve")
	}
}

func TestUTF8MountedStickKeepsWorking(t *testing.T) {
	lib, err := OpenMounted(musicStick(t), "rw,relatime,iocharset=utf8,errors=remount-ro")
	if err != nil {
		t.Fatal(err)
	}
	if lib.Charset() != CharsetUTF8 {
		t.Fatalf("charset %q", lib.Charset())
	}
	srv, _ := serveLibrary(t, lib)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ds, err := dlna.DescribeServer(ctx, srv.URL+DescriptionPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := dlna.Browse(ctx, ds, "Album 1", 0, 50)
	if err != nil || len(res.Items) == 0 {
		t.Fatalf("browse: %+v %v", res, err)
	}
	it := res.Items[0]
	if it.Title != "01 Ärger" || it.ID != "Album 1/01 Ärger.mp3" ||
		it.StreamURL != srv.URL+FilePrefix+"Album%201/01%20%C3%84rger.mp3" {
		t.Fatalf("item: %+v", it)
	}
	resp, err := http.Get(it.StreamURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
}
