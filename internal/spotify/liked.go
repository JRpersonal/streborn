// liked.go: saving the song that plays to the account's Liked Songs and
// removing it again, for the remote's thumbs up and thumbs down keys
// (discussion #1165).
//
// No Web API call and no OAuth scope are involved. The engine's own
// POST /library/liked writes to Spotify's internal collection service
// (spclient /collection/v2/write) with the session it is logged in with, the
// same session that plays the audio. The access token /token hands out
// (accounts.go) is NOT used: whether that token carries user-library-modify
// for PUT /v1/me/tracks is not known, and nothing here depends on it.
//
// The engine has the route since the fork's 2026-10 upstream merge
// (JRpersonal/go-librespot, upstream 479f3fc). It is not in the "features"
// list of GET / (enginefeatures.go), so an older build is told by its answer
// instead: the engine's router is net/http's ServeMux, which answers an
// unknown route with 404 and a text body ("404 page not found"), or 405 when
// only another method is registered, while the daemon's own error answers
// carry a status and no body. That is the capability check, made on the press
// itself: nothing is probed ahead of time.

package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// likedPath is the engine route that saves to and removes from Liked Songs.
const likedPath = "/library/liked"

// likeRepeatWindow is how long a second like (or a second removal) of the
// same song is answered from memory instead of being written again: a doubled
// key event or an impatient second press must not write twice (each save
// re-stamps the song's "added" date in the list). Only the SAME action is
// remembered: a removal right after a save, or a save right after a removal,
// is always written, so the last press wins.
const likeRepeatWindow = 30 * time.Second

// The reasons a like did not happen. Each is its own error so the caller can
// log what the user can do about it.
var (
	// ErrLikeNoTrack: the engine has no song loaded.
	ErrLikeNoTrack = errors.New("no Spotify song is loaded")
	// ErrLikeNotATrack: what plays is not a song (a podcast episode); Liked
	// Songs only holds songs.
	ErrLikeNotATrack = errors.New("what plays is not a song, Liked Songs only takes songs")
	// ErrLikeUnsupported: this engine build has no /library/liked.
	ErrLikeUnsupported = errors.New("this Spotify engine build cannot save to Liked Songs yet")
	// ErrLikeNoSession: the engine is not logged in to Spotify right now.
	ErrLikeNoSession = errors.New("the Spotify engine has no session")
	// ErrLikeUnauthorized: Spotify did not accept the engine's login (401).
	ErrLikeUnauthorized = errors.New("spotify did not accept the login for this change")
	// ErrLikeForbidden: Spotify refused the change for this account (403).
	ErrLikeForbidden = errors.New("spotify refused the change for this account")
	// ErrLikeRateLimited: too many requests (429).
	ErrLikeRateLimited = errors.New("spotify is rate limiting this speaker")
	// ErrLikeRejected: the engine found the request invalid (400).
	ErrLikeRejected = errors.New("the Spotify engine rejected the request")
	// ErrLikeFailed: anything else, the engine's status is in the message.
	ErrLikeFailed = errors.New("saving to Liked Songs failed")
)

// LikedTrack is the song a like or a removal was about.
type LikedTrack struct {
	URI  string
	Name string
	// Repeat is true when the same action already ran for this song inside
	// likeRepeatWindow and nothing was written this time.
	Repeat bool
}

// isTrackURI reports whether uri names a song. The engine takes the base-62
// id form only (spotify:track:<id>).
func isTrackURI(uri string) bool {
	id, ok := strings.CutPrefix(uri, "spotify:track:")
	return ok && id != "" && !strings.ContainsAny(id, ":/? \t\r\n")
}

// LikeCurrentTrack saves the song the engine has loaded to the logged-in
// account's Liked Songs. The returned track is filled whenever the song was
// known, also on an error, so the caller can name it.
func (m *Manager) LikeCurrentTrack(ctx context.Context) (LikedTrack, error) {
	return m.setCurrentTrackLiked(ctx, true)
}

