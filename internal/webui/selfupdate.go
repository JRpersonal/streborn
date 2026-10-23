package webui

// The speaker updates itself, no desktop app involved (idea #1091).
//
// Until now an STR update could only come from the desktop app, which carries
// the agent binary and pushes it to /api/agent/update. A phone owner without a
// computer had no way to update at all. Here the agent fetches the latest
// release from GitHub on its own and installs it through the SAME handlers the
// desktop push uses, so the space reclaim, the RAM-staged swap, the flash
// verify, the stick refresh and the reboot that deploys run.sh are not
// re-implemented: the downloaded body is handed to handleAgentUpdate (and to
// handleAgentSidecar for the Spotify engine) as if the desktop app had sent it.
//
// Trust: the release carries update.json, signed with the project's release
// key (internal/updatesig). Nothing is installed unless that signature
// verifies, the version is newer than the running one, and every byte matches
// the signed hash and size. The hash check sits in front of the handler:
// heldBackBody withholds the final byte until the whole download hashed
// correctly, so a tampered or truncated download reaches the handler as a
// short body and is discarded before anything is renamed into place.
//
// Only the owner starts it, from the phone remote or any LAN client. Nothing
// here runs on a timer: a binary reaches the speaker only while an install or
// update the owner asked for is running (standing rule, 2026-07-29). The one
// thing that continues on its own is the second half of that same update after
// its reboot: the Spotify engine, which the update may have deleted to make
// room, is fetched again by the new agent (selfUpdatePendingPath).

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/internal/streamproxy"
	"github.com/JRpersonal/streborn/internal/updatesig"
)

// selfUpdateDefaultBase is where releases live. The layout below it is
// GitHub's: latest/download/<file> for the newest release, download/<tag>/<file>
// for a given one.
const selfUpdateDefaultBase = "https://github.com/JRpersonal/streborn/releases/"

var (
	// selfUpdatePendingPath marks an update that rebooted into the new agent
	// and still has its Spotify engine to fetch. It holds the signed hash and
	// size from the index, so the new agent needs no second signature check.
	selfUpdatePendingPath = "/mnt/nv/streborn/selfupdate-pending.json"
	// selfUpdateSourcePath lets a developer point a test speaker at a local
	// release and an extra key. It sits on the NAND, where only root can
	// write, and root can replace the agent anyway, so it opens nothing new.
	selfUpdateSourcePath = "/mnt/nv/streborn/selfupdate-source.json"
	// selfUpdateResultPath keeps the outcome of the last update across the
	// reboot, so the phone can say "updated to v1.2.3" when it reconnects.
	selfUpdateResultPath = "/mnt/nv/streborn/selfupdate-last.json"
	// selfUpdateEngineRetry is how long the new agent keeps trying to fetch
	// the engine after the update's reboot (Wi-Fi and the clock come up late).
	selfUpdateEngineRetry = []time.Duration{20 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}
)

// selfUpdateStallTimeout aborts a download that delivers nothing for this long.
const selfUpdateStallTimeout = 90 * time.Second

// selfUpdateCheckTTL is how long a check result counts as current for an install.
const selfUpdateCheckTTL = 15 * time.Minute

type selfUpdateSource struct {
	Base string   `json:"base"`
	Keys []string `json:"keys"`
	// Peers narrows "update all speakers" to these addresses, so a developer
	// can run the whole flow against test speakers on a LAN that has others.
	Peers []string `json:"peers,omitempty"`
}

// selfUpdatePending is the marker the update leaves for the agent it installs.
type selfUpdatePending struct {
	Version string          `json:"version"`
	From    string          `json:"from"`
	Engine  *updatesig.File `json:"engine,omitempty"`
	Base    string          `json:"base"`
	At      time.Time       `json:"at"`
	// Fleet is the "update all speakers" run this update was the last step
	// of, carried across the reboot so the phone gets its summary.
	Fleet *fleetRun `json:"fleet,omitempty"`
}

// selfUpdateResult is what the last update ended with.
type selfUpdateResult struct {
	Version string    `json:"version"`
	From    string    `json:"from,omitempty"`
	OK      bool      `json:"ok"`
	Error   string    `json:"error,omitempty"`
	At      time.Time `json:"at"`
}

