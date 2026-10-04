// Adopting a stereo pair the firmware already holds.
//
// The firmware asks marge for its group after every boot (GET
// .../device/<dev>/group/) and treats the answer as the truth: a pair marge does
// not know about is a pair the firmware drops. Measured on two SoundTouch 10s,
// 2026-10-04: with no record in STR's store the group poll was answered with the
// ACCOUNT document, the firmware logged
//
//	EXCEPTION in GetGroupCB xml parsing: group expected, but XML was 'account'
//
// and twenty seconds later sent DELETE .../group/<id> for its own pair.
//
// STR's store is empty exactly when it matters most: the first boot after STR
// is installed on a speaker whose pair was formed in the Bose app, back when the
// cloud still ran. The pair lives in the firmware, not in STR, so answering "no
// group" (or worse, the account document) throws away something the owner set up
// years ago. And when only one half of such a pair gets STR, the other half keeps
// the pair and waits for a partner that has let go, which is how a speaker ends
// up refusing to leave standby (field case, 2026-10-04).
//
// So when the store is empty, marge asks the firmware first. A complete, healthy
// pair that names this speaker is adopted and served back, and the firmware keeps
// it. Anything marge cannot vouch for (a pair reporting GROUP_ERROR, a document
// that does not name this speaker, a firmware that does not answer in time) gets
// a 503: the same "cloud unavailable" a stock speaker has been getting since the
// Bose shutdown, which leaves the firmware's state exactly as it was instead of
// deciding about it. The app's stale-pair detection is what surfaces those.

package marge

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// FirmwareRole is one half of a pair as the firmware's own /getGroup reports it.
type FirmwareRole struct {
	DeviceID string
	Role     string
	IP       string
}

// FirmwareGroup is the firmware's own /getGroup document, parsed by the caller.
// An unpaired speaker reports an empty document: no ID, no roles.
type FirmwareGroup struct {
	ID             string
	Name           string
	MasterDeviceID string
	Status         string
	Roles          []FirmwareRole
}

// empty reports whether the firmware says it is not paired.
func (g FirmwareGroup) empty() bool {
	return strings.TrimSpace(g.ID) == "" && len(g.Roles) == 0 && strings.TrimSpace(g.MasterDeviceID) == ""
}

// FirmwareGroupProbe reads the speaker's own /getGroup. It must honour ctx: the
// group poll that calls it is a request from that same firmware, and a probe
// that waited on the firmware forever would hold the firmware's own request.
type FirmwareGroupProbe func(ctx context.Context) (FirmwareGroup, error)

// WithFirmwareGroupProbe wires the probe. Without one an empty store answers the
// poll with an empty group, which is what the cloud answered an unpaired speaker.
func WithFirmwareGroupProbe(p FirmwareGroupProbe) Option {
	return func(s *Server) { s.groupProbe = p }
}

// probeBudget bounds one probe. The firmware's poll is waiting on the answer.
const probeBudget = 1500 * time.Millisecond

// probeQuiet is how long an "empty" verdict is trusted before the firmware is
// asked again. The group poll repeats; the firmware does not need to be asked
// on every one of them, and a speaker is spared the extra request.
const probeQuiet = 60 * time.Second

// adoptState carries the probe cache. Separate mutex: the probe runs outside
// s.mu so a slow firmware never blocks the rest of marge.
type adoptState struct {
	mu        sync.Mutex
	emptyTill time.Time
}

// adoptVerdict is what the group poll does with an empty store.
type adoptVerdict int

const (
	verdictEmpty   adoptVerdict = iota // firmware not paired: answer <group/>
	verdictAdopted                     // pair adopted: answer it
	verdictHold                        // cannot vouch: answer 503, change nothing
)

