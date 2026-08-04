// Typed gabbo frame shapes for the <updates> envelope, plus the helpers for
// bare-root frames (errorUpdate / userActivityUpdate) the envelope parse misses.

package boxws

import (
	"encoding/xml"
	"strings"
)

// handleMessage parses an incoming XML notification.
//
// Bose's WebSocket format for hardware preset buttons (measured 2026-05-15):
//
//	<updates deviceID="...">
//	  <nowSelectionUpdated>
//	    <preset id="1" ... >
//	      <ContentItem source="UPNP" location="http://..." sourceAccount="..." isPresetable="true">
//	        <itemName>NDR Info</itemName>
//	      </ContentItem>
//	    </preset>
//	  </nowSelectionUpdated>
//	</updates>
//
// The box follows up with `<nowSelectionUpdated><preset id="0">` and
// INVALID_SOURCE when it cannot activate the source. We only care about the
// first event with id >= 1.
// wsContentItem is the <ContentItem> Bose nests inside a preset or nowPlaying.
type wsContentItem struct {
	Source        string `xml:"source,attr"`
	Type          string `xml:"type,attr"`
	Location      string `xml:"location,attr"`
	SourceAccount string `xml:"sourceAccount,attr"`
	IsPresetable  string `xml:"isPresetable,attr"`
	ItemName      string `xml:"itemName"`
}

// wsPreset is a <preset> element (nowSelectionUpdated / presetSelectionUpdated /
// presetsUpdated). Inner keeps the raw element body so a status marker like
// INVALID_SOURCE / DO_NOT_RESUME can be matched within this element only, never
// against an unrelated frame's track title.
type wsPreset struct {
	ID          string        `xml:"id,attr"`
	ContentItem wsContentItem `xml:"ContentItem"`
	Inner       string        `xml:",innerxml"`
}

// wsNowPlaying is the <nowPlaying> body. Bose has shipped playStatus both as a
// child element and as an attribute across firmware builds, so capture both and
// resolve with playStatus(); reading it typed is what stops a track title that
// merely contains "STOP_STATE" from firing the user-stop suppressor.
type wsNowPlaying struct {
	Source         string        `xml:"source,attr"`
	PlayStatusEl   string        `xml:"playStatus"`
	PlayStatusAttr string        `xml:"playStatus,attr"`
	ContentItem    wsContentItem `xml:"ContentItem"`
}

func (n wsNowPlaying) playStatus() string {
	if n.PlayStatusEl != "" {
		return n.PlayStatusEl
	}
	return n.PlayStatusAttr
}

// gabboFrame is the typed view of one <updates> notification. Bose sends one
// update child per frame; the rest stay nil. Dispatching on which child is
// present (and reading status markers from typed sub-fields) replaces the old
// whole-frame strings.Contains sniffing, which mis-fired whenever a station
// name or track title happened to contain a marker word such as STOP_STATE,
// INVALID_SOURCE, or even an update element name.
type gabboFrame struct {
	XMLName        xml.Name      `xml:"updates"`
	NowSelection   *wsPreset     `xml:"nowSelectionUpdated>preset"`
	PresetSelected *wsPreset     `xml:"presetSelectionUpdated>preset"`
	NowPlaying     *wsNowPlaying `xml:"nowPlayingUpdated>nowPlaying"`

	// Presence-plus-body markers. The *struct with an Inner field is non-nil
	// exactly when the child element is in the frame; Inner bounds any substring
	// match (e.g. STANDBY) to that element's own body.
	ConnectionState *struct {
		Inner string `xml:",innerxml"`
	} `xml:"connectionStateUpdated"`
	PowerState *struct {
		Inner string `xml:",innerxml"`
	} `xml:"powerStateUpdated"`
	VolumeUpdated *struct{} `xml:"volumeUpdated"`
	// BassUpdated is the same shape for the bass control. Turning the bass knob
	// on the speaker or its remote emits a burst of these, and until now every
	// one was logged as an unrecognized frame: seven in 400 ms filled a field
	// log while the real point, that this IS identifiable user activity, was
	// lost (ST30 bundle, 2026-07-29).
	BassUpdated  *struct{} `xml:"bassUpdated"`
	UserActivity *struct{} `xml:"userActivityUpdate"`

	// PresetsUpdated carries the box's full preset list when it changes; the
	// landing spot for preset sync from the box (#14).
	PresetsUpdated *struct {
		Presets []wsPreset `xml:"presets>preset"`
	} `xml:"presetsUpdated"`

	// ZoneUpdated carries the box's multiroom zone / stereo-pair membership when
	// it changes. Non-nil whenever a <zoneUpdated><zone> is present; an empty
	// <zone/> (Master == "") means the zone dissolved (#70, Klaus 2026-06-12).
	ZoneUpdated *wsZone `xml:"zoneUpdated>zone"`

	// GroupUpdated carries the STEREO PAIR, which the firmware keeps separate
	// from the zone: pairing two speakers emits groupUpdated, not zoneUpdated,
	// and /getZone keeps reporting no members throughout. An empty <group />
	// means the pair was torn down.
	//
	// Both speakers emit their own frame, which is what makes this the reliable
	// place to notice a teardown. Undoing a pair in the BOSE app tells STR's
	// cloud stand-in on the master only, so the other speaker kept its pair
	// record forever - and a speaker that believes it is still half of a pair
	// is no longer offered for pairing (field, 2026-08-04, three SoundTouch
	// 10s). The frames were arriving the whole time and were logged as
	// unrecognized.
	GroupUpdated *wsGroupUpdated `xml:"groupUpdated"`

	// LanguageUpdated carries the box's sysLanguage whenever it changes. Parsed
	// typed (not left as an unrecognized frame) because the Wave firmware
	// overwrites a user's language save within ~40-200 ms (2 then 3 back to
	// back, live bundle 2026-07-25) and the revert can only be root-caused by
	// timing the two frames against what else touched the box in that window.
	LanguageUpdated *struct {
		Sys string `xml:"sysLanguage"`
	} `xml:"languageUpdated"`
}

