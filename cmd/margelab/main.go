// Command margelab runs STR's marge cloud stub on a DEVELOPER MACHINE so a
// real speaker can be pointed at it and response shapes can be swept from the
// console, without rebuilding and redeploying the on-box agent for every try.
//
// Why this exists: finding the XML shape the firmware accepts (the addDevice
// handshake, the account source list, ...) is a search, and each on-box
// iteration costs an agent build, an OTA and a box reboot. Pointed at this
// harness, the same search takes a process restart.
//
// Use (developer machines only, never on a user's speaker):
//
//	go run ./cmd/margelab -listen :9080
//	# then, over the box's TAP shell on :17000:
//	#   sys configuration margeServerUrl http://<dev-ip>:9080
//	#   sys reboot
//	# restore afterwards with:
//	#   sys configuration margeServerUrl https://streaming.bose.com
//
// Every request is logged with method, path and body so the box's side of the
// conversation is visible live.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/marge"
)

func main() {
	addr := flag.String("listen", ":9080", "listen address for the marge stub")
	verbose := flag.Bool("bodies", true, "log request bodies")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv := marge.New(logger, marge.WithSpyLogSize(500))

	h := srv.Handler()
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body string
		if *verbose && r.Body != nil {
			b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			body = strings.TrimSpace(string(b))
			r.Body = io.NopCloser(strings.NewReader(body))
		}
		fmt.Printf("\n>>> %s %s  from=%s\n", r.Method, r.URL.RequestURI(), r.RemoteAddr)
		if body != "" {
			fmt.Printf("    body: %s\n", body)
		}
		h.ServeHTTP(w, r)
	})

	fmt.Printf("margelab listening on %s (%s)\n", *addr, time.Now().Format(time.Kitchen))
	fmt.Println("point a speaker at it:  sys configuration margeServerUrl http://<dev-ip>" + *addr)
	if err := http.ListenAndServe(*addr, logged); err != nil {
		fmt.Fprintln(os.Stderr, "margelab:", err)
		os.Exit(1)
	}
}
