// Deezer: save the speaker's own login before an install changes anything.
//
// A speaker that loses its Deezer login can never get a new one, because
// Deezer issues logins only to Bose's app (docs/streaming/deezer.md). The
// agent keeps the login at its first start, but that start comes after the
// install has already restarted the speaker. This file is the earlier net:
// as soon as an install has SSH, it reads the firmware's source store once,
// and when that holds a Deezer login it
//
//   - writes a copy into the app's own folder on this computer, and
//   - seeds the agent's kept-login file on the speaker when there is none,
//     so the agent starts with the login whatever the restart does.
//
// The login is the owner's credential. Nothing here logs it, returns it to
// the frontend or puts it into a diagnostic file; the copy on this computer
// is mode 0600 in the user's config folder, which the log export never reads.

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// boxSourcesGlob is the firmware's source store, the file the agent reads
// too (internal/boxsources, which this module cannot import).
const boxSourcesGlob = "/mnt/nv/BoseApp-Persistence/*/Sources.xml"

// boxKeptDeezerLoginPath is the agent's kept-login file (cmd/agent,
// keptDeezerLoginPath; shape: internal/marge/deezerlogin.go, keptLogin).
const boxKeptDeezerLoginPath = "/mnt/nv/streborn/deezer-login.json"

// deezerLogin is a Deezer login as the firmware's source store holds it.
type deezerLogin struct {
	Account     string
	DisplayName string
	Secret      string
	SecretType  string
}

// deezerLoginFromSources finds the Deezer login in a Sources.xml document.
// Text around the document (an ssh banner) is ignored. No login, no result.
func deezerLoginFromSources(out string) (deezerLogin, bool) {
	i := strings.Index(out, "<sources")
	if i < 0 {
		return deezerLogin{}, false
	}
	dec := xml.NewDecoder(strings.NewReader(out[i:]))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			return deezerLogin{}, false
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "source" {
			continue
		}
		var raw struct {
			DisplayName string `xml:"displayName,attr"`
			Secret      string `xml:"secret,attr"`
			SecretType  string `xml:"secretType,attr"`
			SourceKey   struct {
				Type    string `xml:"type,attr"`
				Account string `xml:"account,attr"`
			} `xml:"sourceKey"`
		}
		if err := dec.DecodeElement(&raw, &se); err != nil {
			return deezerLogin{}, false
		}
		if !strings.EqualFold(strings.TrimSpace(raw.SourceKey.Type), "DEEZER") ||
			raw.Secret == "" || strings.TrimSpace(raw.SourceKey.Account) == "" {
			continue
		}
		return deezerLogin{
			Account: strings.TrimSpace(raw.SourceKey.Account), DisplayName: raw.DisplayName,
			Secret: raw.Secret, SecretType: raw.SecretType,
		}, true
	}
}

// sourcesXML writes the login back in the firmware's own shape, so a copy
// saved here and a Sources.xml the owner copied off the speaker by hand are
// the same kind of file.
func (l deezerLogin) sourcesXML() ([]byte, error) {
	type sourceKey struct {
		Type    string `xml:"type,attr"`
		Account string `xml:"account,attr"`
	}
	type source struct {
		DisplayName string    `xml:"displayName,attr"`
		Secret      string    `xml:"secret,attr"`
		SecretType  string    `xml:"secretType,attr"`
		SourceKey   sourceKey `xml:"sourceKey"`
	}
	doc := struct {
		XMLName xml.Name `xml:"sources"`
		Source  source   `xml:"source"`
	}{Source: source{
		DisplayName: l.DisplayName, Secret: l.Secret, SecretType: l.SecretType,
		SourceKey: sourceKey{Type: "DEEZER", Account: l.Account},
	}}
	b, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// keptJSON is the login in the shape of the agent's kept-login file.
func (l deezerLogin) keptJSON(now time.Time) ([]byte, error) {
	return json.Marshal(struct {
		Account     string    `json:"account"`
		DisplayName string    `json:"displayName,omitempty"`
		SecretType  string    `json:"secretType,omitempty"`
		Secret      string    `json:"secret"`
		From        string    `json:"from"`
		KeptAt      time.Time `json:"keptAt"`
	}{l.Account, l.DisplayName, l.SecretType, l.Secret, "sources-file", now.UTC()})
}

// fileName names the copy after a hash of the login, so the same login
// saved twice is one file and the name says nothing about the account.
func (l deezerLogin) fileName() string {
	sum := sha256.Sum256([]byte(l.Account + "\x00" + l.SecretType + "\x00" + l.Secret))
	return "deezer-login-" + hex.EncodeToString(sum[:6]) + ".xml"
}

// deezerBackupDir is where the copies live on this computer.
func deezerBackupDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ST Reborn", "deezer-backups"), nil
}

