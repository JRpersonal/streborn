package main

// This file was split out of app.go (wave-1 move-only refactor):
// playback: slot recall, direct URL play, and the play queue.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// PlaySlot triggert POST /api/play/<slot>.
func (a *App) PlaySlot(host string, port int, slot int) error {
	resp, err := a.playPost(host, port, fmt.Sprintf("/api/play/%d", slot), "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s", friendlyError(resp))
	}
	return nil
}

// isTransportNotReady reports whether err is a connection-level failure
// (timeout, refused, reset, no route) rather than an HTTP response from a
// live agent. On BCO boxes the :17008->:8888 redirect and the agent take
// a few seconds to come up after a reboot or OTA; a play issued in that
// window fails at the transport layer and should read as "still starting"
// instead of a raw timeout (a POST :17008/api/play context
// deadline exceeded right after the box rebooted).
func isTransportNotReady(err error) bool {
	if err == nil {
		return false
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"deadline exceeded", "connection refused", "actively refused", "connection reset", "no route to host", "timeout"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// playPost issues a play POST, but first confirms the agent is actually
// reachable with a cheap, fast probe. This is both quicker and more
// reliable than blindly POSTing and waiting out the play timeout:
//
//   - When the box is ready (the common case) the probe answers in well
//     under a second, then the play runs with its full timeout, so a
//     legitimately slow play (e.g. the agent waking the box from standby)
//     is never cut short. Stability is unchanged.
//   - When the box is still coming up after a reboot/OTA, the probe loop
//     detects "not ready" in a few seconds instead of hanging on the
//     full play timeout, and returns the sentinel "box_not_ready" for the
//     UI to render a localized "speaker is still starting" hint.
func (a *App) playPost(host string, port int, path, body string) (*http.Response, error) {
	if !a.waitAgentReady(host, port) {
		return nil, fmt.Errorf("box_not_ready")
	}
	resp, err := a.boxDo(host, port, http.MethodPost, path, "application/json", body)
	if err != nil {
		if isTransportNotReady(err) {
			return nil, fmt.Errorf("box_not_ready")
		}
		return nil, err
	}
	return resp, nil
}

// waitAgentReady probes the agent's version endpoint (the same cheap
// endpoint discovery uses) with a short per-try timeout, briefly
// retrying so a box whose :17008->:8888 redirect and agent are still
// coming up gets a moment to answer. Returns true the instant it
// responds (so a ready box adds only one sub-second round trip), false
// if it stays unreachable within the budget.
func (a *App) waitAgentReady(host string, port int) bool {
	deadline := time.Now().Add(4 * time.Second)
	for {
		// Try each candidate port; the one that answers is cached so the
		// subsequent play (and every later call) goes straight to it. This
		// is where a box that switched ports (reboot/freeze) gets re-pinned.
		for _, p := range a.candidatePorts(host, port) {
			url := fmt.Sprintf("http://%s:%d/api/agent/version", host, p)
			ctx, cancel := context.WithTimeout(a.appCtx(), 1200*time.Millisecond)
			body, err := httpGetSmall(ctx, url, 1200*time.Millisecond, 512)
			cancel()
			if err == nil && strings.Contains(string(body), `"version"`) {
				a.rememberPort(host, p)
				return true
			}
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-time.After(400 * time.Millisecond):
		case <-a.ctx.Done():
			return false
		}
	}
}

// PlayURL triggers POST /api/play with an arbitrary stream URL. icon is
// the station logo URL (shown on the box), uuid lets
// radio-browser count the click. mime is the library-file codec MIME (also
// the "play direct" marker, #139) and stays empty for radio; codec is
// radio-browser's station codec ("MP3", "AAC+"), which the agent maps to the
// DIDL MIME so AAC stations do not decode to silence (#252).
func (a *App) PlayURL(host string, port int, streamURL, title, icon, uuid, mime, homepage, codec string) error {
	body, _ := json.Marshal(map[string]string{
		"url":      streamURL,
		"title":    title,
		"icon":     icon,
		"uuid":     uuid,
		"mime":     mime,
		"homepage": homepage,
		"codec":    codec,
	})
	resp, err := a.playPost(host, port, "/api/play", string(body))
	if err != nil {
		a.logger.Info("play: url failed", "host", host, "title", title, "err", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg := friendlyError(resp)
		a.logger.Info("play: url rejected", "host", host, "title", title, "status", resp.StatusCode, "err", msg)
		return fmt.Errorf("%s", msg)
	}
	a.logger.Info("play: url accepted", "host", host, "title", title, "mime", mime, "codec", codec)
	return nil
}

// StartQueue starts an agent-side library play queue. payloadJSON is the full
// request body the agent expects:
// {"items":[{"url","title","art","mime","duration_sec"}],"start","shuffle","repeat"}.
// The queue auto-advances on the box; a single PlayURL later clears it.
func (a *App) StartQueue(host string, port int, payloadJSON string) error {
	// Item count and shuffle flag pulled out for the log line only; the
	// payload itself passes through untouched (the frontend owns the shape).
	var q struct {
		Items   []json.RawMessage `json:"items"`
		Shuffle bool              `json:"shuffle"`
	}
	_ = json.Unmarshal([]byte(payloadJSON), &q)
	resp, err := a.playPost(host, port, "/api/queue", payloadJSON)
	if err != nil {
		a.logger.Info("queue: start failed", "host", host, "items", len(q.Items), "err", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg := friendlyError(resp)
		a.logger.Info("queue: start rejected", "host", host, "items", len(q.Items), "status", resp.StatusCode, "err", msg)
		return fmt.Errorf("%s", msg)
	}
	a.logger.Info("queue: started", "host", host, "items", len(q.Items), "shuffle", q.Shuffle)
	return nil
}

// QueueNext / QueuePrev skip within the active queue.
func (a *App) QueueNext(host string, port int) error {
	return a.queuePost(host, port, "/api/queue/next", "")
}

func (a *App) QueuePrev(host string, port int) error {
	return a.queuePost(host, port, "/api/queue/prev", "")
}

// QueueShuffle turns shuffle on or off for the active queue.
func (a *App) QueueShuffle(host string, port int, on bool) error {
	b, _ := json.Marshal(map[string]bool{"on": on})
	return a.queuePost(host, port, "/api/queue/shuffle", string(b))
}

// QueueRepeat sets the repeat mode ("off", "all", "one") for the active queue.
func (a *App) QueueRepeat(host string, port int, mode string) error {
	b, _ := json.Marshal(map[string]string{"mode": mode})
	return a.queuePost(host, port, "/api/queue/repeat", string(b))
}

func (a *App) queuePost(host string, port int, path, body string) error {
	resp, err := a.boxDo(host, port, http.MethodPost, path, "application/json", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}

// GetQueue returns the current queue snapshot (active, pos, shuffle, repeat,
// items) or an empty object when no queue is active.
func (a *App) GetQueue(host string, port int) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/queue", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
