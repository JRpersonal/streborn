package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

type postedCall struct{ path, body string }

func TestPairInsideSetupBracketWalksTheStatesAndLeaves(t *testing.T) {
	var calls []postedCall
	post := func(path, body string) (int, string, error) {
		calls = append(calls, postedCall{path, body})
		return http.StatusOK, "<ok/>", nil
	}
	var log strings.Builder
	if !pairInsideSetupBracket(post, &log) {
		t.Fatal("an accepted account must report true")
	}
	want := []string{"SETUP_START", "SETUP_IDENTIFY_DEVICE_ENTER", "SETUP_ENTER", "SETUP_IDENTIFY_DEVICE_LEAVE", "PairDeviceWithAccount", "SETUP_LEAVE"}
	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d: %+v", len(calls), len(want), calls)
	}
	for i, w := range want {
		if !strings.Contains(calls[i].body, w) {
			t.Errorf("call %d = %q, want it to carry %s", i, calls[i].body, w)
		}
	}
	if calls[4].path != "/setMargeAccount" || calls[5].path != "/setup" {
		t.Errorf("account then leave expected, got %s then %s", calls[4].path, calls[5].path)
	}
	if strings.Count(log.String(), "HTTP 200") != len(want) {
		t.Errorf("every answer belongs in the unlock log, got:\n%s", log.String())
	}
}

func TestPairInsideSetupBracketRefusedStillLeavesSetup(t *testing.T) {
	var calls []postedCall
	post := func(path, body string) (int, string, error) {
		calls = append(calls, postedCall{path, body})
		if path == "/setMargeAccount" {
			return http.StatusInternalServerError, `<error value="1038" name="SET_MARGE_ACCOUNT_ERROR">1038</error>`, nil
		}
		return http.StatusOK, "<ok/>", nil
	}
	var log strings.Builder
	if pairInsideSetupBracket(post, &log) {
		t.Fatal("a refused account must report false")
	}
	if last := calls[len(calls)-1]; !strings.Contains(last.body, "SETUP_LEAVE") {
		t.Errorf("the speaker must not stay in setup, last call was %+v", last)
	}
	if !strings.Contains(log.String(), "1038") {
		t.Errorf("the refusal belongs in the unlock log, got:\n%s", log.String())
	}
}

func TestPairInsideSetupBracketTransportErrorSkipsTheAccount(t *testing.T) {
	var calls []postedCall
	post := func(path, body string) (int, string, error) {
		calls = append(calls, postedCall{path, body})
		if strings.Contains(body, "SETUP_ENTER") {
			return 0, "", errors.New("connection reset")
		}
		return http.StatusOK, "<ok/>", nil
	}
	var log strings.Builder
	if pairInsideSetupBracket(post, &log) {
		t.Fatal("a broken walk must report false")
	}
	for _, c := range calls {
		if c.path == "/setMargeAccount" {
			t.Error("the account must not be sent after a broken walk")
		}
	}
	if last := calls[len(calls)-1]; !strings.Contains(last.body, "SETUP_LEAVE") {
		t.Errorf("setup was started, so it must be left; last call was %+v", last)
	}
}

func TestPairInsideSetupBracketNothingStartedNothingLeft(t *testing.T) {
	n := 0
	post := func(path, body string) (int, string, error) {
		n++
		return 0, "", errors.New("no route")
	}
	var log strings.Builder
	if pairInsideSetupBracket(post, &log) || n != 1 {
		t.Fatalf("one failed SETUP_START and nothing else expected, got %d calls", n)
	}
}
