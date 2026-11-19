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

func TestRepeatLanguageOnlyWhenSetupSaysUnset(t *testing.T) {
	var calls []postedCall
	post := func(path, body string) (int, string, error) {
		calls = append(calls, postedCall{path, body})
		return http.StatusOK, "<sysLanguage>3</sysLanguage>", nil
	}
	gets := 0
	get := func(path string) string { gets++; return `<setupStateResponse systemstate="SETUP_LANG_SET" />` }
	var log strings.Builder

	if repeatLanguageIfSetupSaysUnset(`<setupStateResponse state="SETUP_INACTIVE" systemstate="SETUP_LANG_SET" />`, 3, post, get, &log) {
		t.Fatal("a speaker whose language is set must be left alone")
	}
	if len(calls) != 0 || gets != 0 {
		t.Fatalf("nothing may be sent, got %d posts and %d reads", len(calls), gets)
	}

	if !repeatLanguageIfSetupSaysUnset(`<setupStateResponse state="SETUP_INACTIVE" systemstate="SETUP_LANG_NOT_SET" />`, 3, post, get, &log) {
		t.Fatal("an accepted language must report true")
	}
	if len(calls) != 1 || calls[0].path != "/language" || calls[0].body != "<sysLanguage>3</sysLanguage>" {
		t.Fatalf("one language post expected, got %+v", calls)
	}
	if gets != 1 {
		t.Errorf("the state after the language step must be read once for the log, got %d reads", gets)
	}
	if !strings.Contains(log.String(), "language repeat: POST /language") {
		t.Errorf("the answer belongs in the unlock log, got:\n%s", log.String())
	}
}

func TestRepeatLanguageTransportErrorIsLoggedNotFatal(t *testing.T) {
	post := func(path, body string) (int, string, error) { return 0, "", errors.New("connection reset") }
	var log strings.Builder
	if repeatLanguageIfSetupSaysUnset(`systemstate="SETUP_LANG_NOT_SET"`, 3, post, func(string) string { return "" }, &log) {
		t.Fatal("a failed post must report false")
	}
	if !strings.Contains(log.String(), "connection reset") {
		t.Errorf("the failure belongs in the unlock log, got:\n%s", log.String())
	}
}

func TestAccountRetryPlan(t *testing.T) {
	cases := []struct {
		name                 string
		refused, uuidStored  bool
		attempt, maxAttempts int
		want                 string
	}{
		{"stored ends it", true, true, 1, 3, "continue"},
		{"not refused continues", false, false, 1, 3, "continue"},
		{"refused with attempts left", true, false, 1, 3, "retry"},
		{"refused on the last attempt", true, false, 3, 3, "stop"},
	}
	for _, c := range cases {
		if got := accountRetryPlan(c.refused, c.uuidStored, c.attempt, c.maxAttempts); got != c.want {
			t.Errorf("%s: accountRetryPlan(%v,%v,%d,%d) = %q, want %q", c.name, c.refused, c.uuidStored, c.attempt, c.maxAttempts, got, c.want)
		}
	}
}
