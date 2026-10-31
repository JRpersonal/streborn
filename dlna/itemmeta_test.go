package dlna

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// soapField pulls one element's text out of a SOAP request body.
func soapField(body, tag string) string {
	i := strings.Index(body, "<"+tag+">")
	if i < 0 {
		return ""
	}
	v := body[i+len(tag)+2:]
	if j := strings.Index(v, "<"); j >= 0 {
		return v[:j]
	}
	return v
}

func TestItemMetadataReturnsTheFreshStreamURL(t *testing.T) {
	var flag, object string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		flag, object = soapField(string(b), "BrowseFlag"), soapField(string(b), "ObjectID")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>` +
			`<u:BrowseResponse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><Result>` +
			`&lt;DIDL-Lite xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/"&gt;` +
			`&lt;item id="obj-7" parentID="c-1"&gt;&lt;dc:title&gt;Song&lt;/dc:title&gt;` +
			`&lt;upnp:class&gt;object.item.audioItem.musicTrack&lt;/upnp:class&gt;` +
			`&lt;res protocolInfo="http-get:*:audio/mpeg:*"&gt;http://192.0.2.5:10243/WMPNSSv4/222/1_new.mp3&lt;/res&gt;` +
			`&lt;/item&gt;&lt;/DIDL-Lite&gt;` +
			`</Result><NumberReturned>1</NumberReturned><TotalMatches>1</TotalMatches></u:BrowseResponse></s:Body></s:Envelope>`))
	}))
	defer ts.Close()
	it, err := ItemMetadata(context.Background(), Server{CDSControlURL: ts.URL}, "obj-7")
	if err != nil {
		t.Fatal(err)
	}
	if flag != "BrowseMetadata" || object != "obj-7" {
		t.Errorf("asked flag=%q object=%q, want BrowseMetadata on obj-7", flag, object)
	}
	if it.StreamURL != "http://192.0.2.5:10243/WMPNSSv4/222/1_new.mp3" || it.Title != "Song" || it.ParentID != "c-1" {
		t.Errorf("item = %+v", it)
	}
	if _, err := ItemMetadata(context.Background(), Server{CDSControlURL: ts.URL}, ""); err == nil {
		t.Error("an empty id must not be asked for")
	}
}

func TestItemMetadataSurfacesAFault(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault>` +
			`<detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>701</errorCode></UPnPError></detail>` +
			`</s:Fault></s:Body></s:Envelope>`))
	}))
	defer ts.Close()
	if _, err := ItemMetadata(context.Background(), Server{CDSControlURL: ts.URL}, "gone"); err == nil {
		t.Fatal("a 701 No such object must be an error")
	}
}
