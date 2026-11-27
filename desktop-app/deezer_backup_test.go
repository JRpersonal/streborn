package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testDeezerSecret = "fr-test-token-not-a-real-one"

const testSourcesXML = `Warning: Permanently added '192.0.2.1' (RSA) to the list of known hosts.
<?xml version="1.0" encoding="UTF-8" ?>
<sources>
    <source displayName="AUX IN" secret="" secretType="token">
        <sourceKey type="AUX" account="AUX" />
    </source>
    <source displayName="user@example.com" secret="` + testDeezerSecret + `" secretType="token_version_3">
        <sourceKey type="DEEZER" account="1234567" />
    </source>
</sources>
`

func TestDeezerLoginFromSources(t *testing.T) {
	l, ok := deezerLoginFromSources(testSourcesXML)
	if !ok {
		t.Fatal("the Deezer login was not found")
	}
	want := deezerLogin{Account: "1234567", DisplayName: "user@example.com", Secret: testDeezerSecret, SecretType: "token_version_3"}
	if l != want {
		t.Fatalf("got %+v, want %+v", l, want)
	}
	for name, doc := range map[string]string{
		"empty secret": strings.Replace(testSourcesXML, testDeezerSecret, "", 1),
		"no deezer":    strings.Replace(testSourcesXML, `type="DEEZER"`, `type="TUNEIN"`, 1),
		"no document":  "cat: can't open '/mnt/nv/BoseApp-Persistence/*/Sources.xml'",
		"cut off":      testSourcesXML[:strings.Index(testSourcesXML, "<sourceKey type=\"DEEZER\"")],
		"empty":        "",
	} {
		if _, ok := deezerLoginFromSources(doc); ok {
			t.Errorf("%s: a login was reported", name)
		}
	}
}

// A copy saved on the computer has to read back as the same login, and it
// is the same kind of file as a Sources.xml copied off the speaker by hand.
func TestSaveDeezerBackupRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deezer-backups")
	l, _ := deezerLoginFromSources(testSourcesXML)
	path, err := saveDeezerBackup(dir, l)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	back, ok := deezerLoginFromSources(string(b))
	if !ok || back != l {
		t.Fatalf("read back %+v ok=%v, want %+v", back, ok, l)
	}
	if strings.Contains(filepath.Base(path), l.Account) {
		t.Errorf("the file name %q names the account", filepath.Base(path))
	}
	st1, _ := os.Stat(path)
	if _, err := saveDeezerBackup(dir, l); err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(path)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Error("an identical copy was written again")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("%d files in the folder, want 1", len(entries))
	}
}

func fakeDeezerSSH(t *testing.T, sources, writeReply string) (written *string, dir string) {
	t.Helper()
	dir = t.TempDir()
	got := ""
	oldRead, oldWrite, oldDir := deezerBackupSSHRead, deezerBackupSSHWrite, deezerBackupDirFn
	deezerBackupSSHRead = func(host, cmd string, timeout time.Duration) (string, error) {
		if !strings.Contains(cmd, boxSourcesGlob) {
			t.Errorf("read command %q does not read the source store", cmd)
		}
		return sources, nil
	}
	deezerBackupSSHWrite = func(host, cmd string, in io.Reader, timeout time.Duration) (string, error) {
		if !strings.Contains(cmd, boxKeptDeezerLoginPath) || !strings.Contains(cmd, `[ -s "$F" ]`) {
			t.Errorf("write command %q does not guard an existing kept login", cmd)
		}
		b, _ := io.ReadAll(in)
		got = string(b)
		return writeReply, nil
	}
	deezerBackupDirFn = func() (string, error) { return dir, nil }
	t.Cleanup(func() { deezerBackupSSHRead, deezerBackupSSHWrite, deezerBackupDirFn = oldRead, oldWrite, oldDir })
	return &got, dir
}

