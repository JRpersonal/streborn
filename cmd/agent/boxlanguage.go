package main

// Closing the firmware's out-of-box language gate on a box that never had it
// closed.
//
// A SoundTouch whose sysLanguage is 0 sits in systemstate SETUP_LANG_NOT_SET:
// the "not set up" LED animation, and on every wake the out-of-box SETUP source
// comes back (SoundTouch 20 field bundle 2026-10-22: the leave-setup watcher
// cleared it at boot and again 1m44s later, and a station tapped right after a
// wake was switched away). The leave-setup watcher only ever cleared the
// symptom. run.sh's finalize_oob_setup sets the language, but only on the
// provisioning path, so a box STR was installed onto while already on the LAN
// keeps the gate open for good.
//
// So the agent closes it itself: once per boot, only when the box reports the
// gate open, with a bounded backoff, and only POST /language. Never /name: on
// this firmware a /name write resets margeURL to a Bose default as a side
// effect, which would point the speaker away from STR's marge.
//
// The value follows run.sh's resolve_setup_language: the language the user
// picked in the desktop app's setup wizard (lang.conf on the stick, when one
// is in), else the one persisted next to region.txt on NAND, else the
// speaker's country (the wizard's region, else the firmware's own
// countryCode), else English. 0 is never sent: it is the factory "unset" value
// and leaves the gate exactly where it was.
//
// While the box is in its setup access point (SETUP_AP_OOB) the provisioning
// in run.sh owns the gate and sets the same value itself, so the agent stays
// out of it and looks again later.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// sysLanguageEnglish is the neutral worldwide default (3, NOT 0: 0 is unset).
const sysLanguageEnglish = 3

// systemStateLangNotSet is the /setup systemstate of a box whose language
// gate is still open.
const systemStateLangNotSet = "SETUP_LANG_NOT_SET"

// setupStateAPOOB is the /setup state while the box runs its setup access
// point, i.e. while it is being provisioned.
const setupStateAPOOB = "SETUP_AP_OOB"

// stickLangConf is the desktop app's lang.conf on the stick (run.sh's $STICK).
const stickLangConf = "/media/sda1/lang.conf"

// boxLanguageRetryDelays bounds the gate's work per boot: the first check
// after boxLanguageFirstCheck (or earlier on a nudge), then one more per
// entry, and nothing after the list ends. Total under 20 minutes, six
// /setup reads at most: no polling loop on a config endpoint.
var (
	boxLanguageFirstCheck  = 30 * time.Second
	boxLanguageRetryDelays = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}
)

// boxLanguageNudge lets the leave-setup watcher, which reads /setup anyway when
// it finds the SETUP source, start the gate check at once instead of waiting
// out the first delay. Buffered by one; a nudge with one pending is dropped.
var boxLanguageNudge = make(chan struct{}, 1)

// nudgeBoxLanguage asks the language gate to check now. Never blocks.
func nudgeBoxLanguage() {
	select {
	case boxLanguageNudge <- struct{}{}:
	default:
	}
}

// validSysLanguage reports whether n is a language the firmware defines (1 to
// 25, 14 is a gap in the enum).
func validSysLanguage(n int) bool {
	return n >= 1 && n <= 25 && n != 14
}

// parseLangFile reads the first one- or two-digit number in a lang.txt body,
// the same rule run.sh's resolve_setup_language applies.
func parseLangFile(b []byte) int {
	s := string(b)
	start := strings.IndexAny(s, "0123456789")
	if start < 0 {
		return 0
	}
	end := start
	for end < len(s) && end-start < 2 && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(s[start:end])
	if err != nil {
		return 0
	}
	return n
}

// parseLangConf reads sysLanguage from the desktop app's lang.conf JSON.
func parseLangConf(b []byte) int {
	var c struct {
		SysLanguage int `json:"sysLanguage"`
	}
	if json.Unmarshal(b, &c) != nil {
		return 0
	}
	return c.SysLanguage
}

// resolveBoxLanguage picks the sysLanguage to close the gate with, and names
// where it came from for the log line. Called only when a POST is due, so the
// files are read a handful of times per boot at most.
func resolveBoxLanguage(langConf, langFile string, country func() string) (int, string) {
	if langConf != "" {
		if b, err := os.ReadFile(langConf); err == nil {
			if n := parseLangConf(b); validSysLanguage(n) {
				return n, "lang.conf"
			}
		}
	}
	if langFile != "" {
		if b, err := os.ReadFile(langFile); err == nil {
			if n := parseLangFile(b); validSysLanguage(n) {
				return n, "lang.txt"
			}
		}
	}
	if country != nil {
		if cc := country(); cc != "" {
			if n := sysLanguageForCountry(cc); validSysLanguage(n) {
				return n, "country " + strings.ToUpper(strings.TrimSpace(cc))
			}
		}
	}
	return sysLanguageEnglish, "default"
}

