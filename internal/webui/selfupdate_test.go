package webui

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	h := func(w http.ResponseWriter, r *http.Request) {
		gotLen = r.ContentLength
		if !isLocalLAN(r.RemoteAddr) {
			t.Error("in-process call does not count as local")
		}
		got, _ = io.ReadAll(r.Body)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
	code, _, err := s.installDownloaded(base+"download/v1.0.13/"+updatesig.AgentFile, fileOf(agent), "/api/agent/update", h)
	if err != nil || code != http.StatusOK || !bytes.Equal(got, agent) || gotLen != int64(len(agent)) {
		t.Fatalf("code=%d err=%v got=%d len=%d", code, err, len(got), gotLen)
	}

	// The same download against a different signed hash never completes.
	other := fileOf(append([]byte{1}, agent[1:]...))
	_, _, err = s.installDownloaded(base+"download/v1.0.13/"+updatesig.AgentFile, other, "/api/agent/update", h)
	if err == nil || len(got) >= len(agent) {
		t.Fatalf("mismatched hash: err=%v delivered=%d", err, len(got))
	}
}
