package main

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/JRpersonal/streborn/internal/mdnshost"
	"github.com/JRpersonal/streborn/internal/spotify"
	"github.com/JRpersonal/streborn/internal/webui"
)

// mdnsHostLeaseWait bounds how long a speaker without an address at boot keeps
// waiting for one before the name responder is given up for this run.
const mdnsHostLeaseWait = 3 * time.Minute

// startMDNSHost answers for this speaker's own mDNS name BEFORE the engine is
// told to advertise it. The order is the safety property, not a detail: the
// engine names the box's Linux hostname today, which is the Bose chassis
// codename, and nothing on the network answers a query for it. Pointing the
// engine at a new name that is equally unanswered would be a step backwards,
// because the codename at least still sits in some routers' caches. So the
// name is only handed over once it means something, and a responder that
// cannot start leaves everything exactly as it is today.
//
// The agent is often up before the first DHCP lease, a wired speaker above
// all: on #841 (2026-10-29) the address arrived eight seconds after this ran,
// and the responder was never started for the whole run. Without an address
// it now waits for the lease in the background. The engine starts minutes
// later, so it still picks the name up (SetZeroconfHost applies at the next
// engine start).
func (a *agent) startMDNSHost(ctx context.Context, logger *slog.Logger, deviceID string, spotifyMgr *spotify.Manager) {
	// The kernel's UDP counters, for a flood on the network like the one on
	// #841. They need no responder, so they are in the bundle either way.
	webui.RegisterDebugSection("udp_counters", func() any { return mdnshost.UDPCounters() })

	label := mdnshost.LabelFor(deviceID)
	if label == "" {
		return
	}
	if ip := routableIPv4(); ip != nil {
		a.launchMDNSHost(ctx, logger, label, ip, spotifyMgr, true)
		return
	}
	logger.Warn("mdns host: no routable address yet, waiting for the first lease", "label", label)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		deadline := time.NewTimer(mdnsHostLeaseWait)
		defer deadline.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-deadline.C:
				logger.Warn("mdns host: still no routable address, leaving the engine on the chassis name",
					"label", label, "waited", mdnsHostLeaseWait)
				return
			case <-tick.C:
				if ip := routableIPv4(); ip != nil {
					a.launchMDNSHost(ctx, logger, label, ip, spotifyMgr, false)
					return
				}
			}
		}
	}()
}

// launchMDNSHost starts the responder on ip and hands its name to everything
// that names this speaker. atBoot is false for a responder started after the
// first lease: the agent's own announcer has read mdnsHostLabel by then, so it
// keeps the hostname for this run rather than racing that read.
func (a *agent) launchMDNSHost(ctx context.Context, logger *slog.Logger, label string, ip net.IP, spotifyMgr *spotify.Manager, atBoot bool) {
	resp, err := mdnshost.Start(ctx, logger.With("comp", "mdnshost"), label, ip)
	if err != nil {
		logger.Warn("mdns host: not answering for this speaker's own name, leaving the engine on the chassis name",
			"err", err, "label", label)
		return
	}
	spotifyMgr.SetZeroconfHost(resp.Label())
	// The agent's own service gets the same treatment. Its entry names the
	// chassis codename today just like the Spotify one did, so three
	// SoundTouch 10s announce themselves under one name. It is less visible
	// than the Spotify fault only because the desktop app takes the address
	// out of the service answer instead of looking the name up.
	if atBoot {
		mdnsHostLabel = resp.Label()
	}
	// Tell the HTTP surface too: the desktop app reads this back to put a name
	// on the phone-remote QR code instead of an address.
	webui.SetMDNSHostName(resp.Name())
	// Put the counters in the diagnostic bundle. The open question they
	// answer: a SoundTouch 30 here is missing from four browses in a row while
	// its engine runs and is logged in. If queriesSeen climbs on that box, the
	// questions arrive and the answers are getting lost; if it stays at zero,
	// the box is never being asked. Those are different faults.
	webui.RegisterDebugSection("mdns_host", func() any { return resp.Stats() })
	// Captured for the post-switch network refresh (#697): after a live Wi-Fi
	// move the responder must be repointed at the new address. Closed in
	// wait(), once the listeners are down.
	a.mdnsHost.Store(resp)
	if !atBoot {
		logger.Info("mdns host: answering for this speaker's own name after the first lease", "label", label)
	}
}
