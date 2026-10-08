// Partner probe: whether the OTHER half of a restored stereo pair answers.
// See loadGroup for why this matters (a pair whose partner is gone makes the
// firmware refuse every source activation).

package marge

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Startup probe budget. A speaker that has just rebooted (an OTA, a power cut)
// often starts the agent before its Wi-Fi is associated, so the first probe
// fails with "network is unreachable" although the partner is fine. Latching
// that single miss made every play on both halves of both pairs fail with
// stereo-incomplete after the v1.0.9 update (#1208, four ST10 in two pairs).
// So the startup probe retries for about a minute and a half before it records
// the partner as gone. Bounded and once per agent start, not a poll.
const (
	partnerProbeAttempts = 6
	partnerProbeInterval = 15 * time.Second
	partnerProbeTimeout  = 4 * time.Second
	// partnerRecheckTimeout bounds the on-demand re-probe a play request or a
	// zone read triggers while the partner is recorded as gone.
	partnerRecheckTimeout = 3 * time.Second
	// partnerRecheckEvery rate-limits that re-probe: inside the window the
	// cached verdict stands, so a burst of key presses or the app's zone poll
	// costs at most one request to the partner per window.
	partnerRecheckEvery = 10 * time.Second
)

// probePartnerHTTP is the default partner probe: the partner's own Bose web
// API answers /info whenever that speaker is on the network.
func probePartnerHTTP(ip string, timeout time.Duration) error {
	c := &http.Client{Timeout: timeout}
	resp, err := c.Get("http://" + ip + ":8090/info")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// probePartner runs the configured probe (injectable for tests).
func (s *Server) probePartner(ip string, timeout time.Duration) error {
	if s.partnerProbe != nil {
		return s.partnerProbe(ip, timeout)
	}
	return probePartnerHTTP(ip, timeout)
}

// pairPartner names the OTHER half of g: the role with an address that is not
// this speaker. Empty when the document names no such address.
func (s *Server) pairPartner(g *groupRecord) (ip, id string) {
	self := strings.TrimSpace(s.deviceID)
	for _, r := range g.Roles {
		if strings.TrimSpace(r.IP) == "" {
			continue
		}
		if self != "" && strings.EqualFold(strings.TrimSpace(r.DeviceID), self) {
			continue // this half is us
		}
		ip, id = strings.TrimSpace(r.IP), strings.TrimSpace(r.DeviceID)
	}
	return ip, id
}

// PartnerUnreachable reports the pair partner STR could not reach, empty when
// the partner answered or when there is no pair. It never probes; see
// RecheckPartner for the variant that re-asks a partner recorded as gone.
func (s *Server) PartnerUnreachable() (ip, deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.partnerGoneIP, s.partnerGoneID
}

// notePartnerReachability asks the OTHER half of a restored pair whether it is
// there. It runs once per agent start, retrying a bounded number of times
// (partnerProbeAttempts, partnerProbeInterval apart) and stopping at the first
// answer. No timer and no polling afterwards, because this runs on every
// speaker and NAND and CPU are finite. Only the final failure is recorded.
//
// A failure is recorded, never acted on. Dissolving a pair because a probe
// missed would take a working stereo setup away from somebody whose partner
// was merely restarting, which is far worse than the silence this replaces.
// Nor is the record final: RecheckPartner clears it once the partner answers.
func (s *Server) notePartnerReachability(g *groupRecord) {
	ip, id := s.pairPartner(g)
	if ip == "" {
		return
	}
	attempts := s.partnerAttempts
	if attempts <= 0 {
		attempts = partnerProbeAttempts
	}
	interval := s.partnerInterval
	if interval <= 0 {
		interval = partnerProbeInterval
	}
	var err error
	for i := 1; i <= attempts; i++ {
		if i > 1 {
			time.Sleep(interval)
			s.mu.Lock()
			paired := s.group != nil
			s.mu.Unlock()
			if !paired {
				return // dissolved meanwhile: no half left to miss
			}
		}
		err = s.probePartner(ip, partnerProbeTimeout)
		if err == nil {
			s.mu.Lock()
			s.partnerGoneIP, s.partnerGoneID = "", ""
			s.partnerCheckedAt = time.Now()
			s.mu.Unlock()
			if i > 1 {
				s.logger.Info("marge group: the other half of the stereo pair answered after a retry",
					slog.String("comp", "marge"), slog.String("partner", ip),
					slog.String("partnerDeviceId", id), slog.Int("attempt", i))
			}
			return
		}
		s.logger.Info("marge group: the other half of the stereo pair did not answer yet",
			slog.String("comp", "marge"), slog.String("partner", ip),
			slog.Int("attempt", i), slog.Int("of", attempts), slog.String("err", err.Error()))
	}
	s.mu.Lock()
	if s.group == nil {
		s.mu.Unlock()
		return
	}
	s.partnerGoneIP, s.partnerGoneID = ip, id
	s.partnerCheckedAt = time.Now()
	s.mu.Unlock()
	s.logger.Warn("marge group: the other half of the stereo pair did not answer; the firmware refuses to play while a pair is incomplete",
		slog.String("comp", "marge"), slog.String("partner", ip),
		slog.String("partnerDeviceId", id), slog.Int("attempts", attempts),
		slog.String("err", err.Error()))
}

// RecheckPartner re-probes a partner recorded as gone, clears the record when
// it answers, and reports the verdict afterwards (empty = not gone). It is
// synchronous and runs on demand only (a play request, a zone read), never on
// a timer. With nothing recorded it returns at once without a network call.
// With something recorded at most one probe runs per partnerRecheckEvery;
// calls inside that window, or while a probe is in flight, get the cached
// verdict.
func (s *Server) RecheckPartner() (ip, deviceID string) {
	s.mu.Lock()
	ip, deviceID = s.partnerGoneIP, s.partnerGoneID
	every := s.partnerRecheckEvery
	if every <= 0 {
		every = partnerRecheckEvery
	}
	if ip == "" || s.partnerRechecking || time.Since(s.partnerCheckedAt) < every {
		s.mu.Unlock()
		return ip, deviceID
	}
	s.partnerRechecking = true
	s.mu.Unlock()

	err := s.probePartner(ip, partnerRecheckTimeout)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.partnerRechecking = false
	s.partnerCheckedAt = time.Now()
	if err == nil && s.partnerGoneIP == ip {
		s.partnerGoneIP, s.partnerGoneID = "", ""
		s.logger.Info("marge group: the other half of the stereo pair answers again, the pair is no longer reported incomplete",
			slog.String("comp", "marge"), slog.String("partner", ip),
			slog.String("partnerDeviceId", deviceID))
	}
	return s.partnerGoneIP, s.partnerGoneID
}

// clearPartnerGoneLocked drops the partner-gone record when the pair itself
// changes: a dissolved pair has no half that can be missing, and a pair the
// app just installed was formed with both halves live. Callers hold s.mu.
func (s *Server) clearPartnerGoneLocked() {
	s.partnerGoneIP, s.partnerGoneID = "", ""
}
