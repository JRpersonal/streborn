package marge

// XML Templates und Konstanten für Marge Antworten.
//
// Quelle: docs/probe-api-ST10.txt, gezogen aus der echten BoseApp HTTP API
// auf 8090. Diese Antworten zeigen welchen State die Box aus Marge Daten
// ableitet. Wir bauen unsere Antworten so dass die Box am Ende dieselbe
// (oder eine reichere) Konfiguration ableiten kann.
//
// Achtung: Alle hier definierten Templates sind read only und enthalten
// Platzhalter wie {{.DeviceID}}, die zur Laufzeit gefüllt werden.

// AccountConfigured beschreibt das Skelett einer Antwort die der Box
// signalisiert "Du bist mit einem Bose Konto verbunden".
//
// Wir wissen noch nicht welcher konkrete Endpunkt der Box diese Information
// liefert. Vermutung anhand der BoseApp API:
//   - /info zeigt margeAccountUUID
//   - /soundTouchConfigurationStatus wechselt zu SOUNDTOUCH_CONFIGURED
//
// Sobald der Spy Mode die echten Anfragen offenlegt, wird das hier konkret.
const AccountConfiguredXMLTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<MargeAccount>
  <accountUUID>{{.AccountUUID}}</accountUUID>
  <accountEmail>{{.AccountEmail}}</accountEmail>
  <token>{{.AuthToken}}</token>
  <status>ACTIVE</status>
  <created>{{.CreatedAt}}</created>
</MargeAccount>`

// EmptyPresetsXML ist die Antwort wenn der Account noch keine Presets hat.
// Match mit /presets Antwort der Box im Werkszustand.
const EmptyPresetsXML = `<?xml version="1.0" encoding="UTF-8"?>
<presets/>`

// PresetsXMLTemplate ist die Antwort wenn der Account Presets hat.
// Jeder Preset enthält ein ContentItem das wiederum source, sourceAccount
// und mit Source spezifischen Feldern versehen ist.
const PresetsXMLTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<presets>{{range .Presets}}
  <preset id="{{.ID}}" createdOn="{{.CreatedOn}}" updatedOn="{{.UpdatedOn}}">
    <ContentItem source="{{.Source}}" type="{{.Type}}" location="{{.Location}}" sourceAccount="{{.SourceAccount}}" isPresetable="true">
      <itemName>{{.ItemName}}</itemName>
      <containerArt>{{.ContainerArt}}</containerArt>
    </ContentItem>
  </preset>{{end}}
</presets>`

// EmptyRecentsXML wenn keine Recents vorhanden.
const EmptyRecentsXML = `<?xml version="1.0" encoding="UTF-8"?>
<recents/>`

// ServiceAvailabilityXMLTemplate listet welche Streaming Provider verfuegbar
// sind. Beobachtung aus der Box: PANDORA hat geo restriction reason,
// AMAZON/DEEZER/SPOTIFY/AIRPLAY/LOCAL_MUSIC sind verfuegbar.
const ServiceAvailabilityXMLTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<serviceAvailability>
  <services>{{range .Services}}
    <service type="{{.Type}}" isAvailable="{{.Available}}"{{if .Reason}} reason="{{.Reason}}"{{end}}/>{{end}}
  </services>
</serviceAvailability>`

// SourcesXMLTemplate ist das Format das die Box ueber /sources ausspielt.
// Wir liefern die gleiche Struktur wenn Marge die Source Liste pushed.
const SourcesXMLTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<sources deviceID="{{.DeviceID}}">{{range .Items}}
  <sourceItem source="{{.Source}}"{{if .Account}} sourceAccount="{{.Account}}"{{end}} status="{{.Status}}" isLocal="{{.IsLocal}}" multiroomallowed="{{.MultiroomAllowed}}">{{.DisplayName}}</sourceItem>{{end}}
</sources>`

// SoundTouchConfiguredXML signalisiert eine erfolgreich konfigurierte Box.
const SoundTouchConfiguredXML = `<?xml version="1.0" encoding="UTF-8"?>
<SoundTouchConfigurationStatus status="SOUNDTOUCH_CONFIGURED"/>`

// SoundTouchNotConfiguredXML ist der Default Zustand.
const SoundTouchNotConfiguredXML = `<?xml version="1.0" encoding="UTF-8"?>
<SoundTouchConfigurationStatus status="SOUNDTOUCH_NOT_CONFIGURED"/>`

// DefaultServices ist die Liste der Streaming Provider die wir als verfuegbar
// melden. Geo Restriction Reasons sind aus der echten ST10 Probe uebernommen.
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

// ServiceAvailability ist ein einzelner Streaming Provider Eintrag.
type ServiceAvailability struct {
	Type      string
	Available bool
	Reason    string
}

// Preset bildet einen einzelnen Preset Eintrag ab.
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

// SourceItem bildet eine Streaming Source ab.
type SourceItem struct {
	Source           string
	Account          string
	Status           string
	IsLocal          bool
	MultiroomAllowed bool
	DisplayName      string
}

// AccountInfo enthält die Daten des Marge Accounts.
type AccountInfo struct {
	AccountUUID  string
	AccountEmail string
	AuthToken    string
	CreatedAt    string
}
