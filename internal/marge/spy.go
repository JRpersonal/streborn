// Spy: request recording middleware and the diagnostic spy-log endpoints.

package marge

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// spyMiddleware logs every incoming request before it is passed on to the
// actual handler. The body is buffered so it can be both logged
// and read by the handler.
func (s *Server) spyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Copy the body so downstream can read it.
		var bodyCopy []byte
		if r.Body != nil {
			buf, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
			if err == nil {
				bodyCopy = buf
				r.Body = io.NopCloser(bytes.NewReader(buf))
			}
		}

		entry := SpyEntry{
			When:    time.Now(),
			Method:  r.Method,
			Host:    r.Host,
			Path:    r.URL.RequestURI(),
			Headers: r.Header.Clone(),
			Body:    string(bodyCopy),
		}
		// A Pandora or iHeartRadio registration can carry the account
		// credential. The spy log (/__spy/log) prints bodies, so the recorded
		// copy is masked; the handler below still reads the original.
		nativeType := nativeRequestType(r.URL.Path, entry.Body)
		if nativeType != "" {
			entry.Body = maskCredentials(entry.Body)
		}
		s.recordSpy(entry)
		// The firmware's TuneIn client is believed dead on 27.0.6 (no request
		// to its partner host has ever been seen), so any request to that host
		// or to the TuneIn BMX adapter is news worth an INFO line: it is the
		// answer to whether the box fetches station titles itself (#500). It
		// never fires in normal operation, so it cannot flood the log.
		if isTuneInRequest(r) || strings.HasPrefix(r.URL.Path, "/bmx/tunein") {
			s.logger.Info("marge: TuneIn request from the box",
				slog.String("method", entry.Method),
				slog.String("host", entry.Host),
				slog.String("path", entry.Path),
				slog.Int("bodyBytes", len(bodyCopy)),
				slog.String("ua", r.UserAgent()))
		}

		// The firmware's own US services (usservices.go): every stand-in
		// request about Pandora or iHeartRadio is logged, so a US tester's
		// bundle shows whether the firmware registered the source at all.
		// Path and size only, never the body: it can carry the account
		// credential.
		if nativeType != "" {
			s.logger.Info("marge: US service request from the box",
				slog.String("service", nativeType),
				slog.String("method", entry.Method),
				slog.String("path", entry.Path),
				slog.Int("bodyBytes", len(bodyCopy)))
		}

		// At debug level so the periodic Bose Lisa polls (every few min)
		// do not flood the log. On errors INFO/WARN is logged in the
		// handler.
		s.logger.Debug("marge request",
			slog.String("method", entry.Method),
			slog.String("path", entry.Path),
			slog.Int("bodyBytes", len(bodyCopy)),
			slog.String("ua", r.UserAgent()),
			slog.String("contentType", r.Header.Get("Content-Type")),
		)

		next.ServeHTTP(w, r)
	})
}

// recordSpy stores an entry in the ring buffer.
func (s *Server) recordSpy(e SpyEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestLog = append(s.requestLog, e)
	if len(s.requestLog) > s.requestLogMax {
		s.requestLog = s.requestLog[len(s.requestLog)-s.requestLogMax:]
	}
}

// RecentRequests returns a copy of the most recently seen requests.
func (s *Server) RecentRequests() []SpyEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SpyEntry, len(s.requestLog))
	copy(out, s.requestLog)
	return out
}

// RecentRequestLines renders the newest n spy entries as compact one-line
// strings (millisecond timestamps) for the diagnostic bundle. The trail is what
// lets a bundle answer "did the box talk to marge inside THIS 200 ms window?"
// - the question the Wave sysLanguage revert investigation hangs on.
func (s *Server) RecentRequestLines(n int) []string {
	entries := s.RecentRequests()
	if n > 0 && len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		line := fmt.Sprintf("%s %s %s bodyBytes=%d",
			e.When.Format("2006-01-02T15:04:05.000Z07:00"), e.Method, e.Path, len(e.Body))
		if e.Host != "" {
			line += " host=" + e.Host
		}
		out = append(out, line)
	}
	return out
}

// handleHealthz is the standard probe endpoint.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleSpyLog returns the request log as plain text.
// Intended for debug purposes only, do not expose in production.
func (s *Server) handleSpyLog(w http.ResponseWriter, _ *http.Request) {
	entries := s.RecentRequests()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, e := range entries {
		fmt.Fprintf(w, "%s  %s %s\n", e.When.Format(time.RFC3339), e.Method, e.Path)
		for k, vs := range e.Headers {
			for _, v := range vs {
				fmt.Fprintf(w, "  %s: %s\n", k, v)
			}
		}
		if e.Body != "" {
			fmt.Fprintf(w, "  ---\n  %s\n", strings.ReplaceAll(e.Body, "\n", "\n  "))
		}
		fmt.Fprintln(w, "----------------------------------------")
	}
}

// --- BMX registry fetch counter ---------------------------------------------
//
// The registry response carries the baseUrl the box resolves a native radio
// preset's relative location against. A box that never fetches it cannot
// resolve a preset press, and the failure surfaces far away from the cause:
// the speaker reports a JSON parse error and drops to INVALID_SOURCE, which
// reads like a broken station (#600). One counter and one timestamp turn that
// into a fact a bundle states outright.

// noteRegistryFetch records that the box asked for the BMX service list.
func (s *Server) noteRegistryFetch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registryFetches++
	s.lastRegistryFetch = time.Now()
}

// RegistryFetches reports how often the box has asked for the BMX service list
// since this agent started, and when it last did. count 0 means never.
func (s *Server) RegistryFetches() (count int, last time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.registryFetches, s.lastRegistryFetch
}
