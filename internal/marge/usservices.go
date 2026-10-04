// The firmware's own US music services: Pandora and iHeartRadio.
//
// Every SoundTouch carries clients for both in its firmware (source PANDORA,
// provider id 1, and source IHEART, provider id 16, in the source-provider
// catalogue). They talk to Pandora and iHeart directly; a US owner reports
// both still working in the Bose app after the cloud shutdown. What the
// firmware lost is the account bookkeeping around them, which now runs
// through STR's stand-in:
//
//   - it asks which services are available (serviceAvailability) and keeps
//     a service off while the answer says so;
//   - after an account is added (through the Bose app, or the local
//     POST :8090/setMusicServiceAccount) it registers the source with its
//     cloud (POST /streaming/account/<id>/source, the provider id above);
//   - it rebuilds its source list from the account document (/full) on every
//     boot and drops any source that document does not name.
//
// STR's stand-in answered PANDORA "unavailable, geo-restricted" (copied from a
// probe of a German speaker), filed any registration as a media server, and
// named neither source in /full. That shut both services on a US speaker the
// moment STR took over.
//
// What changes, and for whom:
//
//   - A US speaker (region.Info: the STR region, or else the firmware's
//     countryCode) gets both services automatically.
//   - Anywhere else nothing changes unless the opt-in marker exists (the
//     Pandora test from #1140, which now covers iHeartRadio too).
//   - Where enabled: serviceAvailability reports the service available, the
//     firmware's registration is answered as Bose did and persisted, and /full
//     and the account source list carry the source back on every boot.
//
// An account the speaker held before STR is carried over by the reflect file
// boxsnapshot seeds from the first /sources snapshot (Deezer "Path A"): /full
// names it from the first boot on. Once the firmware registers the source
// itself, the stored registration replaces the reflected entry.
//
// iHeart's own GetCountry lookup at api2.iheart.com is deliberately left alone
// (docs/FIRMWARE-NOTES.md): it goes to iHeart directly and is never answered
// here.

package marge

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// nativeService describes one of the firmware's own US services.
type nativeService struct {
	// Type is the firmware's source enum, also the serviceAvailability type.
	Type string
	// ProviderID is the numeric id in the source-provider catalogue.
	ProviderID string
	// SourceID is the id STR gives the source in the account documents,
	// clear of the radio source (3), the media servers (10+) and the
	// reflected sources (100+).
	SourceID string
	// Name is the source's display name in the account documents.
	Name string
	// aliases are other sourcename spellings accepted in a registration.
	aliases []string
}

// nativeServices is the list, in the order the documents carry them.
var nativeServices = []nativeService{
	{Type: "PANDORA", ProviderID: "1", SourceID: "200", Name: "Pandora"},
	{Type: "IHEART", ProviderID: "16", SourceID: "201", Name: "iHeartRadio", aliases: []string{"IHEARTRADIO"}},
}

// NativeServiceTypes lists the source types this file handles.
func NativeServiceTypes() []string {
	out := make([]string, len(nativeServices))
	for i, n := range nativeServices {
		out[i] = n.Type
	}
	return out
}

func nativeByType(typ string) (nativeService, bool) {
	typ = strings.ToUpper(strings.TrimSpace(typ))
	for _, n := range nativeServices {
		if n.Type == typ {
			return n, true
		}
		for _, a := range n.aliases {
			if a == typ {
				return n, true
			}
		}
	}
	return nativeService{}, false
}

// nativeRecord is a source as the firmware registered it. The credential is
// whatever the firmware sent in its addSource callback, kept verbatim so /full
// can hand the same value back; it is stored with 0600 on the speaker's own
// flash, which is where the firmware keeps its accounts too, and it is never
// logged.
type nativeRecord struct {
	Username       string    `json:"username"`
	CredentialType string    `json:"credentialType,omitempty"`
	Credential     string    `json:"credential,omitempty"`
	AddedAt        time.Time `json:"addedAt"`
}

