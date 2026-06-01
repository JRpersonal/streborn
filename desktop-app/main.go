package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
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

	// Create an instance of the app structure
	app := NewApp()

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "ST Reborn",
		Width:  1100,
		Height: 780,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
