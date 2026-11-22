package usbmedia

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// The ContentDirectory the stick is served as: a device description, the
// service description, and the SOAP control endpoint for Browse and Search.
// Small on purpose. Sorting and eventing are not offered. Search reads the
// stick's directories on the agent itself (search.go), so a client is not left
// to walk the tree over SOAP, one Browse per folder, matching only what the
// DIDL carries (discussion #1136).

// Paths under the agent's :8888.
const (
	DescriptionPath = "/usb/device.xml"
	SCPDPath        = "/usb/cds.xml"
	ControlPath     = "/usb/control"
)

// Info names the served stick.
type Info struct {
	// UDN is "uuid:...", stable per speaker so a key saved from the stick finds
	// it again after a restart.
	UDN string
	// FriendlyName is what a generic UPnP client shows.
	FriendlyName string
	// UpdateID changes whenever a different stick is served, so a client that
	// caches a listing can tell.
	UpdateID uint32
}

// DeviceDescription is the UPnP root device description of the stick.
func DeviceDescription(info Info) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<root xmlns="urn:schemas-upnp-org:device-1-0"><specVersion><major>1</major><minor>0</minor></specVersion><device>`)
	b.WriteString(`<deviceType>urn:schemas-upnp-org:device:MediaServer:1</deviceType>`)
	b.WriteString(`<friendlyName>` + esc(info.FriendlyName) + `</friendlyName>`)
	b.WriteString(`<manufacturer>ST Reborn</manufacturer><modelName>USB stick</modelName>`)
	b.WriteString(`<UDN>` + esc(info.UDN) + `</UDN>`)
	b.WriteString(`<serviceList><service><serviceType>urn:schemas-upnp-org:service:ContentDirectory:1</serviceType>`)
	b.WriteString(`<serviceId>urn:upnp-org:serviceId:ContentDirectory</serviceId>`)
	b.WriteString(`<SCPDURL>` + SCPDPath + `</SCPDURL><controlURL>` + ControlPath + `</controlURL><eventSubURL></eventSubURL>`)
	b.WriteString(`</service></serviceList></device></root>`)
	return []byte(b.String())
}

// SCPD is the ContentDirectory service description: the five actions the
// control endpoint answers.
const SCPD = `<?xml version="1.0" encoding="utf-8"?>
<scpd xmlns="urn:schemas-upnp-org:service-1-0"><specVersion><major>1</major><minor>0</minor></specVersion><actionList>` +
	`<action><name>Browse</name><argumentList>` +
	`<argument><name>ObjectID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_ObjectID</relatedStateVariable></argument>` +
	`<argument><name>BrowseFlag</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_BrowseFlag</relatedStateVariable></argument>` +
	`<argument><name>Filter</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Filter</relatedStateVariable></argument>` +
	`<argument><name>StartingIndex</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Index</relatedStateVariable></argument>` +
	`<argument><name>RequestedCount</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>` +
	`<argument><name>SortCriteria</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_SortCriteria</relatedStateVariable></argument>` +
	`<argument><name>Result</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Result</relatedStateVariable></argument>` +
	`<argument><name>NumberReturned</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>` +
	`<argument><name>TotalMatches</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>` +
	`<argument><name>UpdateID</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_UpdateID</relatedStateVariable></argument>` +
	`</argumentList></action>` +
	`<action><name>Search</name><argumentList>` +
	`<argument><name>ContainerID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_ObjectID</relatedStateVariable></argument>` +
	`<argument><name>SearchCriteria</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_SearchCriteria</relatedStateVariable></argument>` +
	`<argument><name>Filter</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Filter</relatedStateVariable></argument>` +
	`<argument><name>StartingIndex</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Index</relatedStateVariable></argument>` +
	`<argument><name>RequestedCount</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>` +
	`<argument><name>SortCriteria</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_SortCriteria</relatedStateVariable></argument>` +
	`<argument><name>Result</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Result</relatedStateVariable></argument>` +
	`<argument><name>NumberReturned</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>` +
	`<argument><name>TotalMatches</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>` +
	`<argument><name>UpdateID</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_UpdateID</relatedStateVariable></argument>` +
	`</argumentList></action>` +
	`<action><name>GetSearchCapabilities</name><argumentList><argument><name>SearchCaps</name><direction>out</direction><relatedStateVariable>SearchCapabilities</relatedStateVariable></argument></argumentList></action>` +
	`<action><name>GetSortCapabilities</name><argumentList><argument><name>SortCaps</name><direction>out</direction><relatedStateVariable>SortCapabilities</relatedStateVariable></argument></argumentList></action>` +
	`<action><name>GetSystemUpdateID</name><argumentList><argument><name>Id</name><direction>out</direction><relatedStateVariable>SystemUpdateID</relatedStateVariable></argument></argumentList></action>` +
	`</actionList><serviceStateTable>` +
	`<stateVariable sendEvents="no"><name>A_ARG_TYPE_ObjectID</name><dataType>string</dataType></stateVariable>` +
	`<stateVariable sendEvents="no"><name>A_ARG_TYPE_BrowseFlag</name><dataType>string</dataType><allowedValueList><allowedValue>BrowseMetadata</allowedValue><allowedValue>BrowseDirectChildren</allowedValue></allowedValueList></stateVariable>` +
	`<stateVariable sendEvents="no"><name>A_ARG_TYPE_Filter</name><dataType>string</dataType></stateVariable>` +
	`<stateVariable sendEvents="no"><name>A_ARG_TYPE_Index</name><dataType>ui4</dataType></stateVariable>` +
	`<stateVariable sendEvents="no"><name>A_ARG_TYPE_Count</name><dataType>ui4</dataType></stateVariable>` +
	`<stateVariable sendEvents="no"><name>A_ARG_TYPE_SearchCriteria</name><dataType>string</dataType></stateVariable>` +
	`<stateVariable sendEvents="no"><name>A_ARG_TYPE_SortCriteria</name><dataType>string</dataType></stateVariable>` +
	`<stateVariable sendEvents="no"><name>A_ARG_TYPE_Result</name><dataType>string</dataType></stateVariable>` +
	`<stateVariable sendEvents="no"><name>A_ARG_TYPE_UpdateID</name><dataType>ui4</dataType></stateVariable>` +
	`<stateVariable sendEvents="no"><name>SearchCapabilities</name><dataType>string</dataType></stateVariable>` +
	`<stateVariable sendEvents="no"><name>SortCapabilities</name><dataType>string</dataType></stateVariable>` +
	`<stateVariable sendEvents="yes"><name>SystemUpdateID</name><dataType>ui4</dataType></stateVariable>` +
	`</serviceStateTable></scpd>`

// UPnP error codes the control endpoint answers with.
const (
	ErrCodeInvalidAction       = 401
	ErrCodeInvalidArgs         = 402
	ErrCodeActionFailed        = 501
	ErrCodeNoSuchObject        = 701
	ErrCodeUnsupportedCriteria = 708
)

// SearchCaps is what GetSearchCapabilities answers: the properties a Search
// can test (ParseCriteria).
const SearchCaps = "dc:title,upnp:artist,upnp:album,dc:creator,upnp:genre"

// maxSOAPBody bounds one control request. A Browse is a few hundred bytes.
const maxSOAPBody = 16 << 10

// maxBrowseCount bounds one Browse page, whatever a client asks for. The STR
// clients ask for 200; a generic client asking for 0 ("all") gets this many
// and pages on, as the TotalMatches tells it to.
const maxBrowseCount = 500

// Control answers one ContentDirectory SOAP request. base is the
// scheme+authority the res and cover URLs are built from: the Host the client
// used, so each client is handed an address it can reach.
func (l *Library) Control(w http.ResponseWriter, r *http.Request, base string, info Info) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSOAPBody))
	if err != nil {
		WriteFault(w, ErrCodeInvalidArgs, "Invalid Args")
		return
	}
	switch soapAction(r.Header.Get("SOAPACTION"), body) {
	case "Browse":
		l.browse(w, body, base, info)
	case "Search":
		l.search(r.Context(), w, body, base, info)
	case "GetSystemUpdateID":
		writeResponse(w, "GetSystemUpdateID", "<Id>"+strconv.FormatUint(uint64(info.UpdateID), 10)+"</Id>")
	case "GetSearchCapabilities":
		writeResponse(w, "GetSearchCapabilities", "<SearchCaps>"+SearchCaps+"</SearchCaps>")
	case "GetSortCapabilities":
		writeResponse(w, "GetSortCapabilities", "<SortCaps></SortCaps>")
	default:
		WriteFault(w, ErrCodeInvalidAction, "Invalid Action")
	}
}

// soapAction reads the action name from the SOAPACTION header
// ("urn:...:ContentDirectory:1#Browse"), or from the body's first element
// inside Body when a client sends no header.
func soapAction(header string, body []byte) string {
	h := strings.Trim(strings.TrimSpace(header), `"`)
	if i := strings.LastIndex(h, "#"); i >= 0 {
		return h[i+1:]
	}
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	inBody := false
	for i := 0; i < 64; i++ {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			if inBody {
				return se.Name.Local
			}
			inBody = se.Name.Local == "Body"
		}
	}
	return ""
}