// UnlikeCurrentTrack removes the song the engine has loaded from the
// logged-in account's Liked Songs. A song that was not in the list is not an
// error: the collection service takes the removal either way, and STR does
// not ask first. The track is filled as for LikeCurrentTrack.
func (m *Manager) UnlikeCurrentTrack(ctx context.Context) (LikedTrack, error) {
	return m.setCurrentTrackLiked(ctx, false)
}

func (m *Manager) setCurrentTrackLiked(ctx context.Context, liked bool) (LikedTrack, error) {
	if m == nil || m.client == nil {
		return LikedTrack{}, ErrLikeNoSession
	}
	st, ok := m.readEngineStatus(ctx)
	if !ok {
		return LikedTrack{}, fmt.Errorf("%w: the engine's status could not be read", ErrLikeFailed)
	}
	if st.Track == nil || st.Track.URI == "" {
		return LikedTrack{}, ErrLikeNoTrack
	}
	tr := LikedTrack{URI: st.Track.URI, Name: st.Track.Name}
	if !isTrackURI(tr.URI) {
		return tr, ErrLikeNotATrack
	}

	m.mu.Lock()
	repeat := m.likedURI == tr.URI && m.likedState == liked && time.Since(m.likedAt) < likeRepeatWindow
	m.mu.Unlock()
	if repeat {
		tr.Repeat = true
		return tr, nil
	}

	if err := m.setLiked(ctx, tr.URI, liked); err != nil {
		return tr, err
	}
	m.mu.Lock()
	m.likedURI, m.likedState, m.likedAt = tr.URI, liked, time.Now()
	m.mu.Unlock()
	return tr, nil
}

// setLiked posts one song to the engine's /library/liked, liked=true to save
// it and false to remove it, and turns the answer into one of the errors
// above. The value is always sent: the engine refuses a request without it. Not through apiPost: this is
// no player command, so it must not open the own-command echo window.
func (m *Manager) setLiked(ctx context.Context, uri string, liked bool) error {
	body, err := json.Marshal(struct {
		URIs  []string `json:"uris"`
		Liked bool     `json:"liked"`
	}{URIs: []string{uri}, Liked: liked})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLikeFailed, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+m.apiAddr+likedPath, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLikeFailed, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: the engine did not answer: %w", ErrLikeFailed, err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return likeAnswerError(resp.StatusCode, len(strings.TrimSpace(string(answer))) > 0)
}

// likeAnswerError maps the engine's answer to POST /library/liked. hasBody
// tells the router's own 404/405 (a text body: the route does not exist in
// this build) from the daemon's (no body: Spotify's answer passed on).
func likeAnswerError(status int, hasBody bool) error {
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusNoContent:
		return ErrLikeNoSession
	case (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) && hasBody:
		return ErrLikeUnsupported
	case status == http.StatusBadRequest:
		return ErrLikeRejected
	case status == http.StatusUnauthorized:
		return ErrLikeUnauthorized
	case status == http.StatusForbidden:
		return ErrLikeForbidden
	case status == http.StatusTooManyRequests:
		return ErrLikeRateLimited
	}
	return fmt.Errorf("%w: the engine answered %d", ErrLikeFailed, status)
}

// LikeFailureReason is a short, stable word for why a save or a removal failed,
// for the log: a bundle can be searched for it.
func LikeFailureReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrLikeUnsupported):
		return "engine-too-old"
	case errors.Is(err, ErrLikeNoTrack):
		return "no-track"
	case errors.Is(err, ErrLikeNotATrack):
		return "not-a-song"
	case errors.Is(err, ErrLikeNoSession):
		return "no-session"
	case errors.Is(err, ErrLikeUnauthorized):
		return "unauthorized-401"
	case errors.Is(err, ErrLikeForbidden):
		return "forbidden-403"
	case errors.Is(err, ErrLikeRateLimited):
		return "rate-limited-429"
	case errors.Is(err, ErrLikeRejected):
		return "rejected-400"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "failed"
}