// saveDeezerBackup writes the copy into dir (mode 0600, folder 0700). An
// identical copy that is already there is left alone.
func saveDeezerBackup(dir string, l deezerLogin) (string, error) {
	doc, err := l.sourcesXML()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, l.fileName())
	if have, err := os.ReadFile(path); err == nil && bytes.Equal(have, doc) {
		return path, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, doc, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// Seams for the tests: the two SSH calls and the folder.
var (
	deezerBackupSSHRead  = boxSSHOutput
	deezerBackupSSHWrite = boxSSHUploadStdin
	deezerBackupDirFn    = deezerBackupDir
)

// seedKeptDeezerCmd writes stdin to the agent's kept-login file unless one
// with content is already there. It never prints the file.
const seedKeptDeezerCmd = `F=` + boxKeptDeezerLoginPath + `; ` +
	`if [ -s "$F" ]; then echo STR_DEEZER_KEPT_EXISTS; else ` +
	`mkdir -p /mnt/nv/streborn && umask 077 && cat > "$F.tmp" && mv "$F.tmp" "$F" && sync && echo STR_DEEZER_KEPT_WRITTEN; fi`

// preserveDeezerLogin is the install's Deezer step: called once SSH answers
// and before anything is written to the speaker. It never fails the install;
// a speaker without a Deezer login costs one small read and nothing else.
// It reports whether a login was found.
func (a *App) preserveDeezerLogin(host string) bool {
	out, err := deezerBackupSSHRead(host, "cat "+boxSourcesGlob+" 2>/dev/null", 15*time.Second)
	login, ok := deezerLoginFromSources(out)
	if !ok {
		// Never the output: it is the file that holds the logins.
		if err != nil && !strings.Contains(out, "<sources") {
			a.logger.Info("deezer-backup: the speaker's source store could not be read, nothing saved", "host", host, "err", err)
		}
		return false
	}
	saved := false
	if dir, derr := deezerBackupDirFn(); derr != nil {
		a.logger.Warn("deezer-backup: no folder for the copy on this computer", "err", derr)
	} else if _, serr := saveDeezerBackup(dir, login); serr != nil {
		a.logger.Warn("deezer-backup: the Deezer login could not be saved on this computer", "host", host, "err", serr)
	} else {
		saved = true
	}
	seeded := "failed"
	if doc, jerr := login.keptJSON(time.Now()); jerr == nil {
		res, werr := deezerBackupSSHWrite(host, seedKeptDeezerCmd, bytes.NewReader(doc), 20*time.Second)
		switch {
		case strings.Contains(res, "STR_DEEZER_KEPT_WRITTEN"):
			seeded = "written"
		case strings.Contains(res, "STR_DEEZER_KEPT_EXISTS"):
			seeded = "already-there"
		case werr != nil:
			a.logger.Warn("deezer-backup: the kept login could not be written to the speaker", "host", host, "err", werr)
		}
	}
	a.logger.Info("deezer-backup: the speaker holds a Deezer login, saved before the install changes anything",
		"host", host, "savedOnThisComputer", saved, "keptOnSpeaker", seeded, "secretType", login.SecretType)
	return true
}

var (
	deezerSourceItemRe = regexp.MustCompile(`(?i)<sourceItem\b[^>]*\bsource="DEEZER"[^>]*>`)
	sourceAccountRe    = regexp.MustCompile(`(?i)\bsourceAccount="([^"]*)"`)
)

// sourcesListDeezerAccount reports whether a :8090/sources answer lists a
// Deezer entry with an account (the secret never shows there).
func sourcesListDeezerAccount(sourcesXML string) bool {
	for _, item := range deezerSourceItemRe.FindAllString(sourcesXML, -1) {
		if m := sourceAccountRe.FindStringSubmatch(item); m != nil && strings.TrimSpace(m[1]) != "" {
			return true
		}
	}
	return false
}

// boxCarriesDeezerAccount: the speaker has a Bose account of its own AND
// lists Deezer with an account. Such a speaker runs its account check by
// itself, so the stick-free unlock needs no placeholder account, and it is
// the one speaker where changing the account before the login is saved
// could cost something that cannot be replaced. Any doubt answers false and
// the unlock behaves as it always did.
func (a *App) boxCarriesDeezerAccount(host string) bool {
	info, err := a.boseGet(host, "/info")
	if err != nil {
		return false
	}
	if v, found := margeUUIDFromInfo(info); !found || v == "" {
		return false
	}
	src, err := a.boseGet(host, "/sources")
	if err != nil {
		return false
	}
	return sourcesListDeezerAccount(src)
}