// NativePost is one registration the firmware posted for a US service, as the
// diagnostic bundle shows it: masked account, never the credential.
type NativePost struct {
	At             time.Time `json:"at"`
	Service        string    `json:"service"`
	Path           string    `json:"path"`
	Username       string    `json:"username,omitempty"`
	CredentialType string    `json:"credentialType,omitempty"`
	HasCredential  bool      `json:"hasCredential"`
	// Accepted is false when the service was not enabled on this speaker and
	// the registration went to the generic handler (today's behaviour).
	Accepted bool `json:"accepted"`
}

// nativePostCap bounds the in-memory registration history.
const nativePostCap = 16

// WithPandora wires the opt-in marker and the Pandora store. markerPath is the
// file whose presence enables both US services on any speaker (e.g.
// /mnt/nv/streborn/pandora-optin), storePath where the registered Pandora
// source is persisted. Empty paths keep the feature off.
func WithPandora(markerPath, storePath string) Option {
	return func(s *Server) {
		s.nativeMarker = markerPath
		s.setNativeStore("PANDORA", storePath)
	}
}

// WithIHeart wires the store for the registered iHeartRadio source.
func WithIHeart(storePath string) Option {
	return func(s *Server) { s.setNativeStore("IHEART", storePath) }
}

// WithRegion gives the stand-in the speaker's country (ISO 3166-1 alpha-2,
// "" when unknown), read on every use. A US speaker gets the US services
// without the opt-in.
func WithRegion(fn func() string) Option {
	return func(s *Server) { s.region = fn }
}

func (s *Server) setNativeStore(typ, path string) {
	if path == "" {
		return
	}
	if s.nativeStores == nil {
		s.nativeStores = map[string]string{}
	}
	s.nativeStores[typ] = path
}

// Region returns the country the stand-in works with, "" when unknown.
func (s *Server) Region() string {
	if s.region == nil {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(s.region()))
}

// USRegion reports whether the speaker is a US speaker.
func (s *Server) USRegion() bool { return s.Region() == "US" }

// PandoraEnabled reports whether the opt-in marker exists. It stats the marker
// on every call, so switching it needs no restart. The name predates
// iHeartRadio; the marker enables both services.
func (s *Server) PandoraEnabled() bool {
	if s.nativeMarker == "" {
		return false
	}
	_, err := os.Stat(s.nativeMarker)
	return err == nil
}

// SetPandoraEnabled creates or removes the opt-in marker.
func (s *Server) SetPandoraEnabled(on bool) error {
	if s.nativeMarker == "" {
		return os.ErrInvalid
	}
	if !on {
		if err := os.Remove(s.nativeMarker); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.nativeMarker), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.nativeMarker, []byte("on\n"), 0o644)
}

// nativeMode reports why a service is enabled: "us" (automatic on a US
// speaker), "optin" (the marker), or "off". A service without a store is off:
// a registration that cannot be kept would be lost at the next boot.
func (s *Server) nativeMode(typ string) string {
	if s.nativeStores[typ] == "" {
		return "off"
	}
	if s.USRegion() {
		return "us"
	}
	if s.PandoraEnabled() {
		return "optin"
	}
	return "off"
}

// NativeServiceEnabled reports whether the stand-in offers and keeps the
// given service (PANDORA, IHEART).
func (s *Server) NativeServiceEnabled(typ string) bool {
	n, ok := nativeByType(typ)
	return ok && s.nativeMode(n.Type) != "off"
}