// sysLanguageForCountry maps an ISO 3166-1 alpha-2 country code to the Bose
// sysLanguage of its dominant language, 0 when unknown. It MUST match
// sticksetup.SysLanguageForCountry (and run.sh's lang_int_for_cc); the agent
// cannot import sticksetup, which embeds a Windows binary, so
// TestSysLanguageForCountryParity pins the copies together.
func sysLanguageForCountry(cc string) int {
	switch strings.ToUpper(strings.TrimSpace(cc)) {
	case "DK", "GL", "FO":
		return 1
	case "DE", "AT", "CH", "LI":
		return 2
	case "US", "GB", "IE", "AU", "NZ", "CA", "ZA", "IN", "SG", "NG", "PH", "MT":
		return 3
	case "ES", "MX", "AR", "CO", "CL", "PE", "VE", "EC", "GT", "CU", "BO",
		"DO", "HN", "PY", "SV", "NI", "CR", "PA", "UY":
		return 4
	case "FR", "LU", "MC", "SN", "CI":
		return 5
	case "IT", "SM", "VA":
		return 6
	case "NL", "BE", "SR":
		return 7
	case "SE":
		return 8
	case "JP":
		return 9
	case "CN":
		return 10
	case "TW", "HK", "MO":
		return 11
	case "KR", "KP":
		return 12
	case "TH":
		return 13
	case "CZ":
		return 15
	case "FI":
		return 16
	case "GR", "CY":
		return 17
	case "NO":
		return 18
	case "PL":
		return 19
	case "PT", "BR", "AO", "MZ":
		return 20
	case "RO", "MD":
		return 21
	case "RU", "BY", "KZ", "KG", "UA":
		return 22
	case "SI":
		return 23
	case "TR":
		return 24
	case "HU":
		return 25
	}
	return 0
}

// boxLanguageGate is the once-per-boot check. The fields are the seams the
// tests drive; newBoxLanguageGate wires the real box.
type boxLanguageGate struct {
	logger *slog.Logger
	// setupState reads /setup: its state and systemstate attributes.
	setupState func(ctx context.Context) (state, systemState string, err error)
	// setLanguage POSTs /language.
	setLanguage func(ctx context.Context, n int) error
	// resolve picks the value (resolveBoxLanguage).
	resolve func() (int, string)
	// wait blocks for d, returning early on a nudge or false on ctx end.
	wait func(ctx context.Context, d time.Duration) bool
}

func newBoxLanguageGate(boxHost, langFile string, country func() string, logger *slog.Logger) *boxLanguageGate {
	if boxHost == "" {
		boxHost = "127.0.0.1"
	}
	client := boxapi.New(boxHost)
	return &boxLanguageGate{
		logger: logger,
		setupState: func(ctx context.Context) (string, string, error) {
			st, err := client.GetSetupStatus(ctx)
			return st.State, st.SystemState, err
		},
		setLanguage: client.SetSysLanguage,
		resolve:     func() (int, string) { return resolveBoxLanguage(stickLangConf, langFile, country) },
		wait: func(ctx context.Context, d time.Duration) bool {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-t.C:
			case <-boxLanguageNudge:
			}
			return true
		},
	}
}

// run checks the gate at most 1+len(boxLanguageRetryDelays) times and stops
// for this boot as soon as the box reports it closed, either because it
// already was or because STR's POST closed it. It returns whether the gate is
// known to be closed.
func (g *boxLanguageGate) run(ctx context.Context) bool {
	delays := append([]time.Duration{boxLanguageFirstCheck}, boxLanguageRetryDelays...)
	for i, d := range delays {
		if !g.wait(ctx, d) {
			return false
		}
		done, retry := g.check(ctx, i+1)
		if done {
			return true
		}
		if !retry {
			return false
		}
	}
	g.logger.Warn("box language: the speaker's language gate is still open after every attempt this boot; leaving it until the next boot",
		"attempts", len(delays))
	return false
}

// check is one look at the gate. done: the gate is closed. retry: worth
// another look later.
func (g *boxLanguageGate) check(ctx context.Context, attempt int) (done, retry bool) {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	setup, state, err := g.setupState(rctx)
	cancel()
	if err != nil {
		g.logger.Debug("box language: /setup not readable yet", "attempt", attempt, "err", err)
		return false, true
	}
	if state != systemStateLangNotSet {
		g.logger.Debug("box language: the speaker's language gate is closed", "systemState", state, "attempt", attempt)
		return true, false
	}
	if setup == setupStateAPOOB {
		g.logger.Info("box language: the speaker has no language set but is being provisioned (setup access point), leaving the language to the provisioning",
			"attempt", attempt)
		return false, true
	}
	n, from := g.resolve()
	if !validSysLanguage(n) {
		n, from = sysLanguageEnglish, "default"
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = g.setLanguage(wctx, n)
	cancel()
	if err != nil {
		g.logger.Warn("box language: the speaker has no language set (out-of-box state), setting it failed",
			"sysLanguage", n, "from", from, "attempt", attempt, "err", err)
		return false, true
	}
	rctx, cancel = context.WithTimeout(ctx, 5*time.Second)
	_, after, err := g.setupState(rctx)
	cancel()
	if err == nil && after != systemStateLangNotSet {
		g.logger.Info("box language: the speaker had no language set and sat in its out-of-box setup state; set it so it stops returning to setup on every wake",
			"sysLanguage", n, "from", from, "systemState", after, "attempt", attempt)
		return true, false
	}
	g.logger.Warn("box language: set the speaker's language, but it still reports its out-of-box state",
		"sysLanguage", n, "from", from, "systemState", after, "attempt", attempt)
	return false, true
}