type browseArgs struct {
	ObjectID       string `xml:"Body>Browse>ObjectID"`
	BrowseFlag     string `xml:"Body>Browse>BrowseFlag"`
	StartingIndex  int    `xml:"Body>Browse>StartingIndex"`
	RequestedCount int    `xml:"Body>Browse>RequestedCount"`
}

func (l *Library) browse(w http.ResponseWriter, body []byte, base string, info Info) {
	var a browseArgs
	if err := xml.Unmarshal(body, &a); err != nil {
		WriteFault(w, ErrCodeInvalidArgs, "Invalid Args")
		return
	}
	id := strings.TrimSpace(a.ObjectID)
	if id == "" {
		id = "0"
	}
	var didl strings.Builder
	didl.WriteString(didlHead)
	returned, total := 0, 0
	switch strings.TrimSpace(a.BrowseFlag) {
	case "BrowseMetadata":
		e, err := l.Lookup(id)
		if err != nil {
			faultFor(w, err)
			return
		}
		if id == "0" {
			e.Title = info.FriendlyName
		}
		writeEntry(&didl, e, base, true)
		returned, total = 1, 1
	case "BrowseDirectChildren":
		count := a.RequestedCount
		if count <= 0 || count > maxBrowseCount {
			count = maxBrowseCount
		}
		page, n, err := l.Browse(id, a.StartingIndex, count)
		if err != nil {
			faultFor(w, err)
			return
		}
		for _, e := range page {
			writeEntry(&didl, e, base, false)
		}
		returned, total = len(page), n
	default:
		WriteFault(w, ErrCodeInvalidArgs, "Invalid Args")
		return
	}
	didl.WriteString(`</DIDL-Lite>`)
	writeResponse(w, "Browse",
		"<Result>"+esc(didl.String())+"</Result>"+
			"<NumberReturned>"+strconv.Itoa(returned)+"</NumberReturned>"+
			"<TotalMatches>"+strconv.Itoa(total)+"</TotalMatches>"+
			"<UpdateID>"+strconv.FormatUint(uint64(info.UpdateID), 10)+"</UpdateID>")
}

