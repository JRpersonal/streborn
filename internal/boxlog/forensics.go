package boxlog

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Forensics: the firmware already logs why a stream did not start, when the
// speaker went to standby and came back, how the Wi-Fi looks, and when it
// rejects one of STR's marge answers. All of that lives only in the RAM ring
// and is gone at the next reboot, so the reader keeps a small bounded copy of
// the lines that matter (a classified event ring plus a redacted raw tail) and
// hands it to the diagnostic bundle. Nothing here touches NAND.
//
// Every line shape below was measured live on 2026-09-06 (Portable/taigan and
// SoundTouch 10/rhino, firmware 27.0.6); the tests carry them verbatim.

const (
	// eventRingLen bounds the classified event ring.
	eventRingLen = 200
	// tailRingLen bounds the redacted raw tail.
	tailRingLen = 150
	// setupTailFollow is how many further lines the frozen setup tail keeps
	// after the event that triggered it.
	setupTailFollow = 200
	// setupTailLen sizes the frozen buffer so it holds the snapshot AND the
	// lines that follow it. Sizing it at tailRingLen alone would let the
	// follow-up lines overwrite the very snapshot the freeze exists to keep.
	// Allocated only when a setup event actually happens, so a healthy box
	// carries none of it.
	setupTailLen = tailRingLen + setupTailFollow
	// setupRearmQuiet is how long the box has to go WITHOUT a setup
	// access-point line before a completed capture may be replaced by a later
	// one.
	//
	// Quiet since the last setup event, deliberately, and not the age of the
	// freeze. The freeze's own age says nothing about whether the episode that
	// armed it is over: the episodes measured on the #873 reporter's box ran
	// about a quarter of an hour and re-raised the access point every two to
	// five minutes inside that, so an age gate fires in the MIDDLE of a live
	// episode and throws away the opening snapshot - the one part of the
	// capture that nothing else can reconstruct, and the whole reason the
	// freeze exists.
	//
	// Ten minutes is twice the longest gap observed inside one episode, so a
	// continuing episode cannot look like a new one, while a freeze spent on
	// an unrelated access-point line at boot still frees up for the real
	// episode later.
	setupRearmQuiet = 10 * time.Minute
	// maxMessageLen truncates a stored message so one long firmware line
	// cannot bloat the bundle.
	maxMessageLen = 240
	// playbackLogGap rate-limits the agent log line for one playback failure
	// class: a stream in a retry loop repeats the same reason every second.
	playbackLogGap = 30 * time.Second
	// powerLogGap guards the standby/wake lines: each transition is logged,
	// a chronic flap is not.
	powerLogGap = 5 * time.Second
	// rareLogGap rate-limits the swamped warning and the marge complaints.
	rareLogGap = 10 * time.Minute
)

// Class names one family of firmware lines. The string is what the debug
// section and the agent log show.
type Class string

const (
	// Playback failure reasons (APServer and BoseApp).
	ClassPlayBadURL       Class = "play_bad_url"        // AudioPathHttpStream: HTTP error on the stream URL
	ClassPlayNoFirstFrame Class = "play_no_first_frame" // Decoder: no first frame from the stream
	ClassPlayFailure      Class = "play_failure"        // APAudioControl: PlaybackFailure ERROR_*
	ClassPlayServerError  Class = "play_server_error"   // AudioIF: server state has a terminal error
	ClassPlayUnderrun     Class = "play_underrun"       // RBDecoded: buffer underrun
	ClassPlaySelect       Class = "play_select"         // AudioIF: the URL the firmware tried to play
	// Standby, wake and power management.
	ClassStandby    Class = "standby"     // HSM: ChangeState(... >> Standby)
	ClassWake       Class = "wake"        // HSM: ChangeState(Standby >> ...)
	ClassPowerEvent Class = "power_event" // scmmond: low power notification
	ClassPowerSleep Class = "power_sleep" // scmmond: sleep stage / processor clock
	// Wi-Fi (once a minute on the sm2 chassis; ring only, never the agent log).
	ClassWiFiStatus  Class = "wifi_status"
	ClassWiFiQuality Class = "wifi_quality"
	// The setup / network state machine (#873). None of these had a class, so
	// the one question two reporters' bundles could not answer - what the
	// firmware was doing while the speaker was off the LAN - had no evidence
	// at all in them. The v0.9.75 tails were the first to show these lines
	// exist; naming them is what makes the next bundle decidable.
	ClassSetupState    Class = "setup_state"    // the firmware's own Wi-Fi/setup state machine
	ClassSetupAP       Class = "setup_ap"       // the speaker raising its own access point
	ClassNetManager    Class = "netmanager"     // Bose's NetManager and its wpa control socket
	ClassWPASupplicant Class = "wpa_supplicant" // the supplicant's own association events
	ClassDHCP          Class = "dhcp"           // udhcpc discover / lease
	// Marge complaints about STR's own answers.
	ClassMargeError Class = "marge_error"
	// BoseApp overload.
	ClassSwamped Class = "swamped"
)

