package main

// Group templates (#70, BETA): bound App methods for the agent's
// /api/box/zone/templates endpoints, plus a local mirror of the last-seen
// template list per master so the app can offer to re-seed a speaker that
// lost its NAND store (factory reset, reinstall). Templates live on the
// MASTER's agent - the app is only a remote control and a backup.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ZoneTemplateMember is one speaker in a saved group template: its stable
// deviceID plus a last-known IP hint (DHCP leases change; the agent
// re-resolves at activation time).
type ZoneTemplateMember struct {
	DeviceID string `json:"deviceID"`
	IP       string `json:"ip"`
}

// ZoneTemplate mirrors the agent's template object (the fields the app
// needs; extra fields from newer agents are ignored on decode).
type ZoneTemplate struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	Master    ZoneTemplateMember   `json:"master"`
	Members   []ZoneTemplateMember `json:"members"`
	Permanent bool                 `json:"permanent"`
}

// errAgentTooOldForTemplates marks the old-agent trap: an agent that predates
// the templates endpoints answers GET /api/box/zone/templates with 200 + HTML
// from its index catch-all, which would otherwise read as an empty list. The
// "agent-too-old" prefix is the contract the frontend matches on.
var errAgentTooOldForTemplates = errors.New("agent-too-old: this speaker's agent predates group templates; update the speaker first")

// requireTemplatesJSON guards every templates call against the catch-all
// index of a pre-feature agent: only the endpoint's JSON reply is the
// endpoint (same false-success class as the stereo pair-document relay in
// relayStereoGroupDoc / the agent's pushGroupDocToPartner).
func requireTemplatesJSON(resp *http.Response) error {
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		return errAgentTooOldForTemplates
	}
	return nil
}

