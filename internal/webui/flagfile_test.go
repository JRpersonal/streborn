package webui

import (
	"os"
	"path/filepath"
	"testing"
)

// readBoolFlag backs every on/off switch stored as a NAND flag file. The
// default decides for a missing file and for a damaged one; an explicit value
// wins in either direction, however it was spelled.
func TestReadBoolFlag(t *testing.T) {
	dir := t.TempDir()
	for _, def := range []bool{true, false} {
		if got := readBoolFlag(filepath.Join(dir, "missing"), def); got != def {
			t.Errorf("missing file with default %v read as %v", def, got)
		}
	}
	cases := []struct {
		content string
		want    bool
		fixed   bool // the value decides, whatever the default
	}{
		{"1\n", true, true},
		{"true", true, true},
		{" ON ", true, true},
		{"Yes\n", true, true},
		{"0\n", false, true},
		{"false", false, true},
		{"off\n", false, true},
		{"No", false, true},
		{"", false, false},
		{"   \n", false, false},
		{"maybe", false, false},
		{"2", false, false},
	}
	path := filepath.Join(dir, "flag")
	for _, c := range cases {
		if err := os.WriteFile(path, []byte(c.content), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, def := range []bool{true, false} {
			want := def
			if c.fixed {
				want = c.want
			}
			if got := readBoolFlag(path, def); got != want {
				t.Errorf("%q with default %v read as %v, want %v", c.content, def, got, want)
			}
		}
	}
}
