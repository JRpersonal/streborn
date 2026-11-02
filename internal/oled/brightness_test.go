package oled

import "testing"

func TestDimFrame(t *testing.T) {
	frame := func() []byte { return []byte{0, 1, 8, 15} }
	for _, tc := range []struct {
		level int
		want  []byte
	}{
		{-1, []byte{0, 1, 8, 15}},
		{70, []byte{0, 1, 8, 15}},
		{100, []byte{0, 1, 8, 15}},
		{35, []byte{0, 1, 4, 8}},
		{0, []byte{0, 1, 1, 1}},
	} {
		buf := frame()
		dimFrame(buf, tc.level)
		if string(buf) != string(tc.want) {
			t.Errorf("level %d: got %v, want %v", tc.level, buf, tc.want)
		}
	}
}

func TestSetTrackBrightness(t *testing.T) {
	defer SetTrackBrightness(-1)
	SetTrackBrightness(250)
	if TrackBrightness() != 100 {
		t.Fatalf("got %d", TrackBrightness())
	}
	SetTrackBrightness(20)
	if TrackBrightness() != 20 {
		t.Fatalf("got %d", TrackBrightness())
	}
}
