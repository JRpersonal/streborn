package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A healthy ST10 answered its version endpoint in 3.3 seconds on 2026-08-23,
// and the probe budget was 3 seconds, so it read as gone. The budget had
// already been raised once, from 1.2 s to 3 s, for the same reason. Chasing the
// number is the wrong move: the reason it was kept small is that it was also
// the CONNECT budget, and every dead address in a sweep pays that one.
//
// Splitting the two removes the trade-off, so these tests pin both halves.

func TestProbeWaitsOutASlowButPresentSpeaker(t *testing.T) {
	// Answers after two seconds: far past the old 1.2 s budget, and past what a
	// dial is ever allowed to take, but this host is plainly there.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		fmt.Fprint(w, `{"version":"v0.9.55","build":"2026-08-23-2200"}`)
	}))
	defer srv.Close()

	start := time.Now()
	body, err := httpGetSmall(context.Background(), srv.URL, probeAnswerBudget, 1024)
	if err != nil {
		t.Fatalf("a speaker that answers in 2s must not be written off: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("no body")
	}
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Errorf("returned in %v, so it cannot have waited for the answer", elapsed)
	}
}

func TestProbeGivesUpQuicklyOnAHostThatIsNotThere(t *testing.T) {
	// The other half: a sweep must not pay the generous budget on every dead
	// address. 203.0.113.0/24 is TEST-NET-3 and routes nowhere.
	start := time.Now()
	_, err := httpGetSmall(context.Background(), "http://203.0.113.1:8888/api/agent/version", probeAnswerBudget, 1024)
	if err == nil {
		t.Fatal("expected a failure against an unroutable address")
	}
	// Allow generous slack for a slow CI box, but it must be nowhere near the
	// answer budget, or the split has stopped working.
	if elapsed := time.Since(start); elapsed > probeAnswerBudget-2*time.Second {
		t.Errorf("gave up after %v, which is the ANSWER budget; the dial budget is %v", elapsed, probeDialTimeout)
	}
}

func TestProbeStillFailsAHostThatConnectsAndNeverAnswers(t *testing.T) {
	// A socket that accepts and then says nothing must not hang a sweep for
	// ever; the answer budget is the backstop.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			_ = c // accepted, then deliberately silent
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := httpGetSmall(ctx, "http://"+ln.Addr().String()+"/api/agent/version", probeAnswerBudget, 1024); err == nil {
		t.Fatal("a silent socket must eventually fail, not hang")
	}
}

func TestProbeBudgetsAreTheRightWayRound(t *testing.T) {
	if probeDialTimeout >= probeAnswerBudget {
		t.Fatalf("dial %v must be well under the answer budget %v, or the split buys nothing",
			probeDialTimeout, probeAnswerBudget)
	}
	// The measured case that caused this: 3.3 s to answer.
	if probeAnswerBudget <= 3300*time.Millisecond {
		t.Errorf("answer budget %v does not cover the 3.3s reply that started this", probeAnswerBudget)
	}
}
