// Pandora, route A: the speaker's OWN Pandora client, opt-in.
//
// Every SoundTouch carries a Pandora client in its firmware (source PANDORA,
// provider id 1 in the source-provider catalogue). It logs in to Pandora
// itself, with the account the user adds through the speaker's local API
// (POST :8090/setMusicServiceAccount, source="PANDORA"). After that local call
// the firmware registers the source with its cloud (POST
// /streaming/account/<id>/source, provider id 1) and keeps it only while the
// account document it polls (/full) still names it.
//
// STR's stand-in has always answered the speaker's service-availability poll
// with PANDORA unavailable and PANDORA_GEO_RESTRICTION_ERROR, a value copied
// from a probe of a German speaker. That shut route A for a US speaker too. The
// opt-in changes three answers and nothing else:
//
//   - the availability poll reports PANDORA available,
//   - the stand-in accepts the firmware's Pandora registration and keeps it
//     (persisted, so it survives an agent restart),
//   - /full and the account source list carry that source back.
//
// Whether Pandora still accepts the firmware's partner login after the Bose
// shutdown is the open question this exists to answer. Pandora is US-only by
// IP, so only a US tester can; the diagnostics below are what their bundle
// shows. Default off: without the marker file nothing here changes a single
// byte of any answer.

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

// pandoraProviderID is PANDORA's numeric id in the source-provider catalogue.
const pandoraProviderID = "1"

// pandoraSourceID is the id STR gives the Pandora source in the account
// documents. Clear of the radio source (3), the media servers (10+) and the
// reflected sources (100+).
const pandoraSourceID = "200"

// pandoraRecord is the Pandora source as the firmware registered it. The
// credential is whatever the firmware sent in its addSource callback, kept
// verbatim so /full can hand the same value back; it is stored with 0600 on the
// speaker's own flash, which is where the firmware keeps its accounts too, and
// it is never logged.
type pandoraRecord struct {
	Username       string    `json:"username"`
	CredentialType string    `json:"credentialType,omitempty"`
	Credential     string    `json:"credential,omitempty"`
	AddedAt        time.Time `json:"addedAt"`
}

// WithPandora wires the route-A opt-in. markerPath is the file whose presence
// enables it (e.g. /mnt/nv/streborn/pandora-optin), storePath where the
// registered source is persisted. Empty paths keep the feature off.
func WithPandora(markerPath, storePath string) Option {
	return func(s *Server) {
		s.pandoraMarker = markerPath
		s.pandoraStore = storePath
	}
}

// PandoraEnabled reports whether the route-A opt-in is on. It stats the marker
// on every call, so switching it needs no restart.
func (s *Server) PandoraEnabled() bool {
	if s.pandoraMarker == "" {
		return false
	}
	_, err := os.Stat(s.pandoraMarker)
	return err == nil
}

// SetPandoraEnabled creates or removes the opt-in marker.
func (s *Server) SetPandoraEnabled(on bool) error {
	if s.pandoraMarker == "" {
		return os.ErrInvalid
	}
	if !on {
		if err := os.Remove(s.pandoraMarker); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.pandoraMarker), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.pandoraMarker, []byte("on\n"), 0o644)
}

func (s *Server) loadPandora() (pandoraRecord, bool) {
	if s.pandoraStore == "" {
		return pandoraRecord{}, false
	}
	b, err := os.ReadFile(s.pandoraStore)
	if err != nil {
		return pandoraRecord{}, false
	}
	var rec pandoraRecord
	if json.Unmarshal(b, &rec) != nil || rec.Username == "" {
		return pandoraRecord{}, false
	}
	return rec, true
}

func (s *Server) savePandora(rec pandoraRecord) error {
	if s.pandoraStore == "" {
		return os.ErrInvalid
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.pandoraStore), 0o755); err != nil {
		return err
	}
	tmp := s.pandoraStore + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.pandoraStore)
}

