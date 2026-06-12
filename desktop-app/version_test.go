package main

import (
	"reflect"
	"testing"
)

func TestParseVersionParts(t *testing.T) {
	cases := map[string][]int{
		"v0.7.21":                 []int{0, 7, 21},
		"0.7.21":                  []int{0, 7, 21},
		" v1.2.3 ":                []int{1, 2, 3},
		"v0.7.21-3-gabc123-dirty": []int{0, 7, 21}, // git-describe suffix stops at first non-digit
		"v0.7":                    []int{0, 7},
		"":                        nil,
		"vdev":                    nil, // no leading digits
	}
	for in, want := range cases {
		if got := parseVersionParts(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseVersionParts(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestVersionLess(t *testing.T) {
	type c struct {
		a, b string
		want bool
	}
	for _, tc := range []c{
		{"v0.6.5", "v0.6.6", true},
		{"v0.6.6", "v0.6.5", false},
		{"v0.6.6", "v0.6.6", false},          // equal is not less
		{"0.6.6", "v0.6.6", false},           // v-prefix is irrelevant
		{"v0.7", "v0.7.1", true},             // shorter, missing parts treated as 0
		{"v0.7.1", "v0.7", false},            // longer with a non-zero tail
		{"v0.7.0", "v0.7", false},            // explicit trailing zero equals shorter
		{"v0.9.0", "v0.10.0", true},          // numeric, not lexical, compare
		{"v0.7.21-3-gabc", "v0.7.21", false}, // git suffix ignored -> equal
	} {
		if got := versionLess(tc.a, tc.b); got != tc.want {
			t.Errorf("versionLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
