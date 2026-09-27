// sdkcloudurls.go: the Bose SDK's cloud URLs, where the firmware reads them
// from, and how a rival mod's leftover value is healed back to stock (#986).
//
// The firmware asks three hosts for everything that used to be the cloud: the
// marge account/preset service, the BMX service registry, and the stats
// endpoint. STR does not talk those hosts out of the box; it redirects the
// STOCK hostnames to its own listeners via /etc/hosts. So a box whose config
// names a DIFFERENT host is invisible to that redirect and silently never
// reaches STR at all: the marge stub sees no request, LOCAL_INTERNET_RADIO is
// never registered, and every preset recall falls back to the dead-cloud path
// and answers 1036 NOT_LOGGED_IN. That is exactly what #986's ST30 did for
// three days with margeServerUrl pointing at an OpenCloudTouch server
// (http://content.api.bose.io:7777) while STR listened on 9080/443.
//
// Two files carry the values, and the NAND one wins:
//
//	/mnt/nv/OverrideSdkPrivateCfg.xml        NAND, writable, authoritative
//	/opt/Bose/etc/SoundTouchSdkPrivateCfg.xml  rootfs, READ-ONLY (ubifs ro)
//
// usb-stick/install.sh (heal_sdk_cloud_urls) and usb-stick/run.sh already heal
// the override file at install and at every boot, which covers a box that HAS
// one. #986's box had none: OpenCloudTouch had edited the rootfs copy and
// stashed the pristine original as /mnt/nv/SoundTouchSdkPrivateCfg.xml.oct-backup,
// so the heal's `[ -f ]` gate made it a no-op and the bad URL survived every
// boot, the cleanup button, and a factory reset.
//
// The heal here closes that gap without touching the rootfs (remounting it rw
// is firmware bending and off the table by standing rule): it takes the box's
// OWN SDK config as the template, rewrites the three URL tags to their stock
// values, and writes the result to the NAND override path. Copying a file the
// firmware itself wrote means no schema is invented, which is what makes this
// safe on a variant nobody has measured.

package webui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/autopair"
	"github.com/JRpersonal/streborn/internal/boxapi"
)

// The three cloud URL tags, and their stock values. These are the same
// canonical strings usb-stick/install.sh and usb-stick/run.sh heal to; keep
// the three copies in step.
const (
	sdkMargeTag = "margeServerUrl"
	sdkBmxTag   = "bmxRegistryUrl"
	sdkStatsTag = "statsServerUrl"
)

var stockCloudURLs = map[string]string{
	sdkMargeTag: "https://streaming.bose.com",
	sdkBmxTag:   "https://content.api.bose.io/bmx/registry/v1/services",
	sdkStatsTag: "https://events.api.bosecm.com",
}

// sdkCloudTags in a fixed order, so every report and log line lists them the
// same way.
var sdkCloudTags = []string{sdkMargeTag, sdkBmxTag, sdkStatsTag}

// Paths are vars so the tests can point them at a temp tree.
var (
	sdkOverridePath = "/mnt/nv/OverrideSdkPrivateCfg.xml"
	sdkRootfsPath   = "/opt/Bose/etc/SoundTouchSdkPrivateCfg.xml"

	// octBackupGlob matches OpenCloudTouch's backup of the SDK config. A GLOB,
	// not a literal: v0.9.x keyed detection on
	// /mnt/nv/OverrideSdkPrivateCfg.xml.oct-backup, a name taken from a
	// SUGGESTION in issue #698 and never measured. The one box that has been
	// measured (#986, an ST30) carries
	// /mnt/nv/SoundTouchSdkPrivateCfg.xml.oct-backup, so detection missed it
	// and the cleanup button deleted nothing while reporting success.
	octBackupGlob = "/mnt/nv/*SdkPrivateCfg.xml*.oct-backup"

	// octHostsBackupPath is OpenCloudTouch's copy of the pristine /etc/hosts
	// (44 bytes, the localhost line, on #986's box). CORROBORATING only: it
	// never raises the warning on its own, because a marker that turns out to
	// be Bose-native pins a banner the user can never clear, which is the
	// v0.9.6 mistake documented in detectConflictingMod. Absent on all three
	// of the maintainer's chassis (scm ST30, sm2 ST10, taigan Portable,
	// measured 2026-09-25), so it is removed once OCT is established by the
	// SDK-config backup or a live hosts block.
	octHostsBackupPath = "/mnt/nv/hosts_backup"
)