type searchArgs struct {
	ContainerID    string `xml:"Body>Search>ContainerID"`
	SearchCriteria string `xml:"Body>Search>SearchCriteria"`
	StartingIndex  int    `xml:"Body>Search>StartingIndex"`
	RequestedCount int    `xml:"Body>Search>RequestedCount"`
}

// search answers the Search action from the stick's directories (Search). A
// client pages by StartingIndex; each page searches again from the start and
// collects one hit more than it hands over, so TotalMatches tells the client
// whether to ask for the next page without a count of the whole tree.
func (l *Library) search(ctx context.Context, w http.ResponseWriter, body []byte, base string, info Info) {
	var a searchArgs
	if err := xml.Unmarshal(body, &a); err != nil || a.StartingIndex < 0 {
		WriteFault(w, ErrCodeInvalidArgs, "Invalid Args")
		return
	}
	terms, err := ParseCriteria(a.SearchCriteria)
	if err != nil {
		// A test the stick cannot answer: the client walks instead.
		WriteFault(w, ErrCodeUnsupportedCriteria, "Unsupported or invalid search criteria")
		return
	}
	count := a.RequestedCount
	if count <= 0 || count > maxBrowseCount {
		count = maxBrowseCount
	}
	start := min(a.StartingIndex, SearchMaxHits)
	res, err := l.Search(ctx, strings.TrimSpace(a.ContainerID), terms, start+count+1)
	if err != nil {
		faultFor(w, err)
		return
	}
	total := len(res.Hits)
	hits := res.Hits[min(start, total):]
	if len(hits) > count {
		hits = hits[:count]
	}
	var didl strings.Builder
	didl.WriteString(didlHead)
	for _, e := range hits {
		writeEntry(&didl, e, base, false)
	}
	didl.WriteString(`</DIDL-Lite>`)
	writeResponse(w, "Search",
		"<Result>"+esc(didl.String())+"</Result>"+
			"<NumberReturned>"+strconv.Itoa(len(hits))+"</NumberReturned>"+
			"<TotalMatches>"+strconv.Itoa(total)+"</TotalMatches>"+
			"<UpdateID>"+strconv.FormatUint(uint64(info.UpdateID), 10)+"</UpdateID>")
}