func TestPreserveDeezerLoginSavesAndSeeds(t *testing.T) {
	written, dir := fakeDeezerSSH(t, testSourcesXML, "STR_DEEZER_KEPT_WRITTEN\n")
	var logs bytes.Buffer
	a := &App{logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	if !a.preserveDeezerLogin("192.0.2.1") {
		t.Fatal("no login reported")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("%d copies on the computer, want 1", len(entries))
	}
	// What goes to the speaker has to be a kept-login file the agent loads
	// (internal/marge loadKeptLogin: account and secret set).
	var kept struct {
		Account, DisplayName, SecretType, Secret, From string
		KeptAt                                         time.Time
	}
	if err := json.Unmarshal([]byte(*written), &kept); err != nil {
		t.Fatalf("the kept-login file is not JSON: %v", err)
	}
	if kept.Account != "1234567" || kept.Secret != testDeezerSecret || kept.SecretType != "token_version_3" ||
		kept.From != "sources-file" || kept.KeptAt.IsZero() {
		t.Errorf("kept-login file is %+v", kept)
	}
	if out := logs.String(); strings.Contains(out, testDeezerSecret) || strings.Contains(out, "1234567") ||
		strings.Contains(out, "user@example.com") {
		t.Errorf("the log names the login or the account:\n%s", out)
	}
	if !strings.Contains(logs.String(), "keptOnSpeaker=written") {
		t.Errorf("the log does not say the speaker got its kept login:\n%s", logs.String())
	}
}

func TestPreserveDeezerLoginWithoutDeezerTouchesNothing(t *testing.T) {
	noDeezer := strings.Replace(testSourcesXML, `type="DEEZER"`, `type="TUNEIN"`, 1)
	written, dir := fakeDeezerSSH(t, noDeezer, "")
	a := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if a.preserveDeezerLogin("192.0.2.1") {
		t.Fatal("a login was reported")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 || *written != "" {
		t.Errorf("something was written: %d files, speaker got %d bytes", len(entries), len(*written))
	}
}

func fakeDeezerRestore(t *testing.T, path string, sshOpens bool) {
	t.Helper()
	oldPick, oldOpen, oldRecheck, oldClose := deezerRestorePick, deezerRestoreOpenSSH, deezerRestoreRecheck, deezerRestoreCloseSSH
	deezerRestorePick = func(*App) (string, error) { return path, nil }
	deezerRestoreOpenSSH = func(*App, string) bool { return sshOpens }
	deezerRestoreCloseSSH = func(*App, string, int) {}
	deezerRestoreRecheck = func(*App, string, int) (map[string]any, error) {
		return map[string]any{"state": "connecting", "recreate": map[string]any{"ran": true}}, nil
	}
	t.Cleanup(func() {
		deezerRestorePick, deezerRestoreOpenSSH, deezerRestoreRecheck, deezerRestoreCloseSSH = oldPick, oldOpen, oldRecheck, oldClose
	})
}

func writeDeezerBackupFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "deezer-backup.xml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A Sources.xml copied off a speaker by hand goes back onto a speaker as the
// agent's kept-login file, and neither the answer nor the log names the login.
func TestDeezerRestoreBackupWritesTheKeptLogin(t *testing.T) {
	written, dir := fakeDeezerSSH(t, "", "STR_DEEZER_KEPT_WRITTEN\n")
	fakeDeezerRestore(t, writeDeezerBackupFile(t, testSourcesXML), true)
	var logs bytes.Buffer
	a := &App{logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	out, err := a.DeezerRestoreBackup("192.0.2.1", 8888)
	if err != nil {
		t.Fatal(err)
	}
	if out["result"] != "written" || out["state"] != "connecting" {
		t.Fatalf("answer is %v", out)
	}
	var kept struct{ Account, Secret, SecretType string }
	if err := json.Unmarshal([]byte(*written), &kept); err != nil {
		t.Fatalf("the kept-login file is not JSON: %v", err)
	}
	if kept.Account != "1234567" || kept.Secret != testDeezerSecret || kept.SecretType != "token_version_3" {
		t.Errorf("kept-login file is %+v", kept)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("%d copies on the computer, want 1", len(entries))
	}
	answer, _ := json.Marshal(out)
	for _, text := range []string{logs.String(), string(answer)} {
		if strings.Contains(text, testDeezerSecret) || strings.Contains(text, "1234567") || strings.Contains(text, "user@example.com") {
			t.Errorf("the login or the account shows:\n%s", text)
		}
	}
}

func TestDeezerRestoreBackupRefusals(t *testing.T) {
	a := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for name, c := range map[string]struct {
		file, reply string
		ssh         bool
		want        string
		wantWrite   bool
	}{
		"canceled":     {"", "", true, "canceled", false},
		"no login":     {strings.Replace(testSourcesXML, `type="DEEZER"`, `type="TUNEIN"`, 1), "", true, "no-login", false},
		"not xml":      {"hello", "", true, "no-login", false},
		"no ssh":       {testSourcesXML, "", false, "no-ssh", false},
		"already kept": {testSourcesXML, "STR_DEEZER_KEPT_EXISTS\n", true, "already-kept", true},
		"write failed": {testSourcesXML, "", true, "write-failed", true},
	} {
		written, dir := fakeDeezerSSH(t, "", c.reply)
		path := ""
		if name != "canceled" {
			path = writeDeezerBackupFile(t, c.file)
		}
		fakeDeezerRestore(t, path, c.ssh)
		out, err := a.DeezerRestoreBackup("192.0.2.1", 8888)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out["result"] != c.want {
			t.Errorf("%s: result %v, want %s", name, out["result"], c.want)
		}
		if (*written != "") != c.wantWrite {
			t.Errorf("%s: speaker got %d bytes", name, len(*written))
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s: a copy was saved on the computer", name)
		}
		if _, has := out["state"]; has {
			t.Errorf("%s: a recheck ran", name)
		}
	}
}

func TestSourcesListDeezerAccount(t *testing.T) {
	for doc, want := range map[string]bool{
		`<sources><sourceItem source="DEEZER" sourceAccount="user@example.com" status="READY" isLocal="false">x</sourceItem></sources>`: true,
		`<sources><sourceItem source="DEEZER" sourceAccount="" status="UNAVAILABLE"/></sources>`:                                        false,
		`<sources><sourceItem source="DEEZER" status="UNAVAILABLE"/></sources>`:                                                         false,
		`<sources><sourceItem source="TUNEIN" sourceAccount="x" status="READY"/></sources>`:                                             false,
		``: false,
	} {
		if got := sourcesListDeezerAccount(doc); got != want {
			t.Errorf("%q: got %v, want %v", doc, got, want)
		}
	}
}
