//go:build linux

package syscheck

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoLibrespotState(t *testing.T) {
	cases := []struct {
		name string
		// path builds the probe path per case so t.TempDir stays scoped to it.
		path func(t *testing.T) string
		// want is the exact expected word; wantPrefix matches instead when the
		// tail is platform text (the stat error message).
		want       string
		wantPrefix string
	}{
		{
			name: "no path configured",
			path: func(t *testing.T) string { return "" },
			want: "unconfigured",
		},
		{
			name: "binary never delivered (the #45 cause)",
			path: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "go-librespot")
			},
			want: "MISSING",
		},
		{
			name: "path is a directory, not a binary",
			path: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "go-librespot")
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			want: "MISSING(is-dir)",
		},
		{
			name: "binary in place reports its size",
			path: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "go-librespot")
				if err := os.WriteFile(p, make([]byte, 2048), 0o755); err != nil {
					t.Fatal(err)
				}
				return p
			},
			want: "present:2048B",
		},
		{
			name: "zero-byte stub still counts as present",
			path: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "go-librespot")
				if err := os.WriteFile(p, nil, 0o755); err != nil {
					t.Fatal(err)
				}
				return p
			},
			want: "present:0B",
		},
		{
			name: "stat failure that is not not-exist",
			path: func(t *testing.T) string {
				// A NUL byte makes stat fail with EINVAL, which is neither
				// not-exist nor a directory, so the unknown branch must report
				// it instead of masking it as MISSING.
				return "go\x00librespot"
			},
			wantPrefix: "unknown(",
		},
	}
	for _, c := range cases {
		got := goLibrespotState(c.path(t))
		if c.wantPrefix != "" {
			if !strings.HasPrefix(got, c.wantPrefix) {
				t.Errorf("%s: goLibrespotState()=%q, want prefix %q", c.name, got, c.wantPrefix)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s: goLibrespotState()=%q, want %q", c.name, got, c.want)
		}
	}
}

func TestItoa(t *testing.T) {
	cases := []struct {
		name string
		n    int64
		want string
	}{
		{"zero", 0, "0"},
		{"single digit", 7, "7"},
		{"round ten", 10, "10"},
		{"typical binary size", 24696832, "24696832"},
		{"negative", -1, "-1"},
		{"negative multi-digit", -4096, "-4096"},
		{"max int64 fills the buffer", math.MaxInt64, "9223372036854775807"},
		{"min int64 plus one", math.MinInt64 + 1, "-9223372036854775807"},
	}
	for _, c := range cases {
		if got := itoa(c.n); got != c.want {
			t.Errorf("%s: itoa(%d)=%q, want %q", c.name, c.n, got, c.want)
		}
	}
}
