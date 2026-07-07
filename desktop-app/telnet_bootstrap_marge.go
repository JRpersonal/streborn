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
	hits atomic.Int64 // box requests served; diagnostic only, hence atomic
}

// startBootstrapMarge binds 0.0.0.0:bootstrapMargePort and serves the marge
// endpoints. It returns the running responder (Stop it when done) or an error if
// the port cannot be bound (e.g. already in use).
func startBootstrapMarge() (*bootstrapMarge, error) {
	b := &bootstrapMarge{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", b.handle)
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", bootstrapMargePort))
	if err != nil {
		return nil, fmt.Errorf("bind marge responder: %w", err)
	}
	b.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = b.srv.Serve(ln) }()
	return b, nil
}

// handle answers the box's device-check requests. Responses mirror
// internal/marge/marge.go (adddeviceresponse "elem" form). The box only needs a
// well-formed reply so its check cycle keeps running; the exact token is
// irrelevant to the SSH unlock.
func (b *bootstrapMarge) handle(w http.ResponseWriter, r *http.Request) {
	b.hits.Add(1)
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	switch {
	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/device"):
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>` +
			"\n<adddeviceresponse><margetoken>str-bootstrap</margetoken></adddeviceresponse>"))
	case strings.Contains(r.URL.Path, "/sourceproviders"):
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>` + "\n<sourceproviders></sourceproviders>"))
	default:
		_, _ = w.Write([]byte(`<response status="OK"></response>`))
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
	fmt.Fprintf(&log, "PC marge address for the box: http://%s:%d\n", pcIP, bootstrapMargePort)

	marge, err := startBootstrapMarge()
	if err != nil {
		a.logger.Warn("telnet-bootstrap: could not start the local marge responder (port in use?)", "host", host, "err", err)
		return false, "could not start local marge responder: " + err.Error()
	}
	defer marge.Stop()
	// Surface the box->PC marge callbacks to the install heartbeat so the UI can
	// warn about a firewall block (0 callbacks after ~60s) instead of a silent
	// multi-minute wait. Cleared when the bootstrap returns.
	a.installMargeHits = func() int64 { return marge.hits.Load() }
	defer func() { a.installMargeHits = nil }()

	t, err := dialTAP(host, 5*time.Second)
	if err != nil {
		return false, "port 17000 not reachable: " + err.Error()
	}

	// Give the box an account so it begins doing marge checks. Done after the TAP
	// dial succeeds so we do not set a dummy account on a box we then cannot reach
	// over :17000 to inject or recover.
	a.setMargeAccountDummy(host)

	// From here the box config carries this PC's live IP; every failure exit must
	// restore stock URLs and reboot so the box is not left dialing a dead host.
	base := fmt.Sprintf("http://%s:%d/", pcIP, bootstrapMargePort)
	for _, cmd := range buildBootstrapEnableSSHCommands(base) {
		if serr := sendAndLog(t, &log, cmd); serr != nil {
			t.close()
			// The caller restores stock URLs + reboots on any failure exit (it
			// owns cleanup so the simple path is covered too), so a partial write
			// here is not left pointed at this PC.
			return false, log.String()
		}
	}
	_, _ = t.send("sys reboot")
	t.close()
	a.logger.Info("telnet-bootstrap: dummy account set, envswitch injection written, sys reboot sent; waiting for the box to check marge and open SSH", "host", host, "pcMarge", base)

	// Budget: boot (~90s) plus at least one ~60s marge-check cycle. Give it the
	// slow-model budget plus a margin so a genuine success is never cut off.
	budget := agentWaitBudget(model) + 90*time.Second
	if a.waitForSSHOpen(host, budget) {
		a.logger.Info("telnet-bootstrap: SSH opened on the factory-reset box via the local-marge unlock", "host", host, "margeHits", marge.hits.Load())
		return true, log.String()
	}
	a.logger.Info("telnet-bootstrap: SSH did not open within budget; caller will restore stock URLs so the box stops dialing this PC", "host", host, "budget", budget.String(), "margeHits", marge.hits.Load())
	return false, log.String()
}