// liveMargeURLFn is autopair's snapshot of the firmware's own answer, wrapped
// in a var so the tests can stand in for a speaker.
var liveMargeURLFn = autopair.LiveMargeURL

// sdkTagRe returns the regexp matching one cloud URL tag and its value.
func sdkTagRe(tag string) *regexp.Regexp {
	return regexp.MustCompile(`<` + tag + `>([^<]*)</` + tag + `>`)
}

// sdkCloudURLs reads the non-empty cloud URL tags out of one SDK config file.
// A missing or unreadable file yields an empty map, never an error: every
// caller treats "cannot tell" the same as "nothing to report".
func sdkCloudURLs(path string) map[string]string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, tag := range sdkCloudTags {
		if m := sdkTagRe(tag).FindSubmatch(b); m != nil {
			if v := strings.TrimSpace(string(m[1])); v != "" {
				out[tag] = v
			}
		}
	}
	return out
}

// effectiveSDKCloudURLs returns the file the firmware's cloud URLs actually
// come from and the values in it. The NAND override wins when it exists and
// carries at least one tag; otherwise the read-only rootfs config answers.
func effectiveSDKCloudURLs() (string, map[string]string) {
	if urls := sdkCloudURLs(sdkOverridePath); len(urls) > 0 {
		return sdkOverridePath, urls
	}
	if urls := sdkCloudURLs(sdkRootfsPath); len(urls) > 0 {
		return sdkRootfsPath, urls
	}
	return "", nil
}

// foreignCloudURLs returns the tags whose value is neither empty nor the stock
// host, i.e. the ones STR's /etc/hosts redirect can never catch.
func foreignCloudURLs(urls map[string]string) map[string]string {
	out := map[string]string{}
	for tag, v := range urls {
		if v != "" && v != stockCloudURLs[tag] {
			out[tag] = v
		}
	}
	return out
}