// Event is one classified firmware line.
type Event struct {
	Class Class `json:"class"`
	// At is when the line was read; the ring's own stamp has one-second
	// resolution and no year.
	At time.Time `json:"at"`
	// Process is the logging process (BoseApp, APServer, scmmond, ...).
	Process string `json:"process"`
	// Facility is the firmware's DPrint facility, empty for non-DPrint lines.
	Facility string `json:"facility,omitempty"`
	// Message is the redacted, truncated message after the DPrint prefix.
	Message string `json:"message"`
}

// Line is one syslog line split into its fields. The ring's format is
//
//	<Mon dd HH:MM:SS> <host> <prio> <proc>[<pid>]: [(<tid>):<Facility>:<LEVEL>]<message>
//
// where the DPrint prefix in brackets is present for the Bose daemons and
// absent for the kernel and busybox.
type Line struct {
	Stamp    string
	Process  string
	Facility string
	Level    string
	Message  string
}

// ParseLine splits one ring line. ok is false for a line that has no
// "proc[pid]: " part at all.
func ParseLine(line string) (l Line, ok bool) {
	// Timestamp: busybox writes "Mon dd HH:MM:SS" padded to 15 characters.
	rest := line
	if len(rest) > 16 && rest[15] == ' ' && rest[3] == ' ' {
		l.Stamp = rest[:15]
		rest = rest[16:]
	}
	// Host and priority are the next two space-separated words.
	for i := 0; i < 2; i++ {
		sp := strings.IndexByte(rest, ' ')
		if sp < 0 {
			return l, false
		}
		rest = rest[sp+1:]
	}
	colon := strings.Index(rest, ": ")
	if colon < 0 {
		return l, false
	}
	proc := rest[:colon]
	if br := strings.IndexByte(proc, '['); br > 0 {
		proc = proc[:br]
	}
	l.Process = proc
	msg := rest[colon+2:]
	if strings.HasPrefix(msg, "[(") {
		if end := strings.IndexByte(msg, ']'); end > 0 {
			parts := strings.Split(msg[2:end], ":")
			if len(parts) == 3 {
				l.Facility = parts[1]
				l.Level = parts[2]
				msg = msg[end+1:]
			}
		}
	}
	l.Message = msg
	return l, true
}

// IsNoise reports the lines that must never be stored: TPDA and STSCertified
// retry a dead localhost socket about twenty times a second, and APServer's
// clock-sync and BDSP chatter carries nothing a bug report needs.
func IsNoise(line string) bool {
	return strings.Contains(line, "Failed connect call for 127.0.0.1") ||
		strings.Contains(line, "BDSP") ||
		strings.Contains(line, "ClockSync") ||
		strings.Contains(line, "clock sync") ||
		strings.Contains(line, "clocksync")
}

