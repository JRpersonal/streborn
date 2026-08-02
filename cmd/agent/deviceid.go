package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/marge"
)

// correctDeviceIDFromBox replaces the MAC-derived startup guess with the id the
// speaker reports for itself in GET /info.
//
// Why this exists: the agent's startup value comes from a network interface
// MAC, and a speaker with two interfaces uses only one of them as its identity.
// Measured on an ST10, /info carries deviceID="94E36DF9CE40" (its SCM
// interface) while the agent had guessed "10CEA9E8CF31" (its SMSC interface).
// The id lands in the <devices> block of the emulated account, and the firmware
// throws away an account whose device list does not contain itself - together
// with the <sources> block in it, which is what registers the radio source that
// the hardware preset keys need. So a wrong id here reads, on the speaker, as
// "the preset buttons stopped working after a reboot".
//
// Read-only and bounded: it polls /info until the firmware answers (it is still
// booting for the first minute or so), then stops. It never writes to the box,
// so it cannot affect the standby countdown, and it gives up quietly rather
// than retrying forever if the box never answers - in which case the old
// guessed value simply remains in place, exactly as before.
func correctDeviceIDFromBox(ctx context.Context, m *marge.Server, boxHost string, logger *slog.Logger) {
	if m == nil || boxHost == "" {
		return
	}
	const (
		attempts = 20
		gap      = 6 * time.Second
	)
	client := boxapi.New(boxHost)
	for i := 0; i < attempts; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		info, err := client.GetInfo(c)
		cancel()
		// Validate rather than trust: boxapi only trims the attribute, and a
		// malformed id would be written straight into the account payload,
		// where it fails exactly like the wrong one it was meant to replace.
		if id := marge.ValidDeviceID(info.DeviceID); err == nil && id != "" {
			if m.SetDeviceID(id) {
				// WARN, not INFO: this is the moment a speaker's emulated
				// account becomes valid for it, and a diagnostic bundle needs
				// to show whether it happened and how long it took.
				logger.Warn("marge: corrected the deviceID from the box itself (the startup guess named a different network interface)",
					"deviceID", id, "afterAttempts", i+1)
			} else {
				logger.Info("marge: the box confirms the deviceID the agent already used", "deviceID", id)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(gap):
		}
	}
	logger.Warn("marge: the box never reported its deviceID, keeping the MAC-derived guess (source registration may not take on this speaker)",
		"guess", m.DeviceID())
}
