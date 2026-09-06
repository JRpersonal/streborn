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
	}
	return 0
}

// ssidRe matches the SSID attribute of the WiFiStatus XML. The firmware
// writes plain quotes; the escaped form covers a line that was itself quoted
// into another log.
var ssidRe = regexp.MustCompile(`SSID=\\?"([^"\\]*)\\?"`)

// Redact replaces every SSID value with a hash tag. The app's bundle
// anonymizer masks IPs and device ids on its own, but it cannot recognise a
// bare network name in free text, so that happens here, on the speaker.
func Redact(s string) string {
	if !strings.Contains(s, "SSID=") {
		return s
	}
	return ssidRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := ssidRe.FindStringSubmatch(m)
		if len(sub) < 2 || sub[1] == "" {
			return m
		}
		return `SSID="` + HashTag(sub[1]) + `"`
	})
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
	if !classified {
		return ev, false, false
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
