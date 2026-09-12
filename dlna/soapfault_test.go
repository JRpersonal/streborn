package dlna

import (
	"net/http"
	"strings"
	"testing"
)

// The fault from #929, as a Synology DS918+ sends it. The point of the test is
// the position of errorCode: it is the LAST thing in the envelope, which is why
// reporting the first 240 characters showed the reader everything except the
// answer.
const synologyFault = `<?xml version="1.0"?>` +
	`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">` +
	`<s:Body><s:Fault>` +
	`<faultcode>s:Client</faultcode>` +
	`<faultstring>UPnPError</faultstring>` +
	`<detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0">` +
	`<errorCode>701</errorCode><errorDescription>No such object</errorDescription>` +
	`</UPnPError></detail>` +
	`</s:Fault></s:Body></s:Envelope>`

func TestSoapFaultMessage(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		status int
		want   []string
		absent []string
	}{
		{
			name: "the #929 fault", raw: synologyFault, status: 500,
			want:   []string{"701", "No such object"},
			absent: []string{"<s:Envelope", "faultcode"},
		},
		{
			// A server that answers a code without a description: the code
			// still has to mean something to the reader.
			name: "code without description", status: 500,
			raw: `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault>` +
				`<faultstring>UPnPError</faultstring><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0">` +
				`<errorCode>708</errorCode></UPnPError></detail></s:Fault></s:Body></s:Envelope>`,
			want: []string{"708", "unsupported search criteria"},
		},
		{
			// A fault with no UPnPError block at all still carries its string.
			name: "faultstring only", status: 500,
			raw: `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault>` +
				`<faultcode>s:Server</faultcode><faultstring>Internal Error</faultstring>` +
				`</s:Fault></s:Body></s:Envelope>`,
			want: []string{"Internal Error", "500"},
		},
		{
			// Something that is not a media server at all: the raw text is
			// still the most useful thing available.
			name: "an html error page", raw: "<html><body>404 not found</body></html>", status: http.StatusNotFound,
			want: []string{"404", "not found"},
		},
		{
			name: "empty body", raw: "", status: 500,
			want: []string{"500"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := soapFaultMessage([]byte(tc.raw), tc.status)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("message %q does not mention %q", got, w)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("message %q still dumps raw XML (%q)", got, a)
				}
			}
		})
	}
}

// The whole complaint in #929 is that the useful part fell off the end. A
// message the dialog can show without truncating is part of the fix.
func TestSoapFaultMessageStaysShort(t *testing.T) {
	if got := soapFaultMessage([]byte(synologyFault), 500); len(got) > 120 {
		t.Errorf("message is %d chars, long enough to be truncated again: %q", len(got), got)
	}
}