// ClearPandora forgets the registered Pandora source.
func (s *Server) ClearPandora() error {
	if s.pandoraStore == "" {
		return nil
	}
	if err := os.Remove(s.pandoraStore); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// PandoraStatus is what the diagnostic bundle shows. The credential itself
// never leaves the speaker: only its type and whether one is present.
type PandoraStatus struct {
	OptIn          bool      `json:"optIn"`
	Registered     bool      `json:"registered"`
	Username       string    `json:"username,omitempty"`
	CredentialType string    `json:"credentialType,omitempty"`
	HasCredential  bool      `json:"hasCredential"`
	AddedAt        time.Time `json:"addedAt,omitempty"`
}

// PandoraStatusSnapshot returns the opt-in state for the diagnostic bundle.
func (s *Server) PandoraStatusSnapshot() PandoraStatus {
	st := PandoraStatus{OptIn: s.PandoraEnabled()}
	if rec, ok := s.loadPandora(); ok {
		st.Registered = true
		st.Username = maskAccount(rec.Username)
		st.CredentialType = rec.CredentialType
		st.HasCredential = rec.Credential != ""
		st.AddedAt = rec.AddedAt
	}
	return st
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

// services returns the availability list, with PANDORA flipped to available
// while the opt-in is on. DefaultServices itself is never modified.
func (s *Server) services() []ServiceAvailability {
	out := make([]ServiceAvailability, len(DefaultServices))
	copy(out, DefaultServices)
	if !s.PandoraEnabled() {
		return out
	}
	for i := range out {
		if out[i].Type == "PANDORA" {
			out[i].Available = true
			out[i].Reason = ""
		}
	}
	return out
}

// isPandoraRegistration reports whether an addSource body registers Pandora.
func isPandoraRegistration(body string) bool {
	if strings.TrimSpace(firstXMLValue(body, "sourceproviderid")) == pandoraProviderID {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(firstXMLValue(body, "sourcename")), "PANDORA")
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

// respondPandoraAddSource answers the firmware's Pandora registration while the
// opt-in is on: it keeps the source and echoes it back in the addSource shape.
func (s *Server) respondPandoraAddSource(w http.ResponseWriter, r *http.Request, body string) {
	username := firstXMLValue(body, "username")
	credType, cred := credentialOf(body)
	rec := pandoraRecord{Username: username, CredentialType: credType, Credential: cred, AddedAt: time.Now().UTC()}
	if err := s.savePandora(rec); err != nil {
		s.logger.Warn("marge: Pandora source could not be persisted", slog.String("err", err.Error()))
	}
	s.logger.Info("marge: Pandora source registered by the box",
		slog.String("path", r.URL.Path),
		slog.String("username", maskAccount(username)),
		slog.String("credentialType", credType),
		slog.Bool("hasCredential", cred != ""))
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.Header().Set("METHOD_NAME", "addSource")
	w.Header().Set("ETag", `"str-source-`+pandoraSourceID+`"`)
	w.Header().Set("Location", r.URL.Path)
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>` + pandoraSourceXML(rec, false)))
}

// pandoraSourceXML renders the Pandora source. full=true uses the /full
// element set (timestamps, sourceSettings), the same order the other /full
// sources use, since a source missing an element the firmware expects fails
// the whole account document.
func pandoraSourceXML(rec pandoraRecord, full bool) string {
	const ts = "2020-01-01T00:00:00.000+00:00"
	cred := `<credential type="` + xmlEscapeText(rec.CredentialType) + `">` + xmlEscapeText(rec.Credential) + `</credential>`
	if !full {
		return `<source id="` + pandoraSourceID + `" type="Audio" status="READY">` + cred +
			`<name>Pandora</name><username>` + xmlEscapeText(rec.Username) + `</username>` +
			`<sourceproviderid>` + pandoraProviderID + `</sourceproviderid><sourcename>PANDORA</sourcename></source>`
	}
	return `<source id="` + pandoraSourceID + `" type="Audio">` +
		`<createdOn>` + ts + `</createdOn>` + cred +
		`<name>Pandora</name>` +
		`<sourceproviderid>` + pandoraProviderID + `</sourceproviderid>` +
		`<sourcename>PANDORA</sourcename>` +
		`<sourceSettings/>` +
		`<updatedOn>` + ts + `</updatedOn>` +
		`<username>` + xmlEscapeText(rec.Username) + `</username>` +
		`</source>`
}

// pandoraFullXML is the Pandora source for /full, or "" when the opt-in is off
// or nothing is registered.
func (s *Server) pandoraFullXML(reveal bool) string {
	rec, ok := s.pandoraRecordFor(reveal)
	if !ok {
		return ""
	}
	return pandoraSourceXML(rec, true)
}

// pandoraRecordFor loads the record for rendering. The firmware reaches the
// stand-in over loopback (/etc/hosts); the stand-in also listens on the LAN,
// so anyone else asking gets the source with its credential masked.
func (s *Server) pandoraRecordFor(reveal bool) (pandoraRecord, bool) {
	if !s.PandoraEnabled() {
		return pandoraRecord{}, false
	}
	rec, ok := s.loadPandora()
	if !ok {
		return pandoraRecord{}, false
	}
	if !reveal && rec.Credential != "" {
		rec.Credential = "***"
	}
	return rec, true
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

// pandoraListXML is the Pandora source for the account source list.
func (s *Server) pandoraListXML(reveal bool) string {
	rec, ok := s.pandoraRecordFor(reveal)
	if !ok {
		return ""
	}
	return pandoraSourceXML(rec, false)
}

// isPandoraRequest reports whether a stand-in request concerns Pandora, for the
// diagnostic INFO line. It matches the path or a body naming the source; the
// body itself is never logged.
func isPandoraRequest(path, body string) bool {
	if strings.Contains(strings.ToLower(path), "pandora") {
		return true
	}
	if body == "" {
		return false
	}
	return strings.Contains(body, "PANDORA") ||
		strings.Contains(body, "<sourceproviderid>"+pandoraProviderID+"</sourceproviderid>")
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

// PandoraUsername returns the registered account name unmasked, for the agent's
// own removal call to the firmware. Never for display.
func (s *Server) PandoraUsername() string {
	rec, _ := s.loadPandora()
	return rec.Username
}
