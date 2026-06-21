// Package bmx emulates the Bose BMX broker server (content.api.bose.io).
// BMX resolves TuneIn station IDs into real stream URLs. This emulation
// uses the open TuneIn API and the Radio Browser API as backend.
package bmx

import (
	"log/slog"
	"net/http"
)

// Resolver resolves station IDs into stream URLs.
type Resolver struct {
	logger *slog.Logger
}

// New creates a new BMX resolver.
func New(logger *slog.Logger) *Resolver {
	return &Resolver{logger: logger}
}

// Handler returns the HTTP handler for the BMX endpoints.
func (r *Resolver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// Resolver endpoints follow in a later task.
	return mux
}
