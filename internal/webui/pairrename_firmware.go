// Carrying a stereo pair rename into the speaker firmware.

package webui

import (
	"context"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// A pair has three names in the Spotify app's eyes, and a rename used to move
// only two of them (#1752).
//
// "Save name" wrote the new name into the marge group record on both halves.
// STR's own engine reads that record, so the "(STR)" entry followed at once.
// The speaker's NATIVE Spotify client does not: the master's firmware
// advertises "<pair name> (L+R)" from its own group state, and that state only
// changes through the firmware's /updateGroup. The reporter's list therefore
// showed "Kitchen pair (STR)" next to "Stereo Pair (L+R)", the name the pair was
// formed with. Measured on two SoundTouch 10s on 2026-11-05: after the marge
// rename the native entry (:8200/zc getInfo remoteName) kept the old name; one
// POST /updateGroup on the master moved it, and both firmwares and both marge
// records then carried the new name with the pair still GROUP_OK.
//
// The reporter's bundle adds one detail that shapes the rule below: after a
// reboot their master's /getGroup already reported the new name (the firmware
// re-reads the marge record at boot), while the native entry still carried the
// old one. So "the firmware already has this name" is NOT proof that the native
// entry does, and the update is sent on every rename rather than skipped when
// the names match. It is idempotent and costs one local request.

// pairFirmware is the slice of the firmware API a pair rename needs. It exists
// so the decision can be tested without a speaker on :8090.
type pairFirmware interface {
	GetGroup(ctx context.Context) (boxapi.Group, error)
	GetInfo(ctx context.Context) (boxapi.Info, error)
	UpdateGroup(ctx context.Context, g boxapi.Group) error
}

// Budgets for the rename. The group read gets the same short budget the zone
// poll uses, because /getGroup hangs instead of refusing on chassis that cannot
// pair; the update gets a little more because the firmware answers it only
// after its own marge round trip.
const (
	pairRenameReadBudget   = 1500 * time.Millisecond
	pairRenameUpdateBudget = 4 * time.Second
)

// Outcomes of renamePairOnFirmware, reported in the rename's JSON answer and in
// the log so a bundle shows which branch ran.
const (
	pairFirmwareRenamed   = "renamed"
	pairFirmwareNoName    = "skipped-empty-name"
	pairFirmwareNoPair    = "no-pair-on-firmware"
	pairFirmwareUnread    = "group-unreadable"
	pairFirmwareNoMaster  = "master-unknown"
	pairFirmwareRefused   = "update-failed"
	pairFirmwareNotWired  = "not-wired"
	pairFirmwareBadMaster = "master-not-on-lan"
)

// renamePairOnFirmware sends the pair's new name to the firmware that leads the
// pair. local is this speaker's firmware; dial reaches another speaker's
// firmware by address and is used only when this speaker is the following half,
// so a client that renames through the follower alone still reaches the master.
//
// An empty name is left alone: it clears STR's own label, and the firmware has
// no "no name" for a pair to fall back to.
func renamePairOnFirmware(ctx context.Context, local pairFirmware, dial func(ip string) pairFirmware, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return pairFirmwareNoName, nil
	}
	if local == nil {
		return pairFirmwareNotWired, nil
	}
	rctx, cancel := context.WithTimeout(ctx, pairRenameReadBudget)
	g, err := local.GetGroup(rctx)
	cancel()
	if err != nil {
		return pairFirmwareUnread, err
	}
	if strings.TrimSpace(g.ID) == "" || strings.TrimSpace(g.MasterDeviceID) == "" || len(g.Members) != 2 {
		return pairFirmwareNoPair, nil
	}

	target := local
	ictx, cancel := context.WithTimeout(ctx, pairRenameReadBudget)
	info, ierr := local.GetInfo(ictx)
	cancel()
	self := strings.TrimSpace(info.DeviceID)
	if ierr != nil || self == "" || !strings.EqualFold(self, g.MasterDeviceID) {
		// Not provably the master: hand the update to the master's firmware.
		// Its address comes from the firmware's own pair document, never from
		// the caller, and is still held to the LAN like every other peer dial.
		masterIP := ""
		for _, m := range g.Members {
			if strings.EqualFold(strings.TrimSpace(m.DeviceID), g.MasterDeviceID) {
				masterIP = strings.TrimSpace(m.IP)
			}
		}
		if masterIP == "" {
			return pairFirmwareNoMaster, nil
		}
		if !isLANPeer(masterIP) {
			return pairFirmwareBadMaster, nil
		}
		if dial == nil {
			return pairFirmwareNotWired, nil
		}
		target = dial(masterIP)
	}

	g.Name = name
	uctx, cancel := context.WithTimeout(ctx, pairRenameUpdateBudget)
	defer cancel()
	if err := target.UpdateGroup(uctx, g); err != nil {
		return pairFirmwareRefused, err
	}
	return pairFirmwareRenamed, nil
}

// renamePairOnBoxFirmware is renamePairOnFirmware against the real firmware of
// this speaker, logging what happened. It never fails the rename: the marge
// record is already written and carries STR's own label either way.
func (s *Server) renamePairOnBoxFirmware(ctx context.Context, name string) string {
	var local pairFirmware
	if s.boxHost != "" {
		local = boxapi.New(s.boxHost)
	}
	dial := func(ip string) pairFirmware { return boxapi.New(ip) }
	outcome, err := renamePairOnFirmware(ctx, local, dial, name)
	switch {
	case err != nil:
		s.logger.Warn("stereo: the pair name did not reach the speaker firmware, its own Spotify entry keeps the old name",
			"outcome", outcome, "name", name, "err", err)
	case outcome == pairFirmwareRenamed:
		s.logger.Info("stereo: pair renamed on the speaker firmware, its own Spotify entry follows", "name", name)
	default:
		s.logger.Info("stereo: pair name not sent to the speaker firmware", "outcome", outcome, "name", name)
	}
	return outcome
}
