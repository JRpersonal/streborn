package marge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	adoptSelf    = "94E36DF9CE40"
	adoptPartner = "10CE00112233"
	groupPoll    = "/streaming/account/stick@local/device/" + adoptSelf + "/group/"
)

func newAdoptServer(t *testing.T, probe FirmwareGroupProbe) *Server {
	t.Helper()
	opts := []Option{WithDeviceID(adoptSelf)}
	if probe != nil {
		opts = append(opts, WithFirmwareGroupProbe(probe))
	}
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), opts...)
	s.SetAccount(&AccountInfo{AccountEmail: "stick@local"})
	return s
}

func poll(s *Server) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, groupPoll, nil))
	return w
}

func healthyPair() FirmwareGroup {
	return FirmwareGroup{
		ID: "1234567", Name: "Wohnzimmer", MasterDeviceID: adoptSelf, Status: "GROUP_OK",
		Roles: []FirmwareRole{
			{DeviceID: adoptSelf, Role: "LEFT", IP: "192.0.2.10"},
			{DeviceID: adoptPartner, Role: "RIGHT", IP: "192.0.2.11"},
		},
	}
}

// The regression this file exists for: with nothing stored, the poll was
// answered with the account document, which the firmware rejects before
// deleting its own pair.
func TestEmptyStoreNeverAnswersTheGroupPollWithTheAccount(t *testing.T) {
	w := poll(newAdoptServer(t, nil))
	body := w.Body.String()
	if strings.Contains(body, "<account") {
		t.Fatalf("group poll answered with the account document:\n%s", body)
	}
	if w.Code != http.StatusOK || !strings.Contains(body, "<group/>") {
		t.Fatalf("want 200 <group/>, got %d:\n%s", w.Code, body)
	}
}

func TestUnpairedFirmwareGetsAnEmptyGroup(t *testing.T) {
	calls := 0
	s := newAdoptServer(t, func(context.Context) (FirmwareGroup, error) { calls++; return FirmwareGroup{}, nil })
	if w := poll(s); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<group/>") {
		t.Fatalf("want 200 <group/>, got %d:\n%s", w.Code, w.Body.String())
	}
	// The empty verdict is trusted for a while: the next poll does not ask again.
	poll(s)
	if calls != 1 {
		t.Fatalf("firmware asked %d times, want 1 within the quiet window", calls)
	}
}

func TestHealthyFirmwarePairIsAdoptedAndServedBack(t *testing.T) {
	s := newAdoptServer(t, func(context.Context) (FirmwareGroup, error) { return healthyPair(), nil })
	w := poll(s)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `<group id="1234567">`) ||
		!strings.Contains(body, adoptPartner) || !strings.Contains(body, "<name>Wohnzimmer</name>") {
		t.Fatalf("pair not served back, got %d:\n%s", w.Code, body)
	}
	if _, canonical, ok := s.GroupSnapshot(); !ok || canonical {
		t.Fatalf("adopted pair must be stored and not canonical: ok=%v canonical=%v", ok, canonical)
	}
}

func TestPairsSTRCannotVouchForAreHeldNotAdopted(t *testing.T) {
	broken := healthyPair()
	broken.Status = "GROUP_ERROR"
	foreign := healthyPair()
	foreign.Roles[0].DeviceID = "AAAAAAAAAAAA"
	single := healthyPair()
	single.Roles = single.Roles[:1]

	cases := map[string]FirmwareGroupProbe{
		"GROUP_ERROR":        func(context.Context) (FirmwareGroup, error) { return broken, nil },
		"does not name self": func(context.Context) (FirmwareGroup, error) { return foreign, nil },
		"one role":           func(context.Context) (FirmwareGroup, error) { return single, nil },
		"firmware silent": func(context.Context) (FirmwareGroup, error) {
			return FirmwareGroup{}, errors.New("timeout")
		},
	}
	for name, probe := range cases {
		t.Run(name, func(t *testing.T) {
			s := newAdoptServer(t, probe)
			w := poll(s)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("want 503 (leave the firmware's state alone), got %d:\n%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "<group/>") || strings.Contains(w.Body.String(), "<account") {
				t.Fatalf("hold must not say 'not grouped':\n%s", w.Body.String())
			}
			if _, _, ok := s.GroupSnapshot(); ok {
				t.Fatal("nothing may be stored")
			}
		})
	}
}

// A stored record wins: the firmware is not asked at all.
func TestStoredRecordIsServedWithoutAskingTheFirmware(t *testing.T) {
	s := newAdoptServer(t, func(context.Context) (FirmwareGroup, error) {
		t.Fatal("probe called although a record is stored")
		return FirmwareGroup{}, nil
	})
	if err := s.SetCanonicalGroup(CanonicalGroupXML("Paar", adoptSelf, "192.0.2.10", adoptPartner, "192.0.2.11")); err != nil {
		t.Fatal(err)
	}
	if w := poll(s); !strings.Contains(w.Body.String(), adoptPartner) {
		t.Fatalf("stored pair not served:\n%s", w.Body.String())
	}
}

func TestAdoptRefusesWhenARecordExists(t *testing.T) {
	s := newAdoptServer(t, nil)
	_ = s.SetCanonicalGroup(CanonicalGroupXML("Paar", adoptSelf, "", adoptPartner, ""))
	if ok, _ := s.AdoptFirmwareGroup(healthyPair()); ok {
		t.Fatal("adoption must never replace a stored record")
	}
}

func TestProviderSettingsIsAnsweredWithProviderSettings(t *testing.T) {
	s := newAdoptServer(t, nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/streaming/account/stick@local/provider_settings", nil))
	body := w.Body.String()
	if strings.Contains(body, "<account") || !strings.Contains(body, "<providerSettings/>") {
		t.Fatalf("provider_settings must be a <providerSettings> document:\n%s", body)
	}
}
