package webui

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

const pandoraTestAccount = "listener@example.com"

func pandoraLinked(box *fakeNativeBox) {
	box.sources = []boxapi.Source{
		{Source: "TUNEIN", Status: "READY"},
		{Source: "PANDORA", SourceAccount: pandoraTestAccount, Status: "READY", DisplayName: "Pandora"},
	}
}

func pandoraStation(loc, name string) boxapi.ServiceStation {
	return boxapi.ServiceStation{
		Name: name, Source: "PANDORA", SourceAccount: pandoraTestAccount, Location: loc, Type: "stationurl",
		ItemName: name, ContainerArt: "http://img.example.com/" + loc + ".jpg", Presetable: true,
		ContentItem: `<ContentItem source="PANDORA" type="stationurl" location="` + loc + `" sourceAccount="` +
			pandoraTestAccount + `" isPresetable="true"><itemName>` + name + `</itemName></ContentItem>`,
	}
}

func TestPandoraEndpointsLANOnlyAndMethods(t *testing.T) {
	s, _, box := nativeTestServer(true)
	pandoraLinked(box)
	w := httptest.NewRecorder()
	s.handlePandoraStations(w, nativeReq(http.MethodGet, "/api/pandora/stations", "", "203.0.113.7:5000", ""))
	if w.Code != http.StatusForbidden {
		t.Errorf("non-LAN list: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.handlePandoraPlay(w, nativeReq(http.MethodPost, "/api/pandora/play", `{"location":"1"}`, "203.0.113.7:5000", "application/json"))
	if w.Code != http.StatusForbidden || len(box.selected) != 0 {
		t.Errorf("non-LAN play: %d, selected %v", w.Code, box.selected)
	}
	w = httptest.NewRecorder()
	s.handlePandoraPlay(w, nativeReq(http.MethodGet, "/api/pandora/play", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET play: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.handlePandoraPlay(w, nativeReq(http.MethodPost, "/api/pandora/play", `{"location":"1"}`, "192.168.1.5:5000", "application/x-www-form-urlencoded"))
	if w.Code != http.StatusUnsupportedMediaType || len(box.selected) != 0 {
		t.Errorf("form play: %d", w.Code)
	}
}

func TestPandoraStationsNeedServiceAndAccount(t *testing.T) {
	s, p, box := nativeTestServer(false)
	w := httptest.NewRecorder()
	s.handlePandoraStations(w, nativeReq(http.MethodGet, "/api/pandora/stations", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "service-off") {
		t.Fatalf("service off: %d %s", w.Code, w.Body.String())
	}
	p.on = true
	box.sources = []boxapi.Source{{Source: "PANDORA", Status: "UNAVAILABLE"}}
	w = httptest.NewRecorder()
	s.handlePandoraStations(w, nativeReq(http.MethodGet, "/api/pandora/stations", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "not-linked") || box.listedSource != "" {
		t.Fatalf("not linked: %d %s", w.Code, w.Body.String())
	}
}

func TestPandoraStationsListsWithoutAccount(t *testing.T) {
	s, _, box := nativeTestServer(true)
	pandoraLinked(box)
	box.stations = []boxapi.ServiceStation{pandoraStation("111", "Jazz Radio"), pandoraStation("222", "Blues Radio")}
	w := httptest.NewRecorder()
	s.handlePandoraStations(w, nativeReq(http.MethodGet, "/api/pandora/stations", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if box.listedSource != "PANDORA" || box.listedAccount != pandoraTestAccount {
		t.Fatalf("navigate asked for %q/%q", box.listedSource, box.listedAccount)
	}
	if strings.Contains(w.Body.String(), pandoraTestAccount) || strings.Contains(w.Body.String(), "ContentItem") {
		t.Fatalf("list leaks the account or the raw item: %s", w.Body.String())
	}
	var out struct {
		Status   string `json:"status"`
		Stations []struct {
			Name, Location, Type, Art string
		} `json:"stations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "READY" || len(out.Stations) != 2 || out.Stations[1].Name != "Blues Radio" ||
		out.Stations[1].Location != "222" || out.Stations[1].Type != "stationurl" || out.Stations[1].Art != "http://img.example.com/222.jpg" {
		t.Fatalf("list = %+v", out)
	}
}

func TestPandoraStationsEmptyListIsArray(t *testing.T) {
	s, _, box := nativeTestServer(true)
	pandoraLinked(box)
	w := httptest.NewRecorder()
	s.handlePandoraStations(w, nativeReq(http.MethodGet, "/api/pandora/stations", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"stations":[]`) {
		t.Fatalf("empty list: %d %s", w.Code, w.Body.String())
	}
}

func TestPandoraStationsFirmwareErrorMasksAccount(t *testing.T) {
	s, _, box := nativeTestServer(true)
	var logBuf bytes.Buffer
	s.logger = slog.New(slog.NewTextHandler(&logBuf, nil))
	pandoraLinked(box)
	box.stationsErr = errors.New(`box /navigate: 500: bad account ` + pandoraTestAccount)
	w := httptest.NewRecorder()
	s.handlePandoraStations(w, nativeReq(http.MethodGet, "/api/pandora/stations", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "bad account") {
		t.Fatalf("firmware error: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), pandoraTestAccount) || strings.Contains(logBuf.String(), pandoraTestAccount) {
		t.Fatalf("account in clear: body %s log %s", w.Body.String(), logBuf.String())
	}
}

func TestPandoraPlayReplaysListedItem(t *testing.T) {
	s, _, box := nativeTestServer(true)
	pandoraLinked(box)
	st := pandoraStation("111", "Jazz Radio")
	box.stations = []boxapi.ServiceStation{st}
	s.handlePandoraStations(httptest.NewRecorder(), nativeReq(http.MethodGet, "/api/pandora/stations", "", "192.168.1.5:5000", ""))
	w := httptest.NewRecorder()
	s.handlePandoraPlay(w, nativeReq(http.MethodPost, "/api/pandora/play", `{"location":"111","name":"Jazz Radio"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusOK || len(box.selected) != 1 || box.selected[0] != st.ContentItem {
		t.Fatalf("play: %d, selected %v", w.Code, box.selected)
	}
}

func TestPandoraPlayRebuildsUnlistedItem(t *testing.T) {
	s, _, box := nativeTestServer(true)
	pandoraLinked(box)
	w := httptest.NewRecorder()
	s.handlePandoraPlay(w, nativeReq(http.MethodPost, "/api/pandora/play", `{"location":"333","name":"Rock & Roll"}`, "192.168.1.5:5000", "application/json"))
	want := `<ContentItem source="PANDORA" type="stationurl" location="333" sourceAccount="` + pandoraTestAccount +
		`" isPresetable="true"><itemName>Rock &amp; Roll</itemName></ContentItem>`
	if w.Code != http.StatusOK || len(box.selected) != 1 || box.selected[0] != want {
		t.Fatalf("play: %d, selected %v", w.Code, box.selected)
	}
}

func TestPandoraPlayErrors(t *testing.T) {
	s, _, box := nativeTestServer(true)
	w := httptest.NewRecorder()
	s.handlePandoraPlay(w, nativeReq(http.MethodPost, "/api/pandora/play", `{"name":"x"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("no location: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.handlePandoraPlay(w, nativeReq(http.MethodPost, "/api/pandora/play", `{"location":"1"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "not-linked") {
		t.Fatalf("not linked: %d %s", w.Code, w.Body.String())
	}
	pandoraLinked(box)
	box.selectErr = errors.New("box /select: 1005 UNKNOWN_SOURCE_ERROR")
	w = httptest.NewRecorder()
	s.handlePandoraPlay(w, nativeReq(http.MethodPost, "/api/pandora/play", `{"location":"1"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "UNKNOWN_SOURCE_ERROR") {
		t.Fatalf("refused: %d %s", w.Code, w.Body.String())
	}
}
