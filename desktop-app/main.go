package main

import (
	"embed"
	"net"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// The embed below requires frontend/dist to exist at build time.
// An empty frontend/dist/.gitkeep is tracked so a fresh clone can
// run `go build` even without `wails build` first; the real Vite
// output replaces it during the desktop-app build pipeline.
//
//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Last-resort crash trace. A panic that escapes wails.Run (or
	// happens during app construction) would otherwise vanish in a
	// production build with no console. Record it to str.log, then
	// re-panic so the OS still files its own crash report
	// (e.g. macOS DiagnosticReports).
	defer func() {
		if r := recover(); r != nil {
			logCrash("main", r)
			panic(r)
		}
	}()

	// Force Go's pure-Go DNS resolver instead of the cgo one. On macOS the
	// default cgo resolver (getaddrinfo) crashed the app a few seconds after
	// launch when the startup update check resolved st-reborn.de, the app's
	// only external host (reported: crash gone with the update check
	// disabled, and it leaves no Go panic trace because a cgo SIGSEGV is not
	// recoverable). The pure-Go resolver avoids that native code path. It
	// resolves public names like st-reborn.de fine on all three platforms.
	net.DefaultResolver.PreferGo = true

	// Create an instance of the app structure
	app := NewApp()

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "ST Reborn " + appVersion,
		Width:  1100,
		Height: 780,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		// Single-instance guard. Two running app instances would each poll
		// the speaker, doubling (or worse) the request rate the Bose
		// firmware app already struggles with. The UniqueId is a FIXED
		// string with no version or build stamp in it on purpose: the lock
		// is an OS-level named mutex (Windows) / lock file (mac, Linux)
		// keyed on this id, so it must collide even when the two instances
		// are different builds (e.g. an old copy still open while the user
		// launches a freshly updated one). When a second instance starts it
		// exits immediately and hands its launch over to the first, which
		// just raises and focuses its existing window.
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "de.st-reborn.desktop.singleinstance",
			OnSecondInstanceLaunch: func(data options.SecondInstanceData) {
				if app.ctx == nil {
					return
				}
				// If the user double-clicked a freshly downloaded NEWER build while
				// this older one is running, hand off to it (quit + start the new
				// one) instead of just raising this old window, which would leave
				// them stuck on the old version (#71 follow-up). Only triggers for a
				// different file whose filename version is strictly newer.
				if app.tryHandOffTo(resolveSecondInstanceExe(data.Args, data.WorkingDirectory)) {
					return
				}
				runtime.WindowUnminimise(app.ctx)
				runtime.Show(app.ctx)
				// Brief always-on-top pulse to force the window to the
				// foreground across platforms, then release it so it does
				// not stay pinned above everything else.
				runtime.WindowSetAlwaysOnTop(app.ctx, true)
				runtime.WindowSetAlwaysOnTop(app.ctx, false)
			},
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
