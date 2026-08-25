package main

// Post-switch network state refresh (#697).
//
// A live Wi-Fi switch (the wpaConfirmed arm in internal/webui/wlan.go, and the
// boot guard's finishCorrection) can land the box on a NEW address, and until
// v0.9.57 nothing derived from the old one was ever told: the mDNS host
// responder and the service announcer kept answering with the boot address,
// the peer roster then re-adopted that stale self-announcement as a peer and
// dialed the dead address every sweep, and the per-IP REDIRECT rules from
// run.sh piled up for both addresses. Only a cold boot healed all three
// (field report on #697, ST20 moved 192.168.178.x -> 10.10.50.x live).
//
// The refresh is self-computing: everything stale is recognisable as "carries
// our identity or our rule shape, but not one of our CURRENT addresses", so no
// pre-switch snapshot has to survive the switch.

import (
	"log/slog"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/netutil"
)

// netRefreshDeps is what the refresh needs from run()'s wiring. Funcs rather
// than the objects themselves: the mDNS announcer and host responder are
// locals of run(), guarded by its own mutexes.
type netRefreshDeps struct {
	// setMDNSHostAddr repoints the A-record responder for str-<id>.local
	// (mdnshost.Responder.SetAddress). nil when the responder never started;
	// SetAddress itself no-ops and re-announces only on an actual change.
	setMDNSHostAddr func(ip net.IP)
	// currentMDNSAddr reports the address the responder hands out right now
	// (mdnshost.Responder.Addr), nil when the responder never started. Used to
	// recognise a confirmed switch that kept the address, where tearing down a
	// working registration would only risk losing it.
	currentMDNSAddr func() net.IP
	// reannounce tears down and re-registers the service announcer with
	// freshly picked interfaces and addresses (discovery.Announcer.Reannounce).
	reannounce func(reason string) error
}

// refreshAfterNetworkChange runs on its own goroutine, fired by the webui's
// networkChangedFn hook right after renewDHCPLease. The udhcpc renew is
// asynchronous, so the first job is waiting for the lease to actually land.
func refreshAfterNetworkChange(reason string, deps netRefreshDeps, logger *slog.Logger) {
	logger = logger.With("comp", "netrefresh")
	// Two-stage wait, both bounded, same goroutine: 45 s at a tight cadence
	// for the common fast renew, then a slow tail for a DHCP server that
	// answers late, which is exactly the network where live switches happen.
	ip := waitForLeaseAfterSwitch(45*time.Second, 2*time.Second)
	if ip == nil {
		logger.Warn("network refresh: no LAN address yet, keeping a slow watch before touching anything", "reason", reason)
		ip = waitForLeaseAfterSwitch(4*time.Minute, 5*time.Second)
	}
	// The stale-self purge is address-independent and safe in every outcome.
	purgeSelfPeers(logger)
	if ip == nil {
		// Never tear working state down while the box holds no address at all:
		// a failed re-register would leave the box announcing NOTHING, which
		// is strictly worse than the stale announcement (adversarial review of
		// d842293), and deleting REDIRECT rules against an empty own-address
		// set would drop them ALL. The pre-switch state stays; the wlan guard
		// or a reboot handles a network that stays down this long.
		logger.Warn("network refresh: still no LAN address, leaving mDNS and REDIRECT rules untouched", "reason", reason)
		return
	}
	logger.Info("network refresh: address settled", "reason", reason, "address", ip.String())
	// A confirmed switch that KEPT the address (same-subnet move, password
	// correction) has nothing stale to re-announce, and a teardown at the most
	// network-unstable moment risks ending unregistered for nothing.
	if deps.currentMDNSAddr != nil {
		if cur := deps.currentMDNSAddr(); cur != nil && cur.Equal(ip.To4()) {
			logger.Info("network refresh: address unchanged, mDNS left as it is", "address", ip.String())
			cleanupStaleRedirects(logger)
			return
		}
	}
	if deps.setMDNSHostAddr != nil {
		deps.setMDNSHostAddr(ip)
	}
	if deps.reannounce != nil {
		// Bounded retries: reannounce tears down before it registers, and a
		// register at this moment can fail transiently (multicast join while
		// the interface is still replumbing). One failure must not leave the
		// box unannounced until the next rename or reboot.
		for attempt, wait := range []time.Duration{0, 10 * time.Second, 30 * time.Second} {
			time.Sleep(wait)
			err := deps.reannounce("network change: " + reason)
			if err == nil {
				break
			}
			logger.Warn("network refresh: mDNS re-announce failed",
				"attempt", attempt+1, "err", err)
		}
	}
	cleanupStaleRedirects(logger)
}

