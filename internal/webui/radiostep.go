package webui

// Next and Previous on the remote step through the radio keys (#1619).
//
// While a radio station from a preset key plays, the remote's Next goes to the
// next key that holds a radio station and Previous to the one before, like the
// channel keys on a TV remote. All twelve keys count, the second row (a key
// pressed twice) included, in slot order: 1..6, then 2x1..2x6. Keys that are
// empty or hold something other than a radio station (a Spotify playlist, a
// music-library folder or song, a speaker service) are stepped over, so the
// next press steps again instead of landing on a playlist where the same keys
// skip songs. The ends wrap around.
//
// Before this the keys did nothing useful on radio: the speaker cannot skip a
// station, and STR's own skip only knows the play queue. So it is simply how
// the keys work now, without a setting. On Spotify and the music library the
// keys keep skipping songs (HardwareSkip, transportSkip).
//
// Two signals report the press, and a speaker may send either or both: the
// firmware's QPLAY_SKIP_*_FAILED frame on the gabbo bus while STR's UPnP stream
// plays (HardwareSkip), and the speaker's own key trace (RadioStepKey, see
// internal/boxlog), which also sees the press on a station the speaker plays
// natively. One press must step once, so an arrival from one signal that finds
// an unpaired arrival of the other within radioStepPairWindow is taken as the
// same press and dropped. Two real presses each bring their own pair, so
// tapping Next twice still steps twice.
//
// The step itself is the app's recall of the target key (playSlot), so it
// behaves exactly like pressing that key in the app. Steps run one at a time:
// the next press reads the key the previous step landed on.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/presets"
)

// radioStepPairWindow is how far apart the two signals of ONE press may
// arrive. The gabbo frame and the syslog line of a press land within a few
// hundred milliseconds; a person cannot press the same key twice in less.
const radioStepPairWindow = 700 * time.Millisecond

// radioStepVia names the signal a press arrived on.
type radioStepVia int

const (
	radioStepViaSkipFrame radioStepVia = iota // QPLAY_SKIP_*_FAILED (HardwareSkip)
	radioStepViaKeyTrace                      // the key trace (RadioStepKey)
)

// radioStepState is what the radio step keeps between presses.
type radioStepState struct {
	// mu runs steps one at a time, from reading the current key to the
	// recall of the next.
	mu sync.Mutex
	// pairMu guards unpaired: per signal, the arrivals that stepped and
	// have not been matched with the other signal's report of the same press.
	pairMu   sync.Mutex
	unpaired [2][]time.Time
}

// samePress reports whether an arrival on via at t is the second report of a
// press the other signal already stepped for, and consumes that match. When it
// is not, the arrival is recorded so the other signal's report of it is
// recognised in turn.
func (st *radioStepState) samePress(via radioStepVia, t time.Time) bool {
	st.pairMu.Lock()
	defer st.pairMu.Unlock()
	other := &st.unpaired[1-via]
	fresh := (*other)[:0]
	for _, at := range *other {
		if t.Sub(at) <= radioStepPairWindow {
			fresh = append(fresh, at)
		}
	}
	*other = fresh
	if len(*other) > 0 {
		*other = (*other)[1:]
		return true
	}
	own := &st.unpaired[via]
	kept := (*own)[:0]
	for _, at := range *own {
		if t.Sub(at) <= radioStepPairWindow {
			kept = append(kept, at)
		}
	}
	*own = append(kept, t)
	return false
}

// isRadioPreset reports whether p is a radio station: not a Spotify playlist,
// not a music-library folder or song (Source set), not a service the speaker
// plays itself.
func isRadioPreset(p presets.Preset) bool {
	return (p.Type == "" || p.Type == "radio") && p.Source == "" && p.StreamURL != "" && p.Native == nil
}

// nextRadioSlot is the radio key one step from slot from (forward = Next), in
// slot order over all keys, wrapping at the ends. 0 when no other key holds a
// radio station.
func nextRadioSlot(all []presets.Preset, from int, forward bool) int {
	radio := map[int]bool{}
	for _, p := range all {
		if presets.ValidSlot(p.Slot) && isRadioPreset(p) {
			radio[p.Slot] = true
		}
	}
	dir := 1
	if !forward {
		dir = -1
	}
	slot := from
	for range presets.MaxSlot - 1 {
		slot += dir
		if slot > presets.MaxSlot {
			slot = 1
		} else if slot < 1 {
			slot = presets.MaxSlot
		}
		if radio[slot] {
			return slot
		}
	}
	return 0
}

