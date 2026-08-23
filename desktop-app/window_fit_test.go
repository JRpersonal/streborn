package main

import "testing"

// #681: the app "flickers and disappears" on a multi-monitor Windows PC. The
// window was not crashing, it was being repositioned off the virtual desktop.
// fitWindowToScreen read the position with WindowGetPosition (an absolute
// desktop coordinate) and wrote it back with WindowSetPosition (an offset into
// the CURRENT monitor's work area), so the monitor origin was applied twice.
//
// The repositioning only happens now when the window did not fit, which is the
// case the function exists for. This test pins the "it fits, leave it alone"
// branch, because that is what keeps the window on the screen the platform
// chose.

const testChrome = 80

func TestFitWindowSizeLeavesAFittingWindowAlone(t *testing.T) {
	// A window comfortably inside a 1920x1080 screen must come back unchanged,
	// so fitWindowToScreen takes its early return and never repositions.
	for _, tc := range []struct{ w, h int }{
		{1200, 800},
		{1920, 1000}, // exactly the screen width, still inside the height budget
		{640, 480},
	} {
		gotW, gotH := fitWindowSize(tc.w, tc.h, 1920, 1080, testChrome)
		if gotW != tc.w || gotH != tc.h {
			t.Errorf("fitWindowSize(%d,%d, 1920,1080) = %d,%d, want it untouched",
				tc.w, tc.h, gotW, gotH)
		}
	}
}

func TestFitWindowSizeShrinksToTheScreen(t *testing.T) {
	// The case the function is for: a 1080p screen at 150% scaling reports 720
	// logical pixels of height, so a 740-tall window does not fit.
	gotW, gotH := fitWindowSize(1000, 740, 1280, 720, testChrome)
	if gotH != 720-testChrome {
		t.Errorf("height = %d, want %d", gotH, 720-testChrome)
	}
	if gotW != 1000 {
		t.Errorf("width = %d, want it left at 1000 (it fits)", gotW)
	}

	// Too wide as well: both are clamped.
	gotW, gotH = fitWindowSize(1600, 900, 1280, 720, testChrome)
	if gotW != 1280 || gotH != 720-testChrome {
		t.Errorf("got %d,%d, want 1280,%d", gotW, gotH, 720-testChrome)
	}
}

func TestFitWindowSizeNeverGrowsAWindow(t *testing.T) {
	// A small window on a big screen must stay small. Growing it would move a
	// window the user had deliberately sized down.
	gotW, gotH := fitWindowSize(800, 600, 3840, 2160, testChrome)
	if gotW != 800 || gotH != 600 {
		t.Errorf("got %d,%d, want 800,600", gotW, gotH)
	}
}
