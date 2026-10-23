package versioncmp

import (
	"reflect"
	"testing"
)

func TestParts(t *testing.T) {
	cases := map[string][]int{
		"v0.7.21":                 {0, 7, 21},
		"0.7.21":                  {0, 7, 21},
		" v1.2.3 ":                {1, 2, 3},
		"v0.7.21-3-gabc123-dirty": {0, 7, 21}, // git-describe suffix ends the segment
		"v1.0.19-preview.1":       {1, 0, 19, 1},
		"1.2.3rc1":                {1, 2, 3},
		"v0.7":                    {0, 7},
		"":                        nil,
		"vdev":                    nil, // no leading digits
	}
	for in, want := range cases {
		if got := Parts(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Parts(%q) = %v, want %v", in, got, want)
		}
	}
}

// Real release tags, in release order. Every neighbour must be strictly older
// than the next, in that direction only, and equal to itself.
func TestLessOrdersTheRealTags(t *testing.T) {
	tags := []string{
		"v0.9.8", "v0.9.9", "v0.9.10", "v0.9.20", "v0.9.33", "v0.9.77", "v0.9.95",
		"v1.0.1", "v1.0.2", "v1.0.9", "v1.0.10", "v1.0.18", "v1.0.19",
	}
	for i, tag := range tags {
		if Less(tag, tag) {
			t.Errorf("Less(%q, %q) = true, a version is not older than itself", tag, tag)
		}
		if i == 0 {
			continue
		}
		prev := tags[i-1]
		if !Less(prev, tag) {
			t.Errorf("Less(%q, %q) = false, want true", prev, tag)
		}
		if Less(tag, prev) {
			t.Errorf("Less(%q, %q) = true, want false", tag, prev)
		}
	}
}

func TestLess(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v0.6.5", "v0.6.6", true},
		{"v0.6.6", "v0.6.5", false},
		{"v0.6.6", "v0.6.6", false}, // equal is not less
		{"0.6.6", "v0.6.6", false},  // v-prefix is irrelevant
		{"1.0.0", "v1.0.1", true},
		{"v0.7", "v0.7.1", true},    // shorter, missing parts treated as 0
		{"v0.7.1", "v0.7", false},   // longer with a non-zero tail
		{"v0.7.0", "v0.7", false},   // explicit trailing zero equals shorter
		{"v0.9.0", "v0.10.0", true}, // numeric, not lexical, compare
		{"v1.0.2", "v1.0.10", true}, // the same, on the patch segment
		{"v1.0.10", "v1.0.2", false},
		{"v1.1.0", "v1.0.99", false},
		{"v0.7.21-3-gabc", "v0.7.21", false}, // git suffix ignored: equal
		{"v1.0.12-64-ge7068773-dirty", "v1.0.13", true},
		{"v1.0.13-rc1", "v1.0.13", false},
		{"1.2.3rc1", "1.2.3", false},
		{"1.2.3rc1", "1.2.4", true},
		// Previews: ordered among themselves by N, above the plain release.
		{"v1.0.19-preview.1", "v1.0.19-preview.2", true},
		{"v1.0.19-preview.2", "v1.0.19-preview.1", false},
		{"v1.0.19", "v1.0.19-preview.1", true},
		{"v1.0.19-preview.1", "v1.0.19", false},
		{"v1.0.19-preview.2", "v1.0.20", true},
		// Unparseable versions read as v0.
		{"", "v1.0.1", true},
		{"v1.0.1", "", false},
		{"", "", false},
		{"vdev", "v0.0.1", true},
	} {
		if got := Less(tc.a, tc.b); got != tc.want {
			t.Errorf("Less(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