// Classify assigns a line to one of the families above. ok is false for the
// (vast) majority of lines. Substring gates only, no regexp, so the per-line
// cost on a box that logs many lines a second stays negligible.
func Classify(l Line) (Class, bool) {
	m := l.Message
	switch {
	case strings.Contains(m, "PlaybackFailure"):
		return ClassPlayFailure, true
	case strings.Contains(m, "SERVER ERROR: Server state"):
		return ClassPlayServerError, true
	case strings.Contains(m, "HTTP Error indicated"):
		return ClassPlayBadURL, true
	case strings.Contains(m, "Could not obtain first-frame"):
		return ClassPlayNoFirstFrame, true
	case strings.Contains(m, "ReadyToWrite Interrupted"):
		return ClassPlayUnderrun, true
	case strings.Contains(m, "CAudioInterface::Select("):
		return ClassPlaySelect, true
	// The setup gates come BEFORE the ChangeState one on purpose: the
	// firmware's Wi-Fi state machine logs through the same ChangeState shape,
	// and that case deliberately drops everything whose facility is not HSM.
	// Behind it, a "ChangeState(WifiConfigTopState >> ...)" line would be
	// classified as nothing at all, which is exactly how this family stayed
	// invisible.
	case strings.Contains(m, "WifiConfigTopState"),
		strings.Contains(m, "EVT_SYSTEM_SETUP"),
		strings.Contains(m, "setupStateResponse"):
		return ClassSetupState, true
	case strings.Contains(m, "ACCESS_POINT"),
		strings.Contains(m, "SoftAP"),
		l.Process == "hostapd",
		strings.Contains(m, "hostapd"):
		return ClassSetupAP, true
	// NetManager is the process that owns the running supplicant. Its facility
	// and level are parsed out of the message by ParseLine, so the measured
	// "WiFiManager:ERROR" is matched as the pair it actually is.
	//
	// Deliberately NOT gated on the NetworkServicesController facility. That
	// facility is the one the WiFiStatus and WiFiSignalStrength lines carry,
	// which have their own classes below and their own SSID redaction; taking
	// them here would move a once-a-minute ring-only family onto a class that
	// reaches the NAND-mirrored agent log.
	case strings.Contains(m, "wpa_ctrl_request failed"),
		l.Facility == "WiFiManager" && l.Level == "ERROR":
		return ClassNetManager, true
	case l.Process == "wpa_supplicant",
		strings.Contains(m, "CTRL-EVENT-"),
		strings.Contains(m, "Trying to associate"),
		strings.Contains(m, "wpa_supplicant"):
		return ClassWPASupplicant, true
	case l.Process == "udhcpc",
		strings.Contains(m, "Sending discover"),
		strings.Contains(m, "Lease of"),
		strings.Contains(m, "udhcpc"):
		return ClassDHCP, true
	case strings.Contains(m, "ChangeState("):
		if l.Facility != "HSM" {
			return "", false
		}
		return classifyStateChange(m)
	case strings.Contains(m, "Low power notification"):
		return ClassPowerEvent, true
	case strings.Contains(m, "Go Sleep stage"), strings.Contains(m, "sets processor clock"):
		return ClassPowerSleep, true
	case strings.Contains(m, "WiFiStatus("):
		return ClassWiFiStatus, true
	case strings.Contains(m, "WiFiSignalStrengthDBMToQuality"):
		return ClassWiFiQuality, true
	case strings.Contains(m, "getting swamped"):
		return ClassSwamped, true
	case l.Facility == "MargeClient" && l.Level == "ERROR",
		strings.Contains(m, "UpdatePresetFailureCB"),
		strings.Contains(m, "AddRecentCB Failed"):
		return ClassMargeError, true
	}
	return "", false
}

// classifyStateChange reads the first "ChangeState(A >> B)" of an HSM line.
// Only transitions into or out of Standby are kept; the system controller
// logs every source change through the same shape.
func classifyStateChange(m string) (Class, bool) {
	i := strings.Index(m, "ChangeState(")
	rest := m[i+len("ChangeState("):]
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		return "", false
	}
	from, to, found := strings.Cut(rest[:end], " >> ")
	if !found {
		return "", false
	}
	switch {
	case strings.HasPrefix(to, "Standby") && !strings.HasPrefix(from, "Standby"):
		return ClassStandby, true
	case strings.HasPrefix(from, "Standby") && !strings.HasPrefix(to, "Standby"):
		return ClassWake, true
	}
	return "", false
}

// Playback reports whether c is one of the playback failure reasons (the
// Select line is what the firmware tried, not a failure).
func (c Class) Playback() bool {
	switch c {
	case ClassPlayBadURL, ClassPlayNoFirstFrame, ClassPlayFailure, ClassPlayServerError, ClassPlayUnderrun:
		return true
	}
	return false
}

