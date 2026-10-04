package webui

import (
	"strings"
	"testing"
	"time"
)

func TestAHangingSectionDoesNotHoldUpTheDebugState(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	fns := map[string]func() any{
		"fast":    func() any { return "ok" },
		"hanging": func() any { <-block; return "late" },
		"panics":  func() any { panic("boom") },
	}
	start := time.Now()
	out := collectDebugSections(fns, 150*time.Millisecond)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("collect took %s; the hanging section held it up", d)
	}
	if out["fast"] != "ok" {
		t.Errorf("fast = %v, want ok", out["fast"])
	}
	if s, _ := out["hanging"].(string); !strings.Contains(s, "did not answer") {
		t.Errorf("hanging = %v, want a timeout note", out["hanging"])
	}
	if s, _ := out["panics"].(string); !strings.Contains(s, "panicked") {
		t.Errorf("panics = %v, want a panic note", out["panics"])
	}
}

func TestSectionsRunInParallel(t *testing.T) {
	slow := func() any { time.Sleep(100 * time.Millisecond); return "done" }
	fns := map[string]func() any{"a": slow, "b": slow, "c": slow, "d": slow}
	start := time.Now()
	out := collectDebugSections(fns, 2*time.Second)
	if d := time.Since(start); d > 350*time.Millisecond {
		t.Fatalf("four 100 ms sections took %s; they ran one after another", d)
	}
	for k, v := range out {
		if v != "done" {
			t.Errorf("%s = %v", k, v)
		}
	}
}
