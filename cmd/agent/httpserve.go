// HTTP and HTTPS listener bring-up with readiness checks and the
// filtered http.Server error log.

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/netutil"
)

// httpErrorLogWriter bridges the stdlib http.Server ErrorLog into slog. The
// Bose firmware opens a TCP connection to the redirected streaming.bose.com TLS
// port (our marge-tls listener) about once a minute and closes it without ever
// sending a ClientHello, so net/http logged `http: TLS handshake error from
// <box>: EOF` to the process default logger every 60s. On the speaker that
// default logger is teed to the NAND log, so the benign probe churned ~1440
// NAND writes a day for no diagnostic value (seen across every box in #185 /
// #187 bundles). Route that handshake noise to DEBUG (dropped at the default
// info level) while keeping any genuine server error (e.g. a recovered handler
// panic, which also reaches ErrorLog) at WARN so it still lands in a bundle.
type httpErrorLogWriter struct {
	logger *slog.Logger
	name   string
}

func (w httpErrorLogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	if msg == "" {
		return len(p), nil
	}
	if strings.Contains(msg, "TLS handshake error") && !tlsHandshakeIsRejection(msg) {
		w.logger.Debug("http server error", "comp", w.name, "msg", msg)
	} else {
		w.logger.Warn("http server error", "comp", w.name, "msg", msg)
	}
	return len(p), nil
}

// tlsHandshakeIsRejection separates the box actively REFUSING our certificate
// from the benign once-a-minute empty probe (EOF before any ClientHello). A
// refusal reaches the alert/certificate stage ("remote error: tls: bad
// certificate", "unknown certificate authority", "expired certificate") and is
// the on-box evidence for two known field states: a plug-pull boot whose 2015
// clock makes every cert invalid (#419 Finding 4) and a stale root CA after a
// bundle regen. Demoting these to DEBUG with the probe noise hid exactly that
// evidence from bundles.
func tlsHandshakeIsRejection(msg string) bool {
	for _, marker := range []string{"remote error", "alert", "certificate", "expired"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// newHTTPErrorLog returns the *log.Logger to wire into http.Server.ErrorLog so
// per-connection noise does not bypass slog and hit the NAND log directly.
func newHTTPErrorLog(logger *slog.Logger, name string) *log.Logger {
	return log.New(httpErrorLogWriter{logger: logger, name: name}, "", 0)
}

// startHTTP starts an HTTP server in a goroutine and reports errors to errs.
//
// The listener is opened via netutil.ListenTCP, which sets SO_REUSEADDR on
// the socket. Without that, a watchdog-driven respawn while the previous
// listener is still in TIME_WAIT fails with "address already in use".
//
// Phase-marker logs are at WARN level on purpose: visible on any
// --log-level setting and in the diagnostic bundle's tail capture.
// waitListenerReady blocks until a plain TCP dial to the loopback side of addr
// succeeds (proving the local listener accepts), ctx is cancelled, or timeout
// elapses. Used to hold the /etc/hosts redirect until marge is actually up. A
// TLS listener still accepts the TCP connection, so this works for :443 too.
func waitListenerReady(ctx context.Context, addr string, timeout time.Duration, logger *slog.Logger) {
	var port string
	if _, p, err := net.SplitHostPort(addr); err == nil {
		port = p
	} else {
		port = strings.TrimPrefix(addr, ":")
	}
	target := net.JoinHostPort("127.0.0.1", port)
	deadline := time.Now().Add(timeout)
	for {
		d := net.Dialer{Timeout: time.Second}
		if c, err := d.DialContext(ctx, "tcp", target); err == nil {
			_ = c.Close()
			return
		}
		if ctx.Err() != nil {
			return
		}
		if time.Now().After(deadline) {
			if logger != nil {
				logger.Warn("marge endpoint not ready before timeout, applying hosts redirect anyway", "endpoint", target)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func startHTTP(ctx context.Context, wg *sync.WaitGroup, errs chan<- error, name, addr string, handler http.Handler, logger *slog.Logger) {
	logger.Warn("listener phase: spawn", "comp", name, "addr", addr)
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv := &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			ErrorLog:          newHTTPErrorLog(logger, name),
		}
		logger.Warn("listener phase: calling ListenTCP", "comp", name, "addr", addr)
		ln, err := netutil.ListenTCP(ctx, addr)
		if err != nil {
			logger.Error("listener phase: ListenTCP failed", "comp", name, "addr", addr, "err", err)
			errs <- fmt.Errorf("%s: listen %s: %w", name, addr, err)
			return
		}
		logger.Warn("listener phase: ListenTCP succeeded", "comp", name, "addr", addr, "local", ln.Addr().String())
		serveErr := make(chan error, 1)
		go func() {
			serveErr <- srv.Serve(ln)
		}()
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		case err := <-serveErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("%s: %w", name, err)
			}
		}
	}()
}

// startHTTPS starts an HTTPS server analogous to startHTTP, with the
// supplied TLS configuration.
func startHTTPS(ctx context.Context, wg *sync.WaitGroup, errs chan<- error, name, addr string, handler http.Handler, tlsConfig *tls.Config, logger *slog.Logger) {
	logger.Warn("listener phase: spawn TLS", "comp", name, "addr", addr)
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv := &http.Server{
			Addr:              addr,
			Handler:           handler,
			TLSConfig:         tlsConfig,
			ReadHeaderTimeout: 10 * time.Second,
			ErrorLog:          newHTTPErrorLog(logger, name),
		}
		logger.Warn("listener phase: calling ListenTCP TLS", "comp", name, "addr", addr)
		ln, err := netutil.ListenTCP(ctx, addr)
		if err != nil {
			logger.Error("listener phase: ListenTCP TLS failed", "comp", name, "addr", addr, "err", err)
			errs <- fmt.Errorf("%s: listen %s: %w", name, addr, err)
			return
		}
		logger.Warn("listener phase: ListenTCP TLS succeeded", "comp", name, "addr", addr, "local", ln.Addr().String())
		// ServeTLS upgrades the listener with the supplied TLSConfig.
		// We pass empty paths since the cert is in TLSConfig.Certificates.
		serveErr := make(chan error, 1)
		go func() {
			serveErr <- srv.ServeTLS(ln, "", "")
		}()
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		case err := <-serveErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("%s: %w", name, err)
			}
		}
	}()
}