// selfUpdateStatus is the JSON the phone polls.
type selfUpdateStatus struct {
	Current   string            `json:"current"`
	State     string            `json:"state"` // idle, checking, downloading, installing, rebooting, engine, failed
	Phase     string            `json:"phase,omitempty"`
	Done      int64             `json:"done,omitempty"`
	Total     int64             `json:"total,omitempty"`
	Latest    string            `json:"latest,omitempty"`
	Available bool              `json:"available"`
	CheckedAt *time.Time        `json:"checkedAt,omitempty"`
	Error     string            `json:"error,omitempty"`
	Engine    string            `json:"engine"` // present, missing
	Last      *selfUpdateResult `json:"last,omitempty"`
	// Fleet is the "update all speakers" plan or run (selfupdate_fleet.go).
	Fleet *fleetRun `json:"fleet,omitempty"`
	// PhoneRun is set while a phone walks such a run itself
	// (selfupdate_phone.go). It never carries the token.
	PhoneRun *phoneRunView `json:"phoneRun,omitempty"`
}

type selfUpdater struct {
	mu        sync.Mutex
	state     string
	phase     string
	done      int64
	total     int64
	err       string
	index     *updatesig.Index
	base      string
	checkedAt time.Time
	busy      bool
}

func (u *selfUpdater) set(state, phase string) {
	u.mu.Lock()
	u.state, u.phase, u.done, u.total = state, phase, 0, 0
	if state != "failed" {
		u.err = ""
	}
	u.mu.Unlock()
}

func (u *selfUpdater) fail(err error) {
	u.mu.Lock()
	u.state, u.err = "failed", err.Error()
	u.mu.Unlock()
}

func (u *selfUpdater) progress(done, total int64) {
	u.mu.Lock()
	u.done, u.total = done, total
	u.mu.Unlock()
}

// claim marks the updater busy; false when another check or install runs.
func (u *selfUpdater) claim() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.busy {
		return false
	}
	u.busy = true
	return true
}

// isBusy reports whether a check or install holds the updater.
func (u *selfUpdater) isBusy() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.busy
}

func (u *selfUpdater) release() {
	u.mu.Lock()
	u.busy = false
	u.mu.Unlock()
}

func (s *Server) selfUpdateStatus() selfUpdateStatus {
	u := &s.selfUpd
	u.mu.Lock()
	st := selfUpdateStatus{
		Current: agentVersion(),
		State:   u.state,
		Phase:   u.phase,
		Done:    u.done,
		Total:   u.total,
		Error:   u.err,
	}
	if st.State == "" {
		st.State = "idle"
	}
	if u.index != nil {
		st.Latest = u.index.Version
		st.Available = updatesig.VersionLess(st.Current, u.index.Version)
		t := u.checkedAt
		st.CheckedAt = &t
	}
	u.mu.Unlock()
	st.Engine = "missing"
	if present, _ := goLibrespotStamp(); present {
		st.Engine = "present"
	}
	if b, err := os.ReadFile(selfUpdateResultPath); err == nil {
		var r selfUpdateResult
		if json.Unmarshal(b, &r) == nil && r.Version != "" {
			st.Last = &r
		}
	}
	st.Fleet = s.fleet.snapshot()
	st.PhoneRun = s.phoneRun.view(time.Now())
	return st
}