// logGap says how often the agent log may carry a line of this class; zero
// means never (Wi-Fi, the Select line, sleep stages: ring only).
func (c Class) logGap() time.Duration {
	switch {
	case c.Playback():
		return playbackLogGap
	case c == ClassStandby, c == ClassWake, c == ClassPowerEvent:
		return powerLogGap
	case c == ClassSwamped, c == ClassMargeError:
		return rareLogGap
	case c == ClassSetupState, c == ClassSetupAP, c == ClassNetManager:
		// The three that decide #873 do reach the agent log, but through the
		// rare gap: on a box in a setup episode these repeat every few seconds
		// for a quarter of an hour, and the agent log is mirrored to NAND.
		// One line per ten minutes is enough to timestamp the episode; the
		// frozen tail carries the detail.
		return rareLogGap
	}
	// wpa_supplicant and udhcpc are ring-only: they are the noisiest families
	// on the box and the whole point of keeping them is the frozen tail, not
	// the agent log (SCHONE die Box-Hardware).
	return 0
}

// ssidRe matches a network name wherever the box writes one, which since the
// wpa_supplicant / setup_ap classes were added is more than the WiFiStatus
// XML's own attribute:
//
//	SSID="HomeNet"                    the firmware's WiFiStatus attribute
//	SSID=\"HomeNet\"                  the same line quoted into another log
//	(SSID='HomeNet' freq=5220 MHz)    wpa_supplicant's "Trying to associate"
//	ssid="HomeNet"                    wpa_supplicant's CTRL-EVENT lines
//
// The last two are new Classify gates, so the lines that carry them are now
// deliberately KEPT - and the old double-quoted, case-sensitive pattern
// matched neither of them.
var ssidRe = regexp.MustCompile(`(?i)(ssid=)(?:(\\?")([^"\\]*)(\\?")|'([^']*)')`)

// bssidRe matches a MAC address in the same association lines. The router's
// BSSID is as identifying as the network name, and a speaker device id is on
// the never-publish list; the app's export anonymizer masks these downstream,
// but /debug/state answers an unauthenticated LAN GET and is not covered by
// it.
var bssidRe = regexp.MustCompile(`\b[0-9a-fA-F]{2}(?::[0-9a-fA-F]{2}){5}\b`)

// Redact replaces every SSID value and every MAC address with a hash tag. The
// app's bundle anonymizer masks IPs and device ids on its own, but it cannot
// recognise a bare network name in free text, so that happens here, on the
// speaker.
func Redact(s string) string {
	if containsFoldASCII(s, "ssid=") {
		s = ssidRe.ReplaceAllStringFunc(s, func(m string) string {
			sub := ssidRe.FindStringSubmatch(m)
			if sub == nil {
				return m
			}
			if sub[3] != "" {
				return sub[1] + sub[2] + HashTag(sub[3]) + sub[4]
			}
			if sub[5] != "" {
				return sub[1] + "'" + HashTag(sub[5]) + "'"
			}
			return m
		})
	}
	// Five colons, not one. A MAC always carries exactly five, while a bare
	// "contains a colon" test is true of practically every syslog line (the
	// timestamp alone has two), so it let the regex scan the whole box log line
	// by line. Redact runs on every line the firmware emits, on a speaker with
	// 120 MB of RAM, so the guard has to actually guard.
	if countByte(s, ':') >= 5 {
		s = bssidRe.ReplaceAllStringFunc(s, func(m string) string { return "mac#" + HashTag(m)[5:] })
	}
	return s
}

// countByte counts b in s, stopping as soon as it has seen enough for the
// caller. strings.Count would walk the whole line every time.
func countByte(s string, b byte) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			n++
			if n >= 5 {
				return n
			}
		}
	}
	return n
}

// containsFoldASCII is the allocation-free ASCII case-insensitive substring
// test the per-line guard needs: Redact runs on every syslog line the box
// emits, and a strings.ToLower there would allocate on each of them.
//
// The first byte is compared directly before the fold, so the common case (a
// line with no 's' or 'S' at that offset) costs one byte compare rather than a
// call into the Unicode-aware strings.EqualFold.
func containsFoldASCII(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(s) < len(sub) {
		return false
	}
	lo, up := lowerASCII(sub[0]), upperASCII(sub[0])
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i] != lo && s[i] != up {
			continue
		}
		if strings.EqualFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}

func upperASCII(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 'a' + 'A'
	}
	return b
}

// HashTag is the anonymized stand-in for a network name: stable, so two
// lines about the same network still match, and not reversible.
func HashTag(v string) string {
	sum := sha256.Sum256([]byte(v))
	return "ssid#" + hex.EncodeToString(sum[:])[:8]
}

