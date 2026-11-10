// The one /presets reader.
//
// The speaker's preset list used to be read three ways: a set of regexes in
// the agent's reconcile, an XML struct in the native-preset probe and a second
// XML struct in the pre-takeover snapshot. The regex reader handed out the
// location still XML-escaped ("&amp;") while it unescaped the name, and the
// two struct readers decoded both, so the same slot had two spellings
// depending on who asked. This file owns the fetch and the parse; every field
// it hands out is entity-decoded. Callers keep their own timeout and their own
// policy for an unreadable speaker (the fetch returns an error; a body that is
// not well-formed XML is reported through Parsed and each caller decides).

package boxapi

import (
	"context"
	"encoding/xml"
	"regexp"
	"strconv"
	"strings"
)

// Presets is one read of the speaker's /presets: the slots in document order
// plus the body they were parsed from.
type Presets struct {
	// Items holds every <preset> of the document, in document order. A slot
	// the speaker has nothing on is simply absent; a <preset> without a
	// <ContentItem> is present with an empty ContentItem.
	Items []Preset

	// Raw is the body exactly as the speaker sent it (no UTF-8 repair, no
	// entity decoding), capped at nowPlayingBodyLimit.
	Raw []byte
	// Parsed is false when the body was not well-formed XML (a truncated or
	// malformed document). Items is then salvaged by scanning Raw with the
	// regexes the reconcile used to read every document with, and the
	// attribute values and names in it are NOT entity-decoded: they are the
	// document's own bytes.
	Parsed bool
}

// Preset is one slot of the speaker's preset list.
type Preset struct {
	// ID is the id attribute, the hardware key 1 to 6. 0 when the attribute
	// is missing or not a number.
	ID int
	// ContentItem is what the key recalls. ItemName is the element's text as
	// written (not trimmed).
	ContentItem ContentItem
}

// Presets reads and parses the speaker's /presets once. The deadline is the
// caller's (callCtx supplies the 6 s default for a context without one). A
// non-200 answer is a *BoxError.
func (c *Client) Presets(ctx context.Context) (*Presets, error) {
	raw, err := c.GetRaw(ctx, "/presets")
	if err != nil {
		return nil, err
	}
	return ParsePresets(raw), nil
}

// presetsXML is the wire shape:
//
//	<presets><preset id="1" createdOn=".." updatedOn="..">
//	  <ContentItem source=".." type=".." location=".." sourceAccount=".." isPresetable="true">
//	    <itemName>..</itemName><containerArt>..</containerArt>
//	  </ContentItem></preset></presets>
type presetsXML struct {
	Presets []struct {
		ID          string `xml:"id,attr"`
		ContentItem struct {
			Source        string `xml:"source,attr"`
			SourceAccount string `xml:"sourceAccount,attr"`
			Location      string `xml:"location,attr"`
			Type          string `xml:"type,attr"`
			IsPresetable  string `xml:"isPresetable,attr"`
			ItemName      string `xml:"itemName"`
			ContainerArt  string `xml:"containerArt"`
		} `xml:"ContentItem"`
	} `xml:"preset"`
}

// The legacy scans, kept for the salvage path only. rePresetBlock captures one
// <preset id="N" ...> ... </preset> block; (?s) lets . span the newlines the
// firmware puts inside the block. The others pull the first matching attribute
// or element out of that block.
var (
	rePresetBlock    = regexp.MustCompile(`(?s)<preset id="(\d+)".*?</preset>`)
	rePresetLocation = regexp.MustCompile(`location="([^"]*)"`)
	rePresetItemName = regexp.MustCompile(`<itemName>([^<]*)</itemName>`)
	rePresetSource   = regexp.MustCompile(`source="([^"]*)"`)
	rePresetType     = regexp.MustCompile(`type="([^"]*)"`)
	rePresetAccount  = regexp.MustCompile(`sourceAccount="([^"]*)"`)
)

// ParsePresets turns a /presets body into a Presets. It never fails: a body
// that is not well-formed XML gets its slots salvaged by the regex scans the
// reconcile used (Parsed reports which path ran), so the reconcile's verdict
// over a truncated document is the verdict it always was. A caller that wants
// a malformed document treated as unreadable checks Parsed.
func ParsePresets(raw []byte) *Presets {
	ps := &Presets{Raw: raw}
	var doc presetsXML
	if err := xml.Unmarshal(ensureUTF8(raw), &doc); err != nil {
		ps.Items = salvagePresets(raw)
		return ps
	}
	ps.Parsed = true
	for _, p := range doc.Presets {
		id, err := strconv.Atoi(strings.TrimSpace(p.ID))
		if err != nil {
			id = 0
		}
		ps.Items = append(ps.Items, Preset{
			ID: id,
			ContentItem: ContentItem{
				Source:        p.ContentItem.Source,
				SourceAccount: p.ContentItem.SourceAccount,
				Location:      p.ContentItem.Location,
				Type:          p.ContentItem.Type,
				IsPresetable:  strings.EqualFold(p.ContentItem.IsPresetable, "true"),
				ItemName:      p.ContentItem.ItemName,
				ContainerArt:  strings.TrimSpace(p.ContentItem.ContainerArt),
			},
		})
	}
	return ps
}

// salvagePresets reads the slots out of a body xml.Unmarshal refused, exactly
// the way the reconcile's regex reader did: one block per `<preset id="N"`,
// the first location, source, type and sourceAccount attribute and the first
// itemName element inside it. Nothing is decoded.
func salvagePresets(raw []byte) []Preset {
	var out []Preset
	for _, blk := range rePresetBlock.FindAllStringSubmatch(string(raw), -1) {
		id, err := strconv.Atoi(blk[1])
		if err != nil {
			id = 0
		}
		p := Preset{ID: id}
		if m := rePresetLocation.FindStringSubmatch(blk[0]); m != nil {
			p.ContentItem.Location = m[1]
		}
		if m := rePresetItemName.FindStringSubmatch(blk[0]); m != nil {
			p.ContentItem.ItemName = m[1]
		}
		if m := rePresetSource.FindStringSubmatch(blk[0]); m != nil {
			p.ContentItem.Source = m[1]
		}
		if m := rePresetType.FindStringSubmatch(blk[0]); m != nil {
			p.ContentItem.Type = m[1]
		}
		if m := rePresetAccount.FindStringSubmatch(blk[0]); m != nil {
			p.ContentItem.SourceAccount = m[1]
		}
		out = append(out, p)
	}
	return out
}

// Slot returns the first preset the document lists for key id.
func (ps *Presets) Slot(id int) (Preset, bool) {
	for _, p := range ps.Items {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}
