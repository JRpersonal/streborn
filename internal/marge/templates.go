package marge

// XML templates and constants for Marge responses.
//
// Source: docs/probe-api-ST10.txt, pulled from the real BoseApp HTTP API
// on 8090. These responses show which state the box derives from Marge data.
// We build our responses so that the box can derive the same
// (or a richer) configuration in the end.
//
// Caution: all templates defined here are read only and contain
// placeholders like {{.DeviceID}}, which are filled at runtime.

// AccountConfigured describes the skeleton of a response that signals to the box
// "You are connected to a Bose account".
//
// We do not yet know which concrete endpoint of the box returns this
// information. Guess based on the BoseApp API:
//   - /info shows margeAccountUUID
//   - /soundTouchConfigurationStatus switches to SOUNDTOUCH_CONFIGURED
//
// Once spy mode reveals the real requests, this will become concrete here.
const AccountConfiguredXMLTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<MargeAccount>
  <accountUUID>{{.AccountUUID}}</accountUUID>
  <accountEmail>{{.AccountEmail}}</accountEmail>
  <token>{{.AuthToken}}</token>
  <status>ACTIVE</status>
  <created>{{.CreatedAt}}</created>
</MargeAccount>`

// EmptyPresetsXML is the response when the account has no presets yet.
// Matches the /presets response of the box in factory state.
const EmptyPresetsXML = `<?xml version="1.0" encoding="UTF-8"?>
<presets/>`

// PresetsXMLTemplate is the response when the account has presets.
// Each preset contains a ContentItem which in turn carries source, sourceAccount
// and source-specific fields.
const PresetsXMLTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<presets>{{range .Presets}}
  <preset id="{{.ID}}" createdOn="{{.CreatedOn}}" updatedOn="{{.UpdatedOn}}">
    <ContentItem source="{{.Source}}" type="{{.Type}}" location="{{.Location}}" sourceAccount="{{.SourceAccount}}" isPresetable="true">
      <itemName>{{.ItemName}}</itemName>
      <containerArt>{{.ContainerArt}}</containerArt>
    </ContentItem>
  </preset>{{end}}
</presets>`

// EmptyRecentsXML when no recents are present.
const EmptyRecentsXML = `<?xml version="1.0" encoding="UTF-8"?>
<recents/>`

// ServiceAvailabilityXMLTemplate lists which streaming providers are available.
// Observation from the box: PANDORA has a geo restriction reason,
// AMAZON/DEEZER/SPOTIFY/AIRPLAY/LOCAL_MUSIC are available.
const ServiceAvailabilityXMLTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<serviceAvailability>
  <services>{{range .Services}}
    <service type="{{.Type}}" isAvailable="{{.Available}}"{{if .Reason}} reason="{{.Reason}}"{{end}}/>{{end}}
  </services>
</serviceAvailability>`

// SourcesXMLTemplate is the format the box emits via /sources.
// We return the same structure when Marge pushes the source list.
const SourcesXMLTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<sources deviceID="{{.DeviceID}}">{{range .Items}}
  <sourceItem source="{{.Source}}"{{if .Account}} sourceAccount="{{.Account}}"{{end}} status="{{.Status}}" isLocal="{{.IsLocal}}" multiroomallowed="{{.MultiroomAllowed}}">{{.DisplayName}}</sourceItem>{{end}}
</sources>`

// SoundTouchConfiguredXML signals a successfully configured box.
const SoundTouchConfiguredXML = `<?xml version="1.0" encoding="UTF-8"?>
<SoundTouchConfigurationStatus status="SOUNDTOUCH_CONFIGURED"/>`

// SoundTouchNotConfiguredXML is the default state.
const SoundTouchNotConfiguredXML = `<?xml version="1.0" encoding="UTF-8"?>
<SoundTouchConfigurationStatus status="SOUNDTOUCH_NOT_CONFIGURED"/>`

// DefaultServices is the list of streaming providers we report as available.
// Geo restriction reasons are taken from the real ST10 probe.
var DefaultServices = []ServiceAvailability{
	{Type: "PANDORA", Available: false, Reason: "PANDORA_GEO_RESTRICTION_ERROR"},
	{Type: "AIRPLAY", Available: true},
	{Type: "AMAZON", Available: true},
	{Type: "BLUETOOTH", Available: false, Reason: "INVALID_SOURCE_TYPE"},
	{Type: "BMX", Available: false},
	{Type: "DEEZER", Available: true},
	{Type: "LOCAL_MUSIC", Available: true},
	{Type: "NOTIFICATION", Available: false},
	{Type: "QPLAY", Available: false},
	{Type: "SPOTIFY", Available: true},
	{Type: "STORED_MUSIC_MEDIA_RENDERER", Available: false},
	{Type: "UPNP", Available: false},
}

// ServiceAvailability is a single streaming provider entry.
type ServiceAvailability struct {
	Type      string
	Available bool
	Reason    string
}

// Preset represents a single preset entry.
//
// Escaping contract: every string field is inserted VERBATIM into the presets
// XML (see PresetsXML) - the caller must XML-escape user-controlled values
// (station names, art URLs) before handing them over. The agent's preset
// source does this via its margeXMLEscape helper; keep any new producer to the
// same rule or a station name containing '&' breaks the box's preset parse.
type Preset struct {
	ID            int
	CreatedOn     int64
	UpdatedOn     int64
	Source        string
	Type          string
	Location      string
	SourceAccount string
	ItemName      string
	ContainerArt  string
}

// SourceItem represents a streaming source.
type SourceItem struct {
	Source           string
	Account          string
	Status           string
	IsLocal          bool
	MultiroomAllowed bool
	DisplayName      string
}

// AccountInfo contains the data of the Marge account.
type AccountInfo struct {
	AccountUUID  string
	AccountEmail string
	AuthToken    string
	CreatedAt    string
}
