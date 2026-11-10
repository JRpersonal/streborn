package webui

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// baseSpotifyFake implements only the base SpotifyEngine interface, none of
// the optional sub-interfaces.
type baseSpotifyFake struct{}

func (baseSpotifyFake) SwitchedAway(context.Context)                           {}
func (baseSpotifyFake) UserStopped(context.Context, string)                    {}
func (baseSpotifyFake) ServeStream(http.ResponseWriter, *http.Request)         {}
func (baseSpotifyFake) Play(context.Context, string, string, bool, bool) error { return nil }
func (baseSpotifyFake) CurrentUser(context.Context) string                     { return "" }
func (baseSpotifyFake) PlayingContext() string                                 { return "" }
func (baseSpotifyFake) Shuffling(context.Context) bool                         { return false }
func (baseSpotifyFake) Repeating(context.Context) bool                         { return false }
func (baseSpotifyFake) Meta(context.Context, string) (cover, title string)     { return "", "" }
func (baseSpotifyFake) Streaming() bool                                        { return false }
func (baseSpotifyFake) Skip(context.Context, bool) error                       { return nil }
func (baseSpotifyFake) SkipBoundary() time.Time                                { return time.Time{} }
func (baseSpotifyFake) ArmRecallCut()                                          {}
func (baseSpotifyFake) BoundaryStaleKB() int64                                 { return 0 }
func (baseSpotifyFake) Ready() bool                                            { return true }
func (baseSpotifyFake) CanRecall(context.Context) bool                         { return true }
func (baseSpotifyFake) PremiumRequired() bool                                  { return false }
func (baseSpotifyFake) SetRecalling()                                          {}
func (baseSpotifyFake) SuppressActivate(time.Duration)                         {}
func (baseSpotifyFake) ExpectReattach(time.Duration)                           {}

// fullSpotifyFake adds every optional sub-interface on top of the base.
type fullSpotifyFake struct {
	baseSpotifyFake
	stops int
}

func (f *fullSpotifyFake) ExportCred() ([]byte, error)                     { return []byte("cred"), nil }
func (f *fullSpotifyFake) ImportCred(context.Context, []byte) error        { return nil }
func (f *fullSpotifyFake) ServeInfo(http.ResponseWriter, *http.Request)    {}
func (f *fullSpotifyFake) ServeQuality(http.ResponseWriter, *http.Request) {}
func (f *fullSpotifyFake) Reload() bool                                    { return true }
func (f *fullSpotifyFake) Stop() bool                                      { f.stops++; return true }

var (
	_ SpotifyEngine          = baseSpotifyFake{}
	_ SpotifyEngine          = (*fullSpotifyFake)(nil)
	_ SpotifyCredentialSync  = (*fullSpotifyFake)(nil)
	_ SpotifyStatusPages     = (*fullSpotifyFake)(nil)
	_ SpotifyEngineLifecycle = (*fullSpotifyFake)(nil)
)

// spotifyBaseFields lists every field the base interface fills.
func spotifyBaseFields(s *Server) map[string]bool {
	return map[string]bool{
		"spotifySwitchedAway":     s.spotifySwitchedAway != nil,
		"spotifyUserStopped":      s.spotifyUserStopped != nil,
		"spotifyStream":           s.spotifyStream != nil,
		"spotifyPlay":             s.spotifyPlay != nil,
		"spotifyUser":             s.spotifyUser != nil,
		"spotifyContext":          s.spotifyContext != nil,
		"spotifyShuffle":          s.spotifyShuffle != nil,
		"spotifyRepeat":           s.spotifyRepeat != nil,
		"spotifyMeta":             s.spotifyMeta != nil,
		"spotifyStreaming":        s.spotifyStreaming != nil,
		"spotifySkip":             s.spotifySkip != nil,
		"spotifySkipBoundary":     s.spotifySkipBoundary != nil,
		"spotifyArmRecallCut":     s.spotifyArmRecallCut != nil,
		"spotifyBoundaryStaleKB":  s.spotifyBoundaryStaleKB != nil,
		"spotifyReady":            s.spotifyReady != nil,
		"spotifyCanRecall":        s.spotifyCanRecall != nil,
		"spotifyPremiumRequired":  s.spotifyPremiumRequired != nil,
		"spotifySetRecalling":     s.spotifySetRecalling != nil,
		"spotifySuppressActivate": s.spotifySuppressActivate != nil,
		"spotifyExpectReattach":   s.spotifyExpectReattach != nil,
	}
}

// spotifyOptionalFields lists every field an optional sub-interface fills.
func spotifyOptionalFields(s *Server) map[string]bool {
	return map[string]bool{
		"spotifyExportCred": s.spotifyExportCred != nil,
		"spotifyImportCred": s.spotifyImportCred != nil,
		"spotifyInfo":       s.spotifyInfo != nil,
		"spotifyQuality":    s.spotifyQuality != nil,
		"spotifyReload":     s.spotifyReload != nil,
		"spotifyStop":       s.spotifyStop != nil,
	}
}

