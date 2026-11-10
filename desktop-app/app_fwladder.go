package main

// The firmware ladder: the step-by-step update for a speaker whose firmware is
// too old to take Bose's last file in one jump (bosefw/ladder.go has the why
// and the table).
//
// Each in-between step goes by USB stick. When the user clicks "Prepare
// stick", the user's own PC downloads the step's zip from the Internet Archive
// (archive.org, the only host besides Bose's that the app fetches firmware
// from), checks the zip's size and sha1 against the archive's own metadata,
// takes the single Update.stu out of it, checks that file's size and CRC-32 (and
// sha1 where known), and writes it to the stick the user picked, through the
// same drive checks the install stick uses. STR never mirrors or redistributes
// a firmware image: the zip stays in the user's cache folder, the extracted file
// is deleted once it is on the stick. Before the write, STR's own install files
// come off the stick (clearFirmwareStick): Bose's updater wants Update.stu
// alone on it. The user can also download the zip in the
// browser and hand it to the app ("I already downloaded the file"); the same
// checks run on it.
//
// The final step, Bose's own 27.0.6, keeps the one-click network update
// (FlashBoseFirmware), with the stick as a fallback.

import (
	"archive/zip"
	"context"
	"crypto/sha1" // #nosec G505 -- sha1 is what the Internet Archive publishes; integrity check, not security
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/JRpersonal/streborn/sticksetup"
	"streborn-app/bosefw"
)

// fwLadderEvent carries the stick preparation's progress, with the same phase
// words as "firmware:progress" plus extract, write and eject.
const fwLadderEvent = "fwladder:progress"

const (
	fwPhaseExtract = "extract"
	fwPhaseWrite   = "write"
	fwPhaseEject   = "eject"
)

// fwArchivePrefix is the only archive.org address a ladder zip is fetched
// from; a var for tests.
var fwArchivePrefix = bosefw.ArchiveItemBase

// fwArchiveRedirectOK decides whether a download from fwArchivePrefix may
// follow a redirect to u. archive.org sends every download on to one of its
// storage servers (iaNNNNNN.us.archive.org and the like); nothing else is
// followed. A var for tests.
var fwArchiveRedirectOK = func(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	return u.Scheme == "https" && (host == "archive.org" || strings.HasSuffix(host, ".archive.org"))
}

// fwArchiveCacheDir is where archive zips are kept, beside Bose's files.
var fwArchiveCacheDir = func() (string, error) {
	dir, err := fwCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "archive"), nil
}

// The stick side, as vars so a test can use a temp folder as the stick. The
// real ones only accept a volume ListDrives offers as removable.
var (
	fwStickCheck          = sticksetup.CheckStick
	fwStickEject          = sticksetup.Eject
	fwStickPartitionStyle = sticksetup.PartitionStyle
)

// fwLadderRead reads a speaker's /info; a var for tests.
var fwLadderRead = func(a *App, host string) (FirmwareInfo, error) { return a.GetBoxFirmware(host) }

// errFingerprint marks a file that is not the one the ladder expects. The app
// then shows "This file does not match the expected fingerprint", and nothing
// was written to the stick.
var errFingerprint = errors.New("the file does not match its known fingerprint")

