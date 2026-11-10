package boxsnapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// occupyTemp makes the next durable write to path fail: the writer's temp
// sibling ("<path>.tmp") is occupied by a directory, so it cannot be opened as
// a file. It stands in for a NAND that refuses the write.
func occupyTemp(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
}

// wantWritten asserts a state file is there at 0644 (where the host has POSIX
// permission bits) with no temp file left beside it, and returns its bytes.
func wantWritten(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o644 {
			t.Fatalf("%s mode = %v, want 0644", filepath.Base(path), fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
	return b
}

// The reflect file is what re-links a cloud source after STR took the box
// over. A merge writes it into a directory it creates, and a merge that cannot
// write keeps the previous list.
func TestMergeReflectContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "reflect-sources.json")
	if err := MergeReflect(path, []ReflectSource{{Source: "DEEZER", Account: "123", Name: "Deezer"}}); err != nil {
		t.Fatal(err)
	}
	before := wantWritten(t, path)
	var list []ReflectSource
	if err := json.Unmarshal(before, &list); err != nil || len(list) != 1 || list[0].Source != "DEEZER" {
		t.Fatalf("persisted list = %+v, %v", list, err)
	}

	occupyTemp(t, path)
	if err := MergeReflect(path, []ReflectSource{{Source: "PANDORA", Account: "a", Name: "Pandora"}}); err == nil {
		t.Fatal("blocked merge reported success")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed merge changed the file: %q, %v", after, err)
	}
}

// A removal that cannot write reports the failure and keeps every entry.
func TestRemoveReflectContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reflect-sources.json")
	if err := MergeReflect(path, []ReflectSource{
		{Source: "PANDORA", Account: "a", Name: "Pandora"},
		{Source: "DEEZER", Account: "123", Name: "Deezer"},
		{Source: "AMAZON", Account: "b", Name: "Amazon Music"},
	}); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveReflect(path, "pandora"); err != nil || !removed {
		t.Fatalf("RemoveReflect = %v, %v", removed, err)
	}
	before := wantWritten(t, path)
	if got := LoadReflect(path); len(got) != 2 || got[0].Source != "DEEZER" || got[1].Source != "AMAZON" {
		t.Fatalf("left %+v, want DEEZER and AMAZON", got)
	}

	occupyTemp(t, path)
	if removed, err := RemoveReflect(path, "DEEZER"); err == nil || removed {
		t.Fatalf("blocked removal = %v, %v, want a failure", removed, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed removal changed the file: %q, %v", after, err)
	}
}

// The snapshot is the one record of what the box had before STR took over; it
// is captured once, so a failed rewrite must never cost the copy already there.
func TestWriteSnapshotContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "box-snapshot.json")
	snap := Snapshot{CapturedAt: 1700000000, DeviceID: "device-id-here", LostServices: []string{"DEEZER"}}
	if err := write(path, snap); err != nil {
		t.Fatal(err)
	}
	before := wantWritten(t, path)
	got, err := Load(path)
	if err != nil || got.CapturedAt != 1700000000 || got.DeviceID != "device-id-here" ||
		len(got.LostServices) != 1 || got.LostServices[0] != "DEEZER" {
		t.Fatalf("loaded snapshot = %+v, %v", got, err)
	}

	occupyTemp(t, path)
	if err := write(path, Snapshot{CapturedAt: 1800000000, DeviceID: "other"}); err == nil {
		t.Fatal("blocked write reported success")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed write changed the snapshot: %q, %v", after, err)
	}
}

// The label is one JSON line. A change that cannot be written is reported, and
// the file keeps the label it had, so a restart still shows the old name
// instead of none.
func TestLabelNoteContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "box-label.json")
	s := OpenLabel(path)
	if changed, err := s.Note("Kitchen", "SoundTouch 10"); err != nil || !changed {
		t.Fatalf("first Note = (%v, %v), want a write", changed, err)
	}
	before := wantWritten(t, path)
	var l Label
	if err := json.Unmarshal(before, &l); err != nil || l != (Label{Name: "Kitchen", Model: "SoundTouch 10"}) {
		t.Fatalf("persisted label = %+v, %v", l, err)
	}
	if before[len(before)-1] != '\n' {
		t.Fatalf("label file does not end in a newline: %q", before)
	}

	occupyTemp(t, path)
	if changed, err := s.Note("Bedroom", ""); err == nil || !changed {
		t.Fatalf("blocked Note = (%v, %v), want a reported failure", changed, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed write changed the label file: %q, %v", after, err)
	}
	if got := OpenLabel(path).Get(); got != (Label{Name: "Kitchen", Model: "SoundTouch 10"}) {
		t.Fatalf("after restart label = %+v, want the previous one", got)
	}
}
