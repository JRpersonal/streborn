package webui

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/updatesig"
)

func fileOf(b []byte) updatesig.File {
	sum := sha256.Sum256(b)
	return updatesig.File{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b))}
}

// readAllSmall reads like the upload handlers do: small reads until EOF/error.
func readAllSmall(r io.Reader) ([]byte, error) {
	var out bytes.Buffer
	buf := make([]byte, 7)
	for {
		n, err := r.Read(buf)
		out.Write(buf[:n])
		if err == io.EOF {
			return out.Bytes(), nil
		}
		if err != nil {
			return out.Bytes(), err
		}
	}
}

func TestHeldBackBodyDeliversOnlyVerifiedContent(t *testing.T) {
	good := bytes.Repeat([]byte("streborn"), 100)
	want := fileOf(good)

	b, _ := newHeldBackBody(bytes.NewReader(good), want, nil)
	got, err := readAllSmall(b)
	if err != nil || !bytes.Equal(got, good) {
		t.Fatalf("good download: err=%v len=%d", err, len(got))
	}

	cases := map[string][]byte{
		"tampered":  append(append([]byte{}, good[:len(good)-1]...), 'X'),
		"truncated": good[:len(good)-10],
		"longer":    append(append([]byte{}, good...), 'X'),
	}
	for name, body := range cases {
		b, _ := newHeldBackBody(bytes.NewReader(body), want, nil)
		got, err := readAllSmall(b)
		if err == nil {
			t.Errorf("%s: no error", name)
		}
		// The handler must never have seen the full length: that is what
		// makes it discard the upload instead of installing it.
		if int64(len(got)) >= want.Size {
			t.Errorf("%s: %d of %d bytes delivered", name, len(got), want.Size)
		}
	}
}

// selfUpdateFixture serves a signed release the way GitHub lays it out and
// points the self-update at it through the NAND override file.
func selfUpdateFixture(t *testing.T, version string, agent, engine []byte, signWith ed25519.PrivateKey) (*Server, string) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	if signWith == nil {
		signWith = priv
	}
	ix := updatesig.Index{Version: version, Files: map[string]updatesig.File{
		updatesig.AgentFile:  fileOf(agent),
		updatesig.EngineFile: fileOf(engine),
	}}
	payload, _ := json.Marshal(ix)
	sig := updatesig.Sign(signWith, payload)
	mux := http.NewServeMux()
	mux.HandleFunc("/latest/download/update.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) })
	mux.HandleFunc("/latest/download/update.json.sig", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, sig) })
	mux.HandleFunc("/download/"+version+"/"+updatesig.AgentFile, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(agent) })
	mux.HandleFunc("/download/"+version+"/"+updatesig.EngineFile, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(engine) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	for _, v := range []*string{&selfUpdateSourcePath, &selfUpdatePendingPath, &selfUpdateResultPath} {
		orig := *v
		t.Cleanup(func() { *v = orig })
	}
	selfUpdateSourcePath = filepath.Join(dir, "source.json")
	selfUpdatePendingPath = filepath.Join(dir, "pending.json")
	selfUpdateResultPath = filepath.Join(dir, "last.json")
	src, _ := json.Marshal(selfUpdateSource{Base: srv.URL + "/", Keys: []string{base64.StdEncoding.EncodeToString(pub)}})
	if err := os.WriteFile(selfUpdateSourcePath, src, 0o644); err != nil {
		t.Fatal(err)
	}
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, srv.URL + "/"
}

func postAction(t *testing.T, s *Server, action string) selfUpdateStatus {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/agent/self-update", strings.NewReader(`{"action":"`+action+`"}`))
	req.RemoteAddr = "192.168.1.20:5000"
	rec := httptest.NewRecorder()
	s.handleSelfUpdate(rec, req)
	var st selfUpdateStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	return st
}

func TestSelfUpdateCheckFindsNewerSignedRelease(t *testing.T) {
	origVer := agentVersion
	t.Cleanup(func() { agentVersion = origVer })
	SetAgentVersion("v1.0.12")
	s, _ := selfUpdateFixture(t, "v1.0.13", []byte("agent-binary"), []byte("engine-binary"), nil)
	st := postAction(t, s, "check")
	if st.Latest != "v1.0.13" || !st.Available || st.State != "idle" {
		t.Fatalf("status %+v", st)
	}
	SetAgentVersion("v1.0.13")
	if st := s.selfUpdateStatus(); st.Available {
		t.Fatal("offered the version the speaker already runs")
	}
}

func TestSelfUpdateCheckRefusesUntrustedSignature(t *testing.T) {
	_, stranger, _ := ed25519.GenerateKey(rand.Reader)
	s, _ := selfUpdateFixture(t, "v1.0.13", []byte("agent-binary"), []byte("engine-binary"), stranger)
	st := postAction(t, s, "check")
	if st.Available || st.State != "failed" || !strings.Contains(st.Error, "signature") {
		t.Fatalf("status %+v", st)
	}
}

