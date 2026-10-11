package boxapi

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Stations of a music service the speaker plays by itself (Pandora).
//
// The Bose app used to browse a linked Pandora account through the speaker's
// own /navigate and start a station by handing one of the returned
// ContentItems to /select. Both calls are the speaker's, so they still work
// without the Bose cloud. Confirmed end to end on a SoundTouch 30 on
// 2026-10-11 (docs/streaming/us-services.md, "Starting a station without the
// Bose app").

// ServiceStation is one station from a service's station list.
type ServiceStation struct {
	Name          string `json:"name"`
	Source        string `json:"source"`
	SourceAccount string `json:"-"`
	Location      string `json:"location"`
	Type          string `json:"type"`
	ItemName      string `json:"itemName,omitempty"`
	ContainerArt  string `json:"art,omitempty"`
	Presetable    bool   `json:"presetable"`
	// ContentItem is the station's own <ContentItem> element as the speaker
	// sent it: every attribute and the inner XML unchanged. /select takes it
	// back as it is, which is the form confirmed on hardware.
	ContentItem string `json:"-"`
}

// rawContentItem keeps a ContentItem's attributes and inner XML so it can be
// written back without losing anything the firmware put into it.
type rawContentItem struct {
	Attrs []xml.Attr `xml:",any,attr"`
	Inner string     `xml:",innerxml"`
}

func (ci rawContentItem) attr(name string) string {
	for _, a := range ci.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (ci rawContentItem) child(name string) string {
	var doc struct {
		Items []struct {
			XMLName xml.Name
			Text    string `xml:",chardata"`
		} `xml:",any"`
	}
	if xml.Unmarshal([]byte("<x>"+ci.Inner+"</x>"), &doc) != nil {
		return ""
	}
	for _, it := range doc.Items {
		if it.XMLName.Local == name {
			return strings.TrimSpace(it.Text)
		}
	}
	return ""
}

func (ci rawContentItem) xml() string {
	var b strings.Builder
	b.WriteString("<ContentItem")
	for _, a := range ci.Attrs {
		if a.Name.Local == "" {
			continue
		}
		b.WriteString(" " + a.Name.Local + `="` + xmlEscape(a.Value) + `"`)
	}
	b.WriteString(">" + ci.Inner + "</ContentItem>")
	return b.String()
}

// stationListBody is the /navigate request for an account's station list.
func stationListBody(source, account string, start, count int) string {
	return `<navigate source="` + xmlEscape(source) + `" sourceAccount="` + xmlEscape(account) +
		`" menu="radioStations" sort="dateCreated"><startItem>` + strconv.Itoa(start) +
		`</startItem><numItems>` + strconv.Itoa(count) + `</numItems></navigate>`
}

// maxServiceStations caps one listing. Pandora allows 100 stations per
// account, so the cap is never reached in practice; it only bounds a
// misbehaving reply.
const maxServiceStations = 500

// stationPage is how many stations one /navigate call asks for.
const stationPage = 100

// ListServiceStations reads every station of a linked service account
// (source "PANDORA") from the speaker, paging through /navigate until the
// firmware's totalItems is reached.
func (c *Client) ListServiceStations(ctx context.Context, source, account string) ([]ServiceStation, error) {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(account) == "" {
		return nil, fmt.Errorf("station list needs a source and an account")
	}
	var out []ServiceStation
	for start := 1; len(out) < maxServiceStations; {
		page, total, err := c.serviceStationPage(ctx, source, account, start, stationPage)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) == 0 || len(out) >= total {
			break
		}
		start += len(page)
	}
	if len(out) > maxServiceStations {
		out = out[:maxServiceStations]
	}
	return out, nil
}

func (c *Client) serviceStationPage(ctx context.Context, source, account string, start, count int) ([]ServiceStation, int, error) {
	var raw struct {
		XMLName    xml.Name
		Text       string `xml:",chardata"`
		TotalItems int    `xml:"totalItems"`
		Errors     []struct {
			Value string `xml:"value,attr"`
			Name  string `xml:"name,attr"`
			Text  string `xml:",chardata"`
		} `xml:"error"`
		Items []struct {
			Name string `xml:"name"`
			Type string `xml:"type"`
			Art  string `xml:"art"`
			// The item's own ContentItem is its direct child; take the last
			// one, as BrowseMediaServer does.
			Content []rawContentItem `xml:"ContentItem"`
		} `xml:"items>item"`
	}
	if err := c.postXMLInto(ctx, "/navigate", stationListBody(source, account, start, count), &raw); err != nil {
		return nil, 0, err
	}
	// The firmware can refuse with 200 and an error document.
	switch raw.XMLName.Local {
	case "errors":
		msg := "refused"
		if len(raw.Errors) > 0 {
			e := raw.Errors[0]
			msg = strings.TrimSpace(strings.Join([]string{e.Value, e.Name, strings.TrimSpace(e.Text)}, " "))
		}
		return nil, 0, fmt.Errorf("box /navigate: %s", msg)
	case "error":
		return nil, 0, fmt.Errorf("box /navigate: %s", strings.TrimSpace(raw.Text))
	}
	out := make([]ServiceStation, 0, len(raw.Items))
	for _, it := range raw.Items {
		n := len(it.Content)
		if n == 0 {
			continue
		}
		ci := it.Content[n-1]
		st := ServiceStation{
			Name:          strings.TrimSpace(it.Name),
			Source:        ci.attr("source"),
			SourceAccount: ci.attr("sourceAccount"),
			Location:      ci.attr("location"),
			Type:          ci.attr("type"),
			ItemName:      ci.child("itemName"),
			ContainerArt:  ci.child("containerArt"),
			Presetable:    !strings.EqualFold(ci.attr("isPresetable"), "false"),
			ContentItem:   ci.xml(),
		}
		if st.Location == "" {
			continue
		}
		if st.Name == "" {
			st.Name = st.ItemName
		}
		if st.Type == "" {
			st.Type = strings.TrimSpace(it.Type)
		}
		if st.ContainerArt == "" {
			st.ContainerArt = strings.TrimSpace(it.Art)
		}
		out = append(out, st)
	}
	total := raw.TotalItems
	if total < len(out) {
		total = len(out)
	}
	return out, total, nil
}

// SelectContentItem hands a ContentItem to the speaker's /select. The speaker
// can answer 200 with an error document, so the body decides, not only the
// status.
func (c *Client) SelectContentItem(ctx context.Context, item string) error {
	ctx, cancel := callCtx(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/select"), strings.NewReader(item))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
	if resp.StatusCode >= 400 {
		return newBoxError("/select", resp.StatusCode, body)
	}
	if el, ok := decodeErrorEnvelope(body); ok {
		msg := strings.TrimSpace(strings.Join([]string{el.Value, el.Name, strings.TrimSpace(el.Text)}, " "))
		if msg == "" {
			msg = "refused"
		}
		return fmt.Errorf("box /select: %s", msg)
	}
	return nil
}