// radioSlotPlaying is the radio key STR's last play came from: its stream URL
// is the slot's /stream/N and that slot holds a radio station. 0 otherwise
// (no play, a station from the search, Spotify, the music library).
func (s *Server) radioSlotPlaying() int {
	if s.presets == nil {
		return 0
	}
	path := boxapi.StreamPath(s.play.currentURL())
	slot := 0
	for n := 1; n <= presets.MaxSlot; n++ {
		if path == streamSlotPath(n) {
			slot = n
			break
		}
	}
	if slot == 0 {
		return 0
	}
	if p, ok := s.presets.Get(slot); !ok || !isRadioPreset(p) {
		return 0
	}
	return slot
}

// streamSlotPath is the path of slot n's stream, "/stream/N".
func streamSlotPath(n int) string { return boxapi.StreamPath(boxurl.StreamSlot(n)) }

// radioStepBoxAgrees reports whether the speaker is on the radio key STR last
// played, read fresh: STR's UPnP stream of that key (or no location at all),
// the station the speaker plays natively, or the source the speaker just
// tore down on its own failed skip. Any other source (AUX, Bluetooth, the TV
// input, standby, another app's stream) means the record is stale and the
// press is not STR's to answer.
func (s *Server) radioStepBoxAgrees(ctx context.Context, slot int) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	np, err := s.readBoxNow(ctx)
	if err != nil {
		return false, "speaker not readable"
	}
	switch strings.ToUpper(strings.TrimSpace(np.Source)) {
	case "INVALID_SOURCE", "LOCAL_INTERNET_RADIO":
		return true, ""
	case "UPNP":
		loc := np.Location()
		if loc == "" || boxapi.StreamPath(loc) == streamSlotPath(slot) {
			return true, ""
		}
		return false, "speaker plays another stream"
	}
	return false, "speaker is on " + np.Source
}

// stepRadioPreset answers a Next/Prev press from the gabbo skip frame
// (HardwareSkip). handled is false when no radio key plays, so the caller
// goes on with the song skip.
func (s *Server) stepRadioPreset(ctx context.Context, forward bool) (source string, handled bool) {
	return s.radioStep.step(ctx, s, forward, radioStepViaSkipFrame, time.Now())
}

// RadioStepKey answers a Next/Prev press the speaker's key trace reported
// (cmd/agent keytrace.go). It does nothing unless a radio key plays; the
// song skip of Spotify and the music library stays with HardwareSkip. Call
// it off the key reader's goroutine: a step recalls a station.
func (s *Server) RadioStepKey(forward bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	s.radioStep.step(ctx, s, forward, radioStepViaKeyTrace, time.Now())
}

func (st *radioStepState) step(ctx context.Context, s *Server, forward bool, via radioStepVia, at time.Time) (string, bool) {
	if s.queue != nil && s.queue.isActive() {
		return "", false
	}
	if s.radioSlotPlaying() == 0 {
		return "", false
	}
	if st.samePress(via, at) {
		s.logger.Debug("radio step: same press reported by the other signal, already stepped", "forward", forward)
		return "radio-step", true
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	// Read again under the lock: a step that ran meanwhile moved the key.
	from := s.radioSlotPlaying()
	if from == 0 {
		return "", false
	}
	if ok, why := s.radioStepBoxAgrees(ctx, from); !ok {
		s.logger.Info("radio step: not stepping, the speaker is not on the radio key STR played", "slot", from, "why", why)
		return "", false
	}
	to := nextRadioSlot(s.presets.All(), from, forward)
	if to == 0 {
		s.logger.Info("radio step: no other key holds a radio station, nothing to step to", "slot", from, "forward", forward)
		return "radio-step", true
	}
	p, _ := s.presets.Get(to)
	s.logger.Info("radio step: remote key steps to the next radio key", "from", from, "to", to, "name", p.Name, "forward", forward)
	s.showRadioStepNotice(to, p.Name)
	if err := s.radioStepPlay(ctx, to); err != nil {
		s.logger.Warn("radio step: the key could not be played", "slot", to, "err", err)
	}
	return "radio-step", true
}

// radioStepPlay recalls slot the way the app does. A seam (radioStepPlayFn).
func (s *Server) radioStepPlay(ctx context.Context, slot int) error {
	if s.radioStepPlayFn != nil {
		return s.radioStepPlayFn(ctx, slot)
	}
	_, err := s.PlaySlotForKey(ctx, slot)
	return err
}

// showRadioStepNotice shows the key and the station on the panel while STR's
// track or clock screen holds it, as a preset key press does: the firmware's
// own preset screen is hidden under it. Row 2 says "2x".
func (s *Server) showRadioStepNotice(slot int, name string) {
	if !oled.TrackShowing() {
		return
	}
	key := presets.KeyOf(slot)
	if presets.IsRow2(slot) {
		name = "2x " + name
	}
	oled.Show(oled.PresetPlaying(key, name))
}
