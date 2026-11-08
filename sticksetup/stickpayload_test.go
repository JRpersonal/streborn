package sticksetup

// The payload cleanup runs against temp folders only, never a real drive.

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// writeFullPayload prepares dir the way the install wizard does: every
// template, both binaries, the markers and all config files.
func writeFullPayload(t *testing.T, dir string) {
	t.Helper()
	if _, err := WriteStickFiles(dir, []byte("agent"), []byte("engine"), "1.5.3+test"); err != nil {
		t.Fatal(err)
	}
	if err := WriteWLANConfig(dir, WLANConfig{SSID: "ssid-here", Password: "password-here"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteRegionConfig(dir, RegionConfig{Country: "DE"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteNameConfig(dir, NameConfig{Name: "Kitchen"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteLangConfig(dir, "de", "DE", 2); err != nil {
		t.Fatal(err)
	}
}

func rootNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// PayloadFileNames must name every file the wizard writes, or the firmware
// ladder would leave a new one beside Update.stu.
func TestPayloadFileNamesCoverEveryWriter(t *testing.T) {
	dir := t.TempDir()
	writeFullPayload(t, dir)
	known := map[string]bool{}
	for _, n := range PayloadFileNames() {
		known[n] = true
	}
	written := rootNames(t, dir)
	for _, n := range written {
		if !known[n] {
			t.Errorf("the wizard writes %q, but PayloadFileNames does not list it", n)
		}
	}
	if len(written) < 15 {
		t.Errorf("expected the full payload on the stick, got %d files: %v", len(written), written)
	}
}

func TestRemoveSTRPayloadKeepsUserFiles(t *testing.T) {
	dir := t.TempDir()
	writeFullPayload(t, dir)
	// A half-written payload file, an older Update.stu, the user's own files
	// and a folder that happens to carry a payload name.
	for _, n := range []string{"version.txt.new", FirmwareFileName, "holiday.jpg", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "music"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "run.sh.d"), 0o755); err != nil {
		t.Fatal(err)
	}

	if !IsSTRPayloadStick(dir) {
		t.Fatal("a wizard-prepared stick must count as STR's")
	}
	if STRPayloadBytes(dir) <= 0 {
		t.Error("the payload takes space")
	}
	if !HasOtherFiles(dir) {
		t.Error("holiday.jpg is another file")
	}
	removed, err := RemoveSTRPayload(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) < 15 {
		t.Errorf("removed only %v", removed)
	}
	want := []string{FirmwareFileName, "holiday.jpg", "music", "notes.txt", "run.sh.d"}
	if got := rootNames(t, dir); !reflect.DeepEqual(got, want) {
		t.Errorf("after the cleanup the stick holds %v, want %v", got, want)
	}
	if got, want := OtherFiles(dir), []string{"holiday.jpg", "music/", "notes.txt", "run.sh.d/"}; !reflect.DeepEqual(got, want) {
		t.Errorf("OtherFiles = %v, want %v", got, want)
	}
	if STRPayloadBytes(dir) != 0 {
		t.Error("nothing of STR's is left")
	}
}

// Generic names such as install.sh or version.txt are only STR's on a stick
// that also carries a file nobody else writes.
func TestRemoveSTRPayloadLeavesForeignSticks(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"install.sh", "run.sh", "version.txt", "presets.json", "wlan.conf"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if IsSTRPayloadStick(dir) {
		t.Fatal("no STR marker, not STR's stick")
	}
	removed, err := RemoveSTRPayload(dir)
	if err != nil || len(removed) != 0 {
		t.Fatalf("removed %v (err %v) from a stick STR did not prepare", removed, err)
	}
	if len(rootNames(t, dir)) != 5 {
		t.Error("every file must still be there")
	}
	if !HasOtherFiles(dir) || STRPayloadBytes(dir) != 0 {
		t.Error("on a foreign stick the files are other files and not reclaimable")
	}
}

// A stick that holds only Update.stu and STR's payload shows no hint: the
// payload goes before the write.
func TestHasOtherFilesIgnoresSTRPayload(t *testing.T) {
	dir := t.TempDir()
	writeFullPayload(t, dir)
	if err := os.WriteFile(filepath.Join(dir, FirmwareFileName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if HasOtherFiles(dir) {
		t.Errorf("only STR's payload beside Update.stu, no hint: %v", OtherFiles(dir))
	}
}

// The FAT32 stick may hand the names back in another case.
func TestRemoveSTRPayloadIgnoresCase(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"STR-SHIM.SO", "Run.sh"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := RemoveSTRPayload(dir)
	if err != nil || len(removed) != 2 {
		t.Fatalf("removed %v, err %v", removed, err)
	}
}