// truncate cuts s to maxMessageLen bytes on a rune boundary.
func truncate(s string) string {
	if len(s) <= maxMessageLen {
		return s
	}
	cut := maxMessageLen
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// ring is a fixed-capacity FIFO.
type ring[T any] struct {
	buf   []T
	start int
	n     int
}

func newRing[T any](capacity int) *ring[T] {
	return &ring[T]{buf: make([]T, capacity)}
}

func (r *ring[T]) push(v T) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = v
		r.n++
		return
	}
	r.buf[r.start] = v
	r.start = (r.start + 1) % len(r.buf)
}

func (r *ring[T]) items() []T {
	out := make([]T, 0, r.n)
	for i := 0; i < r.n; i++ {
		out = append(out, r.buf[(r.start+i)%len(r.buf)])
	}
	return out
}

func (r *ring[T]) last() (T, bool) {
	var zero T
	if r.n == 0 {
		return zero, false
	}
	return r.buf[(r.start+r.n-1)%len(r.buf)], true
}

// forensics holds the bounded state behind the two debug sections.
type forensics struct {
	mu          sync.Mutex
	events      *ring[Event]
	tail        *ring[string]
	lastLogged  map[Class]time.Time
	lastPlayErr Event
	dropped     uint64
	classified  uint64
	// lastPower is the first line of the newest standby/wake transition; the
	// other daemon's line about the same transition is folded into it (see
	// power.go).
	lastPower        PowerEvent
	powerTransitions uint64
	powerDuplicates  uint64

	// The frozen setup tail (#873). tailRingLen is 150 lines, which a busy box
	// overwrites in seconds, so a fifteen-minute setup episode rolled straight
	// out of the ring long before anybody exported a bundle. On the FIRST
	// setup event the tail is snapshotted here and kept filling for
	// setupTailFollow further lines, then stopped. One extra buffer, bounded,
	// and the single change that would have answered this issue.
	setupTail     *ring[string]
	setupTrigger  Class
	setupFrozenAt time.Time
	setupFollowed int
	setupDone     bool
	// setupLastAPAt is when the last setup access-point line was seen, which is
	// the clock the re-arm gate reads: a capture may only be replaced after
	// setupRearmQuiet with no setup event at all.
	setupLastAPAt time.Time
	// setupRearms bounds the replacement of a completed capture at one, so a
	// freeze spent on an unrelated AP-mode line at boot does not cost the
	// whole agent run while the buffer count still cannot grow.
	setupRearms int
	// setupNotice carries the one-shot "tail frozen" report to the reader,
	// which owns the logger.
	setupNotice bool
}

func newForensics() *forensics {
	return &forensics{
		events:     newRing[Event](eventRingLen),
		tail:       newRing[string](tailRingLen),
		lastLogged: make(map[Class]time.Time),
	}
}

// observe runs the per-line path: drop the spam, keep a redacted tail line,
// classify, and report whether the caller should write an agent log line for
// the event (rate-limited per class). ok is false for an unclassified line.
func (f *forensics) observe(line string, now time.Time) (ev Event, logIt bool, ok bool) {
	if IsNoise(line) {
		f.mu.Lock()
		f.dropped++
		f.mu.Unlock()
		return ev, false, false
	}
	l, parsed := ParseLine(line)
	if !parsed {
		return ev, false, false
	}
	msg := truncate(Redact(l.Message))
	tailLine := l.Stamp + " " + l.Process
	if l.Facility != "" {
		tailLine += " " + l.Facility + ":" + l.Level
	}
	tailLine += " " + msg
	class, classified := Classify(l)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tail.push(tailLine)
	f.feedSetupTailLocked(tailLine)
	if !classified {
		return ev, false, false
	}
	if class == ClassSetupAP {
		f.freezeSetupTailLocked(class, now)
	}
	f.classified++
	ev = Event{Class: class, At: now, Process: l.Process, Facility: l.Facility, Message: msg}
	f.events.push(ev)
	if class.Playback() {
		f.lastPlayErr = ev
	}
	if gap := class.logGap(); gap > 0 && now.Sub(f.lastLogged[class]) >= gap {
		f.lastLogged[class] = now
		logIt = true
	}
	return ev, logIt, true
}

// lastPlaybackFailure returns the newest playback failure if it arrived
// within d of now.
func (f *forensics) lastPlaybackFailure(now time.Time, d time.Duration) (Event, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastPlayErr.Class == "" || now.Sub(f.lastPlayErr.At) > d {
		return Event{}, false
	}
	return f.lastPlayErr, true
}

