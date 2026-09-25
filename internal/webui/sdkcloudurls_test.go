// Tests for the Bose SDK cloud URL heal (#986). Every fixture here is the
// state measured on the reporter's ST30: no /mnt/nv/OverrideSdkPrivateCfg.xml,
// a rootfs config whose margeServerUrl points at an OpenCloudTouch host, and
// the mod's pristine backup on NAND under a filename the code did not match.

package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The reporter's value, verbatim from #986's /info.
const octMargeURL = "http://content.api.bose.io:7777"

func sdkCfg(marge string) string {
	return `<?xml version="1.0"?>
<SdkPrivateCfg>
  <margeServerUrl>` + marge + `</margeServerUrl>
  <bmxRegistryUrl>https://content.api.bose.io/bmx/registry/v1/services</bmxRegistryUrl>
  <statsServerUrl>https://events.api.bosecm.com</statsServerUrl>
  <someOtherSetting>keep me</someOtherSetting>
</SdkPrivateCfg>
`
}

// redirectSDKPaths points the cloud URL code at a temp tree.
func redirectSDKPaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldOv, oldRf, oldG, oldHB := sdkOverridePath, sdkRootfsPath, octBackupGlob, octHostsBackupPath
	sdkOverridePath = filepath.Join(dir, "OverrideSdkPrivateCfg.xml")
	sdkRootfsPath = filepath.Join(dir, "SoundTouchSdkPrivateCfg.xml")
	octBackupGlob = filepath.Join(dir, "*SdkPrivateCfg.xml*.oct-backup")
	octHostsBackupPath = filepath.Join(dir, "hosts_backup")
	t.Cleanup(func() {
		sdkOverridePath, sdkRootfsPath, octBackupGlob, octHostsBackupPath = oldOv, oldRf, oldG, oldHB
	})
	return dir
}

