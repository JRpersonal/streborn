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
//
// Every exit is logged. A slot recall used to be the only play path that
// wrote nothing at all to the app log, so a failed one left no trace on
// either side: the box log shows no incoming request (the app never got
// that far) and the app log shows nothing either. A bundle taken right
// after a failing preset press was therefore unreadable (#582, shorty310:
// two tiles showing "speaker is still starting" with an idle box log).
func (a *App) PlaySlot(host string, port int, slot int) error {
	resp, err := a.playPost(host, port, fmt.Sprintf("/api/play/%d", slot), "")
	if err != nil {
		a.logger.Info("play: slot failed", "host", host, "port", port, "slot", slot, "err", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg := friendlyError(resp)
		a.logger.Info("play: slot rejected", "host", host, "slot", slot, "status", resp.StatusCode, "err", msg)
		return fmt.Errorf("%s", msg)
	}
	a.logger.Info("play: slot accepted", "host", host, "slot", slot)
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
	// A dial that failed is a transport failure whatever the OS says about
	// it. The keyword list below is English, and Windows words a refused
	// connection in the system language ("Es konnte keine Verbindung
	// hergestellt werden, da der Zielcomputer die Verbindung verweigerte"),
	// so on a German PC a refusal did not read as one: boxDo then stopped at
	// the first port instead of trying the other, and handed back the raw
	// text without the reachability hint.
	var operr *net.OpError
	if errors.As(err, &operr) && operr.Op == "dial" {
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
//     under a second, then the play runs with playCallTimeout rather than
//     the shared 6 s client timeout, so a legitimately slow play (the agent
//     waking the box from standby first) is not cut short.
//   - When the box is still coming up after a reboot/OTA, the probe loop
//     detects "not ready" in a few seconds instead of hanging on the
//     full play timeout, and returns the sentinel "box_not_ready" for the
//     UI to render a localized "speaker is still starting" hint.
func (a *App) playPost(host string, port int, path, body string) (*http.Response, error) {
	if !a.waitAgentReady(host, port) {
		return nil, fmt.Errorf("box_not_ready")
	}
	resp, err := a.boxDoTimeout(host, port, http.MethodPost, path, "application/json", body, playCallTimeout)
	if err != nil {
		if isTransportNotReady(err) {
			// Say what happened. The readiness probe logs when IT gives up, but
			// this second path had no line at all, so a bundle showed a bare
			// "box_not_ready" with nothing behind it. One report carried over
			// eighty of them across four log generations and not one said why
			// (2026-09-29); the cause turned out to be on the speaker, which had
			// answered the probe and then refused to leave standby.
			a.logger.Warn("play: the speaker answered the readiness probe but dropped the play request",
				"host", host, "port", port, "path", path, "err", err)
			return nil, fmt.Errorf("box_not_ready")
		}
		return nil, err
	}
	return resp, nil
}

// playCallTimeout bounds one play POST. The agent's play handlers wake a
// speaker out of standby before they start the stream, and that wake alone may
// take up to about 8.5 s. With the shared 6 s client timeout the app gave up
// a fraction of a second before the music started and showed
// "box_not_ready" for a play that went on to succeed (#1065: click at
// 09:37:39.5, app timeout at 09:37:45.499, speaker playing at 09:37:45.66).
// A speaker that is really not there is still caught fast by the readiness
// probe in front of the POST, so this longer budget only applies to a box
// that answered and is busy.
const playCallTimeout = 20 * time.Second

// agentVersionAnswered reports whether a body is the agent's version payload.
//
// It DECODES rather than searching for a key name, so the verdict cannot depend
// on how many optional flags the speaker happened to include or on where they
// landed in a size-capped read. A non-empty "version" is the whole test: the
// two other answers this probe meets are a bare 400 from the box's own listener
// while the agent is still down, and a stock-firmware 404, neither of which
// parses as an object carrying that field.
func agentVersionAnswered(body []byte) bool {
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return false
	}
	return strings.TrimSpace(v.Version) != ""
}

// The readiness budgets, split for the reason spelled out on waitAgentReady:
// a speaker that is not there must fail fast, a speaker that is there and
// busy is worth waiting for. Deliberately the same values discovery arrived
// at independently (probeDialTimeout / probeAnswerBudget), because it is the
// same question asked of the same endpoint.
const (
	readyDialTimeout   = 1200 * time.Millisecond
	readyAnswerBudget  = 8 * time.Second
	readyOverallBudget = 12 * time.Second
)

// waitAgentReady probes the agent's version endpoint (the same cheap
// endpoint discovery uses), briefly retrying so a box whose :17008->:8888
// redirect and agent are still coming up gets a moment to answer. Returns
// true the instant it responds (so a ready box adds only one sub-second
// round trip), false if it stays unreachable within the budget.
//
// The two timeouts are SPLIT, the way discovery split them, and for the same
// reason. Reaching a speaker that is not there must fail fast, so the dial
// keeps its 1.2 s. Waiting for a reply from a speaker that has already
// answered the connection is a different question: under box load (BoseApp
// churning, loadavg 3 to 4) the agent can take seconds to serve its own
// version, and it is demonstrably alive while it does.
//
// One 1.2 s budget for both is what made this path refuse speakers that were
// running. Measured in a reporter's bundle: the app logged "agent readiness
// probe gave up, reporting the box as not ready" with four ports tried in
// 5.2 s, while the speaker's own log shows the agent healing presets at that
// same second and answering the box. Fourteen seconds later the identical
// play was accepted. Their music library and a radio station had both been
// refused as box_not_ready in the minutes before, and the speaker played that
// exact station from its own preset button 23 s later.
//
// discovery/app_discovery.go already carries this lesson as probeDialTimeout
// and probeAnswerBudget; this path simply never got it.
func (a *App) waitAgentReady(host string, port int) bool {
	// Long enough that a loaded speaker gets one unhurried answer per port,
	// rather than four hurried refusals.
	deadline := time.Now().Add(readyOverallBudget)
	started := time.Now()
	// Kept for the give-up log line: without them a "box_not_ready" is a
	// dead end in a bundle, because it names neither the ports that were
	// tried nor why they did not answer (#582).
	var tried []int
	var lastErr error
	var lastBody string
	for {
		// Try each candidate port; the one that answers is cached so the
		// subsequent play (and every later call) goes straight to it. This
		// is where a box that switched ports (reboot/freeze) gets re-pinned.
		for _, p := range a.candidatePorts(host, port) {
			url := fmt.Sprintf("http://%s:%d/api/agent/version", host, p)
			ctx, cancel := context.WithTimeout(a.appCtx(), readyAnswerBudget)
			// 8 KB, and the readiness test decodes the answer instead of
			// searching a truncated prefix for a key name. At 512 bytes the
			// probe depended on where encoding/json happened to place
			// "version": the agent marshals a map, map keys are sorted, and
			// every optional flag sorts before "version" and pushed it right.
			// A healthy speaker measured 419 to 435 bytes to that key, and a
			// box carrying the conflicting-mod and foreign-cloud-URL flags
			// together added 99, so the key fell outside the window and every
			// play on the one box that needed help was refused as "still
			// starting" (2026-09-25). The agent now emits version first as
			// well; this side stops the whole class.
			body, err := httpGetSmall(ctx, url, readyDialTimeout, 8192)
			cancel()
			if err == nil && agentVersionAnswered(body) {
				a.rememberPort(host, p)
				return true
			}
			tried = append(tried, p)
			lastErr = err
			if err == nil {
				// Answered, but not with an agent version: a bare 400 from the
				// box's own :17008 listener while the agent is not up yet, or a
				// stock-firmware 404. The body separates the two.
				lastBody = string(body)
			}
		}
		if !time.Now().Before(deadline) {
			a.logger.Warn("play: agent readiness probe gave up, reporting the box as not ready",
				"host", host, "port", port, "portsTried", tried,
				"elapsedMs", time.Since(started).Milliseconds(),
				"lastErr", lastErr, "lastBody", truncForLog(lastBody, 120))
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
//
// durationSec is the track length, 0 for radio and anything of unknown length.
// It is not decoration: the firmware answers `<time total="0">` unless the DIDL
// it was handed carries a duration, and with total 0 there is no bar to fill and
// no end of track to detect. A folder has always sent it, which is why a folder
// drew a bar per track while a single track never did, in BOTH UIs, because both
// only read back what the DIDL carried (#845, #844).
func (a *App) PlayURL(host string, port int, streamURL, title, icon, uuid, mime, homepage, codec string, durationSec int) error {
	body, _ := json.Marshal(map[string]any{
		"url":          streamURL,
		"title":        title,
		"icon":         icon,
		"uuid":         uuid,
		"mime":         mime,
		"homepage":     homepage,
		"codec":        codec,
		"duration_sec": durationSec,
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

// folderReplayUnsupported is what ReplayFolderCard returns when the speaker's
// agent is older than the endpoint. The frontend treats it as "fall back to the
// old single-track replay" rather than as a failure, so a speaker that has not
// been updated yet behaves exactly as it did before.
const folderReplayUnsupported = "folder_replay_unsupported"

// ReplayFolderCard replays a Recently-played FOLDER card as the whole folder.
//
// The card itself only stores the first track's URL, and replaying that through
// the single-track path clears the queue, so the folder used to play one song and
// stop (#978, reported 2026-09-26). The speaker knows how to rebuild the queue
// from the card key, which carries the media server and the container, so the
// whole job is one call and the phone remote gets the same fix.
func (a *App) ReplayFolderCard(host string, port int, key, name, art string) error {
	body, _ := json.Marshal(map[string]string{"key": key, "name": name, "art": art})
	resp, err := a.playPost(host, port, "/api/queue/replay-card", string(body))
	if err != nil {
		a.logger.Info("queue: folder card replay failed", "host", host, "key", key, "err", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		// Either the agent predates the endpoint, or the media server is no
		// longer a registered music source. Both are cases where the old
		// behaviour (play the one track the card holds) is better than an error.
		a.logger.Info("queue: folder card replay not available on this speaker", "host", host, "status", resp.StatusCode)
		return fmt.Errorf("%s", folderReplayUnsupported)
	}
	if resp.StatusCode >= 400 {
		msg := friendlyError(resp)
		a.logger.Info("queue: folder card replay rejected", "host", host, "status", resp.StatusCode, "err", msg)
		return fmt.Errorf("%s", msg)
	}
	var out struct {
		Tracks  int    `json:"tracks"`
		Error   string `json:"error"`
		Offline bool   `json:"offline"`
		Server  string `json:"server"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Offline {
		a.logger.Info("queue: folder card replay found the media server offline", "host", host, "server", out.Server)
		return fmt.Errorf("the music server %s is not reachable right now", strings.TrimSpace(out.Server))
	}
	if out.Error != "" {
		a.logger.Info("queue: folder card replay refused", "host", host, "err", out.Error)
		return fmt.Errorf("%s", out.Error)
	}
	a.logger.Info("queue: folder card replayed", "host", host, "tracks", out.Tracks)
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
