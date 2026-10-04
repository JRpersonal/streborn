package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/marge"
)

// firmwareGroupProbe reads the speaker's own /getGroup for marge, so a group
// poll that arrives while STR has no pair record can ask the firmware what it
// holds instead of answering it away (internal/marge/groupadopt.go).
func firmwareGroupProbe(boxHost string) marge.FirmwareGroupProbe {
	if boxHost == "" {
		return nil
	}
	c := boxapi.New(boxHost)
	return func(ctx context.Context) (marge.FirmwareGroup, error) {
		g, err := c.GetGroup(ctx)
		if err != nil {
			return marge.FirmwareGroup{}, err
		}
		return toFirmwareGroup(g), nil
	}
}

func toFirmwareGroup(g boxapi.Group) marge.FirmwareGroup {
	fg := marge.FirmwareGroup{ID: g.ID, Name: g.Name, MasterDeviceID: g.MasterDeviceID, Status: g.Status}
	for _, m := range g.Members {
		fg.Roles = append(fg.Roles, marge.FirmwareRole{DeviceID: m.DeviceID, Role: m.Role, IP: m.IP})
	}
	return fg
}

// adoptFirmwarePairAtStartup adopts the stereo pair the firmware already holds
// before its first group poll can be answered without it. This is the first
// boot after STR was installed on a speaker paired in the Bose app: STR's store
// is empty, the pair lives only in the firmware, and the poll decides whether
// the firmware keeps it. The poll path adopts too (marge asks the firmware when
// its store is empty); this just gets there first whenever the firmware's own
// HTTP server is up before its cloud client.
//
// Reads only, every 20 s while the firmware is booting, and it stops at the
// first definite answer. The single NAND write is the adoption itself.
func adoptFirmwarePairAtStartup(ctx context.Context, m *marge.Server, boxHost string, logger *slog.Logger) {
	if m == nil || boxHost == "" {
		return
	}
	if _, _, stored := m.GroupSnapshot(); stored {
		return
	}
	probe := firmwareGroupProbe(boxHost)
	for attempt := 0; attempt < 30; attempt++ { // up to ~10 minutes of slow boot
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Second):
		}
		if _, _, stored := m.GroupSnapshot(); stored {
			return // the poll path or a pairing got there first
		}
		gctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		fg, err := probe(gctx)
		cancel()
		if err != nil {
			continue // firmware not answering yet
		}
		ok, why := m.AdoptFirmwareGroup(fg)
		switch {
		case ok:
			return
		case why == "firmware not paired":
			return
		case why == "own device id not known yet" || why == "the pair does not name this speaker and a partner":
			// The device id may still be the interface guess; the box corrects
			// it within the first minute (deviceid.go). Try again.
			continue
		default:
			logger.Warn("stereo pair: the firmware holds a pair STR cannot vouch for, leaving it as it is",
				"reason", why, "status", fg.Status, "master", fg.MasterDeviceID)
			return
		}
	}
}