// ListZoneTemplates reads the group templates stored on a master's agent
// (GET /api/box/zone/templates) -> {ok, master{deviceID}, templates[],
// permanentId, out[]}. On success the list is mirrored to the local config
// dir so it survives a speaker factory reset (see ZoneTemplateMirror).
func (a *App) ListZoneTemplates(host string, port int) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/zone/templates", "", "")
	if err != nil {
		a.logger.Warn("zone templates: list failed", "host", host, "err", err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		herr := readHTTPError(resp)
		a.logger.Warn("zone templates: list rejected", "host", host, "err", herr)
		return nil, herr
	}
	if err := requireTemplatesJSON(resp); err != nil {
		a.logger.Info("zone templates: agent too old for templates (answered non-JSON)",
			"host", host, "contentType", resp.Header.Get("Content-Type"))
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	a.mirrorZoneTemplates(raw)
	return out, nil
}

// SaveZoneTemplate creates or updates a template on the master's agent
// (POST /api/box/zone/templates). The agent decides the master (always the
// box the request lands on) and validates members, so only id, name and
// members are sent. The local mirror is refreshed on success.
func (a *App) SaveZoneTemplate(host string, port int, tpl ZoneTemplate) (map[string]any, error) {
	a.logger.Info("zone templates: saving", "host", host, "name", tpl.Name, "members", len(tpl.Members))
	members := tpl.Members
	if members == nil {
		members = []ZoneTemplateMember{}
	}
	b, err := json.Marshal(map[string]any{"id": tpl.ID, "name": tpl.Name, "members": members})
	if err != nil {
		return nil, err
	}
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/zone/templates", "application/json", string(b))
	if err != nil {
		a.logger.Warn("zone templates: save failed", "host", host, "name", tpl.Name, "err", err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		herr := readHTTPError(resp)
		a.logger.Warn("zone templates: save rejected", "host", host, "name", tpl.Name, "err", herr)
		return nil, herr
	}
	if err := requireTemplatesJSON(resp); err != nil {
		a.logger.Warn("zone templates: save answered non-JSON (agent too old)", "host", host)
		return nil, err
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	a.logger.Info("zone templates: saved", "host", host, "name", tpl.Name)
	// Refresh the mirror so a save is backed up even when no UI list read
	// follows (e.g. the fleet-test harness driving the bound methods).
	if _, lerr := a.ListZoneTemplates(host, port); lerr != nil {
		a.logger.Debug("zone templates: post-save mirror refresh failed", "host", host, "err", lerr)
	}
	return out, nil
}

// DeleteZoneTemplate removes a template from the master's agent
// (DELETE /api/box/zone/templates/{id}) and drops it from the local mirror,
// so a deliberate delete is never resurrected by the re-seed offer.
func (a *App) DeleteZoneTemplate(host string, port int, id string) error {
	resp, err := a.boxDo(host, port, http.MethodDelete, "/api/box/zone/templates/"+url.PathEscape(id), "", "")
	if err != nil {
		a.logger.Warn("zone templates: delete failed", "host", host, "id", id, "err", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		herr := readHTTPError(resp)
		a.logger.Warn("zone templates: delete rejected", "host", host, "id", id, "err", herr)
		return herr
	}
	if err := requireTemplatesJSON(resp); err != nil {
		a.logger.Warn("zone templates: delete answered non-JSON (agent too old)", "host", host)
		return err
	}
	dropZoneTemplateFromMirror(id)
	a.logger.Info("zone templates: deleted", "host", host, "id", id)
	return nil
}

// SetPermanentZoneTemplate flips a template's permanent flag on the master's
// agent (POST /api/box/zone/templates/{id}/permanent) -> {ok, permanentId}.
// At most one template is permanent; the agent enforces that.
func (a *App) SetPermanentZoneTemplate(host string, port int, id string, on bool) (map[string]any, error) {
	b, err := json.Marshal(map[string]bool{"permanent": on})
	if err != nil {
		return nil, err
	}
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/zone/templates/"+url.PathEscape(id)+"/permanent", "application/json", string(b))
	if err != nil {
		a.logger.Warn("zone templates: permanent toggle failed", "host", host, "id", id, "on", on, "err", err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		herr := readHTTPError(resp)
		a.logger.Warn("zone templates: permanent toggle rejected", "host", host, "id", id, "on", on, "err", herr)
		return nil, herr
	}
	if err := requireTemplatesJSON(resp); err != nil {
		a.logger.Warn("zone templates: permanent toggle answered non-JSON (agent too old)", "host", host)
		return nil, err
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	a.logger.Info("zone templates: permanent toggled", "host", host, "id", id, "on", on)
	return out, nil
}

// ActivateZoneTemplate forms the template's group in one call
// (POST /api/box/zone/templates/{id}/activate). The AGENT drives the zone
// form (so activation works identically from the phone remote) and answers
// the same body shape as POST /api/box/zone, so the frontend interprets it
// with the code it already has for forming. Long timeout like FormZone: the
// agent wakes the master first and then drives the firmware, which blows
// through the shared 6 s client (#442 class).
func (a *App) ActivateZoneTemplate(host string, port int, id string) (result map[string]any, err error) {
	a.logger.Info("zone templates: activating", "host", host, "id", id)
	defer func() {
		if err != nil {
			a.logger.Warn("zone templates: activate failed", "host", host, "id", id, "err", err)
		} else {
			a.logger.Info("zone templates: activate done", "host", host, "id", id, "result", result)
		}
	}()
	resp, err := a.boxDoTimeout(host, port, http.MethodPost, "/api/box/zone/templates/"+url.PathEscape(id)+"/activate", "application/json", "{}", zoneCallTimeout)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	if err := requireTemplatesJSON(resp); err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// ---- Local mirror (<UserConfigDir>/ST Reborn/zone-templates.json) ----
//
// The mirror is the app's backup of each master's template list, keyed by the
// master's stable deviceID (survives DHCP changes, same keying as
// known-speakers.json). It exists for exactly one flow: a speaker whose NAND
// store came back empty (true factory reset, reinstall) gets a "restore your
// saved templates?" offer. Written mirror-on-read and fingerprint-gated so an
// unchanged list never costs a file write. localStorage is deliberately not
// used (a WebView2/WKWebView profile can reset on an update, see app_state.go).

// zoneTemplateMirrorEntry is one master's mirrored list.
type zoneTemplateMirrorEntry struct {
	UpdatedAt string         `json:"updatedAt"`
	Templates []ZoneTemplate `json:"templates"`
}

// zoneTemplateMirrorFile is the on-disk shape.
type zoneTemplateMirrorFile struct {
	ByMaster map[string]zoneTemplateMirrorEntry `json:"byMaster"`
}

// zoneTplMirrorMu serialises mirror reads-for-write; zoneTplMirrorWritten
// fingerprints the last write per master so unchanged reads skip the file
// (persistKnownSpeakers discipline). Package-level like appFlagsMu: the
// mirror is app-global state, not per-App.
var (
	zoneTplMirrorMu      sync.Mutex
	zoneTplMirrorWritten = map[string]string{}
)

func zoneTemplateMirrorPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ST Reborn", "zone-templates.json"), nil
}

// loadZoneTemplateMirror reads the mirror file leniently: missing or broken
// yields an empty mirror, never an error (the mirror is a convenience).
func loadZoneTemplateMirror() zoneTemplateMirrorFile {
	m := zoneTemplateMirrorFile{ByMaster: map[string]zoneTemplateMirrorEntry{}}
	path, err := zoneTemplateMirrorPath()
	if err != nil {
		return m
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	if m.ByMaster == nil {
		m.ByMaster = map[string]zoneTemplateMirrorEntry{}
	}
	return m
}

// saveZoneTemplateMirror writes the mirror atomically (temp + rename, the
// SetAppFlag pattern). Callers hold zoneTplMirrorMu.
func saveZoneTemplateMirror(m zoneTemplateMirrorFile) error {
	path, err := zoneTemplateMirrorPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// zoneTemplateFingerprint summarises a template list so unchanged mirror-on-
// read cycles can skip the file write.
func zoneTemplateFingerprint(tpls []ZoneTemplate) string {
	var sb strings.Builder
	for _, t := range tpls {
		sb.WriteString(t.ID)
		sb.WriteString("|")
		sb.WriteString(t.Name)
		sb.WriteString("|")
		if t.Permanent {
			sb.WriteString("p")
		}
		for _, m := range t.Members {
			sb.WriteString("|")
			sb.WriteString(m.DeviceID)
			sb.WriteString("@")
			sb.WriteString(m.IP)
		}
		sb.WriteString(";")
	}
	return sb.String()
}

// mirrorZoneTemplates stores the template list from a raw list response,
// keyed by the master deviceID the agent reported. An EMPTY list is never
// mirrored over an existing entry: the whole point of the mirror is the
// re-seed after a factory reset, and mirroring the reset box's empty answer
// would destroy the very backup the offer needs. Deliberate deletes update
// the mirror through dropZoneTemplateFromMirror instead. Best-effort: a
// config-dir hiccup must never fail the list read.
func (a *App) mirrorZoneTemplates(raw []byte) {
	var doc struct {
		Master struct {
			DeviceID string `json:"deviceID"`
		} `json:"master"`
		Templates []ZoneTemplate `json:"templates"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return
	}
	master := strings.ToUpper(strings.TrimSpace(doc.Master.DeviceID))
	if master == "" || len(doc.Templates) == 0 {
		return
	}
	fingerprint := zoneTemplateFingerprint(doc.Templates)
	zoneTplMirrorMu.Lock()
	defer zoneTplMirrorMu.Unlock()
	if zoneTplMirrorWritten[master] == fingerprint {
		return
	}
	m := loadZoneTemplateMirror()
	m.ByMaster[master] = zoneTemplateMirrorEntry{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Templates: doc.Templates,
	}
	if err := saveZoneTemplateMirror(m); err != nil {
		a.logger.Warn("zone templates: mirror write failed", "err", err)
		return
	}
	zoneTplMirrorWritten[master] = fingerprint
}

// dropZoneTemplateFromMirror removes one template (by its agent-assigned id)
// from whichever master's mirror entry holds it, so a template the user
// deleted on purpose is not offered back by the re-seed flow. An entry left
// empty is removed entirely. Best-effort.
func dropZoneTemplateFromMirror(id string) {
	if id == "" {
		return
	}
	zoneTplMirrorMu.Lock()
	defer zoneTplMirrorMu.Unlock()
	m := loadZoneTemplateMirror()
	changed := false
	for master, e := range m.ByMaster {
		kept := e.Templates[:0]
		for _, t := range e.Templates {
			if t.ID != id {
				kept = append(kept, t)
			}
		}
		if len(kept) == len(e.Templates) {
			continue
		}
		changed = true
		if len(kept) == 0 {
			delete(m.ByMaster, master)
		} else {
			e.Templates = kept
			e.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			m.ByMaster[master] = e
		}
		// The stored list changed shape: invalidate the fingerprint so the
		// next mirror-on-read writes the fresh state.
		delete(zoneTplMirrorWritten, master)
	}
	if changed {
		_ = saveZoneTemplateMirror(m)
	}
}

// ZoneTemplateMirror returns the locally mirrored template list for a master
// deviceID (possibly empty). The frontend uses it to offer a re-seed when
// the agent's own list came back empty after a factory reset or reinstall.
func (a *App) ZoneTemplateMirror(deviceID string) []ZoneTemplate {
	e, ok := loadZoneTemplateMirror().ByMaster[strings.ToUpper(strings.TrimSpace(deviceID))]
	if !ok || len(e.Templates) == 0 {
		return []ZoneTemplate{}
	}
	return e.Templates
}
