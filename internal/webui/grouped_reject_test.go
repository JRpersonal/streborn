package webui

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/upnp"
)

// groupedFault is the shape the Bose firmware answers SetAVTransportURI with
// on a zone FOLLOWER (#70): HTTP 500 carrying UPnP error 501.
const groupedFault = `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>501</errorCode><errorDescription>Can't control member of group</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`

func TestIsGroupedRejection(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"grouped follower fault", errors.New("SetURI: soap SetAVTransportURI status 500: " + groupedFault), true},
		{"case-insensitive", errors.New("soap fault: CAN'T CONTROL MEMBER OF GROUP"), true},
		{"unrelated 402", errors.New("soap SetAVTransportURI status 500: <errorCode>402</errorCode> No URI supplied"), false},
		{"plain network error", errors.New("connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGroupedRejection(tc.err); got != tc.want {
				t.Errorf("isGroupedRejection(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A play against a grouped follower must answer a structured 409 the app can
// branch on, not the raw SOAP fault (#70). With no box behind the agent the
// master hint is unknown and must be OMITTED, not sent empty.
func TestGroupedFollowerPlayReturns409(t *testing.T) {
	box := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(groupedFault))
	}))
	t.Cleanup(box.Close)
	s := &Server{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		queue:    newPlayQueue(),
		renderer: &upnp.Renderer{ControlURL: box.URL, Client: box.Client()},
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/play",
		strings.NewReader(`{"url":"http://stream.example/relax","title":"Test Station"}`))
	s.handlePlay(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	if resp["error"] != "box-grouped" {
		t.Errorf(`error = %q, want "box-grouped"`, resp["error"])
	}
	if v, ok := resp["master"]; ok {
		t.Errorf("master = %q sent although unknown, want the field omitted", v)
	}
}
