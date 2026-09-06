// The firmware's own preset store gesture, as seen by marge.
//
// Holding a preset key for about two seconds makes the speaker store what it
// is playing on that key ITSELF: the firmware runs HandleUpdatePresetRequest,
// which first DELETEs the slot on the cloud (when it held something) and then
// PUTs the playing ContentItem to
//
//	/streaming/account/<acct>/device/<deviceid>/preset/<N>
//
// The same PUT arrives once per native slot right after boot, when the firmware
// syncs its own preset list up to the cloud. Until now both fell through to the
// generic account answer, and the firmware parsed that as a preset and gave up:
// "EXCEPTION in GetPresetsCB xml parsing: preset expected, but XML was
// 'account'" followed by "UpdatePresetFailureCB Update or Add preset failed"
// (Portable, 2026-09-06). So the hold gesture never worked on an STR box.
//
// marge does not own the preset store, so the item is handed to a keeper the
// agent injects (WithPresetKeeper). The keeper decides whether STR can map the
// item onto one of its own presets and stores it; marge then answers with the
// one <preset> element the firmware's GetPresetsCB parser expects, in the
// dialect of the list STR already serves. When the keeper refuses, the answer
// stays what it was before this file existed (the account document): the
// firmware's handling of that is measured and harmless, an unknown error shape
// is not.

package marge

import (
	"bytes"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// HeldItem is the ContentItem the firmware asks marge to keep in a preset
// slot. String fields are the raw (unescaped) values from the request.
type HeldItem struct {
	Slot          int
	Source        string
	Type          string
	Location      string
	SourceAccount string
	ItemName      string
	ContainerArt  string
}

// PresetKeeper stores a HeldItem into the agent's own preset store. A nil
// error means slot item.Slot now holds (or already held) that station; any
// error means STR cannot keep it, and marge answers the firmware with the
// pre-existing failure shape.
type PresetKeeper func(item HeldItem) error

// WithPresetKeeper wires the agent's preset store behind the firmware's own
// hold-to-store gesture. Without a keeper the PUT keeps its old answer.
func WithPresetKeeper(fn PresetKeeper) Option {
	return func(s *Server) { s.presetKeeper = fn }
}

// presetSlotPathRe matches the per-slot preset path the firmware writes to.
// The list path (".../preset" or ".../presets") is deliberately not matched.
var presetSlotPathRe = regexp.MustCompile(`/preset/([1-9][0-9]*)/?$`)

// presetSlotFromPath returns the slot of a per-slot preset path, ok=false for
// any other path or a slot outside 1..6.
func presetSlotFromPath(path string) (int, bool) {
	m := presetSlotPathRe.FindStringSubmatch(path)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 || n > 6 {
		return 0, false
	}
	return n, true
}

// parseHeldItem pulls the ContentItem out of the firmware's PUT body.
//
// Tolerant on purpose: the element may come bare or wrapped in a <preset>
// element, with or without an XML declaration, and the two child strings may
// be absent. Only the ContentItem element itself is required.
func parseHeldItem(body []byte) (HeldItem, bool) {
	var item HeldItem
	dec := xml.NewDecoder(bytes.NewReader(body))
	// The firmware declares UTF-8; anything else would need a charset reader,
	// and the values are ASCII plus escaped entities anyway.
	dec.Strict = false
	found := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || !strings.EqualFold(se.Name.Local, "ContentItem") {
			continue
		}
		for _, a := range se.Attr {
			switch strings.ToLower(a.Name.Local) {
			case "source":
				item.Source = strings.TrimSpace(a.Value)
			case "type":
				item.Type = strings.TrimSpace(a.Value)
			case "location":
				item.Location = strings.TrimSpace(a.Value)
			case "sourceaccount":
				item.SourceAccount = strings.TrimSpace(a.Value)
			}
		}
		var children struct {
			ItemName     string `xml:"itemName"`
			ContainerArt string `xml:"containerArt"`
		}
		if err := dec.DecodeElement(&children, &se); err == nil {
			item.ItemName = strings.TrimSpace(children.ItemName)
			item.ContainerArt = strings.TrimSpace(children.ContainerArt)
		}
		found = true
		break
	}
	if !found || item.Source == "" || item.Location == "" {
		return HeldItem{}, false
	}
	return item, true
}

// presetElementXML renders the single <preset> element the firmware expects
// back from a preset PUT, in the same dialect as PresetsXMLTemplate (which the
// firmware provably parses on the list read). The item is echoed as sent, so a
// firmware that compares the answer with its request sees its own item.
func presetElementXML(item HeldItem, now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 10)
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<preset id="` + strconv.Itoa(item.Slot) + `" createdOn="` + ts + `" updatedOn="` + ts + `">` +
		`<ContentItem source="` + xmlEscapeText(item.Source) + `" type="` + xmlEscapeText(item.Type) +
		`" location="` + xmlEscapeText(item.Location) + `" sourceAccount="` + xmlEscapeText(item.SourceAccount) +
		`" isPresetable="true">` +
		`<itemName>` + xmlEscapeText(item.ItemName) + `</itemName>` +
		`<containerArt>` + xmlEscapeText(item.ContainerArt) + `</containerArt>` +
		`</ContentItem></preset>`
}

// respondPresetStore answers the firmware's per-slot preset PUT/POST. It
// reports whether it wrote a response; false means the caller must fall back
// to the pre-existing answer for this path.
func (s *Server) respondPresetStore(w http.ResponseWriter, r *http.Request) bool {
	slot, ok := presetSlotFromPath(r.URL.Path)
	if !ok {
		return false
	}
	s.mu.RLock()
	keeper := s.presetKeeper
	s.mu.RUnlock()
	if keeper == nil {
		return false
	}
	// The spy middleware already buffered and restored the body.
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err != nil {
		return false
	}
	item, ok := parseHeldItem(body)
	if !ok {
		s.logger.Warn("marge preset store: the box sent a preset body without a readable ContentItem, keeping the old answer",
			slog.String("comp", "marge"), slog.Int("slot", slot), slog.Int("bytes", len(body)))
		return false
	}
	item.Slot = slot
	if err := keeper(item); err != nil {
		// The boot-time sync and a retried gesture can repeat this for the
		// same slot; one line per slot and minute is plenty for a bundle.
		if s.presetRefusalLogAllowed(slot) {
			s.logger.Warn("marge preset store: the box asked to keep an item STR cannot map onto a preset, keeping the old answer",
				slog.String("comp", "marge"), slog.Int("slot", slot),
				slog.String("source", item.Source), slog.String("name", item.ItemName),
				slog.String("reason", err.Error()))
		}
		return false
	}
	s.logger.Info("marge preset store: answered the box's own preset store with the slot's preset element",
		slog.String("comp", "marge"), slog.Int("slot", slot), slog.String("name", item.ItemName))
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(presetElementXML(item, time.Now())))
	return true
}

// presetRefusalLogAllowed rate-limits the refusal WARN to one per slot and
// minute. Under the lock because the firmware can PUT several slots in a
// burst (the boot-time sync writes all native slots a second apart).
func (s *Server) presetRefusalLogAllowed(slot int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.presetRefusalLogged == nil {
		s.presetRefusalLogged = map[int]time.Time{}
	}
	now := time.Now()
	if last, ok := s.presetRefusalLogged[slot]; ok && now.Sub(last) < time.Minute {
		return false
	}
	s.presetRefusalLogged[slot] = now
	return true
}
