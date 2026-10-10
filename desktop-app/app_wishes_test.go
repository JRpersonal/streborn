package main

import (
	"strings"
	"testing"
)

func TestParseWishesKeepsOnlyProjectLinks(t *testing.T) {
	in := `{"updatedAt":"2026-10-10T13:56:34Z",
	"voteUrl":"https://github.com/JRpersonal/streborn/discussions/categories/ideas",
	"suggestUrl":"https://evil.example/new",
	"ideas":[
	 {"number":1091,"title":"Update speakers from the phone","url":"https://github.com/JRpersonal/streborn/discussions/1091","votes":10},
	 {"number":1,"title":"Elsewhere","url":"https://evil.example/1","votes":99},
	 {"number":2,"title":"   ","url":"https://github.com/JRpersonal/streborn/discussions/2","votes":3},
	 {"number":1110,"title":"12 presets via double-press","url":"https://github.com/JRpersonal/streborn/discussions/1110","votes":-1}
	]}`
	got, err := parseWishes(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.UpdatedAt == "" {
		t.Fatalf("want OK with a date, got %+v", got)
	}
	if len(got.Wishes) != 2 || got.Wishes[0].Number != 1091 || got.Wishes[1].Number != 1110 {
		t.Fatalf("want the two project wishes in order, got %+v", got.Wishes)
	}
	if got.Wishes[1].Votes != 0 {
		t.Fatalf("negative votes must read as 0, got %d", got.Wishes[1].Votes)
	}
	if got.SuggestURL != wishesSuggestURL {
		t.Fatalf("a foreign suggest link must fall back, got %q", got.SuggestURL)
	}
	if !strings.HasSuffix(got.VoteURL, "/categories/ideas") {
		t.Fatalf("a project vote link is taken over, got %q", got.VoteURL)
	}
}

func TestParseWishesBrokenJSONKeepsTheButtons(t *testing.T) {
	got, err := parseWishes(strings.NewReader("<html>"))
	if err == nil {
		t.Fatal("want an error for a non-JSON body")
	}
	if got.OK || got.VoteURL == "" || got.SuggestURL == "" || got.Wishes == nil {
		t.Fatalf("want the fallback links and an empty list, got %+v", got)
	}
}
