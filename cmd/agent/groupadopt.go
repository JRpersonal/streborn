package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/webui"
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

// phantomPairCheckInterval is the gap between two looks at a restored pair
// record, and phantomPairCheckRounds how many there are: a slow boot gets
// about twenty minutes.
const (
	phantomPairCheckInterval = 30 * time.Second
	phantomPairCheckRounds   = 40
)

// dropPhantomRestoredPair clears a pair record that was restored from NAND and
// describes a pair that does not exist. It runs once per agent start, only
// when such a record was restored, and stops at the first definite answer.
//
// Two things prove a phantom:
//
// The firmware answers /getGroup empty twice in a row with still no live
// confirmation (no firmware group post, no canonical install). That is the
// Bose factory reset, which wipes the firmware's own pairing but not
// /mnt/nv/streborn.
//
// Or the speaker is a model that cannot be half of a stereo pair at all. Bose
// pairs only two SoundTouch 10s, so a record on anything else is wrong whatever
// it says. This had to become its own rule because the first one cannot fire
// there: /getGroup never answers on the scm/BCO chassis, it hangs, every read
// counted as "firmware not answering yet", and the record stayed for good. A
// SoundTouch 30 holding a record that named a SoundTouch 10 as its master then
// refused every play STR was asked for as "the following half of a stereo
// pair": the second preset row, a station from the search, a key in the app.
// The app sent each of them on to the named master, so the music started in
// the other room (#1964). The model is asked first, so such a speaker is never
// sent the read that hangs.
//
// A model that is not known yet (the firmware is still booting) decides
// nothing and the round falls through to the /getGroup read.
func dropPhantomRestoredPair(ctx context.Context, m *marge.Server,
	boxModel func(context.Context) (string, error),
	getGroup func(context.Context) (boxapi.Group, error),
	interval time.Duration, logger *slog.Logger) {
	emptyReads := 0
	for i := 0; i < phantomPairCheckRounds; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		if !m.GroupRestoredUnconfirmed() {
			return // confirmed by a live signal meanwhile
		}
		mctx, mcancel := context.WithTimeout(ctx, 5*time.Second)
		model, merr := boxModel(mctx)
		mcancel()
		if merr == nil && !webui.ModelMayHoldStereoPair(model) {
			logger.Warn("stereo pair: dropping a restored pair record, this model cannot be half of a stereo pair",
				"model", model)
			m.ClearGroup("restored pair record on a model that cannot hold a stereo pair")
			return
		}
		gctx, gcancel := context.WithTimeout(ctx, 6*time.Second)
		g, err := getGroup(gctx)
		gcancel()
		if err != nil {
			emptyReads = 0 // firmware not answering yet; start over
			continue
		}
		if g.ID != "" || len(g.Members) > 0 {
			return // firmware still paired: the restored record is real
		}
		if emptyReads++; emptyReads >= 2 {
			m.ClearGroup("firmware reports no pair after restart (factory reset?)")
			return
		}
	}
}