// AdoptFirmwareGroup stores the firmware's own pair as the marge record when it
// is a complete pair naming this speaker and the store is still empty. It
// returns whether the record was adopted and, when not, why. Exported for the
// agent's startup check, which adopts before the first poll whenever the
// firmware answers in time.
func (s *Server) AdoptFirmwareGroup(fg FirmwareGroup) (bool, string) {
	if fg.empty() {
		return false, "firmware not paired"
	}
	if strings.EqualFold(strings.TrimSpace(fg.Status), "GROUP_ERROR") {
		return false, "firmware reports GROUP_ERROR (one-sided or unreachable partner)"
	}
	self := strings.ToUpper(strings.TrimSpace(s.DeviceID()))
	if self == "" {
		return false, "own device id not known yet"
	}
	if len(fg.Roles) != 2 {
		return false, "not a two-speaker pair"
	}
	hasSelf, hasPartner := false, false
	for _, r := range fg.Roles {
		id := strings.ToUpper(strings.TrimSpace(r.DeviceID))
		switch id {
		case "":
			return false, "a role without a device id"
		case self:
			hasSelf = true
		default:
			hasPartner = true
		}
	}
	if !hasSelf || !hasPartner {
		return false, "the pair does not name this speaker and a partner"
	}
	g := &groupRecord{
		ID:             strings.TrimSpace(fg.ID),
		Name:           strings.TrimSpace(fg.Name),
		MasterDeviceID: strings.TrimSpace(fg.MasterDeviceID),
	}
	for _, r := range fg.Roles {
		g.Roles = append(g.Roles, groupRole{
			DeviceID: strings.TrimSpace(r.DeviceID),
			Role:     strings.TrimSpace(r.Role),
			IP:       strings.TrimSpace(r.IP),
		})
	}
	if g.MasterDeviceID == "" {
		// The firmware names the LEFT half as master by convention.
		for _, r := range g.Roles {
			if strings.EqualFold(r.Role, "LEFT") {
				g.MasterDeviceID = r.DeviceID
			}
		}
	}
	if !describesPair(g) {
		return false, "not a pair document"
	}
	if g.ID == "" {
		g.ID = margeGroupID(g.MasterDeviceID)
	}
	s.mu.Lock()
	if s.group != nil {
		s.mu.Unlock()
		return false, "a record is already stored"
	}
	s.group = g
	s.groupCanonical = false
	s.groupRestored = false
	s.persistGroupLocked()
	s.mu.Unlock()
	s.logger.Info("marge group: adopted the stereo pair the firmware already holds",
		slog.String("comp", "marge"), slog.String("groupId", g.ID),
		slog.String("master", g.MasterDeviceID), slog.String("name", g.Name))
	return true, ""
}

// verdictForEmptyStore decides how to answer the group poll while no record is
// stored, asking the firmware if a probe is wired.
func (s *Server) verdictForEmptyStore(parent context.Context) adoptVerdict {
	if s.groupProbe == nil {
		return verdictEmpty
	}
	s.adopt.mu.Lock()
	quiet := time.Now().Before(s.adopt.emptyTill)
	s.adopt.mu.Unlock()
	if quiet {
		return verdictEmpty
	}
	ctx, cancel := context.WithTimeout(parent, probeBudget)
	defer cancel()
	fg, err := s.groupProbe(ctx)
	if err != nil {
		s.logger.Info("marge group poll: could not read the firmware's own group in time, holding",
			slog.String("comp", "marge"), slog.String("err", err.Error()))
		return verdictHold
	}
	if fg.empty() {
		s.adopt.mu.Lock()
		s.adopt.emptyTill = time.Now().Add(probeQuiet)
		s.adopt.mu.Unlock()
		return verdictEmpty
	}
	if ok, why := s.AdoptFirmwareGroup(fg); !ok {
		s.logger.Warn("marge group poll: the firmware holds a pair STR cannot vouch for, leaving it as it is",
			slog.String("comp", "marge"), slog.String("reason", why),
			slog.String("status", fg.Status), slog.String("master", fg.MasterDeviceID))
		return verdictHold
	}
	return verdictAdopted
}

// respondNoGroup is the cloud's answer to an unpaired speaker's group poll.
func respondNoGroup(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?><group/>`))
}

// respondGroupHold answers "cloud unavailable": what every stock speaker has
// heard since the shutdown, and what keeps the firmware's pair untouched.
func respondGroupHold(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?><errors><error value="503" name="SERVICE_UNAVAILABLE"/></errors>`))
}