// foreignCloudURLSummary describes the foreign tags in one short line for a
// log, a report field or the app banner, or "" when every tag is stock.
func foreignCloudURLSummary(urls map[string]string) string {
	foreign := foreignCloudURLs(urls)
	if len(foreign) == 0 {
		return ""
	}
	parts := make([]string, 0, len(foreign))
	for tag, v := range foreign {
		parts = append(parts, tag+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// foreignCloudURLFiles reports what the CONFIG FILES say, "" when they are
// stock. This is the view the repair can act on. Cheap: two file reads, no
// request to the speaker.
func foreignCloudURLFiles() string {
	_, urls := effectiveSDKCloudURLs()
	return foreignCloudURLSummary(urls)
}

// liveForeignCloudURL reports the cloud host the FIRMWARE last said it was
// using, when that host is not the stock one. Free: autopair already reads
// /info every few minutes and records the value, so nothing extra is asked of
// the speaker.
func liveForeignCloudURL() string {
	live := liveMargeURLFn()
	if live == "" || live == stockCloudURLs[sdkMargeTag] {
		return ""
	}
	return sdkMargeTag + "=" + live
}

// foreignCloudURL reports a cloud address STR's redirect cannot catch, "" on a
// healthy box. The FIRMWARE's own answer wins when there is one: a repair is
// written to a config the firmware reads once, at boot, so a box that has been
// healed but not restarted is still asking the dead host right now, and saying
// otherwise would repeat the very "it is fixed" claim this whole repair path
// was built to stop making. The files answer only when the firmware has not
// been heard from yet.
func foreignCloudURL() string {
	_, urls := effectiveSDKCloudURLs()
	parts := make([]string, 0, len(sdkCloudTags))

	// margeServerUrl: the firmware's own answer wins when there is one.
	if live := liveMargeURLFn(); live != "" {
		if live != stockCloudURLs[sdkMargeTag] {
			parts = append(parts, sdkMargeTag+"="+live)
		}
	} else if v := urls[sdkMargeTag]; v != "" && v != stockCloudURLs[sdkMargeTag] {
		parts = append(parts, sdkMargeTag+"="+v)
	}

	// bmxRegistryUrl and statsServerUrl: /info reports neither, so the config
	// files are the only view there is. Judging them by the live margeURL, as
	// this function did at first, meant a box whose BMX registry pointed at a
	// mod's server was reported healthy the moment its marge host happened to
	// be stock, which is the commonest half-migrated shape there is.
	for _, tag := range []string{sdkBmxTag, sdkStatsTag} {
		if v := urls[tag]; v != "" && v != stockCloudURLs[tag] {
			parts = append(parts, tag+"="+v)
		}
	}
	return strings.Join(parts, " ")
}

// healedSDKConfig rewrites every non-empty cloud URL tag in an SDK config to
// its stock value and reports how many it changed. Empty tags are left alone
// (the firmware treats an empty value as "use the built-in default", and
// filling it in would be a guess), and an already-stock tag is rewritten to
// the same bytes, so the transform is idempotent.
func healedSDKConfig(in []byte) ([]byte, int) {
	out := in
	changed := 0
	for _, tag := range sdkCloudTags {
		re := sdkTagRe(tag)
		out = re.ReplaceAllFunc(out, func(m []byte) []byte {
			sub := re.FindSubmatch(m)
			cur := strings.TrimSpace(string(sub[1]))
			if cur == "" || cur == stockCloudURLs[tag] {
				return m
			}
			changed++
			return []byte("<" + tag + ">" + stockCloudURLs[tag] + "</" + tag + ">")
		})
	}
	return out, changed
}

// octBackupFiles returns OpenCloudTouch's SDK-config backups on NAND, newest
// name first for a stable report order.
func octBackupFiles() []string {
	m, err := filepath.Glob(octBackupGlob)
	if err != nil {
		return nil
	}
	sort.Strings(m)
	return m
}

// sdkCloudHealResult says what healSDKCloudURLs did, so the handler can report
// it and the app can tell the user whether a restart is now needed.
type sdkCloudHealResult struct {
	Healed bool   // the override file was written with stock URLs
	Path   string // the file written
	From   string // the template it was built from
	Tags   int    // how many tags were rewritten
	Note   string // why nothing was done, when nothing was done
	// Foreign is what the config files still say after the heal: "" means the
	// repair is complete as far as anything on disk goes.
	Foreign string
	// RestartPending means the files are already stock but the running firmware
	// is still on the old host, because it reads its config once, at boot. That
	// is a repair waiting for a restart, not a failed one, and telling the two
	// apart is the difference between "press it again" and "restart the box".
	RestartPending bool
}

// healSDKCloudURLs points the box's cloud URLs back at the stock hosts so
// STR's /etc/hosts redirect catches them again (#986).
//
// Three cases, in order:
//
//  1. The NAND override exists and carries a foreign host: rewrite it in
//     place. (install.sh and run.sh already do this at install and at boot;
//     doing it here too makes the button work without waiting for a reboot.)
//  2. No override, but the box's own SDK config (OpenCloudTouch's pristine
//     backup if it left one, otherwise the rootfs copy the mod edited) is
//     readable: heal a copy of it into the override path. The rootfs itself is
//     never written.
//  3. Nothing readable to work from: report that and change nothing.
//
// The result is in effect after the next restart, because the firmware reads
// the SDK config once at boot. Idempotent: a box whose URLs are already stock
// comes back Healed=false with an empty Note.
func healSDKCloudURLs() sdkCloudHealResult {
	res := sdkCloudHealResult{}
	src, urls := effectiveSDKCloudURLs()
	if src == "" {
		res.Note = "no SDK config found at " + sdkOverridePath + " or " + sdkRootfsPath
		return res
	}
	before := foreignCloudURLs(urls)
	if len(before) == 0 {
		// Nothing left to write. If the firmware is still naming the old host,
		// it simply has not re-read the config yet.
		res.RestartPending = liveForeignCloudURL() != ""
		return res
	}

	// The template. An EXISTING override is healed in place: it may carry
	// other settings (envswitch writes this file too) and replacing it
	// wholesale would drop them. Only when there is no override does a
	// template have to be chosen, and then OpenCloudTouch's own pristine
	// backup is the best one available, because it is the config as Bose
	// shipped it. Last resort is the rootfs copy the mod edited: only the
	// three URL tags are rewritten, so every other line stays byte-identical.
	template := src
	if src != sdkOverridePath {
		for _, b := range octBackupFiles() {
			if len(sdkCloudURLs(b)) > 0 {
				template = b
				break
			}
		}
	}
	content, err := os.ReadFile(template)
	if err != nil {
		res.Note = "could not read " + template + ": " + err.Error()
		return res
	}
	healed, _ := healedSDKConfig(content)
	if err := writeNANDFile(sdkOverridePath, healed); err != nil {
		res.Note = "could not write " + sdkOverridePath + ": " + err.Error()
		return res
	}
	res.Healed = true
	res.Path = sdkOverridePath
	res.From = template
	_, after := effectiveSDKCloudURLs()
	res.Foreign = foreignCloudURLSummary(after)
	// What the write achieved, counted against the state it started from, so a
	// pristine template that needed no rewrite still reports the tags it fixed.
	for tag := range before {
		if after[tag] == stockCloudURLs[tag] {
			res.Tags++
		}
	}
	return res
}

// writeNANDFile writes content via a sibling temp file and a rename, so a power
// cut during the write cannot leave the firmware reading half a config.
//
// There is deliberately NO truncate-in-place fallback. It was copied from
// hosts.writeAtomic, which needs one because /etc/hosts is a tmpfs file over a
// read-only rootfs where rename cannot work. /mnt/nv is ordinary read-write
// UBIFS, so a failed write there means the volume is full or read-only, and
// truncating the live SDK config at that moment would destroy the box's only
// copy of its cloud configuration to replace it with nothing. The temp file is
// removed on every failure path, including a partial write, so a full NAND is
// not left carrying a stray .new either.
func writeNANDFile(path string, content []byte) error {
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	// fsync before the rename: on UBIFS the rename can otherwise be durable
	// while the bytes behind it are not, which is the one way this could still
	// hand the firmware an empty config after a power cut.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// sdkCloudURLDebug is the /api/debug/state section for the cloud URLs: which
// file governs, what it says, and whether STR's redirect can catch it. #986
// took three days and a hand-read of /info to establish, because the bundle
// carried neither the values nor the file they came from.
func (s *Server) sdkCloudURLDebug() map[string]any {
	out := map[string]any{}
	// The firmware's OWN live view, next to what the files say. They differ
	// exactly when a heal has been written but the box has not restarted yet,
	// which is the one state a reader of this bundle must not mistake for a
	// failed repair. One plain GET at bundle time, no polling; only the URL is
	// taken from the response, never the body (it carries the SCM MAC).
	if s.boxHost != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if info, err := boxapi.New(s.boxHost).GetInfo(ctx); err == nil {
			out["liveMargeURL"] = info.MargeURL
		} else {
			out["liveMargeURLErr"] = err.Error()
		}
	}
	src, urls := effectiveSDKCloudURLs()
	out["source"] = src
	out["urls"] = urls
	out["overridePresent"] = fileExists(sdkOverridePath)
	out["rootfsPresent"] = fileExists(sdkRootfsPath)
	if live := liveMargeURLFn(); live != "" {
		out["autopairMargeURL"] = live
	}
	if s := foreignCloudURLSummary(urls); s != "" {
		out["foreign"] = s
		out["note"] = fmt.Sprintf("STR redirects only the stock hosts, so the box never reaches STR with these values; heal via %s", sdkOverridePath)
	}
	if backups := octBackupFiles(); len(backups) > 0 {
		out["octBackups"] = backups
	}
	if fileExists(octHostsBackupPath) {
		out["octHostsBackup"] = octHostsBackupPath
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