func TestInstallDownloadedFeedsTheHandlerTheVerifiedBody(t *testing.T) {
	agent := bytes.Repeat([]byte{0x7f, 'E', 'L', 'F'}, 4096)
	s, base := selfUpdateFixture(t, "v1.0.13", agent, []byte("engine-binary"), nil)
	var got []byte
	var gotLen int64
	h := func(up upload) *uploadRefusal {
		gotLen = up.Size
		if !isLocalLAN(up.Remote) {
			t.Error("in-process call does not count as local")
		}
		got, _ = io.ReadAll(up.Body)
		return nil
	}
	code, _, err := s.installDownloaded(base+"download/v1.0.13/"+updatesig.AgentFile, fileOf(agent), h)
	if err != nil || code != http.StatusOK || !bytes.Equal(got, agent) || gotLen != int64(len(agent)) {
		t.Fatalf("code=%d err=%v got=%d len=%d", code, err, len(got), gotLen)
	}

	// The same download against a different signed hash never completes.
	other := fileOf(append([]byte{1}, agent[1:]...))
	_, _, err = s.installDownloaded(base+"download/v1.0.13/"+updatesig.AgentFile, other, h)
	if err == nil || len(got) >= len(agent) {
		t.Fatalf("mismatched hash: err=%v delivered=%d", err, len(got))
	}
}

// A check running beside an install (the phone's "plan" asks for the latest
// release too, and the install checks again when its index is old) leaves the
// install's state alone. #1339: such a check set "idle" for a moment, the
// phone polling right then took the update for over, stopped asking and kept
// "Downloading, 9%" while the speaker finished on its own.
func TestSelfUpdateCheckLeavesARunningInstallAlone(t *testing.T) {
	origVer := agentVersion
	t.Cleanup(func() { agentVersion = origVer })
	SetAgentVersion("v1.0.12")
	s, _ := selfUpdateFixture(t, "v1.0.13", []byte("agent-binary"), []byte("engine-binary"), nil)

	for _, state := range []string{"downloading", "installing", "rebooting", "engine"} {
		s.selfUpd.set(state, "agent")
		s.selfUpd.progress(9, 100)
		if _, err := s.selfUpdateCheck(context.Background()); err != nil {
			t.Fatalf("%s: check failed: %v", state, err)
		}
		st := s.selfUpdateStatus()
		if st.State != state || st.Done != 9 || st.Total != 100 || st.Latest != "v1.0.13" {
			t.Fatalf("%s: a check beside the install changed it: %+v", state, st)
		}
	}

	// An install that starts while a check runs keeps its state when the
	// check ends, whichever way it ends.
	s.selfUpd.set("idle", "")
	if !s.selfUpd.beginCheck() {
		t.Fatal("an idle updater refused the check")
	}
	s.selfUpd.set("downloading", "agent")
	s.selfUpd.endCheck(nil)
	s.selfUpd.endCheck(errors.New("could not check for updates: no route to host"))
	if st := s.selfUpdateStatus(); st.State != "downloading" || st.Error != "" {
		t.Fatalf("the ending check overwrote the install: %+v", st)
	}

	// Without an install the check reports as before.
	s.selfUpd.set("failed", "")
	if _, err := s.selfUpdateCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.selfUpdateStatus(); st.State != "idle" {
		t.Fatalf("a plain check did not settle on idle: %+v", st)
	}
}

// The phone card never freezes on an old answer (#1339, issue 13): its
// status reads have a time limit, a list that arrives mid-update makes the
// card follow that update, and a speaker that said its internet was gone
// keeps that line through the whole outage.
func TestPhoneCardFollowsAnUpdateItDidNotSeeEnd(t *testing.T) {
	page := readIndexHTML(t)
	js := page[strings.Index(page, "(function wireSelfUpdate(){"):strings.Index(page, "(function wireDiag(){")]
	get := js[strings.Index(js, "async function suGet()"):]
	get = get[:strings.Index(get, "\n  }\n")]
	if !strings.Contains(get, "new AbortController()") || !strings.Contains(get, "ctl.abort()") || !strings.Contains(get, "signal: ctl.signal") {
		t.Error("the card's status read has no time limit")
	}
	for _, fn := range []string{"async function load()", "async function ownStatus()"} {
		line := js[strings.Index(js, fn):]
		line = line[:strings.Index(line, "\n")]
		if !strings.Contains(line, "await suGet()") {
			t.Errorf("%s does not use the time-limited read: %s", fn, line)
		}
	}
	pl := js[strings.Index(js, "async function plan()"):]
	pl = pl[:strings.Index(pl, "\n  }\n")]
	if !strings.Contains(pl, "if (suBusyState() && !polling") || !strings.Contains(pl, "poll();") {
		t.Error("a list that arrives mid-update does not make the card follow it")
	}
	rd := js[strings.Index(js, "if (stBusy && lostSince) {"):]
	rd = rd[:strings.Index(rd, "else if (st.state === 'checking')")]
	net, quiet := strings.Index(rd, "st.phase === 'network'"), strings.Index(rd, "show(T.sfNoAnswer")
	if net < 0 || quiet < 0 || net > quiet || !strings.Contains(rd, "T.sfNetWaitOne") {
		t.Error("a silent speaker that last said it lost the internet does not keep the no-internet line")
	}
}