// faultFor maps a Library error to the UPnP fault a client expects: an id
// that does not name anything on the stick is 701, which the STR clients read
// as "this object is gone" rather than as a broken server.
func faultFor(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrBadID) || errors.Is(err, fs.ErrNotExist) {
		WriteFault(w, ErrCodeNoSuchObject, "No such object")
		return
	}
	WriteFault(w, ErrCodeActionFailed, "Action Failed")
}

const didlHead = `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">`

// writeEntry appends one container or item. A folder's title is its name, a
// file's its name without the extension, and a file's album the folder it sits
// in: no tag is read (docs/streaming/usb-stick.md, section 5). Ids and titles
// are the UTF-8 text, the res and cover URLs the on-disk bytes (charset.go).
func writeEntry(b *strings.Builder, e Entry, base string, self bool) {
	parent := Parent(e.ID)
	if e.ID == "0" {
		parent = "-1"
	}
	if e.Folder {
		fmt.Fprintf(b, `<container id="%s" parentID="%s" restricted="1" searchable="1">`, esc(e.ID), esc(parent))
		b.WriteString(`<dc:title>` + esc(e.Title) + `</dc:title><upnp:class>object.container.storageFolder</upnp:class>`)
		if self && e.Cover != "" {
			b.WriteString(`<upnp:albumArtURI>` + esc(ArtURL(base, e.Cover)) + `</upnp:albumArtURI>`)
		}
		b.WriteString(`</container>`)
		return
	}
	fmt.Fprintf(b, `<item id="%s" parentID="%s" restricted="1">`, esc(e.ID), esc(parent))
	b.WriteString(`<dc:title>` + esc(e.Title) + `</dc:title><upnp:class>object.item.audioItem.musicTrack</upnp:class>`)
	if album := FileAlbum(e.ID); album != "" {
		b.WriteString(`<upnp:album>` + esc(album) + `</upnp:album>`)
	}
	if e.Cover != "" {
		b.WriteString(`<upnp:albumArtURI>` + esc(ArtURL(base, e.Cover)) + `</upnp:albumArtURI>`)
	}
	b.WriteString(`<res protocolInfo="http-get:*:` + esc(e.MIME) + `:DLNA.ORG_OP=01;DLNA.ORG_CI=0">` + esc(FileURL(base, e.Path)) + `</res>`)
	b.WriteString(`</item>`)
}

const soapHead = `<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>`
const soapTail = `</s:Body></s:Envelope>`

func writeResponse(w http.ResponseWriter, action, inner string) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	_, _ = io.WriteString(w, soapHead+`<u:`+action+`Response xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1">`+inner+`</u:`+action+`Response>`+soapTail)
}

// WriteFault answers a UPnP error as the SOAP fault the spec prescribes.
func WriteFault(w http.ResponseWriter, code int, desc string) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = io.WriteString(w, soapHead+`<s:Fault><faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail>`+
		`<UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>`+strconv.Itoa(code)+`</errorCode>`+
		`<errorDescription>`+esc(desc)+`</errorDescription></UPnPError></detail></s:Fault>`+soapTail)
}

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// ServeFile answers a GET or HEAD for a playable file or a cover image under
// the given prefix (FilePrefix or ArtPrefix). The Content-Type is set from the
// extension, never sniffed; http.ServeContent answers Range requests, which
// the renderer uses to seek and to read a FLAC header.
func (l *Library) ServeFile(w http.ResponseWriter, r *http.Request, prefix string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, prefix)
	var (
		mime string
		open func(string) (fileHandle, error)
	)
	if prefix == ArtPrefix {
		mime = ImageMIMEFor(id)
		open = func(id string) (fileHandle, error) { return l.OpenArt(id) }
	} else {
		mime = MIMEFor(id)
		open = func(id string) (fileHandle, error) { return l.OpenFile(id) }
	}
	f, err := open(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		http.Error(w, "stick not readable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", mime)
	if prefix != ArtPrefix {
		// What a DLNA server says about a plain file: it can be read from any
		// offset, and it is streamed, not pushed as a whole.
		w.Header().Set("contentFeatures.dlna.org", "DLNA.ORG_OP=01;DLNA.ORG_CI=0")
		w.Header().Set("transferMode.dlna.org", "Streaming")
	}
	http.ServeContent(w, r, path.Base(id), fi.ModTime(), f)
}

// fileHandle is what ServeFile needs from an opened file.
type fileHandle interface {
	io.ReadSeekCloser
	Stat() (fs.FileInfo, error)
}
