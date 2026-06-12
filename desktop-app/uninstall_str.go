// Uninstall STR: SSH into the speaker, remove the STR install entirely,
// reset the Bose-side state to factory OOB, and reboot into vanilla
// Bose. This is the real "back to stock" path. It differs from
// TrueFactoryReset, which deliberately KEEPS STR installed and only
// wipes the Bose network/account state so the box can be re-onboarded
// with the Bose iOS app while STR stays in place.
//
// CRITICAL safeguard: refuse if the STR USB stick is still inserted.
// Bose's SD/USB entry point re-runs the stick's run.sh on the next
// boot, which would reinstall STR right after we removed it. The user
// must pull the stick first.

package main

import (
	"fmt"
	"strings"
	"time"
)

// UninstallSTRResult is the JSON-serialisable outcome of an uninstall
// attempt. StickPresent=true means we aborted because the stick was
// still in the speaker (the frontend shows the pull-the-stick hint).
type UninstallSTRResult struct {
	Step         string   `json:"step"`
	OK           bool     `json:"ok"`
	StickPresent bool     `json:"stickPresent"`
	Message      string   `json:"message"`
	Log          string   `json:"log"`
	RemovedFiles []string `json:"removedFiles"`
}

// UninstallSTR removes STR from the speaker and returns it to vanilla
// Bose factory state, then reboots. SSH must be available (passwordless
// root via the STR install or an inserted stick's remote_services
// marker — but see the stick safeguard below).
//
// Removes:
//   - /mnt/nv/streborn (the whole STR install: agent, certs, presets,
//     run-override.sh, wlan-creds)
//   - /mnt/nv/rc.local (the NAND override entry point, so Bose boots
//     its own stack with no STR hook)
//
// Resets to factory OOB (same Bose persistence files as TrueFactoryReset
// plus the STR-written Wi-Fi profile):
//   - NetworkProfiles.xml, AirPlay2_Home.xml, Spotify*,
//     SystemConfigurationDB.xml (reseeded empty), AirplayConfiguration.xml
//     (STR's accountless PersistentWifiProfile + BCOResetTimer)
//   - the STR marge redirect in /etc/hosts (a tmpfs bind-mount; unbound
//     here and cleared by the reboot regardless)
//
// After the reboot the speaker is a brand-new Bose device with no STR.
func (a *App) UninstallSTR(host string) UninstallSTRResult {
	res := UninstallSTRResult{Step: "start"}
	if host == "" {
		res.Message = "host is required"
		return res
	}
	a.logger.Info("uninstall_str: starting", "host", host)

	// Step 1: SSH handshake.
	res.Step = "ssh-handshake"
	hello, helloErr := sshHandshake(host, 4)
	if helloErr != nil || !strings.Contains(hello, "STR_SSH_OK") {
		res.Log = hello
		res.Message = "SSH handshake to speaker failed: " + classifySSHError(hello, helloErr)
		return res
	}

	// Step 2: the whole uninstall runs as one script. It self-aborts up
	// front if the stick is still inserted (run.sh / the agent binary on
	// /media/sda1), so we never remove STR only to have Bose reinstall it
	// from the stick on the next boot.
	res.Step = "uninstall"
	const script = `set -u
PERSIST=/mnt/nv/BoseApp-Persistence/1
if [ -e /media/sda1/run.sh ] || [ -e /media/sda1/streborn-armv7l ] || [ -e /media/sda1/streborn ]; then
  echo "STR_UNINSTALL_ABORT_STICK_PRESENT"
  exit 0
fi
REMOVED=""
# Stop the running agent so it cannot rewrite anything mid-uninstall.
pkill -TERM streborn-armv7l 2>/dev/null
sleep 1
pkill -KILL streborn-armv7l 2>/dev/null
# Remove the STR install + the NAND override entry point.
if [ -d /mnt/nv/streborn ]; then rm -rf /mnt/nv/streborn && REMOVED="$REMOVED /mnt/nv/streborn"; fi
if [ -f /mnt/nv/rc.local ]; then rm -f /mnt/nv/rc.local && REMOVED="$REMOVED /mnt/nv/rc.local"; fi
# Reset Bose-side state to factory OOB.
for f in NetworkProfiles.xml AirPlay2_Home.xml AirplayConfiguration.xml SPOTIFY.SpotifyConnectUserName.xml SPOTIFY.SpotifyAlexaUserName.xml SystemConfigurationDB.xml; do
  if [ -f "$PERSIST/$f" ]; then rm -f "$PERSIST/$f" && REMOVED="$REMOVED $PERSIST/$f"; fi
done
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
REMOVED="$REMOVED ${PERSIST}/SystemConfigurationDB.xml(reseeded)"
# Drop the STR marge redirect from /etc/hosts. It is a tmpfs bind-mount,
# so unbind it now; a reboot clears it regardless.
umount /etc/hosts 2>/dev/null || true
sync
echo "STR_UNINSTALL_REMOVED:$REMOVED"
`
	out, err := boxSSHOutput(host, script, 40*time.Second)
	res.Log = out
	if err != nil {
		res.Message = "uninstall failed: " + classifySSHError(out, err)
		return res
	}
	if strings.Contains(out, "STR_UNINSTALL_ABORT_STICK_PRESENT") {
		res.StickPresent = true
		res.Step = "stick-present"
		res.Message = "The USB stick is still in the speaker. Pull it out first, " +
			"otherwise the speaker reinstalls STR from the stick on the next reboot. " +
			"Remove the stick and try again."
		a.logger.Info("uninstall_str: aborted, stick still inserted", "host", host)
		return res
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "STR_UNINSTALL_REMOVED:") {
			res.RemovedFiles = append(res.RemovedFiles, strings.Fields(strings.TrimPrefix(line, "STR_UNINSTALL_REMOVED:"))...)
		}
	}
	if len(res.RemovedFiles) == 0 {
		res.Message = "uninstall returned no removed-file list — script may have failed silently. See log."
		return res
	}
	a.logger.Info("uninstall_str: removed", "host", host, "removedCount", len(res.RemovedFiles))

	// Step 3: reboot into vanilla Bose. Connection drops mid-command;
	// fire-and-forget so the drop is not treated as a failure.
	res.Step = "reboot"
	_ = boxReboot(host)

	res.Step = "done"
	res.OK = true
	res.Message = fmt.Sprintf("STR removed (%d entries) and the speaker is rebooting into "+
		"factory Bose state. Wait ~60 s. The speaker no longer runs STR; onboard it again "+
		"with the Bose iOS app, or re-insert a prepared STR stick to bring STR back.",
		len(res.RemovedFiles))
	a.logger.Info("uninstall_str: done", "host", host, "removedCount", len(res.RemovedFiles))
	return res
}