func TestWithSpotifyBaseOnlyLeavesOptionalFieldsNil(t *testing.T) {
	t.Parallel()
	s := &Server{}
	WithSpotify(baseSpotifyFake{})(s)

	for name, set := range spotifyBaseFields(s) {
		if !set {
			t.Errorf("%s: nil after WithSpotify with the base interface", name)
		}
	}
	for name, set := range spotifyOptionalFields(s) {
		if set {
			t.Errorf("%s: set although the fake does not implement its sub-interface", name)
		}
	}
}

func TestWithSpotifyFullSetsOptionalFields(t *testing.T) {
	t.Parallel()
	fake := &fullSpotifyFake{}
	s := &Server{}
	WithSpotify(fake)(s)

	for name, set := range spotifyBaseFields(s) {
		if !set {
			t.Errorf("%s: nil after WithSpotify with the full fake", name)
		}
	}
	for name, set := range spotifyOptionalFields(s) {
		if !set {
			t.Errorf("%s: nil although the fake implements its sub-interface", name)
		}
	}
	// The fields reach the fake.
	if got, err := s.spotifyExportCred(); err != nil || string(got) != "cred" {
		t.Errorf("spotifyExportCred = %q, %v; want cred", got, err)
	}
	if !s.spotifyReload() {
		t.Error("spotifyReload did not reach the fake")
	}
	s.spotifyStop()
	if fake.stops != 1 {
		t.Errorf("Stop called %d times via spotifyStop, want 1", fake.stops)
	}
}

// The OTA write path stops the engine of the server it runs on. The stop used
// to be a package variable WithSpotify overwrote, so with two servers in one
// process the write of the first stopped the engine of the second.
func TestOTAWriteStopsItsOwnServersEngine(t *testing.T) {
	t.Parallel()
	roomy := productionOTAEffects
	roomy.nandHasRoom = func(string, int64) bool { return true }
	first, second := &fullSpotifyFake{}, &fullSpotifyFake{}
	s1 := &Server{ota: &otaState{}, otaEffects: &roomy}
	WithSpotify(first)(s1)
	s2 := &Server{ota: &otaState{}, otaEffects: &roomy}
	WithSpotify(second)(s2)

	st := s1.reclaimEngineForWrite(t.TempDir(), 1024)
	if !st.engineStopped || first.stops != 1 || second.stops != 0 {
		t.Errorf("stops: first=%d second=%d engineStopped=%v; want the first server's engine stopped once and the second left alone",
			first.stops, second.stops, st.engineStopped)
	}
}

func TestWithSpotifyNilLeavesFieldsUntouched(t *testing.T) {
	t.Parallel()
	s := &Server{spotifyReady: func() bool { return true }}
	WithSpotify(nil)(s)
	if s.spotifyReady == nil {
		t.Error("a nil engine must not clear fields set by other means")
	}
	if s.spotifyPlay != nil {
		t.Error("a nil engine must not set fields")
	}
}

// baseMargeFake implements only MargeGroups.
type baseMargeFake struct{}

func (baseMargeFake) GroupSnapshot() (string, bool, bool) { return "<doc/>", true, true }
func (baseMargeFake) SetCanonicalGroup(string) error      { return nil }
func (baseMargeFake) ClearGroup(string)                   {}
func (baseMargeFake) RenameGroup(string) error            { return nil }
func (baseMargeFake) GroupName() string                   { return "Pair" }

// forwardingMargeFake adds the developer relay.
type forwardingMargeFake struct {
	baseMargeFake
	target string
}

func (f *forwardingMargeFake) SetForward(target string) error { f.target = target; return nil }

var (
	_ MargeGroups    = baseMargeFake{}
	_ MargeGroups    = (*forwardingMargeFake)(nil)
	_ MargeForwarder = (*forwardingMargeFake)(nil)
)

func TestWithMargeFillsGroupFieldsAndOptionalForward(t *testing.T) {
	s := &Server{}
	WithMarge(baseMargeFake{})(s)
	for name, set := range map[string]bool{
		"margeGroupGet":    s.margeGroupGet != nil,
		"margeGroupSet":    s.margeGroupSet != nil,
		"margeGroupClear":  s.margeGroupClear != nil,
		"margeGroupRename": s.margeGroupRename != nil,
		"margeGroupName":   s.margeGroupName != nil,
	} {
		if !set {
			t.Errorf("%s: nil after WithMarge", name)
		}
	}
	if s.margeForward != nil {
		t.Error("margeForward: set although the fake does not implement MargeForwarder")
	}
	if doc, canonical, ok := s.margeGroupGet(); doc != "<doc/>" || !canonical || !ok {
		t.Errorf("margeGroupGet = %q, %v, %v; want the fake's snapshot", doc, canonical, ok)
	}

	fwd := &forwardingMargeFake{}
	s = &Server{}
	WithMarge(fwd)(s)
	if s.margeForward == nil {
		t.Fatal("margeForward: nil although the fake implements MargeForwarder")
	}
	if err := s.margeForward("192.0.2.1:9000"); err != nil || fwd.target != "192.0.2.1:9000" {
		t.Errorf("margeForward did not reach the fake: err=%v target=%q", err, fwd.target)
	}

	s = &Server{margeGroupName: func() string { return "kept" }}
	WithMarge(nil)(s)
	if s.margeGroupName == nil || s.margeGroupGet != nil {
		t.Error("a nil marge value must leave every field untouched")
	}
}
