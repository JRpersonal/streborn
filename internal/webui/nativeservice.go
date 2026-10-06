package webui

// Presets for music services the speaker plays by itself (Pandora,
// iHeartRadio; presets.TypeNative).
//
// The firmware's own client plays these with the account the speaker holds.
// STR keeps the ContentItem the speaker reported, writes it back onto the key,
// and starts it by handing the same item to the speaker's /select. Nothing is
// proxied, nothing is pushed over UPnP.

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/presets"
)

// writeStorePresetToBox puts a stored preset on the speaker's key in the form
// its type needs: the speaker's own item for a native-service preset, the
// stream (or native radio) form for everything else.
func (s *Server) writeStorePresetToBox(ctx context.Context, p presets.Preset) error {
	if p.IsNative() {
		item := s.withSpeakerAccount(ctx, *p.Native)
		return boxcli.AddPresetContentItem(ctx, s.boxHost, p.Slot, item.Source, item.ItemType,
			item.Location, p.Name, item.SourceAccount)
	}
	isSpotify := p.Type == "spotify"
	return s.writeBoxPreset(ctx, p.Slot, p.Name, boxPresetURL(p.Slot, isSpotify), p.Art, isSpotify)
}

// nativeContentItemXML renders the /select body for a native item.
func nativeContentItemXML(item presets.NativeItem, name string) string {
	typ := item.ItemType
	b := `<ContentItem source="` + escapeXMLAttr(item.Source) + `"`
	if typ != "" {
		b += ` type="` + escapeXMLAttr(typ) + `"`
	}
	b += ` location="` + escapeXMLAttr(item.Location) + `" sourceAccount="` +
		escapeXMLAttr(item.SourceAccount) + `" isPresetable="true"><itemName>` +
		escapeXMLText(name) + `</itemName></ContentItem>`
	return b
}

// selectNativeItem asks the speaker to play a native-service item, the same
// item its own key holds.
func (s *Server) selectNativeItem(ctx context.Context, item presets.NativeItem, name string) error {
	return s.postSelect(ctx, nativeContentItemXML(s.withSpeakerAccount(ctx, item), name))
}

// withSpeakerAccount returns item with the sourceAccount the speaker itself
// lists for the item's service (presets.ResolveNativeAccount). The stored
// account can be missing or wrong (#1101: a key held on the speaker came back
// with the station name as its account), and the speaker refuses an item
// whose account it does not know. An unreadable source list keeps the item
// as stored.
func (s *Server) withSpeakerAccount(ctx context.Context, item presets.NativeItem) presets.NativeItem {
	if s.boxHost == "" {
		return item
	}
	c, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, "http://"+s.boxHost+":8090/sources", nil)
	if err != nil {
		return item
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return item
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return item
	}
	if acct := presets.ResolveNativeAccount(item, presets.NativeSourceAccounts(body)); acct != item.SourceAccount {
		s.logger.Info("native service item: using the account the speaker lists for this service",
			"source", item.Source, "storedAccountSet", item.SourceAccount != "")
		item.SourceAccount = acct
	}
	return item
}

// validNativeItem checks an item a client sent before it is stored: a service
// STR keeps, a location, and values the speaker's command line can carry
// unchanged (the reconcile writes them back through it).
func validNativeItem(item *presets.NativeItem) error {
	if item == nil {
		return fmt.Errorf("no item")
	}
	if _, ok := presets.NativeServiceLabel(item.Source); !ok {
		return fmt.Errorf("source %q is not a service STR keeps on a key", item.Source)
	}
	if strings.TrimSpace(item.Location) == "" {
		return fmt.Errorf("no location")
	}
	for _, v := range []string{item.Source, item.Location, item.ItemType, item.SourceAccount} {
		if strings.ContainsAny(v, " \t\r\n\"<>") {
			return fmt.Errorf("value %q cannot be stored on a key unchanged", v)
		}
	}
	return nil
}

// nowPlayingNativeItem reads the speaker's now-playing ContentItem and returns
// it when it is a station of a service STR keeps natively.
func (s *Server) nowPlayingNativeItem(ctx context.Context) (presets.NativeItem, bool, error) {
	if s.boxHost == "" {
		return presets.NativeItem{}, false, fmt.Errorf("box host not configured")
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, "http://"+s.boxHost+":8090/now_playing", nil)
	if err != nil {
		return presets.NativeItem{}, false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return presets.NativeItem{}, false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return presets.NativeItem{}, false, err
	}
	item, ok := parseNowPlayingNativeItem(body)
	return item, ok, nil
}

