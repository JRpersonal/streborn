package main

import (
	"context"
	"log/slog"
	"net"
	"time"
)

// This file works around the root cause of the SoundTouch Portable's
// ~27-minute reboot loop, pinned live with strace + /proc forensics
// 2026-06-03. The chain:
//
//   1. The Portable's /opt/Bose/BatteryMonitor process deadlocks at init
//      (main thread parks on a futex) and never opens its service listener
//      on 127.0.0.1:17002. It is a deterministic deadlock: a restart
//      re-wedges immediately, so we cannot fix it by bouncing the process.
//   2. BoseApp's battery UI client (UiBatClient) cannot reach :17002, so it
//      hammers connect() ~137x/s. Every failed attempt spawns a fresh
//      UiBatClient thread pair that allocates an eventfd + a timerfd and then
//      blocks forever; nothing reaps them.
//   3. That leaks ~30 fds/min. When BoseApp's open-fd count reaches its
//      internal ~1024 select()/FD_SETSIZE ceiling its :8090 API deadlocks
//      and the Bose supervisor reboots the whole box. ~27 min per cycle.
//
// Proven fix (live): the storm is driven purely by connect() FAILING. The
// instant ANYTHING accepts on :17002, BoseApp's client connects, the storm
// drops to zero, and the fd/thread count plateaus (measured: connects/4s
// 601 -> 0, UiBat threads and fds flat). So the agent (which runs as root on
// the box) simply listens on 127.0.0.1:17002 itself as a fallback, accepts
// the battery client, and holds the connection. We never need to speak the
// battery protocol; a successful connect is enough to stop the leak.
//
// Safety on healthy boxes: we wait a grace period first, then only bind when
// the port is unserved. On any box whose BatteryMonitor is healthy it has
// already bound :17002, our Listen fails, and we back off and retry without
// ever fighting the real service. On models with no battery at all (ST10/20/
// 30) nothing connects to our listener, so it sits idle and harmless.
//
// This replaces the earlier prlimit-raise + hang-reboot mitigation, which was
// counterproductive: raising the soft RLIMIT_NOFILE past 1024 let the fd
// numbers grow past exactly the select()/FD_SETSIZE limit that wedges BoseApp,
// and the supervisor's reboot always beat the hang watchdog.

const (
	// batteryMonitorAddr is the local Bose BatteryMonitor service address.
	batteryMonitorAddr = "127.0.0.1:17002"

	// bmFallbackGrace gives a healthy BatteryMonitor time to bind :17002
	// before we consider stepping in. On a healthy box it binds at ~19 s of
	// uptime; 45 s is a safe margin while still capping the pre-fix leak to a
	// harmless ~20-25 fds.
	bmFallbackGrace = 45 * time.Second

	// bmFallbackRetry is how often we re-check the port while a healthy
	// BatteryMonitor holds it (our Listen keeps failing). Cheap.
	bmFallbackRetry = 60 * time.Second
)

// serveBatteryMonitorFallback binds 127.0.0.1:17002 whenever the real Bose
// BatteryMonitor is not serving it (the Portable deadlock case), accepts the
// BoseApp battery client and holds the connection so BoseApp stops its
// connect-storm fd leak. No-op on boxes where BatteryMonitor is healthy (the
// port is taken and our Listen fails). Returns when ctx is cancelled.
func serveBatteryMonitorFallback(ctx context.Context, logger *slog.Logger) {
	// Let the real BatteryMonitor win the port on a healthy box.
	select {
	case <-ctx.Done():
		return
	case <-time.After(bmFallbackGrace):
	}

	for {
		ln, err := net.Listen("tcp", batteryMonitorAddr)
		if err != nil {
			// Port already served (healthy BatteryMonitor) or a transient
			// bind error. Re-check later; never fight the real service.
			select {
			case <-ctx.Done():
				return
			case <-time.After(bmFallbackRetry):
				continue
			}
		}
		logger.Warn("battery-monitor fallback: :17002 unserved (BatteryMonitor wedged or absent), agent now accepting BoseApp battery clients to stop the connect-storm fd leak")
		acceptBatteryClients(ctx, ln, logger)
		// acceptBatteryClients only returns on ctx cancellation (it closes
		// the listener on its way out).
		return
	}
}

// acceptBatteryClients accepts on ln until ctx is cancelled, draining each
// connection in its own goroutine. The connection count is bounded by
// BoseApp's battery client (it stops opening new ones once connected), so the
// per-connection goroutines do not grow without bound.
func acceptBatteryClients(ctx context.Context, ln net.Listener, logger *slog.Logger) {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var total int64
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				logger.Warn("battery-monitor fallback: accept stopped", "err", err)
			}
			return
		}
		total++
		if total == 1 || total%200 == 0 {
			logger.Info("battery-monitor fallback: serving BoseApp battery clients", "totalAccepted", total)
		}
		go drainBatteryConn(conn)
	}
}

// drainBatteryConn holds a battery-client connection open and discards
// whatever BoseApp sends. We deliberately never reply: a successful connect is
// what stops BoseApp's retry storm, and a real battery payload would require
// reverse-engineering the BatteryMonitor wire protocol. Read blocks until
// BoseApp closes the socket or the box reboots.
func drainBatteryConn(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 512)
	for {
		if _, err := c.Read(buf); err != nil {
			return
		}
	}
}
