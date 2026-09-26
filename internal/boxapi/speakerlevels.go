// speakerlevels.go: the front-center and rear-surround levels of a home
// theater system, which the SoundTouch Web API exposes as
// /audioproductlevelcontrols.
//
// Mail 2026-09-25, a SoundTouch 300 owner with two soundbars and Virtually
// Invisible 300 surrounds on each: the surrounds play, but their level can
// only be changed together with the bar. That is correct for anything STR did
// until now, because the surrounds hang off the bar's own wireless link and
// never appear on the network, so the only thing STR can address is the bar.
//
// The bar itself has the knob. The official Web API v1.1 documents
// frontCenterSpeakerLevel and rearSurroundSpeakersLevel on this route, gated
// exactly the way the tone controls are: use it only when /capabilities lists
// it. This file follows boxapi.toneControlsBass step for step, including the
// per-host verdict cache, for the same two reasons given there.

package boxapi

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Level names, as used by the HTTP layer and the XML attribute name minus the
// suffix the firmware spells out.
const (
	LevelFrontCenter   = "frontCenter"
	LevelRearSurrounds = "rearSurrounds"
)

// SpeakerLevel is one adjustable level with the range the firmware imposes.
// Avail is false when the system does not offer that particular speaker, which
// happens independently per level: a bar with surrounds but no separate centre
// reports only the rear one.
type SpeakerLevel struct {
	Value int  `json:"value"`
	Min   int  `json:"min"`
	Max   int  `json:"max"`
	Step  int  `json:"step"`
	Avail bool `json:"available"`
}

// SpeakerLevels is the whole /audioproductlevelcontrols state. Supported is
// false when the box does not advertise the capability at all, which is every
// speaker that is not a home theater system.
type SpeakerLevels struct {
	Supported     bool         `json:"supported"`
	FrontCenter   SpeakerLevel `json:"frontCenter"`
	RearSurrounds SpeakerLevel `json:"rearSurrounds"`
}

// levelControlsHosts caches the /capabilities verdict per box host, for the
// same reasons as toneControlsHosts: the settings read runs on the agent's
// 30 s poll and must not re-probe a box that does not have it, and a write
// arrives on a freshly constructed Client that must not probe on the hot
// slider path. Only a definite verdict is stored, so a transport error or a
// boot-window HTTP error leaves the box probeable again.
var levelControlsHosts sync.Map // host string -> bool

// ForgetLevelControls drops the cached /capabilities verdict for one host.
// Only tests need it: httptest hands out ephemeral ports that a later server
// can be given again, and without this a test inherits the verdict cached by
// whichever earlier one happened to bind the same port.
func ForgetLevelControls(host string) { levelControlsHosts.Delete(host) }

// hasLevelControls reports whether /capabilities advertises the route.
func (c *Client) hasLevelControls(ctx context.Context) bool {
	if verdict, cached := levelControlsHosts.Load(c.Host); cached {
		return verdict.(bool)
	}
	var caps struct {
		Capabilities []struct {
			Name string `xml:"name,attr"`
		} `xml:"capability"`
	}
	// A failed probe stores nothing. The box web server answers errors while
	// the firmware is still booting, and caching one stray boot-window reply
	// would hide the sliders for the whole process lifetime.
	if err := c.getXML(ctx, "/capabilities", &caps); err != nil {
		return false
	}
	advertised := false
	for _, entry := range caps.Capabilities {
		if strings.EqualFold(strings.TrimSpace(entry.Name), "audioproductlevelcontrols") {
			advertised = true
			break
		}
	}
	levelControlsHosts.Store(c.Host, advertised)
	return advertised
}

// levelXML is the shape of one <...SpeakerLevel .../> element.
type levelXML struct {
	Value int `xml:"value,attr"`
	Min   int `xml:"minValue,attr"`
	Max   int `xml:"maxValue,attr"`
	Step  int `xml:"step,attr"`
}

func (l *levelXML) toLevel() SpeakerLevel {
	// A collapsed range means there is nothing to adjust, so reporting it as
	// available would paint a slider that cannot move.
	if l == nil || l.Min == l.Max {
		return SpeakerLevel{}
	}
	step := l.Step
	if step < 1 {
		step = 1
	}
	return SpeakerLevel{Value: l.Value, Min: l.Min, Max: l.Max, Step: step, Avail: true}
}

// GetSpeakerLevels reads the centre and surround levels. Supported is false
// and the rest zero on any box that does not advertise the capability, which
// the caller shows as "this speaker has no separate levels" rather than as an
// error: not having surrounds is the normal case.
func (c *Client) GetSpeakerLevels(ctx context.Context) (SpeakerLevels, error) {
	if !c.hasLevelControls(ctx) {
		return SpeakerLevels{}, nil
	}
	var raw struct {
		Front *levelXML `xml:"frontCenterSpeakerLevel"`
		Rear  *levelXML `xml:"rearSurroundSpeakersLevel"`
	}
	if err := c.getXML(ctx, "/audioproductlevelcontrols", &raw); err != nil {
		return SpeakerLevels{}, err
	}
	out := SpeakerLevels{
		Supported:     true,
		FrontCenter:   raw.Front.toLevel(),
		RearSurrounds: raw.Rear.toLevel(),
	}
	// Advertised but offering neither level is not something to show a user a
	// pair of dead sliders for.
	if !out.FrontCenter.Avail && !out.RearSurrounds.Avail {
		out.Supported = false
	}
	return out, nil
}

// SetSpeakerLevel writes one level. which is LevelFrontCenter or
// LevelRearSurrounds.
//
// Only the named element is sent: the route's documented partial-update
// contract is that a level left out of the POST is not changed, which is the
// same contract SetBass relies on for treble. Writing both every time would
// make a stale cached value in the caller overwrite a change made meanwhile on
// the Bose remote.
func (c *Client) SetSpeakerLevel(ctx context.Context, which string, value int) error {
	var el string
	switch which {
	case LevelFrontCenter:
		el = "frontCenterSpeakerLevel"
	case LevelRearSurrounds:
		el = "rearSurroundSpeakersLevel"
	default:
		return fmt.Errorf("unknown speaker level %q", which)
	}
	body := fmt.Sprintf(`<audioproductlevelcontrols><%s value="%d" /></audioproductlevelcontrols>`, el, value)
	return c.postXML(ctx, "/audioproductlevelcontrols", body)
}