// parseNowPlayingNativeItem pulls the ContentItem out of a now_playing
// document; ok is false unless it is a native-service station with a
// location.
func parseNowPlayingNativeItem(body []byte) (presets.NativeItem, bool) {
	var doc struct {
		Source string `xml:"source,attr"`
		Item   struct {
			Source        string `xml:"source,attr"`
			Type          string `xml:"type,attr"`
			Location      string `xml:"location,attr"`
			SourceAccount string `xml:"sourceAccount,attr"`
			ItemName      string `xml:"itemName"`
			ContainerArt  string `xml:"containerArt"`
		} `xml:"ContentItem"`
		Station string `xml:"stationName"`
		Art     string `xml:"art"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return presets.NativeItem{}, false
	}
	src := doc.Item.Source
	if src == "" {
		src = doc.Source
	}
	item := presets.NativeItem{
		Source:        presets.NormalizeNativeSource(src),
		SourceAccount: strings.TrimSpace(doc.Item.SourceAccount),
		Location:      strings.TrimSpace(doc.Item.Location),
		ItemType:      strings.TrimSpace(doc.Item.Type),
		ItemName:      strings.TrimSpace(doc.Item.ItemName),
		ContainerArt:  strings.TrimSpace(doc.Item.ContainerArt),
	}
	if item.ItemName == "" {
		item.ItemName = strings.TrimSpace(doc.Station)
	}
	if item.ContainerArt == "" {
		item.ContainerArt = strings.TrimSpace(doc.Art)
	}
	if _, ok := presets.NativeServiceLabel(item.Source); !ok || item.Location == "" {
		return presets.NativeItem{}, false
	}
	return item, true
}

// handleNativePresetSave stores a native-service preset on slot (PUT
// /api/presets/<slot> with type "native"). A body carrying the item (a
// box-to-box copy) is checked and stored; a body without one (the apps' hold
// gesture while Pandora or iHeartRadio plays) takes the item the speaker is
// playing right now, which is the only place the real item exists.
func (s *Server) handleNativePresetSave(w http.ResponseWriter, r *http.Request, slot int, in presets.Preset) {
	var item presets.NativeItem
	if in.Native != nil && in.Native.Location != "" {
		item = *in.Native
	} else {
		got, ok, err := s.nowPlayingNativeItem(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": "The speaker could not be asked what it is playing. Try again in a moment.",
				"code":  "native-nowplaying-unreadable",
			})
			return
		}
		if !ok {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": "The speaker is not playing a Pandora or iHeartRadio station right now.",
				"code":  "native-not-playing",
			})
			return
		}
		item = got
	}
	if err := validNativeItem(&item); err != nil {
		s.logger.Warn("native preset save refused", "slot", slot, "err", err,
			"from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "This station can't be kept on a key.",
			"code":  "native-item-invalid",
		})
		return
	}
	p, ok := presets.NewNativePreset(slot, item, in.Name)
	if !ok {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "This station can't be kept on a key.",
			"code":  "native-item-invalid",
		})
		return
	}
	// One station, one key (#836), same as every other save.
	for _, other := range s.presets.All() {
		if other.Slot == slot || !other.IsNative() || !presets.SameNativeItem(other.Native, p.Native) {
			continue
		}
		s.logger.Info("preset save refused: this station is already on another slot",
			"slot", slot, "existingSlot", other.Slot, "name", other.Name,
			"from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
		writeJSON(w, http.StatusConflict, map[string]any{
			"code": "already-on-slot", "slot": other.Slot, "name": other.Name,
		})
		return
	}
	prevName := ""
	if old, had := s.presets.Get(slot); had {
		prevName = old.Name
	}
	if err := s.presets.SetSlot(p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logger.Info("preset write accepted",
		"slot", slot, "was", prevName, "now", p.Name, "type", p.Type,
		"source", p.Native.Source, "location", p.Native.Location,
		"from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
	if s.boxHost != "" {
		boxCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		if err := s.writeStorePresetToBox(boxCtx, p); err != nil {
			s.logger.Warn("box preset sync failed", "slot", slot, "err", err)
		}
		cancel()
	}
	writeJSON(w, http.StatusOK, p)
}