func (s *Server) loadNative(typ string) (nativeRecord, bool) {
	path := s.nativeStores[typ]
	if path == "" {
		return nativeRecord{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nativeRecord{}, false
	}
	var rec nativeRecord
	if json.Unmarshal(b, &rec) != nil || rec.Username == "" {
		return nativeRecord{}, false
	}
	return rec, true
}

func (s *Server) saveNative(typ string, rec nativeRecord) error {
	path := s.nativeStores[typ]
	if path == "" {
		return os.ErrInvalid
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ClearNative forgets the registered source of one service.
func (s *Server) ClearNative(typ string) error {
	n, ok := nativeByType(typ)
	if !ok {
		return os.ErrInvalid
	}
	path := s.nativeStores[n.Type]
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// NativeUsername returns the registered account name unmasked, for the
// agent's own removal call to the firmware. Never for display.
func (s *Server) NativeUsername(typ string) string {
	n, ok := nativeByType(typ)
	if !ok {
		return ""
	}
	rec, _ := s.loadNative(n.Type)
	return rec.Username
}

// NativeServiceStatus is one service in the diagnostic bundle. The credential
// itself never leaves the speaker: only its type and whether one is present.
type NativeServiceStatus struct {
	Type string `json:"type"`
	// Mode is "us", "optin" or "off" (see nativeMode).
	Mode           string    `json:"mode"`
	Registered     bool      `json:"registered"`
	Username       string    `json:"username,omitempty"`
	CredentialType string    `json:"credentialType,omitempty"`
	HasCredential  bool      `json:"hasCredential"`
	AddedAt        time.Time `json:"addedAt,omitempty"`
	// CarriedOver is the account the speaker held before STR, re-advertised
	// from the reflect file (masked), "" when there was none.
	CarriedOver string `json:"carriedOver,omitempty"`
}

// NativeServicesStatus is the stand-in's side of the diagnostic section.
type NativeServicesStatus struct {
	Region   string                `json:"region"`
	US       bool                  `json:"us"`
	OptIn    bool                  `json:"optIn"`
	Services []NativeServiceStatus `json:"services"`
	// Served is the serviceAvailability list exactly as the stand-in answers
	// it right now.
	Served []ServiceAvailability `json:"served"`
	// Posts is the recent registration history, newest last.
	Posts []NativePost `json:"posts,omitempty"`
}

// NativeStatusSnapshot returns the state for the diagnostic bundle.
func (s *Server) NativeStatusSnapshot() NativeServicesStatus {
	st := NativeServicesStatus{Region: s.Region(), US: s.USRegion(), OptIn: s.PandoraEnabled(), Served: s.services()}
	carried := map[string]string{}
	for _, r := range s.reflected() {
		if n, ok := nativeByType(r.Source); ok && carried[n.Type] == "" {
			carried[n.Type] = maskAccount(r.Account)
			if carried[n.Type] == "" {
				carried[n.Type] = "(no account name)"
			}
		}
	}
	for _, n := range nativeServices {
		ns := NativeServiceStatus{Type: n.Type, Mode: s.nativeMode(n.Type), CarriedOver: carried[n.Type]}
		if rec, ok := s.loadNative(n.Type); ok {
			ns.Registered = true
			ns.Username = maskAccount(rec.Username)
			ns.CredentialType = rec.CredentialType
			ns.HasCredential = rec.Credential != ""
			ns.AddedAt = rec.AddedAt
		}
		st.Services = append(st.Services, ns)
	}
	s.nativeMu.Lock()
	st.Posts = append([]NativePost(nil), s.nativePosts...)
	s.nativeMu.Unlock()
	return st
}

func (s *Server) recordNativePost(p NativePost) {
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	s.nativePosts = append(s.nativePosts, p)
	if len(s.nativePosts) > nativePostCap {
		s.nativePosts = s.nativePosts[len(s.nativePosts)-nativePostCap:]
	}
}

// maskAccount keeps the first two characters and the domain of an e-mail-like
// account name, enough for a tester to recognise their own entry in a bundle
// they post publicly, not enough to identify them.
func maskAccount(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	local, domain, hasAt := strings.Cut(u, "@")
	keep := 2
	if len(local) < keep {
		keep = len(local)
	}
	out := local[:keep] + "***"
	if hasAt {
		out += "@" + domain
	}
	return out
}

// services returns the availability list: PANDORA flipped to available and
// IHEART added while each is enabled. DefaultServices itself is never
// modified, and a speaker with neither enabled gets exactly today's list.
func (s *Server) services() []ServiceAvailability {
	out := make([]ServiceAvailability, len(DefaultServices))
	copy(out, DefaultServices)
	for _, n := range nativeServices {
		if s.nativeMode(n.Type) == "off" {
			continue
		}
		found := false
		for i := range out {
			if out[i].Type == n.Type {
				out[i].Available = true
				out[i].Reason = ""
				found = true
			}
		}
		if !found {
			out = append(out, ServiceAvailability{Type: n.Type, Available: true})
		}
	}
	return out
}

// nativeRegistrationType returns the US service an addSource body registers,
// or "" for anything else.
func nativeRegistrationType(body string) string {
	pid := strings.TrimSpace(firstXMLValue(body, "sourceproviderid"))
	name := strings.TrimSpace(firstXMLValue(body, "sourcename"))
	for _, n := range nativeServices {
		if pid == n.ProviderID {
			return n.Type
		}
	}
	if n, ok := nativeByType(name); ok {
		return n.Type
	}
	return ""
}

// credentialOf pulls the <credential> element's type and value out of an
// addSource body. The firmware's documents carry the value either as the
// element's text or in a text="" attribute; both are accepted.
func credentialOf(body string) (typ, value string) {
	i := strings.Index(body, "<credential")
	if i < 0 {
		return "", ""
	}
	rest := body[i:]
	end := strings.IndexByte(rest, '>')
	if end < 0 {
		return "", ""
	}
	tag := rest[:end+1]
	typ = attrValue(tag, "type")
	value = attrValue(tag, "text")
	if !strings.HasSuffix(strings.TrimSpace(tag), "/>") {
		if j := strings.Index(rest, "</credential>"); j > end {
			if inner := strings.TrimSpace(rest[end+1 : j]); inner != "" {
				value = inner
			}
		}
	}
	return typ, value
}

// attrValue reads name="..." out of a single start tag.
func attrValue(tag, name string) string {
	key := name + `="`
	i := strings.Index(tag, " "+key)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(key)+1:]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// noteNativeRegistration logs and records a registration for a US service,
// accepted or not, so a bundle shows every one the firmware made.
func (s *Server) noteNativeRegistration(typ string, r *http.Request, body string, accepted bool) {
	username := firstXMLValue(body, "username")
	credType, cred := credentialOf(body)
	s.recordNativePost(NativePost{
		At: time.Now().UTC(), Service: typ, Path: r.URL.Path,
		Username: maskAccount(username), CredentialType: credType,
		HasCredential: cred != "", Accepted: accepted,
	})
	s.logger.Info("marge: US service source registration from the box",
		slog.String("service", typ),
		slog.String("path", r.URL.Path),
		slog.String("username", maskAccount(username)),
		slog.String("credentialType", credType),
		slog.Bool("hasCredential", cred != ""),
		slog.Bool("accepted", accepted))
}

// respondNativeAddSource answers the firmware's registration of an enabled US
// service: it keeps the source and echoes it back in the addSource shape.
func (s *Server) respondNativeAddSource(w http.ResponseWriter, r *http.Request, typ, body string) {
	n, _ := nativeByType(typ)
	username := firstXMLValue(body, "username")
	credType, cred := credentialOf(body)
	rec := nativeRecord{Username: username, CredentialType: credType, Credential: cred, AddedAt: time.Now().UTC()}
	if err := s.saveNative(n.Type, rec); err != nil {
		s.logger.Warn("marge: US service source could not be persisted",
			slog.String("service", n.Type), slog.String("err", err.Error()))
	}
	s.noteNativeRegistration(n.Type, r, body, true)
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.Header().Set("METHOD_NAME", "addSource")
	w.Header().Set("ETag", `"str-source-`+n.SourceID+`"`)
	w.Header().Set("Location", r.URL.Path)
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>` + nativeSourceXML(n, rec, false)))
}

// nativeSourceXML renders a registered source. full=true uses the /full
// element set (timestamps, sourceSettings), the same order the other /full
// sources use, since a source missing an element the firmware expects fails
// the whole account document.
func nativeSourceXML(n nativeService, rec nativeRecord, full bool) string {
	const ts = "2020-01-01T00:00:00.000+00:00"
	cred := `<credential type="` + xmlEscapeText(rec.CredentialType) + `">` + xmlEscapeText(rec.Credential) + `</credential>`
	if !full {
		return `<source id="` + n.SourceID + `" type="Audio" status="READY">` + cred +
			`<name>` + n.Name + `</name><username>` + xmlEscapeText(rec.Username) + `</username>` +
			`<sourceproviderid>` + n.ProviderID + `</sourceproviderid><sourcename>` + n.Type + `</sourcename></source>`
	}
	return `<source id="` + n.SourceID + `" type="Audio">` +
		`<createdOn>` + ts + `</createdOn>` + cred +
		`<name>` + n.Name + `</name>` +
		`<sourceproviderid>` + n.ProviderID + `</sourceproviderid>` +
		`<sourcename>` + n.Type + `</sourcename>` +
		`<sourceSettings/>` +
		`<updatedOn>` + ts + `</updatedOn>` +
		`<username>` + xmlEscapeText(rec.Username) + `</username>` +
		`</source>`
}

// nativeRecordFor loads an enabled service's record for rendering. The
// firmware reaches the stand-in over loopback (/etc/hosts); the stand-in also
// listens on the LAN, so anyone else asking gets the source with its
// credential masked.
func (s *Server) nativeRecordFor(typ string, reveal bool) (nativeRecord, bool) {
	if s.nativeMode(typ) == "off" {
		return nativeRecord{}, false
	}
	rec, ok := s.loadNative(typ)
	if !ok {
		return nativeRecord{}, false
	}
	if !reveal && rec.Credential != "" {
		rec.Credential = "***"
	}
	return rec, true
}

// nativeFullXML is the registered US sources for /full, "" when none.
func (s *Server) nativeFullXML(reveal bool) string {
	var b strings.Builder
	for _, n := range nativeServices {
		if rec, ok := s.nativeRecordFor(n.Type, reveal); ok {
			b.WriteString(nativeSourceXML(n, rec, true))
		}
	}
	return b.String()
}

// nativeListXML is the registered US sources for the account source list.
func (s *Server) nativeListXML(reveal bool) string {
	var b strings.Builder
	for _, n := range nativeServices {
		if rec, ok := s.nativeRecordFor(n.Type, reveal); ok {
			b.WriteString(nativeSourceXML(n, rec, false))
		}
	}
	return b.String()
}

// nativeSupersedes reports whether a reflected (carried-over) entry of this
// source type is replaced by a registration the stand-in holds and serves,
// so /full never names the same service twice.
func (s *Server) nativeSupersedes(source string) bool {
	n, ok := nativeByType(source)
	if !ok {
		return false
	}
	_, ok = s.nativeRecordFor(n.Type, false)
	return ok
}

// fromLoopback reports whether a request came from the speaker itself.
func fromLoopback(r *http.Request) bool {
	if r == nil {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// nativeRequestType returns the US service a stand-in request concerns, for
// the diagnostic INFO line and the spy log masking, or "". It matches the path
// or a body naming the source; the body itself is never logged.
func nativeRequestType(path, body string) string {
	lp := strings.ToLower(path)
	for _, n := range nativeServices {
		if strings.Contains(lp, strings.ToLower(n.Type)) {
			return n.Type
		}
	}
	if body == "" {
		return ""
	}
	for _, n := range nativeServices {
		if strings.Contains(body, "<sourcename>"+n.Type+"</sourcename>") ||
			strings.Contains(body, "<sourceproviderid>"+n.ProviderID+"</sourceproviderid>") ||
			strings.Contains(body, `source="`+n.Type+`"`) {
			return n.Type
		}
	}
	return ""
}

// maskCredentials blanks every <credential> value in a body (element text and
// text="" attribute) for anything that is stored or shown.
func maskCredentials(body string) string {
	var b strings.Builder
	rest := body
	for {
		i := strings.Index(rest, "<credential")
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i])
		rest = rest[i:]
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			b.WriteString(rest)
			return b.String()
		}
		tag := rest[:end+1]
		if v := attrValue(tag, "text"); v != "" {
			tag = strings.Replace(tag, `text="`+v+`"`, `text="***"`, 1)
		}
		b.WriteString(tag)
		rest = rest[end+1:]
		if strings.HasSuffix(strings.TrimSpace(tag), "/>") {
			continue
		}
		if j := strings.Index(rest, "</credential>"); j >= 0 {
			if strings.TrimSpace(rest[:j]) != "" {
				b.WriteString("***")
			}
			b.WriteString("</credential>")
			rest = rest[j+len("</credential>"):]
		}
	}
}
