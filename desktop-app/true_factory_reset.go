// True factory reset: SSH into the speaker and wipe the persistence
// files that Bose's hardware factory reset (Preset 1 + Vol-) leaves
// behind. Without this, /mnt/nv/BoseApp-Persistence/1/NetworkProfiles.xml
// keeps the speaker auto-rejoining its last Wi-Fi within seconds of
// every reboot, and the Bose iOS app's WAC discovery sees an
// "already-configured" speaker and disengages — making it impossible
// for the user to re-onboard via the official Bose app.
//
// Findings that drive this: live capture on rhino/sm2 ST10
// 2026-05-29 — the iPhone briefly joined the speaker's setup-AP,
// GET'd /info, saw <ipAddress>192.168.178.66</ipAddress> (already on
// JJ3 from a leftover NetworkProfiles.xml), and disconnected without
// posting any credentials. Wiping NetworkProfiles.xml +
// SystemConfigurationDB.xml + STR's NAND wlan-creds and rebooting
// puts the speaker into pure OOB state so WAC can run end-to-end.

package main

import (
	"fmt"
	"strings"
	"time"
)

// TrueFactoryResetResult is the JSON-serialisable outcome of a true
// factory-reset attempt. Step lets the frontend show where we
// stopped on failure; WipedFiles is shown to the user so they can
// see what was actually cleared.
type TrueFactoryResetResult struct {
	Step       string   `json:"step"`
	OK         bool     `json:"ok"`
	Message    string   `json:"message"`
	Log        string   `json:"log"`
	WipedFiles []string `json:"wipedFiles"`
}

// TrueFactoryReset performs a deeper reset than Bose's hardware
// reset (Preset 1 + Vol-). SSH must be available — that requires
// either an STR-installed speaker (passwordless root via the
// /media/sda1/remote_services marker the STR stick lays down) or a
// stick currently inserted with that marker.
//
// Wipes:
//   - /mnt/nv/BoseApp-Persistence/1/NetworkProfiles.xml (forces OOB)
//   - /mnt/nv/BoseApp-Persistence/1/SystemConfigurationDB.xml
//     (clears Marge account, device name, account-mode)
//   - /mnt/nv/BoseApp-Persistence/1/AirPlay2_Home.xml (clears the
//     old HomeKit/AirPlay home pairing; otherwise iOS reconnects to
//     a stale home and refuses to re-add the speaker)
//   - /mnt/nv/streborn/wlan-creds (so STR's NAND-replay does not
//     re-provision the old SSID on the next boot, before the iPhone
//     gets a chance to do WAC)
//
// Preserves:
//   - STR install (run-override.sh, agent binary, certs, presets).
//   - Box clock state, debug logs.
//   - Bose firmware itself.
//
// After this call the speaker reboots and comes up in pure
// Setup-AP OOB. The user then opens the Bose iOS app and follows
// the normal onboarding flow.
func (a *App) TrueFactoryReset(host string) TrueFactoryResetResult {
	res := TrueFactoryResetResult{Step: "start"}
	if host == "" {
		res.Message = "host is required"
		return res
	}
	a.logger.Info("true_factory_reset: starting", "host", host)

	// Step 1: confirm SSH reachable + authenticated. Same handshake
	// idiom as install_str.go so the error classification is
	// consistent with the install flow.
	res.Step = "ssh-handshake"
	hello, helloErr := sshHandshake(host, 4)
	if helloErr != nil || !strings.Contains(hello, "STR_SSH_OK") {
		res.Log = hello
		res.Message = "SSH handshake to speaker failed: " + classifySSHError(hello, helloErr)
		return res
	}

	// Step 2: wipe the persistence files and STR's wlan-creds, then
	// re-seed SystemConfigurationDB.xml with the pristine post-OOB
	// shape Bose's NetManager expects. Without this re-seed BoseApp
	// re-creates the file but may also re-write the OLD device name
	// from another cache; the explicit empty values are a hard reset.
	res.Step = "wipe-persistence"
	const wipeScript = `set -u
PERSIST=/mnt/nv/BoseApp-Persistence/1
NV_STR=/mnt/nv/streborn
WIPED=""
for f in NetworkProfiles.xml AirPlay2_Home.xml SPOTIFY.SpotifyConnectUserName.xml SPOTIFY.SpotifyAlexaUserName.xml; do
  if [ -f "$PERSIST/$f" ]; then
    rm -f "$PERSIST/$f" && WIPED="$WIPED $PERSIST/$f"
  fi
done
if [ -f "$PERSIST/SystemConfigurationDB.xml" ]; then
  rm -f "$PERSIST/SystemConfigurationDB.xml" && WIPED="$WIPED $PERSIST/SystemConfigurationDB.xml"
fi
cat > "$PERSIST/SystemConfigurationDB.xml" <<'XML'
<?xml version="1.0" encoding="UTF-8" ?>
<SystemConfiguration>
    <Password />
    <DeviceName />
    <AccountAssociatedEMail />
    <AccountUUID />
    <Locale />
    <acctMode />
    <isMultiDeviceAccount>true</isMultiDeviceAccount>
    <margeAuthServerToken />
    <powerSavingSettings powersaving_en="true" />
</SystemConfiguration>
XML
WIPED="$WIPED ${PERSIST}/SystemConfigurationDB.xml(reseeded)"
if [ -f "$NV_STR/wlan-creds" ]; then
  rm -f "$NV_STR/wlan-creds" && WIPED="$WIPED $NV_STR/wlan-creds"
fi
sync
echo "TFR_WIPED:$WIPED"
`
	out, err := boxSSHOutput(host, wipeScript, 30*time.Second)
	res.Log = out
	if err != nil {
		res.Message = "wipe failed: " + classifySSHError(out, err)
		return res
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "TFR_WIPED:") {
			res.WipedFiles = append(res.WipedFiles, strings.Fields(strings.TrimPrefix(line, "TFR_WIPED:"))...)
		}
	}
	if len(res.WipedFiles) == 0 {
		res.Message = "wipe returned no file list — script may have failed silently. See log."
		return res
	}
	a.logger.Info("true_factory_reset: persistence wiped",
		"host", host, "wipedCount", len(res.WipedFiles))

	// Step 3: reboot. The connection drops mid-command, which is
	// the expected non-zero exit — use fire-and-forget so we don't
	// treat the drop as failure.
	res.Step = "reboot"
	_ = boxReboot(host)

	res.Step = "done"
	res.OK = true
	res.Message = fmt.Sprintf("Speaker wiped (%d entries) and rebooted. "+
		"Wait ~60 s, then open the Bose iOS app and follow the normal onboarding flow. "+
		"The speaker will appear as a brand-new device.",
		len(res.WipedFiles))
	a.logger.Info("true_factory_reset: done",
		"host", host, "wipedCount", len(res.WipedFiles))
	return res
}
