package main

import (
	"context"
	"log/slog"
	"net"
	"slices"
	"time"

	"github.com/JRpersonal/streborn/internal/hosts"
)

const (
	// ntpPinRetry is how soon a pass that resolved nothing is repeated: the
	// agent often starts before the speaker's network is up.
	ntpPinRetry = 2 * time.Minute
	// ntpPinRefresh is how often pinned addresses are looked up again. Pool
	// servers come and go, but an address stays usable for days.
	ntpPinRefresh = 6 * time.Hour
	// ntpLookupBudget bounds one pass over the four names.
	ntpLookupBudget = 20 * time.Second
)

// keepNTPPinned writes the addresses of the firmware's NTP servers into STR's
// hosts block and keeps them current, so the display clock syncs even when
// the firmware's own name lookups are stuck (internal/hosts/ntp.go). It
// returns when ctx ends.
func keepNTPPinned(ctx context.Context, mgr *hosts.Manager, logger *slog.Logger) {
	names := hosts.NTPServers(hosts.NTPServersPath)
	if len(names) == 0 {
		return
	}
	var pinned []hosts.Entry
	for {
		lctx, cancel := context.WithTimeout(ctx, ntpLookupBudget)
		entries := hosts.NTPEntries(lctx, names, net.DefaultResolver.LookupHost)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if len(entries) > 0 && !slices.Equal(entries, pinned) {
			if err := mgr.Apply(append(hosts.DefaultEntries(), entries...)); err != nil {
				logger.Warn("ntp servers: could not pin their addresses in the hosts file", "err", err)
			} else {
				pinned = entries
				logger.Info("ntp servers pinned in the hosts file, so the speaker's clock syncs whatever its own DNS does",
					"pinned", len(entries), "of", len(names))
			}
		}
		wait := ntpPinRefresh
		if len(entries) == 0 {
			wait = ntpPinRetry
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
