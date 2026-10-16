package oled

import "testing"

// The swap notice names the held key big and says where the old item went,
// or that the other key is empty now, within the small line's width.
func TestSwappedNotice(t *testing.T) {
	n := Swapped(5, 6, "Best Of Rock.FM")
	if n.Kind != NoticeSwapped || n.Key != 5 || n.Line1 != "SAVED 5" || n.Line2 != "OLD ONE NOW ON 6" {
		t.Fatalf("swap notice = %+v", n)
	}
	n = Swapped(2, 3, "")
	if n.Line2 != "KEY 3 NOW EMPTY" {
		t.Fatalf("move notice line 2 = %q", n.Line2)
	}
	for _, l := range []string{"OLD ONE NOW ON 6", "KEY 3 NOW EMPTY"} {
		if len(l) > line2Max {
			t.Errorf("%q is longer than %d characters", l, line2Max)
		}
	}
}
