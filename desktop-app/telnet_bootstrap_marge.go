// Factory-reset stick-free SSH bootstrap.
//
// The plain enableSSHViaTelnet path (telnet_enable_ssh.go) opens SSH on a
// speaker that still does marge checks on its own - i.e. one that kept a Bose
// margeAccountUUID from before the cloud shutdown. A FACTORY-RESET box has an
// empty UUID and never checks marge, so the injected payload is written but
// never fires. This file handles that case, proven live on a factory-reset
// SoundTouch 300 (ginger):
//
//   1. POST /setMargeAccount with a dummy account -> the box sets
//      margeAccountUUID and starts doing marge checks (the client POST times out
//      but the box still stores the account).
//   2. Run a throwaway plain-HTTP marge responder on this PC (no TLS needed) so
//      the box's ~60s check cycle actually completes against something.
//   3. Inject the remote_services/sshd payload via `envswitch boseurls set`
//      pointing the base at THIS PC's marge responder. On ginger/taigan the
//      running marge client loads the envswitch layer at boot (not the
//      sys-configuration layer), so envswitch is what actually takes effect.
//   4. sys reboot -> the running client loads the injected URL.
//   5. The box hits the responder every ~60s and shells out the payload ->
//      sshd starts -> :22 opens. Then reset boseurls to stock and install over
//      SSH.
//
// Because this injection embeds THIS PC's live LAN IP into the box's persistent
// config, a failed attempt must not leave it there: restoreStockBoseURLsAndReboot
// runs on every failure exit once the injection has been written, so the box is
// never left marge-checking a dead/reassignable local host.
//
// Credit: builds on the gesellix/Bose-SoundTouch #471/#519 community finding;
// the factory-reset trigger (dummy account + local marge + envswitch layer) was
// worked out on STR's own hardware.

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/sticksetup"
)

// bootstrapMargePort is the LAN port the throwaway marge responder binds during
// a factory-reset unlock. High and fixed so a firewall allow-rule (the desktop
// app already needs LAN access) stays stable across installs.
const bootstrapMargePort = 19080

// bootstrapMarge is a throwaway plain-HTTP marge responder. It answers the few
// streaming.bose.com endpoints a stock box hits during a device check so the
// box's ~60s marge cycle completes and the envswitch shell injection fires.
type bootstrapMarge struct {
	srv  *http.Server
	port int          // the port actually bound (bootstrapMargePort or a fallback)
	hits atomic.Int64 // box requests served; diagnostic only, hence atomic
}

// startBootstrapMarge binds the marge responder and serves the marge endpoints.
// It prefers bootstrapMargePort but falls back to the next few ports when that
// one is already bound - a stale responder from a prior attempt, a second app
// instance, or an unrelated process. A bind failure must NOT abort the unlock:
// it used to surface as misleading "prepare a USB stick" guidance for what is a
// purely local port conflict. Returns the running responder (Stop it when done;
// read .port for the port the box must call back on) or an error only if the
// whole range is taken.
func startBootstrapMarge() (*bootstrapMarge, error) {
	b := &bootstrapMarge{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", b.handle)
	var ln net.Listener
	var err error
	for p := bootstrapMargePort; p <= bootstrapMargePort+5; p++ {
		ln, err = net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p))
		if err == nil {
			b.port = p
			break
		}
	}
	if ln == nil {
		return nil, fmt.Errorf("bind marge responder on %d-%d: %w", bootstrapMargePort, bootstrapMargePort+5, err)
	}
	b.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = b.srv.Serve(ln) }()
	return b, nil
}

