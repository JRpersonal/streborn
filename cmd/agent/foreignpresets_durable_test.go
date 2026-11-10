package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/JRpersonal/streborn/internal/webui"
)

// The foreign-preset file is a JSON list at 0644 in a directory the store
// creates, and a persist that fails keeps the previous file, so a restart
// still finds the slots it knew before.
func TestForeignStorePersistContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "foreign-presets.json")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := newForeignPresetStore(path, logger)
	s.NoteBoxList([]webui.BoxPreset{
		{Slot: 5, Source: "STORED_MUSIC", Location: "0$1$13$75", SourceAccount: "aa/0", Name: "Random Access Memories"},
	})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var list []foreignPreset
	if err := json.Unmarshal(before, &list); err != nil || len(list) != 1 || list[0].Slot != 5 {
		t.Fatalf("persisted list = %+v, %v", list, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o644 {
			t.Fatalf("mode = %v, want 0644", fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}

	// Occupy the temp sibling with a directory so the write cannot start.
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	s.NoteBoxList([]webui.BoxPreset{
		{Slot: 6, Source: "STORED_MUSIC", Location: "0$1$13$76", SourceAccount: "aa/0", Name: "Discovery"},
	})
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("failed persist changed the file:\n%s\nwant:\n%s", after, before)
	}
}
