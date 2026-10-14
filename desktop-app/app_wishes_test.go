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

func TestParseWishesKeepsTranslatedTitles(t *testing.T) {
	in := `{"ideas":[{"number":1098,"title":"Clock on the display while streaming","url":"https://github.com/JRpersonal/streborn/discussions/1098","votes":8,"titles":{"de":"Uhr auf dem Display beim Streamen"}}]}`
	got, err := parseWishes(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Wishes) != 1 || got.Wishes[0].Titles["de"] != "Uhr auf dem Display beim Streamen" {
		t.Fatalf("want the German title carried through, got %+v", got.Wishes)
	}
}

func TestParseWishesReadsRecentlyDone(t *testing.T) {
	in := `{"ideas":[{"number":1091,"title":"Open wish","url":"https://github.com/JRpersonal/streborn/discussions/1091","votes":10,"doneAt":"2026-10-01T00:00:00Z"}],
	"done":[
	 {"number":1182,"title":" iOS app for the phone remote ","url":"https://github.com/JRpersonal/streborn/discussions/1182","votes":5,"doneAt":"2026-10-10T21:37:53.000Z","titles":{"de":"iOS App"}},
	 {"number":1,"title":"Elsewhere","url":"https://evil.example/1","votes":9,"doneAt":"2026-10-09T00:00:00Z"},
	 {"number":2,"title":"No date","url":"https://github.com/JRpersonal/streborn/discussions/2","votes":1},
	 {"number":3,"title":"Bad date","url":"https://github.com/JRpersonal/streborn/discussions/3","votes":1,"doneAt":"last week"},
	 {"number":1107,"title":"Move a station to another key","url":"https://github.com/JRpersonal/streborn/discussions/1107","votes":-2,"doneAt":"2026-10-03T23:13:05Z"}
	]}`
	got, err := parseWishes(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Wishes) != 1 || got.Wishes[0].DoneAt != "" {
		t.Fatalf("an open wish never carries a done date, got %+v", got.Wishes)
	}
	if len(got.Done) != 2 || got.Done[0].Number != 1182 || got.Done[1].Number != 1107 {
		t.Fatalf("want the two dated project wishes in the site's order, got %+v", got.Done)
	}
	d := got.Done[0]
	if d.Title != "iOS app for the phone remote" || d.DoneAt != "2026-10-10T21:37:53Z" || d.Titles["de"] != "iOS App" {
		t.Fatalf("want a trimmed title, a normalized date and the translations, got %+v", d)
	}
	if got.Done[1].Votes != 0 {
		t.Fatalf("negative votes must read as 0, got %d", got.Done[1].Votes)
	}
}

func TestParseWishesWithoutDoneIsAnEmptyList(t *testing.T) {
	in := `{"ideas":[{"number":1091,"title":"Open wish","url":"https://github.com/JRpersonal/streborn/discussions/1091","votes":10}]}`
	got, err := parseWishes(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || len(got.Wishes) != 1 {
		t.Fatalf("want the open wish, got %+v", got)
	}
	if got.Done == nil || len(got.Done) != 0 {
		t.Fatalf("want an empty, non-nil done list for an older ideas.json, got %#v", got.Done)
	}
	broken, _ := parseWishes(strings.NewReader("<html>"))
	if broken.Done == nil {
		t.Fatal("the fallback list must carry an empty done list too")
	}
}
