// Tests for the conflicting-mod cleanup endpoint (#986). The contract these
// pin is the one the reporter's ST30 broke: the cleanup must fix the thing that
// actually keeps the speaker away from STR (its cloud URL), must not throw away
// the file it needs to do that, and must never answer "removed" when it removed
// nothing.

package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func postRemoveConflictingMod(t *testing.T) map[string]any {
	t.Helper()
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/box/remove-conflicting-mod", nil)
	req.RemoteAddr = "192.168.1.10:5000"
	s.handleRemoveConflictingMod(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad JSON %q: %v", rec.Body.String(), err)
	}
	return out
}

// The whole of #986 in one test: the box carries the mod's backup under the
// measured filename and a rootfs config pointing at the mod's dead server. The
// old handler deleted the backup and reported success; the speaker kept asking
// the dead server and the reporter was told it was cleaned.
func TestCleanupHealsTheCloudURLBeforeItDeletesAnything(t *testing.T) {
	dir := redirectSDKPaths(t)
	if detectAfterTouch() {
		t.Skip("host machine has AfterTouch artifacts under /mnt/nv")
	}
	live := filepath.Join(dir, "hosts.live")
	oldLive := hostsLivePath
	hostsLivePath = live
	t.Cleanup(func() { hostsLivePath = oldLive })
	if err := os.WriteFile(live, []byte("127.0.0.1\tlocalhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sdkRootfsPath, []byte(sdkCfg(octMargeURL)), 0o644); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "SoundTouchSdkPrivateCfg.xml.oct-backup")
	if err := os.WriteFile(backup, []byte(sdkCfg(stockCloudURLs[sdkMargeTag])), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(octHostsBackupPath, []byte("127.0.0.1\tlocalhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := postRemoveConflictingMod(t)

	if out["mod"] != "OpenCloudTouch" {
		t.Errorf("mod = %v, want OpenCloudTouch", out["mod"])
	}
	if out["cloudURLHealed"] != true {
		t.Errorf("cloudURLHealed = %v, want true", out["cloudURLHealed"])
	}
	if out["rebootRequired"] != true {
		t.Error("the firmware reads the config at boot, so a restart must be requested")
	}
	if out["stillDetected"] == true {
		t.Errorf("stillDetected after a complete cleanup: %v", out)
	}
	if _, ok := out["foreignCloudURL"]; ok {
		t.Errorf("a foreign cloud URL survived the cleanup: %v", out["foreignCloudURL"])
	}
	// The backup may only go AFTER it has served as the restore template.
	if _, err := os.Stat(backup); err == nil {
		t.Error("the mod's backup was left behind")
	}
	if _, err := os.Stat(octHostsBackupPath); err == nil {
		t.Error("the mod's hosts backup was left behind")
	}
	if _, urls := effectiveSDKCloudURLs(); urls[sdkMargeTag] != stockCloudURLs[sdkMargeTag] {
		t.Errorf("cloud URL is %q after the cleanup", urls[sdkMargeTag])
	}
	removed, _ := out["removed"].([]any)
	if len(removed) == 0 {
		t.Error("removed list empty although three things were done")
	}
}

// When the heal cannot run, the files must STAY: they are the only copy of the
// box's pre-mod config. And the answer must say so rather than report success.
func TestCleanupKeepsTheBackupWhenTheCloudURLCannotBeHealed(t *testing.T) {
	dir := redirectSDKPaths(t)
	if detectAfterTouch() {
		t.Skip("host machine has AfterTouch artifacts under /mnt/nv")
	}
	// A backup with no readable cloud tags, no rootfs config, no override: the
	// heal has nothing to work from and must not invent a config.
	backup := filepath.Join(dir, "SoundTouchSdkPrivateCfg.xml.oct-backup")
	if err := os.WriteFile(backup, []byte("<garbage/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := postRemoveConflictingMod(t)
	if out["cloudURLHealed"] == true {
		t.Error("claimed a heal with nothing to heal from")
	}
	// Nothing to heal means nothing foreign was found either, so the inert
	// backup is removable and the state converges.
	if out["stillDetected"] == true {
		t.Errorf("stillDetected although the leftovers were removable: %v", out)
	}
	if _, err := os.Stat(backup); err == nil {
		t.Error("an inert backup with a healthy cloud URL should be removed")
	}
}

// A foreign cloud URL with every marker file already gone is still worth
// running: it is what survives a factory reset, and it is the part that breaks
// playback. The old handler returned early on an empty mod name and did nothing.
func TestCleanupRunsForAForeignCloudURLWithNoModMarkers(t *testing.T) {
	redirectSDKPaths(t)
	if detectAfterTouch() {
		t.Skip("host machine has AfterTouch artifacts under /mnt/nv")
	}
	if err := os.WriteFile(sdkRootfsPath, []byte(sdkCfg(octMargeURL)), 0o644); err != nil {
		t.Fatal(err)
	}
	out := postRemoveConflictingMod(t)
	if out["mod"] != "" {
		t.Errorf("mod = %v, want empty (no marker files)", out["mod"])
	}
	if out["cloudURLHealed"] != true {
		t.Fatalf("no heal ran: %v", out)
	}
	if _, urls := effectiveSDKCloudURLs(); urls[sdkMargeTag] != stockCloudURLs[sdkMargeTag] {
		t.Errorf("cloud URL is %q after the cleanup", urls[sdkMargeTag])
	}
}

// A clean box must stay a no-op: no file written, nothing claimed.
func TestCleanupIsANoOpOnACleanBox(t *testing.T) {
	redirectSDKPaths(t)
	if detectAfterTouch() {
		t.Skip("host machine has AfterTouch artifacts under /mnt/nv")
	}
	out := postRemoveConflictingMod(t)
	if out["mod"] != "" || out["cloudURLHealed"] == true {
		t.Errorf("clean box was acted on: %v", out)
	}
	if _, err := os.Stat(sdkOverridePath); err == nil {
		t.Error("an override file was created on a clean box")
	}
}

// A box that was healed and not yet restarted must be told to restart, not told
// there is nothing to remove. The files are stock, the firmware is not: that is
// one pending reboot, not a failure, and confusing the two would send the user
// round the button again.
func TestCleanupSaysRestartPendingInsteadOfNothingToDo(t *testing.T) {
	redirectSDKPaths(t)
	if detectAfterTouch() {
		t.Skip("host machine has AfterTouch artifacts under /mnt/nv")
	}
	if err := os.WriteFile(sdkOverridePath, []byte(sdkCfg(stockCloudURLs[sdkMargeTag])), 0o644); err != nil {
		t.Fatal(err)
	}
	withLiveMargeURL(t, octMargeURL)

	out := postRemoveConflictingMod(t)
	if out["cloudURLRestartPending"] != true {
		t.Errorf("no restart-pending signal: %v", out)
	}
	if out["rebootRequired"] != true {
		t.Error("the restart is the remaining step, so it must be requested")
	}
	if out["cloudURLHealed"] == true {
		t.Error("nothing was written, so nothing should be claimed as healed")
	}
	if got, _ := out["liveCloudURL"].(string); got == "" {
		t.Error("the answer must name what the firmware is still asking")
	}
}
