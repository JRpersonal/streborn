// Regression tests for the post-OTA resume suppression: after the agent-update
// handler reboots the box, the marker file it left behind must arm a short
// stand-down window for the automatic resume paths (ResumeLastPlay /
// RecoverAfterReconnect), and the marker must be consumed exactly once so only
// the boot right after the update is affected.

package webui

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResumeSuppressedPostOTA(t *testing.T) {
	tests := []struct {
		name          string
		suppressUntil time.Time
		want          bool
	}{
		{
			name:          "zero value (no OTA marker consumed) does not suppress",
			suppressUntil: time.Time{},
			want:          false,
		},
		{
			name:          "inside the window suppresses",
			suppressUntil: time.Now().Add(postOTAResumeSuppressWindow),
			want:          true,
		},
		{
			name:          "window already elapsed does not suppress",
			suppressUntil: time.Now().Add(-time.Second),
			want:          false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			s.postOTAResumeSuppressUntil = tt.suppressUntil
			if got := s.resumeSuppressedPostOTA(); got != tt.want {
				t.Fatalf("resumeSuppressedPostOTA() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConsumeOTARebootMarker(t *testing.T) {
	orig := otaRebootMarkerPath
	t.Cleanup(func() { otaRebootMarkerPath = orig })

	t.Run("marker present arms the window and removes the marker", func(t *testing.T) {
		otaRebootMarkerPath = filepath.Join(t.TempDir(), "ota-reboot")
		if err := os.WriteFile(otaRebootMarkerPath, []byte("1"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		before := time.Now()
		s.consumeOTARebootMarker()
		after := time.Now()

		if s.postOTAResumeSuppressUntil.IsZero() {
			t.Fatal("consumeOTARebootMarker must arm postOTAResumeSuppressUntil when the marker exists")
		}
		// The window is measured from agent start (now), never from any stored
		// wall-clock time: the boxes have no battery RTC.
		lo := before.Add(postOTAResumeSuppressWindow)
		hi := after.Add(postOTAResumeSuppressWindow)
		if s.postOTAResumeSuppressUntil.Before(lo) || s.postOTAResumeSuppressUntil.After(hi) {
			t.Fatalf("suppress-until = %v, want within [%v, %v]", s.postOTAResumeSuppressUntil, lo, hi)
		}
		if !s.resumeSuppressedPostOTA() {
			t.Fatal("resume must be suppressed right after the marker was consumed")
		}
		if _, err := os.Stat(otaRebootMarkerPath); !os.IsNotExist(err) {
			t.Fatalf("marker must be deleted so it only affects one boot, stat err = %v", err)
		}

		// A second consume (e.g. a later agent restart within the same boot)
		// finds no marker and must not re-arm.
		s2 := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		s2.consumeOTARebootMarker()
		if !s2.postOTAResumeSuppressUntil.IsZero() {
			t.Fatal("a consumed marker must not arm the window again")
		}
	})

	t.Run("no marker leaves suppression unarmed", func(t *testing.T) {
		otaRebootMarkerPath = filepath.Join(t.TempDir(), "ota-reboot")
		s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		s.consumeOTARebootMarker()
		if !s.postOTAResumeSuppressUntil.IsZero() {
			t.Fatal("consumeOTARebootMarker must not arm the window when no marker exists")
		}
		if s.resumeSuppressedPostOTA() {
			t.Fatal("resume must not be suppressed after a normal (non-OTA) boot")
		}
	})
}