// waitForLeaseAfterSwitch polls the primary LAN address until it CHANGES from
// what it read first (with one confirming read, since a mid-renew gap can
// surface transient values), or the budget runs out. A switch inside the same
// subnet legitimately keeps the same address, so running out of budget is the
// normal outcome there, not a failure; the refresh steps behind it are
// idempotent either way.
func waitForLeaseAfterSwitch(budget, step time.Duration) net.IP {
	start := netutil.FirstLANIPv4()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		time.Sleep(step)
		cur := netutil.FirstLANIPv4()
		if cur == nil {
			continue // interface mid-renew
		}
		if start == nil || !cur.Equal(start) {
			time.Sleep(step)
			if again := netutil.FirstLANIPv4(); again != nil && again.Equal(cur) {
				return cur
			}
		}
	}
	return netutil.FirstLANIPv4()
}

// strRedirectPorts are the destination ports of the REDIRECT rules run.sh
// installs with a -d <LANIP> scope (REDIRECT_PORTS plus the BCO 17008->8888
// rule). Only rules matching this tuple shape are ever touched.
var strRedirectPorts = map[string]bool{
	"8888": true, "9080": true, "8081": true, "8443": true, "17008": true,
}

// staleRedirectDeleteArgs parses `iptables -w -t nat -S PREROUTING` output and
// returns the argument vectors that delete every streborn-shaped REDIRECT rule
// whose -d address is not one of ours any more. Pure, for tests.
//
// run.sh installs the rules idempotently per (port, LANIP) tuple and its 30 s
// watchdog re-resolves the CURRENT address each pass, so after a live subnet
// move the old-IP rules linger next to the new-IP ones until reboot. The
// xt_comment marker is absent on kernels without the module (taigan), so the
// match is on the tuple shape, never on the comment; replaying the -S line
// with -A swapped for -D matches the rule exactly as iptables holds it,
// comment or not.
func staleRedirectDeleteArgs(rules string, own map[string]bool) [][]string {
	var out [][]string
	for _, line := range strings.Split(rules, "\n") {
		tok := strings.Fields(strings.TrimSpace(line))
		if len(tok) < 4 || tok[0] != "-A" || tok[1] != "PREROUTING" {
			continue
		}
		dst, dport, redirect := "", "", false
		for i := 0; i < len(tok); i++ {
			switch tok[i] {
			case "-d":
				if i+1 < len(tok) {
					dst = strings.TrimSuffix(tok[i+1], "/32")
				}
			case "--dport":
				if i+1 < len(tok) {
					dport = tok[i+1]
				}
			case "-j":
				if i+1 < len(tok) && tok[i+1] == "REDIRECT" {
					redirect = true
				}
			}
		}
		if !redirect || dst == "" || !strRedirectPorts[dport] || own[dst] {
			continue
		}
		args := append([]string{"-w", "-t", "nat", "-D"}, tok[1:]...)
		out = append(out, args)
	}
	return out
}

// cleanupStaleRedirects removes the REDIRECT rules scoped to addresses this
// box no longer holds. First place the agent itself touches iptables; scope
// is deletion-only, tuple-matched, and only for -d addresses outside
// ownIPv4s(), so the current rules and anything non-STR stay untouched.
// A dev host without iptables (or a box without the nat table) is a no-op.
func cleanupStaleRedirects(logger *slog.Logger) {
	ipt, err := exec.LookPath("iptables")
	if err != nil {
		return
	}
	out, err := exec.Command(ipt, "-w", "-t", "nat", "-S", "PREROUTING").CombinedOutput()
	if err != nil {
		logger.Debug("network refresh: could not list nat PREROUTING", "err", err)
		return
	}
	for _, args := range staleRedirectDeleteArgs(string(out), ownIPv4s()) {
		if derr := exec.Command(ipt, args...).Run(); derr != nil {
			logger.Warn("network refresh: could not delete a stale REDIRECT rule",
				"rule", strings.Join(args, " "), "err", derr)
		} else {
			logger.Info("network refresh: deleted a REDIRECT rule for a previous address",
				"rule", strings.Join(args, " "))
		}
	}
}