// handleSelfUpdate serves /api/agent/self-update.
//
//	GET                       the status the phone polls
//	POST {"action":"check"}   ask GitHub for the latest release (waits for it)
//	POST {"action":"install"} install it; returns at once, poll GET for progress
//	POST {"action":"engine"}  fetch the Spotify engine of the running version
//	POST {"action":"plan"}    list the other STR speakers and what they run
//	POST {"action":"install-all"} update every speaker, this one last; with
//	                          "peers":[...] only those peers, this one not
//	POST {"action":"run-claim","token":"..."} a phone starts or renews its run
//	POST {"action":"run-end","token":"..."}   and ends it
//
// "token" on install, engine and install-all is the claiming phone's; while
// a phone run is claimed, those actions without it answer 409.
func (s *Server) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		s.refuseNonLAN(w, r, "self-update")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.selfUpdateStatus())
		return
	}
	if selfUpdateForeignOrigin(r) {
		s.logger.Warn("self-update: refused a request from a foreign page", "origin", r.Header.Get("Origin"), "remote", r.RemoteAddr)
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	var req struct {
		Action string   `json:"action"`
		Token  string   `json:"token,omitempty"`
		Peers  []string `json:"peers,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Token != "" && !phoneRunTokenRE.MatchString(req.Token) {
		http.Error(w, "bad token", http.StatusBadRequest)
		return
	}
	now := time.Now()
	switch req.Action {
	case "run-claim":
		if req.Token == "" {
			http.Error(w, "token required", http.StatusBadRequest)
			return
		}
		if s.fleet.runningFor() != "" && s.fleet.runningFor() != req.Token {
			writeJSON(w, http.StatusConflict, s.selfUpdateStatus())
			return
		}
		if !s.phoneRun.claim(req.Token, now) {
			writeJSON(w, http.StatusConflict, s.selfUpdateStatus())
			return
		}
		writeJSON(w, http.StatusOK, s.selfUpdateStatus())
		return
	case "run-end":
		s.phoneRun.release(req.Token)
		writeJSON(w, http.StatusOK, s.selfUpdateStatus())
		return
	}
	if (req.Action == "install" || req.Action == "engine" || req.Action == "install-all") && s.phoneRun.heldByOther(req.Token, now) {
		// A phone is walking the speakers one after the other; it updates
		// this one last itself. Anybody else asking now would cut that off.
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a phone is updating all speakers right now"})
		return
	}
	switch req.Action {
	case "check":
		if !s.selfUpd.claim() {
			writeJSON(w, http.StatusConflict, s.selfUpdateStatus())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		_, err := s.selfUpdateCheck(ctx)
		cancel()
		s.selfUpd.release()
		if err != nil {
			s.logger.Warn("self-update: check failed", "err", err)
		}
		writeJSON(w, http.StatusOK, s.selfUpdateStatus())
	case "plan":
		ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
		err := s.fleetPlan(ctx)
		cancel()
		if err != nil {
			s.logger.Warn("self-update fleet: plan failed", "err", err)
		}
		writeJSON(w, http.StatusOK, s.selfUpdateStatus())
	case "install-all":
		if updateInFlight.Load() || s.selfUpd.isBusy() {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "an update is already installing on this speaker"})
			return
		}
		var only []string
		if len(req.Peers) > 0 {
			// The phone could not reach these peers itself and hands them
			// over. Only speakers of this speaker's own list are accepted.
			only = s.fleetKnownPeers(r.Context(), req.Peers)
			if len(only) == 0 {
				http.Error(w, "none of these speakers is on this speaker's list", http.StatusBadRequest)
				return
			}
		}
		if !s.startFleet(only, req.Token) {
			writeJSON(w, http.StatusConflict, s.selfUpdateStatus())
			return
		}
		writeJSON(w, http.StatusAccepted, s.selfUpdateStatus())
	case "install", "engine":
		if s.fleet.running() {
			// This speaker is updating the others and goes last itself; a
			// second driver asking it to update now would cut that run off.
			writeJSON(w, http.StatusConflict, map[string]string{"error": "this speaker is updating all speakers right now"})
			return
		}
		if updateInFlight.Load() {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "an update is already installing on this speaker"})
			return
		}
		if !s.selfUpd.claim() {
			writeJSON(w, http.StatusConflict, s.selfUpdateStatus())
			return
		}
		if req.Action == "install" {
			s.selfUpd.set("downloading", "agent")
			go s.runSelfUpdate()
		} else {
			s.selfUpd.set("engine", "engine")
			go s.runEngineFetch()
		}
		writeJSON(w, http.StatusAccepted, s.selfUpdateStatus())
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// readSelfUpdateSource reads the developer override, if there is one.
func readSelfUpdateSource() (selfUpdateSource, bool) {
	var src selfUpdateSource
	b, err := os.ReadFile(selfUpdateSourcePath)
	if err != nil || json.Unmarshal(b, &src) != nil {
		return selfUpdateSource{}, false
	}
	return src, true
}

// selfUpdateSourceNow returns the release base and the trusted keys.
func selfUpdateSourceNow() (base string, keys []string) {
	base, keys = selfUpdateDefaultBase, updatesig.PublicKeys
	src, ok := readSelfUpdateSource()
	if !ok {
		return base, keys
	}
	if src.Base != "" {
		base = src.Base
		if !strings.HasSuffix(base, "/") {
			base += "/"
		}
	}
	return base, append(append([]string{}, keys...), src.Keys...)
}

// selfUpdateCheck fetches and verifies the latest release index.
func (s *Server) selfUpdateCheck(ctx context.Context) (updatesig.Index, error) {
	s.selfUpd.set("checking", "")
	base, keys := selfUpdateSourceNow()
	ix, err := s.fetchSignedIndex(ctx, base, base+"latest/download/", keys)
	// A network that is only just back (DNS answers late after a router
	// restart) gets a few more tries before the check counts as failed.
	for until := time.Now().Add(selfUpdateCheckRetryFor); err != nil && (isNetworkError(err) || isTransientHTTP(err)) && time.Now().Before(until); {
		s.logger.Info("self-update: check could not reach the release, trying again", "err", err)
		if !sleepCtx(ctx, selfUpdateCheckRetryEvery) {
			break
		}
		ix, err = s.fetchSignedIndex(ctx, base, base+"latest/download/", keys)
	}
	if err != nil {
		s.selfUpd.fail(fmt.Errorf("could not check for updates: %w", err))
		return updatesig.Index{}, err
	}
	s.selfUpd.mu.Lock()
	s.selfUpd.index, s.selfUpd.base, s.selfUpd.checkedAt = &ix, base, time.Now()
	s.selfUpd.mu.Unlock()
	s.selfUpd.set("idle", "")
	s.logger.Info("self-update: checked", "running", agentVersion(), "latest", ix.Version,
		"available", updatesig.VersionLess(agentVersion(), ix.Version))
	return ix, nil
}

// fetchSignedIndex reads update.json and its signature from dir and verifies them.
func (s *Server) fetchSignedIndex(ctx context.Context, base, dir string, keys []string) (updatesig.Index, error) {
	c := s.selfUpdateClient(base)
	payload, err := fetchSmall(ctx, c, dir+updatesig.IndexName, 64<<10)
	if err != nil {
		return updatesig.Index{}, err
	}
	sig, err := fetchSmall(ctx, c, dir+updatesig.SigName, 1<<10)
	if err != nil {
		return updatesig.Index{}, err
	}
	return updatesig.Verify(payload, string(sig), keys)
}

func fetchSmall(ctx context.Context, c *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{name: lastPathPart(url), code: resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", lastPathPart(url), limit)
	}
	return b, nil
}

func lastPathPart(u string) string {
	if i := strings.LastIndexByte(u, '/'); i >= 0 {
		return u[i+1:]
	}
	return u
}

// selfUpdateClient talks to GitHub. Each connection is verified with the
// clock-tolerant check the radio relay uses (a speaker has no RTC and boots
// into 2015), built per host so the redirect to GitHub's asset host is checked
// against that host. A speaker whose trust store does not know the issuer
// still downloads: what is installed is decided by the signed index, not by
// TLS, and the files are public.
func (s *Server) selfUpdateClient(base string) *http.Client {
	logger := s.logger
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	tr := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			raw, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			cfg := streamproxy.ClockTolerantTLSConfigSigned(host, logger)
			// HTTP/1.1 only: a Transport with its own TLS dialer does not speak
			// h2, and offering it made GitHub pick h2 and the request end in a
			// bare EOF (live, ST10, 2026-10-14).
			cfg.NextProtos = []string{"http/1.1"}
			conn := tls.Client(raw, cfg)
			if err := conn.HandshakeContext(ctx); err != nil {
				_ = raw.Close()
				return nil, err
			}
			return conn, nil
		},
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	plainOK := strings.HasPrefix(base, "http://")
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" && !plainOK {
				return errors.New("refusing a redirect away from HTTPS")
			}
			return nil
		},
	}
}

// runSelfUpdate installs the checked release. The agent replaces itself and
// reboots inside handleAgentUpdate; the engine follows after the reboot.
func (s *Server) runSelfUpdate() {
	defer s.selfUpd.release()
	s.selfUpd.mu.Lock()
	ix, base, at := s.selfUpd.index, s.selfUpd.base, s.selfUpd.checkedAt
	s.selfUpd.mu.Unlock()
	if ix == nil || time.Since(at) > selfUpdateCheckTTL {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		fresh, err := s.selfUpdateCheck(ctx)
		cancel()
		if err != nil {
			s.selfUpd.fail(fmt.Errorf("could not check for updates: %w", err))
			return
		}
		ix = &fresh
		s.selfUpd.mu.Lock()
		base = s.selfUpd.base
		s.selfUpd.mu.Unlock()
	}
	from := agentVersion()
	if !updatesig.VersionLess(from, ix.Version) {
		s.selfUpd.fail(fmt.Errorf("this speaker already runs %s, the latest release is %s", from, ix.Version))
		return
	}
	agent := ix.Files[updatesig.AgentFile]
	p := selfUpdatePending{Version: ix.Version, From: from, Base: base, At: time.Now().UTC()}
	if e, ok := ix.Files[updatesig.EngineFile]; ok {
		p.Engine = &e
	}
	if s.fleet.running() {
		p.Fleet = s.fleet.snapshot()
	}
	if err := writeJSONFile(selfUpdatePendingPath, p); err != nil {
		s.selfUpd.fail(fmt.Errorf("could not prepare the update: %w", err))
		return
	}
	enginePresent, _ := goLibrespotStamp()
	s.logger.Info("self-update: installing", "from", from, "to", ix.Version, "bytes", agent.Size)
	s.selfUpd.set("downloading", "agent")
	code, body, err := s.installDownloaded(base+"download/"+ix.Version+"/"+updatesig.AgentFile, agent,
		s.installAgentFromDownload)
	// A download the network cut off is tried again once it is back
	// (selfupdate_netretry.go). The phase tells the phone it is waiting.
	for attempt := 0; err != nil && isNetworkError(err) && attempt < len(selfUpdateNetRetry); attempt++ {
		s.logger.Warn("self-update: the download lost the network, waiting to try again",
			"attempt", attempt+1, "waitSec", int(selfUpdateNetRetry[attempt]/time.Second), "err", err)
		s.selfUpd.set("downloading", "network")
		time.Sleep(selfUpdateNetRetry[attempt])
		s.selfUpd.set("downloading", "agent")
		code, body, err = s.installDownloaded(base+"download/"+ix.Version+"/"+updatesig.AgentFile, agent,
			s.installAgentFromDownload)
	}
	if err != nil && isNetworkError(err) {
		err = fmt.Errorf("the speaker could not reach the internet: %w", err)
	}
	if err != nil || code != http.StatusOK {
		_ = os.Remove(selfUpdatePendingPath)
		if err == nil {
			err = fmt.Errorf("the speaker refused the update (HTTP %d): %s", code, strings.TrimSpace(body))
		}
		s.logger.Warn("self-update: agent install failed", "err", err)
		// The update handler makes room BEFORE the body arrives, and on a
		// tight NAND that means deleting the Spotify engine. A download that
		// then fails (network, or a hash that does not match) leaves the
		// speaker on its old agent but without Spotify. Measured on an ST10,
		// 2026-10-14. Put the engine back from the release just verified:
		// the desktop app pushes its engine to any agent version as well.
		if now, _ := goLibrespotStamp(); enginePresent && !now && p.Engine != nil {
			if eerr := s.ensureEngine(base, ix.Version, *p.Engine); eerr != nil {
				s.logger.Warn("self-update: could not restore the Spotify engine after the failed update", "err", eerr)
				err = fmt.Errorf("%w (and the Spotify engine could not be restored: %w)", err, eerr)
			} else {
				s.logger.Info("self-update: Spotify engine restored after the failed update")
			}
		}
		s.selfUpd.fail(err)
		writeSelfUpdateResult(selfUpdateResult{Version: ix.Version, From: from, Error: err.Error(), At: time.Now().UTC()})
		return
	}
	s.logger.Info("self-update: new agent written, the speaker restarts now", "version", ix.Version)
	s.selfUpd.set("rebooting", "")
}

// resumeSelfUpdate finishes an update after its reboot. Called once at start;
// a no-op unless the update left its marker.
func (s *Server) resumeSelfUpdate() {
	b, err := os.ReadFile(selfUpdatePendingPath)
	if err != nil {
		return
	}
	var p selfUpdatePending
	if json.Unmarshal(b, &p) != nil || p.Version == "" {
		_ = os.Remove(selfUpdatePendingPath)
		return
	}
	now := agentVersion()
	if updatesig.VersionLess(now, p.Version) || updatesig.VersionLess(p.Version, now) {
		// The reboot came back on another version: the new binary did not
		// take (a stick sync reverting it, a failed swap). Say so plainly.
		_ = os.Remove(selfUpdatePendingPath)
		msg := fmt.Sprintf("the speaker restarted on %s instead of %s", now, p.Version)
		s.logger.Warn("self-update: " + msg)
		s.selfUpd.fail(errors.New(msg))
		s.fleetFromPending(p, msg)
		writeSelfUpdateResult(selfUpdateResult{Version: p.Version, From: p.From, Error: msg, At: time.Now().UTC()})
		return
	}
	s.fleetFromPending(p, "")
	if !s.selfUpd.claim() {
		return
	}
	go func() {
		defer s.selfUpd.release()
		var err error
		if present, sha := goLibrespotStamp(); p.Engine != nil && present && sha == p.Engine.SHA256 {
			p.Engine = nil // the update kept the engine and it is the release's
		}
		if p.Engine != nil {
			for i, wait := range selfUpdateEngineRetry {
				s.selfUpd.set("engine", "engine")
				time.Sleep(wait)
				if err = s.ensureEngine(p.Base, p.Version, *p.Engine); err == nil {
					break
				}
				s.logger.Warn("self-update: engine fetch after the update failed", "attempt", i+1, "err", err)
			}
		}
		_ = os.Remove(selfUpdatePendingPath)
		res := selfUpdateResult{Version: p.Version, From: p.From, OK: err == nil, At: time.Now().UTC()}
		if err != nil {
			res.Error = "updated, but the Spotify engine could not be fetched: " + err.Error()
			s.selfUpd.fail(errors.New(res.Error))
		} else {
			s.selfUpd.set("idle", "")
			s.logger.Info("self-update: complete", "from", p.From, "to", p.Version)
		}
		writeSelfUpdateResult(res)
	}()
}

// runEngineFetch puts back the Spotify engine of the running release, on the
// owner's request (a speaker whose engine went missing, from any update path).
func (s *Server) runEngineFetch() {
	defer s.selfUpd.release()
	ver := agentVersion()
	base, keys := selfUpdateSourceNow()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	ix, err := s.fetchSignedIndex(ctx, base, base+"download/"+ver+"/", keys)
	cancel()
	if err != nil {
		s.selfUpd.fail(fmt.Errorf("could not read the release %s: %w", ver, err))
		return
	}
	if ix.Version != ver {
		s.selfUpd.fail(fmt.Errorf("release %s lists itself as %s", ver, ix.Version))
		return
	}
	e, ok := ix.Files[updatesig.EngineFile]
	if !ok {
		s.selfUpd.fail(fmt.Errorf("release %s carries no Spotify engine", ver))
		return
	}
	if err := s.ensureEngine(base, ver, e); err != nil {
		s.selfUpd.fail(err)
		return
	}
	s.selfUpd.set("idle", "")
}

// ensureEngine installs the engine with the given hash unless it is in place.
func (s *Server) ensureEngine(base, version string, want updatesig.File) error {
	if present, sha := goLibrespotStamp(); present && sha == want.SHA256 {
		return nil
	}
	if updateInFlight.Load() {
		return errors.New("an update is installing on this speaker")
	}
	s.selfUpd.set("engine", "engine")
	code, body, err := s.installDownloaded(base+"download/"+version+"/"+updatesig.EngineFile, want,
		func(up upload) *uploadRefusal {
			_, ref := s.installEngineUpload(up)
			return ref
		})
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("the speaker refused the engine (HTTP %d): %s", code, strings.TrimSpace(body))
	}
	s.logger.Info("self-update: Spotify engine installed", "version", version, "bytes", want.Size)
	return nil
}

// installDownloaded streams url into install as an upload of exactly want.Size
// bytes, verified against want.SHA256 on the way (heldBackBody). It reports
// what the HTTP endpoint would have answered, status and body text (200 and
// "" when install took the upload), plus the download error when the bytes
// never arrived whole.
func (s *Server) installDownloaded(url string, want updatesig.File, install func(upload) *uploadRefusal) (int, string, error) {
	base, _ := selfUpdateSourceNow()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := s.selfUpdateClient(base).Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("download %s: HTTP %d", lastPathPart(url), resp.StatusCode)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != want.Size {
		return 0, "", fmt.Errorf("download %s: %d bytes offered, the release says %d", lastPathPart(url), resp.ContentLength, want.Size)
	}
	var stalled atomic.Bool
	stall := time.AfterFunc(selfUpdateStallTimeout, func() { stalled.Store(true); cancel() })
	defer stall.Stop()
	body, err := newHeldBackBody(resp.Body, want, func(n int64) {
		stall.Reset(selfUpdateStallTimeout)
		s.selfUpd.progress(n, want.Size)
	})
	if err != nil {
		return 0, "", err
	}
	code, text := http.StatusOK, ""
	if ref := install(upload{Body: body, Size: want.Size, Remote: "127.0.0.1:0"}); ref != nil {
		code, text = ref.Status, ref.bodyText()
	}
	if body.err != nil {
		if stalled.Load() {
			// Not "context canceled": the network went away mid-download.
			return code, text, fmt.Errorf("%w (after %d of %d bytes)", errDownloadStalled, body.n, want.Size)
		}
		return code, text, body.err
	}
	return code, text, nil
}

// heldBackBody passes a download through but withholds its final byte until
// the whole download has arrived with the signed size and hash. A handler
// reading it therefore never sees a complete body unless the content is the
// signed one: on a mismatch the read fails one byte short, and the handlers
// discard a short upload before anything is renamed into place.
type heldBackBody struct {
	src        io.Reader
	want       []byte
	size       int64
	h          hash.Hash
	n          int64 // bytes handed to the reader
	finished   bool
	err        error
	onProgress func(int64)
}

func newHeldBackBody(src io.Reader, want updatesig.File, onProgress func(int64)) (*heldBackBody, error) {
	sum, err := hex.DecodeString(want.SHA256)
	if err != nil || len(sum) != sha256.Size || want.Size < 2 {
		return nil, errors.New("release index lists no usable hash")
	}
	return &heldBackBody{src: src, want: sum, size: want.Size, h: sha256.New(), onProgress: onProgress}, nil
}

func (b *heldBackBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if b.finished {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if free := b.size - 1 - b.n; free > 0 {
		if int64(len(p)) > free {
			p = p[:free]
		}
		n, err := b.src.Read(p)
		b.h.Write(p[:n])
		b.n += int64(n)
		if n > 0 && b.onProgress != nil {
			b.onProgress(b.n)
		}
		if err == io.EOF {
			return n, b.setErr(fmt.Errorf("download ended after %d of %d bytes", b.n, b.size))
		}
		if err != nil {
			return n, b.setErr(fmt.Errorf("download: %w", err))
		}
		return n, nil
	}
	// The final byte: read it, make sure nothing follows, check the hash.
	var last [1]byte
	if _, err := io.ReadFull(b.src, last[:]); err != nil {
		return 0, b.setErr(fmt.Errorf("download ended after %d of %d bytes", b.n, b.size))
	}
	b.h.Write(last[:])
	var extra [1]byte
	if n, _ := io.ReadFull(b.src, extra[:]); n > 0 {
		return 0, b.setErr(fmt.Errorf("download is larger than the %d bytes the release lists", b.size))
	}
	if got := b.h.Sum(nil); !equalBytes(got, b.want) {
		return 0, b.setErr(fmt.Errorf("download does not match the signed release (sha256 %s)", hex.EncodeToString(got)))
	}
	p[0] = last[0]
	b.n++
	b.finished = true
	if b.onProgress != nil {
		b.onProgress(b.n)
	}
	return 1, nil
}

func (b *heldBackBody) setErr(err error) error {
	b.err = err
	return err
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var d byte
	for i := range a {
		d |= a[i] ^ b[i]
	}
	return d == 0
}

func writeJSONFile(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func writeSelfUpdateResult(r selfUpdateResult) {
	_ = writeJSONFile(selfUpdateResultPath, r)
}