// FirmwareStickResult is the outcome of preparing a stick.
type FirmwareStickResult struct {
	OK bool `json:"ok"`
	// Code says what failed: "fingerprint" (a file check), "stick" (Detail is
	// the stick problem: not-fat32, cluster-too-big, no-space, gone, ...),
	// "download", "write", "cancelled", "busy", "step" (unknown step),
	// "file-type" (a picked file that is neither .zip nor .stu).
	Code    string `json:"code,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Ejected bool   `json:"ejected"`
}

// FirmwareRecheck is what the speaker reports after a step.
type FirmwareRecheck struct {
	Found      bool   `json:"found"`
	Version    string `json:"version"` // short, e.g. "7.0.37"
	Via        string `json:"via"`     // "lan", "setup-ap" or "unknown"
	Model      string `json:"model"`
	ModuleType string `json:"moduleType"`
	DeviceID   string `json:"deviceId"`
}

var fwLadderRun struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

// firmwareLadderNote is added to the "update the firmware first" install
// message when one update is not enough for this speaker, so the copied
// failure report says so too. Empty otherwise.
//
// It only ever follows firmwareTooOldNote, which below 15 already states the
// rule (no single jump, in-between updates one after the other), so this
// note adds only what is particular to the plan: how many steps, whether they
// are checked, or that no path is known.
func firmwareLadderNote(model, moduleType, variant, version string) string {
	p := bosefw.Plan(model, moduleType, variant, version, nil)
	switch p.Status {
	case bosefw.StatusLadder:
		return fmt.Sprintf(" The firmware update guide below walks you through the %d updates this speaker needs.", len(p.Steps))
	case bosefw.StatusNeedsBluetooth:
		return " The firmware update guide below asks one question about this speaker and then shows the steps."
	case bosefw.StatusRoute:
		return fmt.Sprintf(" The firmware update guide below lists the %d updates this speaker probably needs. They are not checked on this model yet, so please read the guide before you start.", len(p.Steps))
	case bosefw.StatusUnsupported:
		return " There is no known step-by-step path for this speaker yet. Please read the firmware update guide below before you try anything."
	}
	return ""
}

// readLadderSpeaker tries the speaker on the home network, then on its own
// setup network (192.168.1.1), the two places an old speaker can answer.
func (a *App) readLadderSpeaker(host string) (FirmwareInfo, string, bool) {
	tries := []struct{ host, via string }{}
	if h := strings.TrimSpace(host); h != "" {
		tries = append(tries, struct{ host, via string }{h, "lan"})
	}
	if strings.TrimSpace(host) != SetupAPHost {
		tries = append(tries, struct{ host, via string }{SetupAPHost, "setup-ap"})
	}
	for _, t := range tries {
		fi, err := fwLadderRead(a, t.host)
		if err == nil && fi.Reachable && fi.Firmware != "" {
			return fi, t.via, true
		}
	}
	return FirmwareInfo{}, "unknown", false
}

// GetFirmwareLadder works out the ladder for the speaker at host. When neither
// the home network nor the setup network answers, the plan says
// "needs-version" and the app asks the user.
func (a *App) GetFirmwareLadder(host string) bosefw.LadderPlan {
	fi, via, ok := a.readLadderSpeaker(host)
	if !ok {
		return bosefw.Plan("", "", "", "", nil)
	}
	p := bosefw.Plan(fi.Model, fi.ModuleType, fi.Variant, fi.Firmware, nil)
	p.DeviceID = fi.DeviceID
	p.VersionFrom = via
	a.fwLog().Info("firmware ladder: plan", "via", via, "model", fi.Model, "moduleType", fi.ModuleType,
		"version", fi.Firmware, "status", p.Status, "steps", len(p.Steps))
	return p
}

// GetFirmwareLadderManual works out the ladder from what the user tells: the
// model, the version (from the dropdown, or read off the speaker with 5 and
// Volume minus), and bluetooth "yes", "no" or "" for the one question the
// ladder may need.
func (a *App) GetFirmwareLadderManual(model, moduleType, version, bluetooth string) bosefw.LadderPlan {
	var bt *bool
	switch strings.ToLower(strings.TrimSpace(bluetooth)) {
	case "yes":
		v := true
		bt = &v
	case "no":
		v := false
		bt = &v
	}
	p := bosefw.Plan(model, moduleType, "", version, bt)
	p.VersionFrom = "manual"
	return p
}

// RecheckFirmware reads the version again after a step: home network first,
// then the setup network.
func (a *App) RecheckFirmware(host string) FirmwareRecheck {
	fi, via, ok := a.readLadderSpeaker(host)
	if !ok {
		return FirmwareRecheck{Via: "unknown"}
	}
	if via == "lan" && fi.Short != "" && !firmwareTooOldForInstall(fi) {
		// The ladder has brought the speaker far enough: the setup wizard may
		// write the STR install stick for it again.
		forgetFirmwareTooOld(host)
	}
	return FirmwareRecheck{Found: true, Version: fi.Short, Via: via, Model: fi.Model, ModuleType: fi.ModuleType, DeviceID: fi.DeviceID}
}

// PickFirmwareFile lets the user pick a zip or Update.stu downloaded in the
// browser. Empty when the dialog was cancelled.
func (a *App) PickFirmwareFile() (string, error) {
	if a.ctx == nil {
		return "", fmt.Errorf("no window")
	}
	return wailsrt.OpenFileDialog(a.ctx, wailsrt.OpenDialogOptions{
		Title:   "Firmware file",
		Filters: []wailsrt.FileFilter{{DisplayName: "Firmware (*.zip, *.stu)", Pattern: "*.zip;*.stu"}},
	})
}

// CancelFirmwareStick stops a running stick preparation. A half-written file
// never stays on the stick.
func (a *App) CancelFirmwareStick() {
	fwLadderRun.mu.Lock()
	defer fwLadderRun.mu.Unlock()
	if fwLadderRun.cancel != nil {
		fwLadderRun.cancel()
	}
}

// PrepareFirmwareStick downloads (or takes from the cache) the file for
// stepID, checks it and writes it to the stick at drivePath as Update.stu.
func (a *App) PrepareFirmwareStick(stepID, drivePath string) FirmwareStickResult {
	return a.prepareFirmwareStick(stepID, drivePath, "")
}

// PrepareFirmwareStickFromFile does the same with a file the user downloaded
// in the browser: a zip from the Internet Archive or a plain Update.stu.
func (a *App) PrepareFirmwareStickFromFile(stepID, drivePath, localPath string) FirmwareStickResult {
	return a.prepareFirmwareStick(stepID, drivePath, localPath)
}

func (a *App) prepareFirmwareStick(stepID, drivePath, localPath string) FirmwareStickResult {
	step, ok := bosefw.StepByID(stepID)
	if !ok {
		return FirmwareStickResult{Code: "step", Detail: stepID}
	}
	fwLadderRun.mu.Lock()
	if fwLadderRun.cancel != nil {
		fwLadderRun.mu.Unlock()
		return FirmwareStickResult{Code: "busy"}
	}
	ctx, cancel := context.WithCancel(a.appCtx())
	fwLadderRun.cancel = cancel
	fwLadderRun.mu.Unlock()
	defer func() {
		cancel()
		fwLadderRun.mu.Lock()
		fwLadderRun.cancel = nil
		fwLadderRun.mu.Unlock()
	}()

	a.fwLog().Info("firmware ladder: preparing stick", "step", step.ID, "drive", drivePath, "fromFile", localPath != "")
	res := a.runPrepareStick(ctx, step, drivePath, localPath)
	if ctx.Err() != nil && !res.OK {
		res = FirmwareStickResult{Code: "cancelled"}
	}
	if res.OK {
		a.fwLog().Info("firmware ladder: stick ready", "step", step.ID, "ejected", res.Ejected)
	} else {
		a.fwLog().Warn("firmware ladder: stick not prepared", "step", step.ID, "code", res.Code, "detail", res.Detail)
	}
	return res
}

func (a *App) runPrepareStick(ctx context.Context, step bosefw.Step, drivePath, localPath string) FirmwareStickResult {
	fail := func(code string, err error) FirmwareStickResult {
		if errors.Is(err, errFingerprint) {
			code = "fingerprint"
		}
		return FirmwareStickResult{Code: code, Detail: err.Error()}
	}
	drivePath = strings.TrimSpace(drivePath)
	if drivePath == "" {
		return FirmwareStickResult{Code: "stick", Detail: "gone"}
	}
	first := fwStickCheck(drivePath)
	if p := sticksetup.FirmwareStickProblem(first, step.InnerSize, fwStickReclaimable(drivePath)); p != "" {
		return FirmwareStickResult{Code: "stick", Detail: p}
	}

	var stu string   // the checked Update.stu to write
	var tempStu bool // stu was extracted by us and goes away afterwards
	switch {
	case localPath != "":
		ext := strings.ToLower(filepath.Ext(localPath))
		switch ext {
		case ".zip":
			out, err := a.extractToCache(ctx, step, localPath)
			if err != nil {
				return fail("fingerprint", err)
			}
			stu, tempStu = out, true
		case ".stu":
			a.emitLadder(step.ID, fwPhaseVerify, 0, step.InnerSize)
			if err := verifyFile(localPath, fileFingerprint{size: step.InnerSize, crc: step.InnerCRC32, checkCRC: true, sha1: step.InnerSHA1}); err != nil {
				return fail("fingerprint", err)
			}
			stu = localPath
		default:
			return FirmwareStickResult{Code: "file-type", Detail: ext}
		}
	case step.Source == bosefw.SourceBose:
		a.emitLadder(step.ID, fwPhaseDownload, 0, step.InnerSize)
		prog := a.ladderProgress(step.ID, fwPhaseDownload, step.InnerSize)
		p, err := a.fetchBoseFileWith(ctx, "", bosefw.File{Name: step.FileName, URL: step.URL, Length: step.InnerSize, CRC: step.InnerCRC32}, prog)
		if err != nil {
			if errors.Is(err, errBoseFileMismatch) {
				return FirmwareStickResult{Code: "fingerprint", Detail: err.Error()}
			}
			return fail("download", err)
		}
		stu = p
	default:
		zipPath, err := a.fetchArchiveZip(ctx, step)
		if err != nil {
			return fail("download", err)
		}
		out, err := a.extractToCache(ctx, step, zipPath)
		if err != nil {
			return fail("fingerprint", err)
		}
		stu, tempStu = out, true
	}
	if tempStu {
		defer os.Remove(stu)
	}
	if ctx.Err() != nil {
		return FirmwareStickResult{Code: "cancelled"}
	}
	// The download and the extract can take minutes. Check the stick again
	// right before writing, so a stick pulled or swapped in the meantime is
	// never written to (the write replaces any Update.stu on it). A stick of
	// another size or file system under the same drive path counts as gone.
	again := fwStickCheck(drivePath)
	if p := sticksetup.FirmwareStickProblem(again, step.InnerSize, fwStickReclaimable(drivePath)); p != "" {
		return FirmwareStickResult{Code: "stick", Detail: p}
	}
	if again.TotalBytes != first.TotalBytes || !strings.EqualFold(again.Filesystem, first.Filesystem) {
		a.fwLog().Warn("firmware ladder: the stick changed during the download, not writing",
			"drive", drivePath, "before", first.TotalBytes, "now", again.TotalBytes)
		return FirmwareStickResult{Code: "stick", Detail: "gone"}
	}

	a.logFirmwareStickLayout(step.ID, drivePath, again)
	a.clearFirmwareStick(step.ID, drivePath)

	a.emitLadder(step.ID, fwPhaseWrite, 0, step.InnerSize)
	prog := a.ladderProgress(step.ID, fwPhaseWrite, step.InnerSize)
	if werr := sticksetup.WriteFirmwareFile(drivePath, stu, step.InnerSize, step.InnerCRC32, prog); werr != nil {
		return fail("write", werr)
	}
	a.emitLadder(step.ID, fwPhaseEject, 0, 0)
	res := FirmwareStickResult{OK: true}
	if err := fwStickEject(drivePath); err == nil {
		res.Ejected = true
	} else {
		a.fwLog().Info("firmware ladder: eject failed, the user removes the stick safely by hand", "err", err)
	}
	a.emitLadder(step.ID, fwPhaseDone, 0, 0)
	return res
}

// fwStickReclaimable is the space on the stick the firmware write frees on
// its way: an Update.stu from an earlier step, which it replaces, and STR's
// own install files, which clearFirmwareStick removes first.
func fwStickReclaimable(drivePath string) int64 {
	return sticksetup.ExistingFirmwareBytes(drivePath) + sticksetup.STRPayloadBytes(drivePath)
}

// fwLogNameCap bounds the file names one log line lists.
const fwLogNameCap = 20

// capNames returns at most fwLogNameCap names, the last one saying how many
// more there are.
func capNames(names []string) []string {
	if len(names) <= fwLogNameCap {
		return names
	}
	out := append([]string{}, names[:fwLogNameCap-1]...)
	return append(out, fmt.Sprintf("... and %d more", len(names)-fwLogNameCap+1))
}

// clearFirmwareStick takes STR's own install files off the stick before
// Update.stu goes on it. Bose's USB updater wants the file alone on the
// stick: a SoundTouch 20 Series I showed the hand after 4 + Volume minus, but
// never a progress bar, again and again, while Update.stu sat beside the 14
// install files the setup wizard had written to the same stick minutes
// earlier (support case, 2026-11). Only files the wizard writes are removed,
// and only from a stick STR prepared (sticksetup.RemoveSTRPayload); anything
// else stays, is logged by count, size and extensions only (never by name),
// and the screen's "other files" hint covers it. A
// failed removal is logged and the write goes ahead: the file is still the
// right one, and the hint stays honest.
func (a *App) clearFirmwareStick(stepID, drivePath string) {
	removed, err := sticksetup.RemoveSTRPayload(drivePath)
	if len(removed) > 0 {
		a.fwLog().Info("firmware ladder: removed STR's install files from the stick, so it holds only the Bose update",
			"step", stepID, "drive", drivePath, "count", len(removed), "files", capNames(removed))
	}
	if err != nil {
		a.fwLog().Warn("firmware ladder: could not remove all of STR's install files from the stick",
			"step", stepID, "drive", drivePath, "err", err)
	}
	// The user's own files are described, never named: their names can be
	// personal, and diagnostic bundles get posted in public.
	if n, size, exts := sticksetup.OtherFilesSummary(drivePath); n > 0 {
		a.fwLog().Info("firmware ladder: the stick holds other files besides the Bose update; left alone, the screen suggests an empty stick",
			"step", stepID, "drive", drivePath, "count", n, "bytes", size, "extensions", capNames(exts))
	}
}

// logFirmwareStickLayout records what the Bose USB updater is picky about:
// file system, cluster size, size and partition style. Without it a speaker
// that never starts the update leaves no trace of whether the stick was the
// reason.
func (a *App) logFirmwareStickLayout(stepID, drivePath string, c sticksetup.StickCheck) {
	style, parts, err := fwStickPartitionStyle(drivePath)
	attrs := []any{"step", stepID, "drive", drivePath, "fs", c.Filesystem, "clusterBytes", c.ClusterBytes,
		"totalBytes", c.TotalBytes, "freeBytes", c.FreeBytes, "partitionStyle", style, "partitions", parts}
	if err != nil {
		attrs = append(attrs, "partitionErr", err.Error())
	}
	a.fwLog().Info("firmware ladder: writing to the stick", attrs...)
}

// extractToCache takes Update.stu out of zipPath into the cache folder and
// returns its path.
func (a *App) extractToCache(ctx context.Context, step bosefw.Step, zipPath string) (string, error) {
	dir, err := fwArchiveCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, step.ID+".Update.stu")
	a.emitLadder(step.ID, fwPhaseExtract, 0, step.InnerSize)
	prog := a.ladderProgress(step.ID, fwPhaseExtract, step.InnerSize)
	if err := extractUpdateStu(ctx, zipPath, dst, step.InnerSize, step.InnerCRC32, step.InnerSHA1, prog); err != nil {
		return "", err
	}
	return dst, nil
}

// archiveURLAllowed reports whether raw is a file inside the one archive.org
// item the ladder uses.
func archiveURLAllowed(raw string) bool {
	if !strings.HasPrefix(raw, fwArchivePrefix) {
		return false
	}
	rest := raw[len(fwArchivePrefix):]
	if rest == "" || strings.Contains(rest, "..") || strings.ContainsAny(rest, "?#\\") {
		return false
	}
	_, err := url.Parse(raw)
	return err == nil
}

// fetchArchiveZip returns the cached zip for step, downloading it from the
// Internet Archive when the cache has no copy with the right size and sha1.
func (a *App) fetchArchiveZip(ctx context.Context, step bosefw.Step) (string, error) {
	if step.Source != bosefw.SourceArchive || !archiveURLAllowed(step.URL) || strings.ContainsAny(step.FileName, `/\`) || step.FileName == "" {
		return "", fmt.Errorf("refusing a firmware link outside the Internet Archive item")
	}
	if step.ZipSize <= 0 || step.ZipSHA1 == "" {
		return "", fmt.Errorf("no size and sha1 known for %s, so the download cannot be checked", step.FileName)
	}
	dir, err := fwArchiveCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, step.FileName)
	a.emitLadder(step.ID, fwPhaseVerify, 0, step.ZipSize)
	want := fileFingerprint{size: step.ZipSize, sha1: step.ZipSHA1}
	if verifyFile(dst, want) == nil {
		a.fwLog().Info("firmware ladder: using the zip already downloaded", "file", step.FileName)
		return dst, nil
	}
	h := sha1.New() // #nosec G401 -- matches the archive's published sha1
	n, err := fetchVerified(ctx, verifiedDownload{
		url: step.URL, dst: dst, size: step.ZipSize, source: "the Internet Archive",
		headerWait: 60 * time.Second, stallAfter: 90 * time.Second,
		checkRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if !fwArchiveRedirectOK(r.URL) {
				return fmt.Errorf("refusing a redirect away from the Internet Archive (%s)", r.URL.Host)
			}
			return nil
		},
		sum: h, onProgress: a.ladderProgress(step.ID, fwPhaseDownload, step.ZipSize),
		alreadyThere: func() bool { return verifyFile(dst, want) == nil },
		check: func(n int64) error {
			got := hex.EncodeToString(h.Sum(nil))
			if n != step.ZipSize || !strings.EqualFold(got, step.ZipSHA1) {
				return fmt.Errorf("%w: the download has %d bytes, sha1 %s; expected %d bytes, sha1 %s", errFingerprint, n, got, step.ZipSize, step.ZipSHA1)
			}
			return nil
		},
	})
	if err != nil {
		return "", err
	}
	a.fwLog().Info("firmware ladder: zip downloaded and checked", "file", step.FileName, "bytes", n)
	return dst, nil
}

func matchInner(n, size int64, c hash.Hash32, s hash.Hash, crc uint32, sha1hex string) error {
	if n != size || c.Sum32() != crc {
		return fmt.Errorf("%w: %d bytes, CRC %08x; expected %d bytes, CRC %08x", errFingerprint, n, c.Sum32(), size, crc)
	}
	if sha1hex != "" {
		if got := hex.EncodeToString(s.Sum(nil)); !strings.EqualFold(got, sha1hex) {
			return fmt.Errorf("%w: sha1 %s, expected %s", errFingerprint, got, sha1hex)
		}
	}
	return nil
}

// extractUpdateStu streams the one entry named Update.stu (at any folder
// depth) out of zipPath into dst, checking size, CRC-32 and, when known, sha1
// while it copies. A zip with no Update.stu, or more than one, is refused.
func extractUpdateStu(ctx context.Context, zipPath, dst string, size int64, crc uint32, sha1hex string, onProgress func(int64)) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("%w: not a readable zip (%w)", errFingerprint, err)
	}
	defer zr.Close()
	var match *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if strings.EqualFold(path.Base(strings.ReplaceAll(f.Name, `\`, "/")), sticksetup.FirmwareFileName) {
			if match != nil {
				return fmt.Errorf("%w: the zip holds more than one Update.stu", errFingerprint)
			}
			match = f
		}
	}
	if match == nil {
		return fmt.Errorf("%w: the zip holds no Update.stu", errFingerprint)
	}
	// The zip's own directory already names the size and CRC: refuse before
	// inflating 100 MB of the wrong file.
	if int64(match.UncompressedSize64) != size || match.CRC32 != crc {
		return fmt.Errorf("%w: the zip lists Update.stu with %d bytes, CRC %08x; expected %d bytes, CRC %08x",
			errFingerprint, match.UncompressedSize64, match.CRC32, size, crc)
	}
	rc, err := match.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	part := dst + ".part"
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = out.Close()
			_ = os.Remove(part)
		}
	}()
	c := crc32.NewIEEE()
	s := sha1.New() // #nosec G401 -- integrity check
	r := io.Reader(&ctxReader{ctx: ctx, r: io.LimitReader(rc, size+1)})
	if onProgress != nil {
		r = &countingReader{r: r, onProgress: onProgress}
	}
	n, err := io.Copy(io.MultiWriter(out, c, s), r)
	if err != nil {
		if errors.Is(err, zip.ErrChecksum) {
			return fmt.Errorf("%w: the zip's own checksum fails", errFingerprint)
		}
		return err
	}
	if err := matchInner(n, size, c, s, crc, sha1hex); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(part, dst); err != nil {
		return err
	}
	ok = true
	return nil
}

// ctxReader stops a long copy when ctx is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// emitLadder sends one "fwladder:progress" event.
func (a *App) emitLadder(stepID, phase string, done, total int64) {
	if a == nil || a.ctx == nil {
		return
	}
	pct := -1
	if total > 0 && done > 0 {
		pct = int(done * 100 / total)
	}
	wailsrt.EventsEmit(a.appCtx(), fwLadderEvent, map[string]any{
		"stepId": stepID, "phase": phase, "pct": pct, "done": done, "total": total,
	})
}

// ladderProgress returns a byte counter that emits at most every 250 ms or
// whole percent.
func (a *App) ladderProgress(stepID, phase string, total int64) func(int64) {
	var last time.Time
	lastPct := -1
	return func(done int64) {
		pct := -1
		if total > 0 {
			pct = int(done * 100 / total)
		}
		now := time.Now()
		if pct == lastPct && now.Sub(last) < 250*time.Millisecond {
			return
		}
		last, lastPct = now, pct
		a.emitLadder(stepID, phase, done, total)
	}
}