// handle answers the box's marge handshake so a fresh box actually reaches
// MargeStateAssociated and PERSISTS the dummy account (the whole point - a
// persisted account is what makes the box run the boot marge check that fires
// the SSH injection). Mirrors the full internal/marge/marge.go handshake, not a
// minimal stub: the earlier minimal responder (plain "elem"/HTTP 200 addDevice,
// no power_on) left the taigan account EMPTY, so the injection never fired.
func (b *bootstrapMarge) handle(w http.ResponseWriter, r *http.Request) {
	b.hits.Add(1)
	// Re-anchor at /streaming/ so an injected or trailing-slash base still routes.
	p := r.URL.Path
	if i := strings.Index(p, "/streaming/"); i >= 0 {
		p = p[i:]
	}
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	switch {
	case strings.Contains(p, "/streaming/support/power_on"):
		// Boot diagnostics: the box needs OK + a server-time or it marks the cloud
		// as down and will not associate.
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
			`<response status="OK"><server-time>` + time.Now().UTC().Format("2006-01-02T15:04:05Z") + `</server-time></response>`))
	case strings.Contains(p, "/streaming/account/") && strings.Contains(p, "/device") && r.Method == http.MethodPost:
		// wrap201: HTTP 201 + a wrapped, UUID-form margetoken is what drives the
		// box to MargeStateAssociated so the dummy account PERSISTS (a plain
		// elem/200 reply left the taigan margeAccountUUID empty). Mirrors
		// internal/marge respondAddDevice wrap201.
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>` + "\n" +
			`<response status="OK"><adddeviceresponse><margetoken>11111111-1111-1111-1111-111111111111</margetoken></adddeviceresponse></response>`))
	case strings.Contains(p, "/full"):
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>` + "\n<fullAccount></fullAccount>"))
	case strings.Contains(p, "sourceproviders"):
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>` + "\n" +
			`<sourceProviders><sourceprovider id="TUNEIN"><name>TuneIn Radio</name></sourceprovider><sourceprovider id="INTERNET_RADIO"><name>Internet Radio</name></sourceprovider></sourceProviders>`))
	default:
		_, _ = w.Write([]byte(`<response status="OK"/>`))
	}
}

// Stop shuts the responder down. Best-effort.
func (b *bootstrapMarge) Stop() {
	if b == nil || b.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = b.srv.Shutdown(ctx)
}

// localIPForBox returns this PC's LAN IP on the route to the box, by opening a
// short TCP connection to the box's Bose port and reading the local address.
// This is the address the box must dial back for the marge check, and it must be
// the CURRENT one - a stale/guessed IP (e.g. from a DHCP change) makes the box
// dial a dead host and the unlock silently never fires.
func localIPForBox(host string) (string, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, "8090"), 4*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if ta, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		return ta.IP.String(), nil
	}
	return "", fmt.Errorf("could not determine local IP for %s", host)
}

// buildBootstrapEnableSSHCommands returns the :17000 command list that writes the
// enable-SSH injection pointing at a LIVE local marge base (unlike
// buildEnableSSHCommands, which uses the dead .invalid placeholder). The
// injection rides both the envswitch layer (what ginger/taigan load at boot) and
// the sys-configuration margeServerUrl. Pure so it can be unit-tested. base must
// end in "/" so the trailing "/;" keeps the http base reachable while the shell
// metacharacters run the payload.
func buildBootstrapEnableSSHCommands(base string) []string {
	inj := base + remoteServicesInjection
	upd := base + "update"
	// ORDER MATTERS: sys configuration FIRST, envswitch LAST.
	//
	// envswitch commits the runtime layer as it stands when it runs, so a
	// sys configuration write issued AFTER it does not survive the reboot. We
	// had it the other way round, which meant the second write was decorative:
	// the fallback we believed we had for chassis we cannot measure was never
	// actually there.
	//
	// Measured by @bitranox on a SoundTouch 20 (variant spotty, FW 27.0.6) and
	// reported in gesellix/Bose-SoundTouch#471. The method is why this is
	// trusted: an already-migrated box was POISONED first, all four URLs set to
	// an unreachable address and confirmed to survive a reboot of their own, so
	// a pass could not be inherited from existing config. With this order all
	// four URLs survived; with envswitch first they did not.
	//
	// It also matters beyond the fallback: bmxRegistryUrl and statsServerUrl
	// have no envswitch form at all, so sys configuration is the only way to
	// set them, and on the old order it never persisted.
	return []string{
		`sys configuration margeServerUrl "` + inj + `"`,
		`envswitch boseurls set "` + inj + `" "` + upd + `"`,
	}
}

// dummyPairXML is the minimal PairDeviceWithAccount body that makes a stock box
// set a margeAccountUUID (so it begins doing marge checks). The account is a
// throwaway used only to trigger the check cycle. It is not cleared explicitly;
// instead, restoring stock boseurls and rebooting the box (both the success
// install-reboot via RepairInstallViaSSH and the failure-path
// restoreStockBoseURLsAndReboot) makes the box drop the account it cannot
// re-validate against streaming.bose.com, which was confirmed live (the UUID
// went back to empty after a stock-URL reboot). STR then pairs with its own
// account on the installed box.
const dummyPairXML = `<?xml version="1.0" encoding="UTF-8" ?>` +
	`<PairDeviceWithAccount><accountId>str-bootstrap</accountId>` +
	`<userAuthToken>str-bootstrap</userAuthToken>` +
	`<accountEmail>bootstrap@str.local</accountEmail></PairDeviceWithAccount>`

// setMargeAccountDummy POSTs the dummy account to the box so it starts checking
// marge. The box's own HTTP response usually times out (it tries to reach its
// then-current marge URL during association), but it still stores the account -
// so a timeout here is treated as success, not failure.
//
// It returns a one-line account of what the box answered, for the unlock log.
// A box that answers at once usually REFUSED the account rather than storing it:
// a factory-fresh Wave (2026-11-17 bundle) answered without ever calling the
// local responder and its UUID never appeared, and nothing recorded what it
// said, so the answer is logged with its status and the start of its body.
//
// refused is true only when the box ANSWERED with something other than 200 (the
// Wave of the 2026-11-18 bundle: HTTP 500, SET_MARGE_ACCOUNT_ERROR 1038, within
// a second). A timeout is not a refusal.
func (a *App) setMargeAccountDummy(host string) (line string, refused bool) {
	client := &http.Client{Timeout: 12 * time.Second}
	url := fmt.Sprintf("http://%s:8090/setMargeAccount", host)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(dummyPairXML))
	if err != nil {
		return "setMargeAccount: request not built: " + err.Error(), false
	}
	req.Header.Set("Content-Type", "application/xml")
	start := time.Now()
	resp, err := client.Do(req)
	took := time.Since(start).Round(100 * time.Millisecond)
	if err != nil {
		// A timeout is the common, expected outcome and still sets the UUID.
		a.logger.Info("telnet-bootstrap: setMargeAccount returned no clean response (expected; the box still stores the account)", "host", host, "err", err, "took", took)
		return fmt.Sprintf("setMargeAccount: no clean response after %v (expected): %v", took, err), false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 600))
	snippet := strings.Join(strings.Fields(string(body)), " ")
	a.logger.Info("telnet-bootstrap: setMargeAccount answered", "host", host, "status", resp.StatusCode, "took", took, "body", snippet)
	return fmt.Sprintf("setMargeAccount: HTTP %d after %v: %s", resp.StatusCode, took, snippet), resp.StatusCode != http.StatusOK
}

// boxPoster POSTs an XML body to one path of the speaker's :8090 API and returns
// the HTTP status and the start of the answer. A seam so the setup bracket below
// is testable without a speaker.
type boxPoster func(path, body string) (status int, answer string, err error)

// boxPosterFor is the real boxPoster for host.
func boxPosterFor(host string) boxPoster {
	client := &http.Client{Timeout: 12 * time.Second}
	return func(path, body string) (int, string, error) {
		req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s:8090%s", host, path), strings.NewReader(body))
		if err != nil {
			return 0, "", err
		}
		req.Header.Set("Content-Type", "application/xml")
		resp, err := client.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 600))
		return resp.StatusCode, strings.Join(strings.Fields(string(b)), " "), nil
	}
}

// setupBracketStates is the setup state machine the Bose app walked BEFORE it
// sent setMargeAccount to a speaker that had never been set up (order from the
// documented app capture: gesellix/Bose-SoundTouch, DEVICE-PAIRING-FLOW). The
// language step of that capture is not repeated here, the install sets the
// language before it ever reaches the unlock.
var setupBracketStates = []string{
	`<setupState state="SETUP_START"/>`,
	`<setupState state="SETUP_IDENTIFY_DEVICE_ENTER" timeout="300000"/>`,
	`<setupState state="SETUP_ENTER"/>`,
	`<setupState state="SETUP_IDENTIFY_DEVICE_LEAVE"/>`,
}

// repeatLanguageIfSetupSaysUnset sets the speaker's language again when its own
// /setup answer says SETUP_LANG_NOT_SET at the moment it refuses the account.
// The install sets the language at its start, but the plain unlock that runs
// before this bootstrap reboots the speaker, and a never-set-up Wave came back
// from that reboot with the language unset again and refused the account with
// 1038, with and without the setup bracket (2026-11-18, v1.8.0). The language
// is the first gate of Bose's own setup, so it is repeated here, right before
// the account is asked for again. Whether that is what lifts the refusal is NOT
// proven; the answer and the state after it go to the unlock log.
func repeatLanguageIfSetupSaysUnset(setupBody string, lang int, post boxPoster, get func(path string) string, log *strings.Builder) bool {
	if !strings.Contains(setupBody, "SETUP_LANG_NOT_SET") {
		return false
	}
	body := fmt.Sprintf("<sysLanguage>%d</sysLanguage>", lang)
	status, answer, err := post("/language", body)
	if err != nil {
		fmt.Fprintf(log, "language repeat: POST /language %s: %v\n", body, err)
		return false
	}
	fmt.Fprintf(log, "language repeat: POST /language %s: HTTP %d: %s\n", body, status, answer)
	get("/setup")
	return status == http.StatusOK
}

// pairInsideSetupBracket repeats the dummy-account request the way the Bose app
// sent it: inside the setup state machine. It is the second attempt for a
// speaker that refused the bare request (a never-set-up Wave answered it with
// SET_MARGE_ACCOUNT_ERROR 1038, 2026-11-18). Whether the bracket is what that
// firmware wants is NOT proven, no such speaker was on the bench; every answer
// is therefore written to the unlock log so the next bundle decides it.
//
// SETUP_LEAVE is sent whenever SETUP_START went out, also after a failure, so
// the speaker is never left inside its setup state. Returns whether the speaker
// accepted the account (HTTP 200).
func pairInsideSetupBracket(post boxPoster, log *strings.Builder) bool {
	step := func(path, body string) (int, bool) {
		status, answer, err := post(path, body)
		if err != nil {
			fmt.Fprintf(log, "setup bracket: POST %s %s: %v\n", path, body, err)
			return 0, false
		}
		fmt.Fprintf(log, "setup bracket: POST %s %s: HTTP %d: %s\n", path, body, status, answer)
		return status, true
	}
	accepted := false
	started := false
	walked := true
	for _, st := range setupBracketStates {
		if _, ok := step("/setup", st); !ok {
			walked = false
			break
		}
		started = true
	}
	if walked {
		status, ok := step("/setMargeAccount", dummyPairXML)
		accepted = ok && status == http.StatusOK
	}
	if started {
		step("/setup", `<setupState state="SETUP_LEAVE"/>`)
	}
	return accepted
}

// runningMargeURL reads the cloud address the speaker is RUNNING with from
// /info (margeURL is loaded at the last start; the :17000 writes only show here
// after a restart on some chassis). For the unlock log only.
func (a *App) runningMargeURL(host string) string {
	body, err := a.boseGet(host, "/info")
	if err != nil {
		return "not readable: " + err.Error()
	}
	const open, close = "<margeURL>", "</margeURL>"
	i := strings.Index(body, open)
	j := strings.Index(body, close)
	if i < 0 || j < i {
		return "no margeURL element in /info"
	}
	return body[i+len(open) : j]
}

// accountRetryPlan says what to do after one account attempt that followed the
// restart onto the responder. Pure, so the decision is unit-tested without a
// speaker:
//
//   - the speaker took the account (stored a UUID, or did not refuse): continue
//     to the injection;
//   - it refused but attempts remain: ask again (the firmware refuses while it
//     is still settling after the restart);
//   - it refused and no attempts remain: stop.
func accountRetryPlan(refused, uuidStored bool, attempt, maxAttempts int) string {
	switch {
	case uuidStored || !refused:
		return "continue"
	case attempt < maxAttempts:
		return "retry"
	default:
		return "stop"
	}
}

// retryAccountAfterRestartOntoResponder is the last rung for a speaker that
// refused the dummy account at once and never called this PC (a never-set-up
// Wave: SET_MARGE_ACCOUNT_ERROR 1038 with the language set and inside the setup
// state machine, 2026-11-18 and again on v1.8.1, 2026-11-19). What the proven
// ginger/taigan runs had and this path did not: the speaker was RUNNING on a
// reachable cloud address when it was asked, while here the plain unlock had
// just restarted it onto the dead .invalid placeholder. So restart it once with
// the responder address already written, set the language again (a never-set-up
// speaker loses it on every restart), and ask for the account again, bare and
// then inside the setup state machine, a few times while the firmware settles.
//
// Whether this lifts the refusal is NOT proven; no such speaker was on the
// bench. Every answer, the running cloud address and the responder call count go
// to the unlock log so the next diagnostic says which part held. t is the open
// :17000 connection, used for the restart; the caller closes it and dials again.
// Returns whether the speaker took the account.
func (a *App) retryAccountAfterRestartOntoResponder(host, model string, t *tapConn, marge *bootstrapMarge, log *strings.Builder) bool {
	fmt.Fprintf(log, "restarting the speaker onto the local responder before asking for the account again\n")
	a.logger.Info("telnet-bootstrap: restarting the speaker onto the local responder before asking for the account again", "host", host)
	sendTAPReboot(t, host)
	boxSSHClients.invalidateHost(host)
	// Down first (the restart takes a few seconds to drop :8090), then up.
	for i := 0; i < 20 && tcpReachable(host, 8090, 2*time.Second); i++ {
		a.emitInstallHeartbeat()
		time.Sleep(2 * time.Second)
	}
	back := false
	deadline := time.Now().Add(agentWaitBudget(model))
	for time.Now().Before(deadline) {
		if _, err := a.boseGet(host, "/info"); err == nil {
			back = true
			break
		}
		a.emitInstallHeartbeat()
		time.Sleep(4 * time.Second)
	}
	if !back {
		fmt.Fprintf(log, "the speaker did not answer on :8090 again after the restart\n")
		a.logger.Warn("telnet-bootstrap: the speaker did not answer on :8090 again after the restart onto the responder", "host", host)
		return false
	}
	fmt.Fprintf(log, "back after the restart: running margeURL %s, responder calls so far %d\n", a.runningMargeURL(host), marge.hits.Load())

	get := func(path string) string {
		body, gerr := a.boseGet(host, path)
		if gerr != nil {
			body = "not readable: " + gerr.Error()
		}
		body = strings.Join(strings.Fields(body), " ")
		fmt.Fprintf(log, "GET %s: %s\n", path, body)
		return body
	}
	post := boxPosterFor(host)
	lang := sticksetup.SysLanguageForLocale(a.appLocale())
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		repeatLanguageIfSetupSaysUnset(get("/setup"), lang, post, get, log)
		line, refused := a.setMargeAccountDummy(host)
		fmt.Fprintf(log, "after restart, attempt %d: %s\n", attempt, line)
		if refused {
			refused = !pairInsideSetupBracket(post, log)
		}
		stored := false
		for w := 0; w < 10; w++ {
			if a.boxHasStoredMargeUUID(host) {
				stored = true
				break
			}
			a.emitInstallHeartbeat()
			time.Sleep(2 * time.Second)
		}
		hits := marge.hits.Load()
		plan := accountRetryPlan(refused, stored, attempt, maxAttempts)
		fmt.Fprintf(log, "after restart, attempt %d: refused=%v uuidStored=%v margeHits=%d -> %s\n", attempt, refused, stored, hits, plan)
		a.logger.Info("telnet-bootstrap: account request after the restart onto the responder", "host", host, "attempt", attempt, "refused", refused, "uuidStored", stored, "margeHits", hits, "plan", plan)
		switch plan {
		case "continue":
			return true
		case "stop":
			return false
		}
	}
	return false
}

// boxHasStoredMargeUUID positively reads a non-empty margeAccountUUID from
// /info. Unlike boxHasResidualMargeUUID it is false on a read error, so a
// speaker that does not answer is never taken for one that stored the account.
func (a *App) boxHasStoredMargeUUID(host string) bool {
	body, err := a.boseGet(host, "/info")
	if err != nil {
		return false
	}
	v, found := margeUUIDFromInfo(body)
	return found && v != ""
}

// enableSSHViaTelnetBootstrap is the factory-reset unlock: it gives the box a
// dummy account, points its marge at a throwaway responder on this PC, injects
// the sshd payload via envswitch, reboots, and waits for :22. Returns whether
// SSH opened, a command log, and the kind of failure when one can be named:
// errBootstrapResponderUnreachable when the box came back on the network but
// never called this PC's responder (on Windows almost always the firewall
// blocking the inbound to :19080), errBootstrapAccountRefused when the speaker
// turned the account down itself before and after a restart onto the responder
// (a speaker reset and never set up again), nil otherwise. The caller uses it to
// give the matching advice.
//
// Cleanup contract: once the injection is written it embeds this PC's live LAN
// IP in the box's persistent config, so ANY failure exit after that point calls
// restoreStockBoseURLsAndReboot before returning, leaving the box back on stock
// URLs. On success the caller resets boseurls to stock (so STR's own marge
// interception takes over) before installing.
func (a *App) enableSSHViaTelnetBootstrap(host, model string) (bool, string, error) {
	var log strings.Builder

	pcIP, err := localIPForBox(host)
	if err != nil {
		a.logger.Info("telnet-bootstrap: cannot determine this PC's LAN IP for the box; skipping factory-reset unlock", "host", host, "err", err)
		return false, "could not determine local IP: " + err.Error(), nil
	}
	marge, err := startBootstrapMarge()
	if err != nil {
		a.logger.Warn("telnet-bootstrap: could not start the local marge responder on any candidate port; close any other STR instance or program using 19080-19085 and retry", "host", host, "err", err)
		return false, "could not start the local marge responder: a port in the 19080-19085 range is in use by another program. Close any other running copy of this app and try again.", nil
	}
	defer marge.Stop()
	fmt.Fprintf(&log, "PC marge address for the box: http://%s:%d\n", pcIP, marge.port)
	// Surface the box->PC marge callbacks to the install heartbeat so the UI can
	// warn about a firewall block (0 callbacks after ~60s) instead of a silent
	// multi-minute wait. Cleared when the bootstrap returns.
	a.installMargeHits = func() int64 { return marge.hits.Load() }
	defer func() { a.installMargeHits = nil }()

	t, err := dialTAP(host, 5*time.Second)
	if err != nil {
		return false, "port 17000 not reachable: " + err.Error(), nil
	}

	// Winning-sequence ORDER (this ordering bug is what failed the taigan unlock).
	// FIRST point marge at the CLEAN local responder (no injection), and only THEN
	// post the dummy account, so the box validates the account against a reachable,
	// well-formed marge server and its margeAccountUUID PERSISTS. Posting the
	// account while marge still points at dead streaming.bose.com means the box
	// cannot validate it and silently drops it, so its boot marge check never runs
	// and the injection never fires (the UUID came back empty after the reboot on
	// the taigan). base is this PC's live IP:port; every failure exit restores
	// stock URLs and reboots so the box is not left dialing a dead host.
	base := fmt.Sprintf("http://%s:%d/", pcIP, marge.port)
	for _, cmd := range []string{
		`envswitch boseurls set "` + base + `" "` + base + `update"`,
		`sys configuration margeServerUrl "` + base + `"`,
	} {
		if serr := sendAndLog(t, &log, cmd); serr != nil {
			t.close()
			return false, log.String(), nil
		}
	}
	pairLine, accountRefused := a.setMargeAccountDummy(host)
	fmt.Fprintf(&log, "%s\n", pairLine)
	if accountRefused {
		// The speaker said no instead of timing out. Record what it thinks its
		// own setup state is, then ask again the way the Bose app did.
		get := func(path string) string {
			body, gerr := a.boseGet(host, path)
			if gerr != nil {
				body = "not readable: " + gerr.Error()
			}
			body = strings.Join(strings.Fields(body), " ")
			fmt.Fprintf(&log, "GET %s: %s\n", path, body)
			return body
		}
		get("/soundTouchConfigurationStatus")
		post := boxPosterFor(host)
		langRepeated := repeatLanguageIfSetupSaysUnset(get("/setup"), sticksetup.SysLanguageForLocale(a.appLocale()), post, get, &log)
		accountRefused = !pairInsideSetupBracket(post, &log)
		a.logger.Info("telnet-bootstrap: language state before the second account request", "host", host, "languageRepeated", langRepeated)
		a.logger.Info("telnet-bootstrap: the speaker refused the bare dummy account; repeated it inside the setup state machine", "host", host, "acceptedNow", !accountRefused)
	}
	// Wait for the association to actually PERSIST the account before injecting.
	// The box POSTs to the responder over a few seconds (device -> full ->
	// provider_settings) and only then stores its margeAccountUUID; checking
	// immediately reads false (too early - live taigon 2026-07-08 read false while
	// the box reached the responder ~30s later). Poll until the UUID is set (or a
	// ~40s cap) so the account is confirmed stuck before we swap marge to the
	// injection - only a persisted account makes the box run the boot marge check
	// that fires the injection.
	uuidStuck := false
	for w := 0; w < 20; w++ {
		if a.boxHasResidualMargeUUID(host) {
			uuidStuck = true
			break
		}
		a.emitInstallHeartbeat()
		time.Sleep(2 * time.Second)
	}
	fmt.Fprintf(&log, "dummy account persisted against local responder: %v (margeHits=%d)\n", uuidStuck, marge.hits.Load())
	a.logger.Info("telnet-bootstrap: dummy account association result", "host", host, "uuidPersisted", uuidStuck, "margeHits", marge.hits.Load())
	if accountRefused && !uuidStuck && marge.hits.Load() == 0 {
		// Refused at once, nothing stored, never called this PC. The speaker was
		// last started on the dead .invalid placeholder the plain unlock wrote, so
		// the responder address the commands above set is not the one it is running
		// on yet. Restart it once onto the responder, then ask again.
		fmt.Fprintf(&log, "running margeURL at the refusal: %s\n", a.runningMargeURL(host))
		accepted := a.retryAccountAfterRestartOntoResponder(host, model, t, marge, &log)
		t.close()
		if !accepted {
			// A speaker without an account does not run the boot check the injection
			// rides on, so the injection reboot and the four-minute wait cannot open
			// SSH. Stop here; the caller restores the stock URLs.
			fmt.Fprintf(&log, "the speaker refused the dummy account before and after a restart onto the local responder; unlock stopped (margeHits=%d)\n", marge.hits.Load())
			a.logger.Warn("telnet-bootstrap: the speaker refused the dummy account before and after a restart onto the local responder and stored none; stopping", "host", host, "margeHits", marge.hits.Load())
			return false, log.String(), errBootstrapAccountRefused
		}
		// The account took after the restart. Re-open :17000 for the injection.
		var derr error
		t, derr = dialTAP(host, 5*time.Second)
		if derr != nil {
			return false, log.String() + "port 17000 not reachable after the restart: " + derr.Error() + "\n", nil
		}
	}

	// Now swap marge to the INJECTION (same reachable base, so the boot curl
	// connects to the responder as GET / and then runs the ;touch;sshd payload).
	// The injection fires on the box's next power_on -> curl margeServerUrl (a shell
	// curl), so a reboot triggers it.
	for _, cmd := range buildBootstrapEnableSSHCommands(base) {
		if serr := sendAndLog(t, &log, cmd); serr != nil {
			t.close()
			return false, log.String(), nil
		}
	}
	sendTAPReboot(t, host)
	t.close()
	boxSSHClients.invalidateHost(host)
	a.logger.Info("telnet-bootstrap: dummy account set, envswitch injection written, sys reboot sent; waiting for the box to check marge and open SSH", "host", host, "pcMarge", base)

	// Budget: boot (~90s) plus at least one ~60s marge-check cycle.
	budget := agentWaitBudget(model) + 90*time.Second
	if a.waitForSSHOpen(host, budget) {
		a.logger.Info("telnet-bootstrap: SSH opened on the factory-reset box via the local-marge unlock", "host", host, "margeHits", marge.hits.Load())
		return true, log.String(), nil
	}
	// State-aware diagnosis instead of a blind "timed out": the box callback count
	// plus its reachability say which failure this is, so the caller/UI can give the
	// right advice (firewall vs power-cycle vs "this chassis needs the USB stick").
	hits := marge.hits.Load()
	reachable := tcpReachable(host, 8090, 2*time.Second)
	// firewallSuspected: the box came back on the network but never called this
	// PC's responder, so the inbound to :19080 was dropped - on Windows almost
	// always the firewall prompt (project_bootstrap_used_box_deadend).
	firewallSuspected := hits == 0 && reachable
	a.logger.Info("telnet-bootstrap: SSH did not open", "host", host, "margeHits", hits, "boxReachableNow", reachable, "firewallSuspected", firewallSuspected, "diagnosis", bootstrapFailureReason(hits, reachable))
	if firewallSuspected {
		return false, log.String(), errBootstrapResponderUnreachable
	}
	return false, log.String(), nil
}

// bootstrapFailureReason turns the bootstrap end-state (how many times the box
// called this PC's marge responder, and whether it is reachable now) into a
// human reason, so a failed factory-reset unlock reports what actually went
// wrong instead of a blind "timed out" plus generic USB-stick guidance.
func bootstrapFailureReason(margeHits int64, reachable bool) string {
	switch {
	case margeHits == 0 && reachable:
		return "the speaker came back on the network but never called this PC: a firewall is blocking the marge responder port, or this chassis resets the stick-free injection on boot and needs the USB stick"
	case margeHits == 0 && !reachable:
		return "the speaker did not come back on the network: it may be wedged (unplug it from mains for ~10s and retry) or it needs the USB stick"
	default:
		return "the speaker reached this PC but SSH still did not open: the injection ran but sshd did not start"
	}
}
