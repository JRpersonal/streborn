// Package region decides which country a speaker is in, for the features that
// depend on it: the US music services the firmware still carries (Pandora,
// iHeartRadio, docs/streaming/us-services.md).
//
// Two inputs exist and neither is perfect:
//
//   - the STR region, the country the owner picked in the setup wizard (or
//     later in the app, PUT /api/region). It is where the owner says they
//     live, so it wins whenever it is set. A network install does not always
//     write it.
//   - the firmware's own countryCode from :8090/info. Bose set it at the
//     factory or during the original Bose pairing, so it describes where the
//     speaker was sold, which is usually but not always where it plays now.
//     It is the fallback.
//
// The firmware asks the stand-in which services are available early in its
// boot, often before the agent has read /info. The last countryCode seen is
// therefore kept in a tiny file on the speaker's flash so the very first
// answer after a reboot is already right. The file is written only when the
// value changes, which in practice is once per install.
package region

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Info is the region as the agent reports it (/api/agent/version, the
// diagnostic bundle).
type Info struct {
	// Country is the effective ISO 3166-1 alpha-2 code, "" when unknown.
	Country string `json:"country"`
	// Source names the input Country came from: "str" (the wizard or app
	// setting), "box" (the firmware's countryCode), or "" when unknown.
	Source string `json:"source"`
	// STR and Box are the two raw inputs, for the bundle.
	STR string `json:"strRegion,omitempty"`
	Box string `json:"boxCountryCode,omitempty"`
}

// IsUS reports whether the effective country is the United States.
func (i Info) IsUS() bool { return i.Country == "US" }

// Normalize returns a two-letter upper-case country code, or "" for anything
// that is not one.
func Normalize(cc string) string {
	cc = strings.ToUpper(strings.TrimSpace(cc))
	if len(cc) != 2 || cc[0] < 'A' || cc[0] > 'Z' || cc[1] < 'A' || cc[1] > 'Z' {
		return ""
	}
	return cc
}

// Resolve applies the precedence rule to the two inputs.
func Resolve(str, box string) Info {
	info := Info{STR: Normalize(str), Box: Normalize(box)}
	switch {
	case info.STR != "":
		info.Country, info.Source = info.STR, "str"
	case info.Box != "":
		info.Country, info.Source = info.Box, "box"
	}
	return info
}

// Resolver holds the current inputs. All methods are safe for concurrent use,
// and a nil *Resolver reports an unknown region.
type Resolver struct {
	mu      sync.RWMutex
	str     string
	box     string
	boxPath string
	logger  *slog.Logger
}

// New returns a Resolver. boxPath is where the last firmware countryCode is
// cached ("" disables the cache); it is read once here.
func New(boxPath string, logger *slog.Logger) *Resolver {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Resolver{boxPath: boxPath, logger: logger}
	if boxPath != "" {
		if b, err := os.ReadFile(boxPath); err == nil {
			r.box = Normalize(string(b))
		}
	}
	return r
}

// SetSTR records the STR region (wizard or app setting).
func (r *Resolver) SetSTR(cc string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.str = Normalize(cc)
	r.mu.Unlock()
}

// SetBox records the firmware's countryCode and caches it on change. An empty
// or malformed value is ignored, so a failed /info read never erases a good
// one.
func (r *Resolver) SetBox(cc string) {
	if r == nil {
		return
	}
	cc = Normalize(cc)
	if cc == "" {
		return
	}
	r.mu.Lock()
	changed := cc != r.box
	r.box = cc
	path := r.boxPath
	r.mu.Unlock()
	if !changed {
		return
	}
	r.logger.Info("region: speaker countryCode recorded", "boxCountryCode", cc)
	if path == "" {
		return
	}
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, []byte(cc+"\n"), 0o644); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		r.logger.Warn("region: box country cache write failed", "err", err)
	}
}

// Info returns the effective region.
func (r *Resolver) Info() Info {
	if r == nil {
		return Info{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Resolve(r.str, r.box)
}

// Country returns the effective country code, "" when unknown.
func (r *Resolver) Country() string { return r.Info().Country }