// eventsSnapshot is the box_syslog_events debug section.
func (f *forensics) eventsSnapshot() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	evs := f.events.items()
	out := make([]map[string]any, 0, len(evs))
	for _, e := range evs {
		out = append(out, map[string]any{
			"at":       e.At.Format(time.RFC3339),
			"class":    string(e.Class),
			"process":  e.Process,
			"facility": e.Facility,
			"message":  e.Message,
		})
	}
	return map[string]any{
		"classified": f.classified,
		"noiseLines": f.dropped,
		"events":     out,
	}
}

// eventsSnapshot's sibling for the section: the ring stats plus the wake
// signal (see power.go). Split so the lock is not held across both.
func (f *forensics) sectionSnapshot() map[string]any {
	out := f.eventsSnapshot()
	out["powerSignal"] = f.powerSnapshot()
	return out
}

// tailSnapshot is the box_syslog_tail debug section.
func (f *forensics) tailSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tail.items()
}

// freezeSetupTailLocked starts the frozen tail on the first ACCESS-POINT
// event: the current ring is copied in, and every later line is appended until
// setupTailFollow of them have passed.
//
// The trigger is ClassSetupAP alone, never ClassSetupState. The state class
// matches "WifiConfigTopState", the firmware's own Wi-Fi state machine, which
// logs during a perfectly ordinary association at every single boot - and it
// matches "setupStateResponse", which STR's own /setup reads provoke. Either
// would have spent the one-shot freeze on the boot, and the real episode seven
// minutes later would have been captured nowhere.
//
// One re-arm, and one only. A capture that is already COMPLETE may be replaced
// by a later one, so a freeze burnt on an unrelated AP-mode status line at boot
// does not cost the whole agent run. Bounded at two allocations per run either
// way. Caller holds f.mu.
//
// The re-arm gate is QUIET TIME since the last setup access-point line, never
// the age of the freeze itself: an episode that keeps re-raising the access
// point for a quarter of an hour would otherwise re-arm in the middle of
// itself and discard the episode-start snapshot the capture exists for. See
// setupRearmQuiet.
func (f *forensics) freezeSetupTailLocked(trigger Class, now time.Time) {
	prevAP := f.setupLastAPAt
	f.setupLastAPAt = now
	if f.setupTail != nil {
		quiet := !prevAP.IsZero() && now.Sub(prevAP) >= setupRearmQuiet
		if !f.setupDone || !quiet || f.setupRearms >= 1 {
			return
		}
		f.setupRearms++
		f.setupDone = false
		f.setupFollowed = 0
	}
	f.setupTail = newRing[string](setupTailLen)
	for _, line := range f.tail.items() {
		f.setupTail.push(line)
	}
	f.setupTrigger = trigger
	f.setupFrozenAt = now
	f.setupNotice = true
}

// feedSetupTailLocked appends one line to the frozen tail while it is still
// collecting. Caller holds f.mu.
func (f *forensics) feedSetupTailLocked(line string) {
	if f.setupTail == nil || f.setupDone {
		return
	}
	f.setupTail.push(line)
	f.setupFollowed++
	if f.setupFollowed >= setupTailFollow {
		f.setupDone = true
	}
}

// takeFreezeNotice claims the one-shot "the tail was frozen" report, so the
// reader (which owns the logger) can say it once.
func (f *forensics) takeFreezeNotice() (lines int, trigger Class, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.setupNotice {
		return 0, "", false
	}
	f.setupNotice = false
	return f.setupTail.n, f.setupTrigger, true
}

// setupTailSnapshot is the box_setup_tail debug section: empty until the
// firmware's setup state machine says something, which is itself the answer
// for a box that never entered setup.
func (f *forensics) setupTailSnapshot() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setupTail == nil {
		return map[string]any{"frozen": false, "lines": []string{}}
	}
	out := map[string]any{
		"frozen":   true,
		"trigger":  string(f.setupTrigger),
		"complete": f.setupDone,
		"lines":    f.setupTail.items(),
	}
	if !f.setupFrozenAt.IsZero() {
		out["frozenSecAgo"] = int(time.Since(f.setupFrozenAt).Seconds())
	}
	if f.setupRearms > 0 {
		// The reader has to know this is not the first capture of the run.
		out["rearmed"] = f.setupRearms
	}
	return out
}
