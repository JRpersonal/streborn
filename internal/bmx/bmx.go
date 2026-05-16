// Package bmx emuliert den Bose BMX Broker Server (content.api.bose.io).
// BMX löst TuneIn Station IDs in echte Stream URLs auf. Diese Emulation
// nutzt die offene TuneIn API und die Radio Browser API als Backend.
package bmx

import (
	"log/slog"
	"net/http"
)

// Resolver löst Station IDs in Stream URLs auf.
type Resolver struct {
	logger *slog.Logger
}

// New erstellt einen neuen BMX Resolver.
func New(logger *slog.Logger) *Resolver {
	return &Resolver{logger: logger}
}

// Handler liefert den HTTP Handler für die BMX Endpunkte.
func (r *Resolver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// Resolver Endpunkte folgen in einer späteren Aufgabe.
	return mux
}