// The measured filename must be found. The literal the code carried until now
// (OverrideSdkPrivateCfg.xml.oct-backup) came from a suggestion in #698 and
// exists on no box; #986's ST30 carries SoundTouchSdkPrivateCfg.xml.oct-backup,
// so the old detection missed it and the cleanup deleted nothing.
func TestOCTBackupIsMatchedByPatternNotByOneGuessedName(t *testing.T) {
	dir := redirectSDKPaths(t)
	for _, name := range []string{
		"SoundTouchSdkPrivateCfg.xml.oct-backup",   // measured, #986
		"OverrideSdkPrivateCfg.xml.oct-backup",     // the old guess, still matched
		"SoundTouchSdkPrivateCfg.xml.1.oct-backup", // a second-run variant
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(sdkCfg(stockCloudURLs[sdkMargeTag])), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := octBackupFiles(); len(got) == 0 {
			t.Errorf("%s not matched by the glob", name)
		}
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	// An unrelated NAND file must never match.
	if err := os.WriteFile(filepath.Join(dir, "rc.local"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := octBackupFiles(); len(got) != 0 {
		t.Errorf("unrelated file matched: %v", got)
	}
}

// The NAND override wins over the read-only rootfs copy: that is why healing
// the override is enough and why the rootfs never has to be written.
func TestTheNANDOverrideDecidesWhichCloudURLCounts(t *testing.T) {
	redirectSDKPaths(t)
	if err := os.WriteFile(sdkRootfsPath, []byte(sdkCfg(octMargeURL)), 0o644); err != nil {
		t.Fatal(err)
	}
	src, urls := effectiveSDKCloudURLs()
	if src != sdkRootfsPath || urls[sdkMargeTag] != octMargeURL {
		t.Fatalf("rootfs config ignored: src=%s urls=%v", src, urls)
	}
	if foreignCloudURL() == "" {
		t.Error("the mod's host in the rootfs config must be reported as foreign")
	}
	if err := os.WriteFile(sdkOverridePath, []byte(sdkCfg(stockCloudURLs[sdkMargeTag])), 0o644); err != nil {
		t.Fatal(err)
	}
	src, _ = effectiveSDKCloudURLs()
	if src != sdkOverridePath {
		t.Errorf("override present but src=%s", src)
	}
	if f := foreignCloudURL(); f != "" {
		t.Errorf("stock override still reported foreign: %s", f)
	}
}

// #986's exact state: no override, the mod's host in the rootfs config, and the
// mod's pristine backup on NAND. The heal must write a NAND override built
// from that backup, and must never touch the read-only rootfs file.
func TestHealBuildsTheOverrideFromTheModsOwnBackup(t *testing.T) {
	redirectSDKPaths(t)
	rootfsBefore := sdkCfg(octMargeURL)
	if err := os.WriteFile(sdkRootfsPath, []byte(rootfsBefore), 0o644); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(filepath.Dir(sdkRootfsPath), "SoundTouchSdkPrivateCfg.xml.oct-backup")
	if err := os.WriteFile(backup, []byte(sdkCfg(stockCloudURLs[sdkMargeTag])), 0o644); err != nil {
		t.Fatal(err)
	}

	res := healSDKCloudURLs()
	if !res.Healed {
		t.Fatalf("heal did nothing: %+v", res)
	}
	if res.Path != sdkOverridePath {
		t.Errorf("wrote %s, want the NAND override %s", res.Path, sdkOverridePath)
	}
	if res.From != backup {
		t.Errorf("template was %s, want the mod's pristine backup %s", res.From, backup)
	}
	if res.Foreign != "" {
		t.Errorf("still foreign after the heal: %s", res.Foreign)
	}
	if got := foreignCloudURL(); got != "" {
		t.Errorf("box still reports a foreign cloud URL: %s", got)
	}
	// The rootfs is read-only on the box; writing it would be firmware bending.
	if b, err := os.ReadFile(sdkRootfsPath); err != nil || string(b) != rootfsBefore {
		t.Error("the heal modified the rootfs config")
	}
	// Everything but the URL tags survives.
	b, err := os.ReadFile(sdkOverridePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "keep me") {
		t.Error("the heal dropped unrelated settings from the config")
	}
}

// No backup left behind: the rootfs copy the mod edited is still a valid
// config, so it serves as the template and only the URL tags are rewritten.
func TestHealFallsBackToTheRootfsConfigWhenNoBackupExists(t *testing.T) {
	redirectSDKPaths(t)
	if err := os.WriteFile(sdkRootfsPath, []byte(sdkCfg(octMargeURL)), 0o644); err != nil {
		t.Fatal(err)
	}
	res := healSDKCloudURLs()
	if !res.Healed || res.From != sdkRootfsPath {
		t.Fatalf("heal did not use the rootfs config: %+v", res)
	}
	_, urls := effectiveSDKCloudURLs()
	if urls[sdkMargeTag] != stockCloudURLs[sdkMargeTag] {
		t.Errorf("margeServerUrl = %q, want stock", urls[sdkMargeTag])
	}
}

// An existing override is healed IN PLACE. Replacing it with a backup would
// drop the other settings it carries (envswitch writes this file too).
func TestHealKeepsAnExistingOverridesOtherSettings(t *testing.T) {
	redirectSDKPaths(t)
	override := strings.Replace(sdkCfg(octMargeURL), "keep me", "override-only value", 1)
	if err := os.WriteFile(sdkOverridePath, []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(filepath.Dir(sdkOverridePath), "SoundTouchSdkPrivateCfg.xml.oct-backup")
	if err := os.WriteFile(backup, []byte(sdkCfg(stockCloudURLs[sdkMargeTag])), 0o644); err != nil {
		t.Fatal(err)
	}
	res := healSDKCloudURLs()
	if !res.Healed || res.From != sdkOverridePath {
		t.Fatalf("existing override was not healed in place: %+v", res)
	}
	b, err := os.ReadFile(sdkOverridePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "override-only value") {
		t.Error("healing replaced the override instead of editing it")
	}
	if !strings.Contains(string(b), stockCloudURLs[sdkMargeTag]) {
		t.Error("the mod's host survived the heal")
	}
}

// A box with nothing to read from must say so instead of writing a config it
// invented. Guessing the schema is the one thing that could stop BoseApp.
func TestHealReportsWhenThereIsNothingToWorkFrom(t *testing.T) {
	redirectSDKPaths(t)
	res := healSDKCloudURLs()
	if res.Healed {
		t.Error("heal claimed success with no SDK config present")
	}
	if res.Note == "" {
		t.Error("heal gave no reason for doing nothing")
	}
	if _, err := os.Stat(sdkOverridePath); err == nil {
		t.Error("heal invented an override file out of nothing")
	}
}

// A healthy box is left alone entirely, and the heal is idempotent.
func TestHealIsANoOpOnAStockBox(t *testing.T) {
	redirectSDKPaths(t)
	stock := sdkCfg(stockCloudURLs[sdkMargeTag])
	if err := os.WriteFile(sdkOverridePath, []byte(stock), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := healSDKCloudURLs(); res.Healed || res.Note != "" {
		t.Errorf("stock box touched: %+v", res)
	}
	b, err := os.ReadFile(sdkOverridePath)
	if err != nil || string(b) != stock {
		t.Error("stock override was rewritten")
	}
	// And the transform itself changes nothing when everything is already stock.
	if out, n := healedSDKConfig([]byte(stock)); n != 0 || string(out) != stock {
		t.Errorf("healedSDKConfig touched a stock config (n=%d)", n)
	}
}

// An empty tag means "use the firmware default" and must stay empty: filling
// it in would be a guess about a box nobody has measured.
func TestHealLeavesEmptyTagsAlone(t *testing.T) {
	in := "<margeServerUrl></margeServerUrl><statsServerUrl>" + octMargeURL + "</statsServerUrl>"
	out, n := healedSDKConfig([]byte(in))
	if n != 1 {
		t.Errorf("changed %d tags, want 1", n)
	}
	if !strings.Contains(string(out), "<margeServerUrl></margeServerUrl>") {
		t.Error("an empty tag was filled in")
	}
	if !strings.Contains(string(out), stockCloudURLs[sdkStatsTag]) {
		t.Error("the foreign stats host was not healed")
	}
}

// The bundle must carry the values, the file they come from, and the flag.
// #986 took three days and a hand-read of /info because it carried none.
func TestTheBundleShowsTheCloudURLsAndFlagsAForeignOne(t *testing.T) {
	redirectSDKPaths(t)
	if err := os.WriteFile(sdkRootfsPath, []byte(sdkCfg(octMargeURL)), 0o644); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(filepath.Dir(sdkRootfsPath), "SoundTouchSdkPrivateCfg.xml.oct-backup")
	if err := os.WriteFile(backup, []byte(sdkCfg(stockCloudURLs[sdkMargeTag])), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	sec := s.sdkCloudURLDebug()
	if sec["source"] != sdkRootfsPath {
		t.Errorf("source = %v, want %s", sec["source"], sdkRootfsPath)
	}
	urls, _ := sec["urls"].(map[string]string)
	if urls[sdkMargeTag] != octMargeURL {
		t.Errorf("urls = %v, want the mod's host", urls)
	}
	f, _ := sec["foreign"].(string)
	if !strings.Contains(f, octMargeURL) {
		t.Errorf("foreign = %q, want the offending value", f)
	}
	backups, _ := sec["octBackups"].([]string)
	if len(backups) != 1 {
		t.Errorf("octBackups = %v, want the one backup", backups)
	}
}

// withLiveMargeURL stands in for the firmware's own answer, which autopair
// records from the /info body it already fetches.
func withLiveMargeURL(t *testing.T, v string) {
	t.Helper()
	old := liveMargeURLFn
	liveMargeURLFn = func() string { return v }
	t.Cleanup(func() { liveMargeURLFn = old })
}

// The firmware's own answer decides. A heal writes a config the firmware reads
// once, at boot, so a healed-but-unrestarted box is STILL asking the dead host,
// and reporting it healthy would be the same false "it is fixed" that #986 was
// about one layer down.
func TestTheFirmwaresOwnAnswerBeatsTheHealedFile(t *testing.T) {
	redirectSDKPaths(t)
	if err := os.WriteFile(sdkOverridePath, []byte(sdkCfg(stockCloudURLs[sdkMargeTag])), 0o644); err != nil {
		t.Fatal(err)
	}
	withLiveMargeURL(t, octMargeURL)
	if foreignCloudURLFiles() != "" {
		t.Fatal("the files are stock, so the file view must be clean")
	}
	if got := foreignCloudURL(); got == "" {
		t.Error("a box still naming the dead host must not read as healthy")
	}
	// And the heal says which of the two states this is.
	res := healSDKCloudURLs()
	if res.Healed {
		t.Error("nothing was left to write, so nothing should have been written")
	}
	if !res.RestartPending {
		t.Error("a repair waiting for a restart must be distinguishable from a failed one")
	}
}

// Once the speaker itself reports the stock host, the warning has to clear even
// if some file still mentions the old one: the firmware is the authority on
// what it is actually doing.
func TestTheWarningClearsWhenTheSpeakerReportsTheStockHost(t *testing.T) {
	redirectSDKPaths(t)
	if err := os.WriteFile(sdkRootfsPath, []byte(sdkCfg(octMargeURL)), 0o644); err != nil {
		t.Fatal(err)
	}
	withLiveMargeURL(t, stockCloudURLs[sdkMargeTag])
	if got := foreignCloudURL(); got != "" {
		t.Errorf("foreignCloudURL = %q although the speaker reports the stock host", got)
	}
	// The files are still the repair's business, though.
	if foreignCloudURLFiles() == "" {
		t.Error("the file view must still show what is on disk")
	}
}

// No answer from the speaker yet (fresh agent, box unreachable): fall back to
// the files rather than reporting a clean box on no evidence.
func TestWithNoAnswerFromTheSpeakerTheFilesDecide(t *testing.T) {
	redirectSDKPaths(t)
	withLiveMargeURL(t, "")
	if err := os.WriteFile(sdkRootfsPath, []byte(sdkCfg(octMargeURL)), 0o644); err != nil {
		t.Fatal(err)
	}
	if foreignCloudURL() == "" {
		t.Error("with no live answer the config files must still raise the flag")
	}
}
