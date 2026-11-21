package marge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A Deezer registration in the shape the firmware uses for every linked
// service (provider id 14). Deezer is not one of the US services, so before
// this masking applied to every body its token was recorded in clear.
const deezerAddSourceBody = `<?xml version="1.0" encoding="UTF-8" ?><source>` +
	`<credential type="token">deezer-secret-token</credential>` +
	`<name>Deezer</name><sourceproviderid>14</sourceproviderid>` +
	`<sourcename>DEEZER</sourcename><username>0000000000</username></source>`

func TestSpyLogMasksEveryCredential(t *testing.T) {
	s := newTestServer()
	bodies := []string{
		deezerAddSourceBody,
		// The attribute form, for a service STR knows nothing about.
		`<source><credential type="password" text="other-secret"/><sourcename>SOMESERVICE</sourcename></source>`,
		// A credentials document with a password.
		`<credentials source="DEEZER" displayName="Deezer"><user>user@example.com</user><pass>pass-secret</pass></credentials>`,
	}
	for _, b := range bodies {
		req := httptest.NewRequest(http.MethodPost, "/streaming/account/stick@local/source", strings.NewReader(b))
		req.Header.Set("Authorization", "auth-header-secret")
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}
	secrets := []string{"deezer-secret-token", "other-secret", "pass-secret", "auth-header-secret"}
	for _, e := range s.RecentRequests() {
		for _, sec := range secrets {
			if strings.Contains(e.Body, sec) {
				t.Fatalf("recorded body keeps %q: %s", sec, e.Body)
			}
			for k, vs := range e.Headers {
				for _, v := range vs {
					if strings.Contains(v, sec) {
						t.Fatalf("recorded header %s keeps %q", k, sec)
					}
				}
			}
		}
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__spy/log", nil))
	log := rec.Body.String()
	for _, sec := range secrets {
		if strings.Contains(log, sec) {
			t.Fatalf("/__spy/log shows %q:\n%s", sec, log)
		}
	}
	// What is not secret stays readable: the log is for diagnosis.
	for _, want := range []string{"<sourcename>DEEZER</sourcename>", `<credential type="token">***</credential>`, "<pass>***</pass>", "Authorization: ***"} {
		if !strings.Contains(log, want) {
			t.Errorf("/__spy/log lacks %q:\n%s", want, log)
		}
	}
}

func TestSpyLogHandlerStillReadsTheOriginal(t *testing.T) {
	// Masking only touches the recorded copy: a handler must see the real
	// body (the US-service store keeps the credential it was sent).
	s := newPandoraServer(t, true)
	serve(s, http.MethodPost, "/streaming/account/stick@local/source", pandoraAddSourceBody, "127.0.0.1:4000")
	full := serve(s, http.MethodGet, "/streaming/account/stick@local/full", "", "127.0.0.1:4000").Body.String()
	if !strings.Contains(full, "secret-token-value") {
		t.Fatalf("the speaker's own /full lost the credential the handler was sent:\n%s", full)
	}
}

func TestMaskElementText(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<pass>x</pass>`, `<pass>***</pass>`},
		{`<pass></pass>`, `<pass></pass>`},
		{`<password>keep</password><passive>keep</passive>`, `<password>keep</password><passive>keep</passive>`},
		{`<a><pass>1</pass><pass>2</pass></a>`, `<a><pass>***</pass><pass>***</pass></a>`},
		{`<pass>unterminated`, `<pass>***`},
	}
	for _, c := range cases {
		if got := maskElementText(c.in, "pass"); got != c.want {
			t.Errorf("maskElementText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskSpyHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "secret")
	h.Set("Cookie", "")
	h.Set("Content-Type", "application/xml")
	got := maskSpyHeaders(h)
	if got.Get("Authorization") != "***" || got.Get("Content-Type") != "application/xml" {
		t.Fatalf("headers = %v", got)
	}
	if v, ok := got["Cookie"]; !ok || len(v) != 1 || v[0] != "" {
		t.Fatalf("empty Cookie changed: %v", got["Cookie"])
	}
	if h.Get("Authorization") != "secret" {
		t.Fatal("the request's own headers were modified")
	}
}
