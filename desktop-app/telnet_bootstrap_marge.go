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
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
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
	return []string{
		`envswitch boseurls set "` + inj + `" "` + upd + `"`,
		`sys configuration margeServerUrl "` + inj + `"`,
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
func (a *App) setMargeAccountDummy(host string) {
	client := &http.Client{Timeout: 12 * time.Second}
	url := fmt.Sprintf("http://%s:8090/setMargeAccount", host)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(dummyPairXML))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/xml")
	resp, err := client.Do(req)
	if err != nil {
		// A timeout is the common, expected outcome and still sets the UUID.
		a.logger.Info("telnet-bootstrap: setMargeAccount returned no clean response (expected; the box still stores the account)", "host", host, "err", err)
		return
	}
	_ = resp.Body.Close()
}

// enableSSHViaTelnetBootstrap is the factory-reset unlock: it gives the box a
// dummy account, points its marge at a throwaway responder on this PC, injects
// the sshd payload via envswitch, reboots, and waits for :22. Returns whether
// SSH opened and a command log.
//
// Cleanup contract: once the injection is written it embeds this PC's live LAN
// IP in the box's persistent config, so ANY failure exit after that point calls
// restoreStockBoseURLsAndReboot before returning, leaving the box back on stock
// URLs. On success the caller resets boseurls to stock (so STR's own marge
// interception takes over) before installing.
func (a *App) enableSSHViaTelnetBootstrap(host, model string) (bool, string) {
	var log strings.Builder

	pcIP, err := localIPForBox(host)
	if err != nil {
		a.logger.Info("telnet-bootstrap: cannot determine this PC's LAN IP for the box; skipping factory-reset unlock", "host", host, "err", err)
		return false, "could not determine local IP: " + err.Error()
	}
	marge, err := startBootstrapMarge()
	if err != nil {
		a.logger.Warn("telnet-bootstrap: could not start the local marge responder on any candidate port; close any other STR instance or program using 19080-19085 and retry", "host", host, "err", err)
		return false, "could not start the local marge responder: a port in the 19080-19085 range is in use by another program. Close any other running copy of this app and try again."
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
		return false, "port 17000 not reachable: " + err.Error()
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
			return false, log.String()
		}
	}
	a.setMargeAccountDummy(host)
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

	// Now swap marge to the INJECTION (same reachable base, so the boot curl
	// connects to the responder as GET / and then runs the ;touch;sshd payload).
	// The injection fires on the box's next power_on -> curl margeServerUrl (a shell
	// curl), so a reboot triggers it.
	for _, cmd := range buildBootstrapEnableSSHCommands(base) {
		if serr := sendAndLog(t, &log, cmd); serr != nil {
			t.close()
			return false, log.String()
		}
	}
	_, _ = t.send("sys reboot")
	t.close()
	boxSSHClients.invalidateHost(host)
	a.logger.Info("telnet-bootstrap: dummy account set, envswitch injection written, sys reboot sent; waiting for the box to check marge and open SSH", "host", host, "pcMarge", base)

	// Budget: boot (~90s) plus at least one ~60s marge-check cycle.
	budget := agentWaitBudget(model) + 90*time.Second
	if a.waitForSSHOpen(host, budget) {
		a.logger.Info("telnet-bootstrap: SSH opened on the factory-reset box via the local-marge unlock", "host", host, "margeHits", marge.hits.Load())
		return true, log.String()
	}
	// State-aware diagnosis instead of a blind "timed out": the box callback count
	// plus its reachability say which failure this is, so the caller/UI can give the
	// right advice (firewall vs power-cycle vs "this chassis needs the USB stick").
	hits := marge.hits.Load()
	reachable := tcpReachable(host, 8090, 2*time.Second)
	a.logger.Info("telnet-bootstrap: SSH did not open", "host", host, "margeHits", hits, "boxReachableNow", reachable, "diagnosis", bootstrapFailureReason(hits, reachable))
	return false, log.String()
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