// wsGroupUpdated is the <groupUpdated> body. A self-closing <group /> still
// yields a non-nil Group with no id and no roles: that is the teardown signal.
type wsGroupUpdated struct {
	Group *struct {
		ID     string `xml:"id,attr"`
		Name   string `xml:"name"`
		Master string `xml:"masterDeviceId"`
		Roles  []struct {
			DeviceID string `xml:"deviceId"`
			Role     string `xml:"role"`
			IP       string `xml:"ipAddress"`
		} `xml:"roles>groupRole"`
	} `xml:"group"`
}

// toState flattens the frame into a GroupState.
func (g *wsGroupUpdated) toState() GroupState {
	var st GroupState
	if g == nil || g.Group == nil {
		return st
	}
	st.ID = strings.TrimSpace(g.Group.ID)
	st.Name = strings.TrimSpace(g.Group.Name)
	st.Master = strings.TrimSpace(g.Group.Master)
	for _, r := range g.Group.Roles {
		st.Members = append(st.Members, GroupMember{
			DeviceID: strings.TrimSpace(r.DeviceID),
			Role:     strings.TrimSpace(r.Role),
			IP:       strings.TrimSpace(r.IP),
		})
	}
	return st
}

// GroupState is a stereo pair as the box reports it. Paired reports whether a
// pair exists at all; the zero value means the pair was torn down.
type GroupState struct {
	ID      string
	Name    string
	Master  string
	Members []GroupMember
}

// GroupMember is one speaker in a stereo pair, with its LEFT/RIGHT role.
type GroupMember struct {
	DeviceID string
	Role     string
	IP       string
}

// Paired reports whether this state describes an existing pair.
func (g GroupState) Paired() bool { return g.ID != "" || len(g.Members) > 0 }

// wsZone is the <zone> body of a zoneUpdated frame. Bose puts the master's
// deviceID in the master attr, its LAN IP in senderIPAddress, whether THIS box
// leads in senderIsMaster, and one <member ipaddress="..">deviceID</member> per
// follower.
type wsZone struct {
	Master         string         `xml:"master,attr"`
	SenderIP       string         `xml:"senderIPAddress,attr"`
	SenderIsMaster string         `xml:"senderIsMaster,attr"`
	Members        []wsZoneMember `xml:"member"`
}

type wsZoneMember struct {
	DeviceID string `xml:",chardata"`
	IP       string `xml:"ipaddress,attr"`
	Role     string `xml:"role,attr"`
}

func (z *wsZone) toState() ZoneState {
	st := ZoneState{
		Master:         strings.TrimSpace(z.Master),
		SenderIP:       strings.TrimSpace(z.SenderIP),
		SenderIsMaster: strings.EqualFold(strings.TrimSpace(z.SenderIsMaster), "true"),
	}
	for _, m := range z.Members {
		st.Members = append(st.Members, ZoneMemberState{
			DeviceID: strings.TrimSpace(m.DeviceID),
			IP:       strings.TrimSpace(m.IP),
			Role:     strings.TrimSpace(m.Role),
		})
	}
	return st
}

// ZoneState is the typed multiroom/stereo-pair membership delivered to the
// handler on a zoneUpdated frame. Master == "" means the zone dissolved.
type ZoneState struct {
	Master         string
	SenderIP       string
	SenderIsMaster bool
	Members        []ZoneMemberState
}

// ZoneMemberState is one follower in a ZoneState.
type ZoneMemberState struct {
	DeviceID string
	IP       string
	Role     string
}

// rootLocalName returns the local name of the first XML start element (the
// frame's root), or "" if the data is not parseable. Used to recognise
// bare-root frames the <updates>-typed parse does not capture.
// parseBoxError extracts the fields of a bare <errorUpdate><error .../></errorUpdate>
// gabbo frame. Returns an empty value when s is not an error frame, so the caller
// can fall through to the generic unrecognized-frame path.
func parseBoxError(s string) (value, name, severity, detail string) {
	var e struct {
		XMLName xml.Name `xml:"errorUpdate"`
		Error   struct {
			Value    string `xml:"value,attr"`
			Name     string `xml:"name,attr"`
			Severity string `xml:"severity,attr"`
			Detail   string `xml:",chardata"`
		} `xml:"error"`
	}
	if err := xml.Unmarshal([]byte(s), &e); err != nil {
		return "", "", "", ""
	}
	return e.Error.Value, e.Error.Name, e.Error.Severity, strings.TrimSpace(e.Error.Detail)
}

func rootLocalName(s string) string {
	dec := xml.NewDecoder(strings.NewReader(s))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
}
