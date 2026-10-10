package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// wishesURL is the website's export of every open feature wish (the Ideas
// category of the GitHub Discussions) with its vote count. The website builds
// it daily with its own token, so the app never talks to GitHub for this and
// needs no token of its own.
const wishesURL = "https://st-reborn.de/ideas.json"

// Fallback links for the Wishes tab when the list cannot be fetched: the
// buttons to vote and to suggest something must work even offline-ish.
const (
	wishesVoteURL    = "https://github.com/JRpersonal/streborn/discussions/categories/ideas?discussions_q=category%3AIdeas+sort%3Atop"
	wishesSuggestURL = "https://github.com/JRpersonal/streborn/discussions/new?category=ideas"
	// Every link the tab opens must point into the project's own discussions.
	// The list comes from the network, and an entry pointing anywhere else is
	// dropped rather than handed to the system browser.
	wishesLinkPrefix = "https://github.com/JRpersonal/streborn/discussions/"
)

// Wish is one votable feature wish.
type Wish struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Votes  int    `json:"votes"`
}

// WishList is what the Wishes tab renders. OK is false when the list could not
// be fetched; VoteURL and SuggestURL are always set.
type WishList struct {
	OK         bool   `json:"ok"`
	UpdatedAt  string `json:"updatedAt"`
	VoteURL    string `json:"voteUrl"`
	SuggestURL string `json:"suggestUrl"`
	Wishes     []Wish `json:"wishes"`
}

// FeatureWishes returns the open feature wishes, most votes first, for the
// Wishes tab. Best effort: on any error it returns OK=false with the two
// fallback links, so the tab still offers voting and suggesting.
func (a *App) FeatureWishes() WishList {
	ctx, cancel := context.WithTimeout(a.appCtx(), 8*time.Second)
	defer cancel()
	out := WishList{VoteURL: wishesVoteURL, SuggestURL: wishesSuggestURL, Wishes: []Wish{}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wishesURL, nil)
	if err != nil {
		return out
	}
	resp, err := updateHTTPClient().Do(req)
	if err != nil {
		a.logger.Info("wishes: list not fetched", "err", err)
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		a.logger.Info("wishes: list not fetched", "status", resp.StatusCode)
		return out
	}
	list, err := parseWishes(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		a.logger.Info("wishes: list unreadable", "err", err)
		return out
	}
	return list
}

// parseWishes reads the website's ideas.json and keeps only entries that link
// into the project's own discussions.
func parseWishes(r io.Reader) (WishList, error) {
	var in struct {
		UpdatedAt  string `json:"updatedAt"`
		VoteURL    string `json:"voteUrl"`
		SuggestURL string `json:"suggestUrl"`
		Ideas      []Wish `json:"ideas"`
	}
	out := WishList{VoteURL: wishesVoteURL, SuggestURL: wishesSuggestURL, Wishes: []Wish{}}
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return out, err
	}
	if strings.HasPrefix(in.VoteURL, wishesLinkPrefix) {
		out.VoteURL = in.VoteURL
	}
	if strings.HasPrefix(in.SuggestURL, wishesLinkPrefix) {
		out.SuggestURL = in.SuggestURL
	}
	for _, w := range in.Ideas {
		w.Title = strings.TrimSpace(w.Title)
		if w.Title == "" || !strings.HasPrefix(w.URL, wishesLinkPrefix) {
			continue
		}
		if w.Votes < 0 {
			w.Votes = 0
		}
		out.Wishes = append(out.Wishes, w)
	}
	out.UpdatedAt = in.UpdatedAt
	out.OK = true
	return out, nil
}
