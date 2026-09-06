package main

import "testing"

// Every version string the pipeline actually produces has to come out as four
// numbers. "dev-<sha>" is what the weekly dry run and a no-version dispatch
// pass, and it failed the Windows build on every scheduled run until now.
func TestNormaliseAcceptsEveryPipelineVersion(t *testing.T) {
	cases := map[string]string{
		"v0.9.74":                    "0.9.74.0",
		"0.9.74":                     "0.9.74.0",
		"v0.9.73-10-gbd4877ea-dirty": "0.9.73.0",
		"dev-4737824":                "0.0.0.0",
		"dev":                        "0.0.0.0",
		"":                           "0.0.0.0",
		"1.2.3.4":                    "1.2.3.4",
		"1.2.3.4.5":                  "1.2.3.4",
	}
	for in, want := range cases {
		got := normalise(in)
		if got != want {
			t.Errorf("normalise(%q) = %q, want %q", in, got, want)
		}
		if !fourPartRE.MatchString(got) {
			t.Errorf("normalise(%q) = %q is not four numbers", in, got)
		}
	}
}
