// Command streborn ist der Agent, der direkt auf der Bose SoundTouch
// Box läuft und die Bose Cloud Endpunkte lokal emuliert.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/JRpersonal/streborn/internal/autopair"
	"github.com/JRpersonal/streborn/internal/bmx"
	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/boxws"
	"github.com/JRpersonal/streborn/discovery"
	"github.com/JRpersonal/streborn/internal/hosts"
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/shepherd"
	"github.com/JRpersonal/streborn/internal/streamproxy"
	"github.com/JRpersonal/streborn/internal/sysinfo"
	"github.com/JRpersonal/streborn/internal/tlsgen"
	"github.com/JRpersonal/streborn/internal/upnp"
	"github.com/JRpersonal/streborn/internal/webui"
)

// version ist die Semver Version. Build Datum wird separat ueber -ldflags
// gesetzt damit man "1.0.0" anzeigen kann und das Build Datum trotzdem
// verfuegbar ist.
var (
	version    = "1.0.0"
	buildStamp = "dev"
)

func init() {
	webui.SetAgentVersion(version)
	webui.SetAgentBuild(buildStamp)
}

func main() {
	// Subcommands vor flag.Parse() abhandeln, damit ihre eigenen Flags
	// nicht vom globalen flag Set verschluckt werden.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "shepherd":
			if err := runShepherdCmd(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "fehler:", err)
				os.Exit(1)
			}
			return
		}
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fehler:", err)
		os.Exit(1)
	}
}

// runShepherdCmd haendlet das shepherd Subcommand.
// Aufrufe:
//   streborn shepherd install   -- /mnt/nv/shepherd aufsetzen
//   streborn shepherd remove    -- /mnt/nv/shepherd entfernen
//   streborn shepherd status    -- aktuellen Stand zeigen
func runShepherdCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("verwendung: shepherd {install|remove|status}")
	}

	fs := flag.NewFlagSet("shepherd", flag.ContinueOnError)
	shepherdDir := fs.String("dir", shepherd.DefaultShepherdDir, "Shepherd Override Verzeichnis")
	boseDir := fs.String("bose-config", shepherd.DefaultBoseConfigDir, "Bose Config Verzeichnis")
	bin := fs.String("binary", shepherd.DefaultStickBin, "Pfad zum Agent Binary")
	presetsPath := fs.String("presets", shepherd.DefaultPresetsPath, "Pfad zur presets.json")

	cmd := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	logger := newLogger("info")
	mgr := shepherd.New(shepherd.Config{
		ShepherdDir:   *shepherdDir,
		BoseConfigDir: *boseDir,
		AgentBinary:   *bin,
		PresetsPath:   *presetsPath,
	}, logger)

	switch cmd {
	case "install":
		return mgr.Install()
	case "remove", "uninstall":
		return mgr.Uninstall()
	case "status":
		st, err := mgr.Check()
		if err != nil {
			return err
		}
		fmt.Printf("ShepherdDir   : %s\n", *shepherdDir)
		fmt.Printf("DirExists     : %v\n", st.DirExists)
		fmt.Printf("HasOwnConfig  : %v\n", st.HasOwnConfig)
		fmt.Printf("Missing       : %v\n", st.MissingSymlinks)
		fmt.Printf("Broken        : %v\n", st.BrokenSymlinks)
		fmt.Printf("Healthy       : %v\n", st.IsHealthy())
		return nil
	default:
		return fmt.Errorf("unbekanntes Subcommand: %s", cmd)
	}
}

func run() error {
	var (
		presetsPath    = flag.String("presets", "/media/sda1/presets.json", "Pfad zur presets.json auf dem USB Stick")
		webuiAddr      = flag.String("listen-webui", ":8888", "Adresse für das Config Web UI")
		margeAddr      = flag.String("listen-marge", ":80", "Adresse für die Marge Emulation HTTP (streaming.bose.com)")
		margeTLSAddr   = flag.String("listen-marge-tls", ":8443", "Adresse für die Marge Emulation HTTPS")
		bmxAddr        = flag.String("listen-bmx", ":81", "Adresse für die BMX Emulation HTTP (content.api.bose.io)")
		hostsPath      = flag.String("hosts", "/etc/hosts", "Pfad zur hosts Datei")
		applyHosts     = flag.Bool("apply-hosts", true, "Hosts Datei beim Start manipulieren und beim Stop wiederherstellen")
		tlsDir         = flag.String("tls-dir", tlsgen.DefaultCADir, "Verzeichnis fuer CA und Server Cert")
		tlsEnabled     = flag.Bool("tls", true, "TLS Termination aktivieren auf listen-marge-tls")
		logLevel       = flag.String("log-level", "info", "Log Level: debug, info, warn, error")
		boxHost        = flag.String("box-host", "127.0.0.1", "Bose Box IP fuer UPnP Calls (Webui /api/play). 127.0.0.1 wenn Agent auf Box laeuft, sonst LAN IP.")
		regionFile     = flag.String("region-file", "", "Pfad zur region.txt mit ISO Country Code (vom Setup Wizard). Default Radio Land und Sprache leiten wir daraus ab.")
		pendingNameFile = flag.String("pending-name-file", "", "Pfad zur name.txt vom Setup Wizard. Inhalt wird einmalig als Box Name angewendet (plus UID Suffix) und die Datei danach geloescht.")
		printVersion   = flag.Bool("version", false, "Version ausgeben und beenden")
	)
	flag.Parse()

	if *printVersion {
		fmt.Println(version)
		return nil
	}

	logger := newLogger(*logLevel)
	logger.Info("streborn startet", "version", version)

	// DeviceID aus MAC ermitteln, damit Marge Antworten die echte Box ID
	// zurueckgeben. Wenn keine MAC gefunden wird, weiter mit leerer ID.
	deviceID, err := sysinfo.DeviceID(nil)
	if err != nil {
		logger.Warn("DeviceID konnte nicht ermittelt werden", "err", err)
		deviceID = ""
	} else {
		logger.Info("DeviceID erkannt", "deviceID", deviceID)
	}

	// Presets laden. Bei Fehler nicht crashen sondern mit leerer Liste weitermachen,
	// damit der Agent zumindest auf seinen Listenern lebt und korrigierbar bleibt.
	store, err := presets.Load(*presetsPath)
	if err != nil {
		logger.Warn("Presets nicht ladbar, weiter mit leerer Liste", "err", err, "datei", *presetsPath)
		store = presets.New()
	} else {
		logger.Info("Presets geladen", "anzahl", len(store.All()), "datei", *presetsPath)
	}

	// Hosts Datei manipulieren
	var hostsMgr *hosts.Manager
	if *applyHosts {
		hostsMgr = hosts.New(*hostsPath, logger)
		if err := hostsMgr.Apply(hosts.DefaultEntries()); err != nil {
			logger.Warn("hosts Datei konnte nicht angepasst werden", "err", err)
			hostsMgr = nil
		}
	}

	// Subsysteme initialisieren
	margeSrv := marge.New(logger.With("comp", "marge"),
		marge.WithDeviceID(deviceID))
	bmxSrv := bmx.New(logger.With("comp", "bmx"))
	// AutoPair Manager wird oben angelegt damit er auch im WS und Webui
	// Handler genutzt werden kann.
	autoPair := autopair.New(logger.With("comp", "autopair"), autopair.Config{
		BoxHost: *boxHost,
	})

	// Initial Preset Sync zur Box im Hintergrund. Box muss alle Presets
	// als UPNP ContentItems kennen damit Hardware Tasten den
	// nowSelectionUpdated WebSocket Event mit Location ausloesen koennen.
	// Plus periodischer Reconciler (alle 5 min) damit Inkonsistenzen
	// die durch Box Reboot oder Bose State Resets entstehen, automatisch
	// geheilt werden — der User braucht den "Hardware Tasten reparieren"
	// Button im Normalfall nie zu druecken.
	go initialBoxPresetSync(store, *boxHost, logger)
	go periodicPresetReconcile(store, *boxHost, logger)

	// Region beim Start aus Datei lesen (vom Setup Wizard provisioniert).
	region := loadRegion(*regionFile, logger)

	// Stream Proxy macht Bose ContentItems gegen Token Expiry resistent:
	// statt der echten CDN URL bekommt Bose http://127.0.0.1:8888/stream/<slot>
	// und der Stick Agent reconnectet intern bei Drops.
	streamProxySrv := streamproxy.New(store, logger.With("comp", "streamproxy"))

	webuiSrv := webui.New(*webuiAddr, logger.With("comp", "webui"),
		webui.WithPresets(store),
		webui.WithBoxHost(*boxHost),
		webui.WithAutoPair(autoPair),
		webui.WithRegion(region),
		webui.WithRegionFile(*regionFile),
		webui.WithStreamProxy(streamProxySrv))

	// Box model — ask the local Bose firmware which actual model
	// it is (SoundTouch 10 vs 20 vs 30 vs Portable). Falls back to
	// the generic "SoundTouch" if /info is not yet answering on a
	// cold boot. Used by the desktop app to disambiguate multiple
	// boxes on the same LAN.
	model := "SoundTouch"
	{
		infoCtx, infoCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if settings, err := boxapi.New(*boxHost).LoadSettings(infoCtx); err == nil && settings.Info.Type != "" {
			model = settings.Info.Type
			logger.Info("Box Modell erkannt", "type", model)
		} else if err != nil {
			logger.Debug("Box /info noch nicht erreichbar, nutze Fallback Model", "err", err)
		}
		infoCancel()
	}

	// mDNS Announce damit die Desktop App den Stick im LAN findet.
	// Bei mehreren Boxen im Netz ist jeder Stick eine eigene Instance,
	// die Desktop App listet alle.
	mdnsAnnouncer, mdnsErr := discovery.Announce(
		logger.With("comp", "discovery"),
		discovery.Config{
			Port:         8888,
			DeviceID:     deviceID,
			FriendlyName: "Bose SoundTouch " + lastN(deviceID, 6),
			Model:        model,
			Version:      version,
			Build:        buildStamp,
		},
	)
	if mdnsErr != nil {
		logger.Warn("mDNS Announce fehlgeschlagen, weiter ohne", "err", mdnsErr)
	}

	// Box Name dynamisch in mDNS halten: regelmaessig info.name pollen
	// und bei Aenderung den Announce TXT Record refreshen. Damit bekommt
	// die Desktop App den neuen Namen sofort mit ohne Box Neustart.
	if mdnsAnnouncer != nil {
		go pollBoxName(context.Background(), *boxHost, mdnsAnnouncer, logger)
	}

	if *pendingNameFile != "" {
		go applyPendingBoxName(context.Background(), *boxHost, *pendingNameFile, deviceID, logger)
	}

	// Wenn der USB Stick ein neueres run.sh hat als das NAND
	// run-override.sh: kopieren. Das ist der Selbst-Update-Pfad fuer
	// den Bootstrap. Ohne das laeuft die alte run-override.sh aus dem
	// allerersten Setup auf ewig und neue Setup Wizard Configs werden
	// ignoriert.
	go syncRunOverrideFromStick(logger)

	// Hardware Preset Tasten: Box sendet via WebSocket auf 8080 (gabbo Protocol)
	// einen presetSelectionUpdated event wenn der User physisch eine Taste
	// drueckt. Wir hooken den Event und triggern unseren UPnP Player.
	renderer := upnp.NewBoseRenderer(*boxHost)
	wsHandler := &presetWsHandler{
		logger:   logger.With("comp", "boxws"),
		store:    store,
		renderer: renderer,
		autoPair: autoPair,
		boxHost:  *boxHost,
	}
	wsClient := boxws.New(
		logger.With("comp", "boxws"),
		fmt.Sprintf("ws://%s:8080/", *boxHost),
		wsHandler,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 3)

	startHTTP(ctx, &wg, errs, "marge", *margeAddr, margeSrv.Handler(), logger)
	startHTTP(ctx, &wg, errs, "bmx", *bmxAddr, bmxSrv.Handler(), logger)

	// TLS Termination fuer Marge auf 8443. iptables redirected die echte
	// Box Anfrage von 443 dorthin. Wenn TLS deaktiviert wird, ueberspringen.
	if *tlsEnabled {
		tlsMgr := tlsgen.New(*tlsDir, nil, logger.With("comp", "tlsgen"))
		bundle, err := tlsMgr.EnsureBundle()
		if err != nil {
			logger.Error("TLS Bundle nicht verfuegbar, fahre ohne TLS Listener fort", "err", err)
		} else {
			cert, err := bundle.TLSCert()
			if err != nil {
				logger.Error("TLS Cert nicht ladbar, fahre ohne TLS Listener fort", "err", err)
			} else {
				tlsConfig := &tls.Config{
					Certificates: []tls.Certificate{cert},
					MinVersion:   tls.VersionTLS12,
				}
				startHTTPS(ctx, &wg, errs, "marge-tls", *margeTLSAddr,
					margeSrv.Handler(), tlsConfig, logger)
			}
		}
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := webuiSrv.Run(ctx); err != nil {
			errs <- fmt.Errorf("webui: %w", err)
		}
	}()

	// Box WebSocket Listener fuer Hardware Preset Tasten
	wg.Add(1)
	go func() {
		defer wg.Done()
		wsClient.Run(ctx)
	}()

	// Auto Pair Background: pairt die Box automatisch beim Start. Re-pairt
	// alle 5 Minuten falls die Box mal verloren geht. Plus: WS Handler
	// triggert TriggerNow bei Preset Press damit Pair sofort kommt nach
	// Standby Aufwachen.
	wg.Add(1)
	go func() {
		defer wg.Done()
		autoPair.RunBackground(ctx, 8*time.Second, 5*time.Minute)
	}()

	logger.Info("alle Subsysteme gestartet")

	var firstErr error
	select {
	case <-ctx.Done():
		logger.Info("Shutdown Signal empfangen")
	case err := <-errs:
		firstErr = err
		logger.Error("Subsystem Fehler, fahre herunter", "err", err)
		cancel()
	}

	wg.Wait()

	if mdnsAnnouncer != nil {
		mdnsAnnouncer.Close()
	}

	if hostsMgr != nil {
		if err := hostsMgr.Restore(); err != nil {
			logger.Warn("hosts Datei wiederherstellen fehlgeschlagen", "err", err)
		}
	}

	logger.Info("streborn beendet")
	return firstErr
}

// startHTTP startet einen HTTP Server in einer Goroutine und meldet Fehler an errs.
func startHTTP(ctx context.Context, wg *sync.WaitGroup, errs chan<- error, name, addr string, handler http.Handler, logger *slog.Logger) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv := &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		}
		serveErr := make(chan error, 1)
		go func() {
			logger.Info("HTTP Server startet", "comp", name, "addr", addr)
			serveErr <- srv.ListenAndServe()
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

// startHTTPS startet einen HTTPS Server analog zu startHTTP, mit der
// uebergebenen TLS Konfiguration.
func startHTTPS(ctx context.Context, wg *sync.WaitGroup, errs chan<- error, name, addr string, handler http.Handler, tlsConfig *tls.Config, logger *slog.Logger) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv := &http.Server{
			Addr:              addr,
			Handler:           handler,
			TLSConfig:         tlsConfig,
			ReadHeaderTimeout: 10 * time.Second,
		}
		serveErr := make(chan error, 1)
		go func() {
			logger.Info("HTTPS Server startet", "comp", name, "addr", addr)
			// ListenAndServeTLS mit leeren Paths, Cert kommt aus TLSConfig
			serveErr <- srv.ListenAndServeTLS("", "")
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

// presetWsHandler implementiert boxws.Handler und ruft bei Hardware Preset
// Tasten den UPnP Renderer mit der Stream URL aus dem Preset Store auf.
type presetWsHandler struct {
	logger   *slog.Logger
	store    *presets.Store
	renderer *upnp.Renderer
	autoPair *autopair.Manager
	boxHost  string
}

func (h *presetWsHandler) OnPresetSelected(ctx context.Context, slot int, location, title string) {
	// URL bleibt die Proxy URL (location = http://127.0.0.1:8888/stream/N)
	// damit der Stream Proxy den Reconnect bei Token Expiry uebernimmt.
	// Name + Icon kommen aus dem Stick Preset Store — die Bose ContentItem
	// Metadata hat keinen Art Eintrag, daher muessen wir die Album Art
	// URL aktiv ueber unser PlayURL Aufruf ins DIDL Lite Metadata
	// reinpacken, sonst zeigt das Display (ST20/30) kein Logo.
	url := location
	name := title
	icon := ""
	if p, ok := h.store.Get(slot); ok {
		if p.Name != "" {
			name = p.Name
		}
		icon = p.Art
	}
	if url == "" {
		h.logger.Info("hardware preset gedrueckt, kein Mapping", "slot", slot)
		return
	}

	// Box aufwecken aus Standby + Pair sicherstellen.
	if h.boxHost != "" {
		wakeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		if err := boxcli.WakeAndWait(wakeCtx, h.boxHost, 6*time.Second); err != nil {
			h.logger.Warn("Box konnte nicht aus STANDBY geholt werden", "err", err)
		}
		cancel()
	}
	if h.autoPair != nil {
		pairCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		h.autoPair.TriggerNow(pairCtx)
		cancel()
	}

	playCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := h.renderer.PlayURL(playCtx, url, name, icon); err != nil {
		h.logger.Error("upnp play fehlgeschlagen", "slot", slot, "err", err)
		return
	}
	h.logger.Info("hardware preset zu upnp gemapped", "slot", slot, "name", name)
}

// loadRegion liest den Country Code aus der region.txt vom Stick. Leer
// wenn die Datei nicht existiert oder leer ist; in dem Fall faellt die
// App spaeter auf Browser/User Default zurueck.
func loadRegion(path string, logger *slog.Logger) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		logger.Debug("region file nicht lesbar", "path", path, "err", err)
		return ""
	}
	cc := ""
	for _, r := range string(b) {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			cc += string(r)
		}
	}
	if len(cc) < 2 {
		return ""
	}
	cc = cc[:2]
	// Uppercase
	out := ""
	for _, r := range cc {
		if r >= 'a' && r <= 'z' {
			r = r - 32
		}
		out += string(r)
	}
	logger.Info("Region geladen", "country", out)
	return out
}

// syncRunOverrideFromStick haelt das NAND run-override.sh aktuell mit
// dem run.sh auf dem Stick. Wichtig: rc.local priorisiert NAND vor
// Stick, daher wuerde ein veraltetes NAND Script die neuen Setup
// Wizard Features (name.conf, region.conf etc) ignorieren.
//
// Wenn die Files identisch sind: no-op (keine Flash Writes).
func syncRunOverrideFromStick(logger *slog.Logger) {
	const stickPath = "/media/sda1/run.sh"
	const nandPath = "/mnt/nv/streborn/run-override.sh"

	time.Sleep(5 * time.Second) // dem Stick Zeit zum mounten geben

	stickData, err := os.ReadFile(stickPath)
	if err != nil {
		logger.Debug("run.sh vom Stick nicht lesbar, kein Sync", "err", err)
		return
	}
	nandData, _ := os.ReadFile(nandPath)
	if len(nandData) > 0 && bytesEqual(stickData, nandData) {
		return // schon identisch
	}
	tmp := nandPath + ".new"
	if err := os.WriteFile(tmp, stickData, 0o755); err != nil {
		logger.Warn("run-override.sh Sync schreiben fehlgeschlagen", "err", err)
		return
	}
	if err := os.Rename(tmp, nandPath); err != nil {
		logger.Warn("run-override.sh Sync rename fehlgeschlagen", "err", err)
		os.Remove(tmp)
		return
	}
	logger.Info("run-override.sh vom Stick auf NAND aktualisiert", "bytes", len(stickData))
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// applyPendingBoxName wendet einen vom Setup Wizard hinterlegten Box
// Namen einmalig auf die Bose Box an und haengt die letzten 4 Hex der
// DeviceID als UID Suffix an damit Dopplungen in mehreren Boxen im LAN
// ausgeschlossen sind. Bei Erfolg wird die Datei geloescht.
func applyPendingBoxName(ctx context.Context, boxHost, path, deviceID string, logger *slog.Logger) {
	if boxHost == "" || path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		// keine Datei, nichts anzuwenden
		return
	}
	raw := strings.TrimSpace(string(b))
	if raw == "" {
		_ = os.Remove(path)
		return
	}
	suffix := ""
	if len(deviceID) >= 4 {
		suffix = strings.ToUpper(deviceID[len(deviceID)-4:])
	}
	wanted := raw
	if suffix != "" && !strings.HasSuffix(strings.ToUpper(wanted), suffix) {
		wanted = raw + " " + suffix
	}
	// Box muss erreichbar sein. Warten bis BoseApp Webserver hochgefahren.
	time.Sleep(10 * time.Second)
	c := boxapi.New(boxHost)
	for attempt := 0; attempt < 12; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.SetName(callCtx, wanted)
		cancel()
		if err == nil {
			logger.Info("Setup Wizard Box Name angewendet", "name", wanted)
			_ = os.Remove(path)
			return
		}
		logger.Debug("Box Name setzen fehlgeschlagen, retry", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	logger.Warn("Box Name aus Setup konnte nicht gesetzt werden, gebe auf", "path", path)
}

// pollBoxName fragt regelmaessig den Box Display Namen ab und aktualisiert
// den mDNS FriendlyName entsprechend. Damit kennt die Desktop App den
// neuen Namen sobald der User die Box umbenennt — ohne Box Reboot.
//
// Erster Aufruf nach kurzem Delay damit Bose Webserver hochgefahren ist.
func pollBoxName(ctx context.Context, boxHost string, ann *discovery.Announcer, logger *slog.Logger) {
	if boxHost == "" || ann == nil {
		return
	}
	time.Sleep(8 * time.Second)
	client := boxapi.New(boxHost)
	var last string
	doOne := func() {
		fetchCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		s, err := client.LoadSettings(fetchCtx)
		if err != nil {
			logger.Debug("pollBoxName fail", "err", err)
			return
		}
		name := s.Info.Name
		if name == "" || name == last {
			return
		}
		if err := ann.UpdateFriendlyName(name); err != nil {
			logger.Warn("mDNS UpdateFriendlyName fehlgeschlagen", "err", err)
			return
		}
		logger.Info("mDNS FriendlyName aktualisiert", "name", name)
		last = name
	}
	doOne()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			doOne()
		}
	}
}

// proxyStreamURL gibt die stabile loopback URL fuer ein Preset zurueck.
// Bose UPnP Player oeffnet die — der Stream Proxy im Stick Agent loest
// dahinter den echten Sender Redirect auf und reconnectet bei Token
// Expiry, ohne dass Bose etwas merkt.
func proxyStreamURL(slot int) string {
	return fmt.Sprintf("http://127.0.0.1:8888/stream/%d", slot)
}

// initialBoxPresetSync wartet auf den Box Boot und synct alle Stick
// Presets an den Box internen Preset Store. Mit Retry Loop: bei
// fehlgeschlagenen Slots wird nach 10s erneut versucht, bis zu 12 mal.
// Hintergrund: die Bose Firmware ist beim Boot manchmal noch nicht
// bereit fuer AddPreset Calls (autopair noch nicht durch, marge state
// noch nicht initialisiert). Ohne Retry blieben Slots dauerhaft ohne
// Box Eintrag — Hardware Tasten 1-6 wuerden dann nichts ausloesen.
// Initial 30 s Warten (war 12 s): in der Praxis gemessen, dass die
// Bose Firmware nach einem Cold Boot ~60 s braucht bis /info auf 8090
// antwortet und marge State steht. 12 s war optimistisch.
// 12 Retry Slots mit je 10 s Pause = ~2 Minuten gesamter Runway.
func initialBoxPresetSync(store *presets.Store, boxHost string, logger *slog.Logger) {
	time.Sleep(30 * time.Second)
	specs := make([]boxcli.PresetSpec, 0, 6)
	for _, p := range store.All() {
		specs = append(specs, boxcli.PresetSpec{
			Slot: p.Slot, Name: p.Name, StreamURL: proxyStreamURL(p.Slot),
		})
	}
	if len(specs) == 0 {
		return
	}
	logger.Info("starte initial box preset sync", "anzahl", len(specs))

	pending := make(map[int]boxcli.PresetSpec, len(specs))
	for _, p := range specs {
		pending[p.Slot] = p
	}

	for attempt := 0; attempt < 12 && len(pending) > 0; attempt++ {
		if attempt > 0 {
			time.Sleep(10 * time.Second)
		}
		retrySpecs := make([]boxcli.PresetSpec, 0, len(pending))
		for _, p := range pending {
			retrySpecs = append(retrySpecs, p)
		}
		errs := boxcli.SyncAllPresets(context.Background(), boxHost, retrySpecs)
		for slot, err := range errs {
			if err == nil {
				delete(pending, slot)
				logger.Info("box preset gesynct", "slot", slot, "name", pending[slot].Name, "attempt", attempt)
			} else if attempt == 5 {
				logger.Warn("box preset sync endgueltig fehlgeschlagen", "slot", slot, "err", err)
			} else {
				logger.Debug("box preset sync fail, retry", "slot", slot, "attempt", attempt, "err", err)
			}
		}
	}
	if len(pending) == 0 {
		logger.Info("alle box presets erfolgreich gesynct")
	}
}

// periodicPresetReconcile prueft alle 5 Minuten ob die Box noch alle
// Stick Presets in ihrer eigenen Liste hat. Fehlende Slots werden via
// boxcli.AddPreset nachgepflegt. Damit greift der Fix automatisch ohne
// User Aktion wenn z.B. die Bose Firmware nach einem Standby Cycle
// einzelne Eintraege verloren hat.
func periodicPresetReconcile(store *presets.Store, boxHost string, logger *slog.Logger) {
	// 90s nach Boot anfangen, danach im 5 min Takt
	time.Sleep(90 * time.Second)
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for {
		reconcileOnce(store, boxHost, logger)
		<-tick.C
	}
}

func reconcileOnce(store *presets.Store, boxHost string, logger *slog.Logger) {
	stick := store.All()
	if len(stick) == 0 {
		return
	}
	boxSlots, err := fetchBoxPresetSlots(boxHost)
	if err != nil {
		logger.Debug("preset reconcile: box presets nicht lesbar", "err", err)
		return
	}
	var missing []boxcli.PresetSpec
	for _, p := range stick {
		if !boxSlots[p.Slot] {
			missing = append(missing, boxcli.PresetSpec{
				Slot: p.Slot, Name: p.Name, StreamURL: proxyStreamURL(p.Slot),
			})
		}
	}
	if len(missing) == 0 {
		return
	}
	logger.Info("preset reconcile: fehlende Slots auf Box, sync", "fehlend", len(missing))
	errs := boxcli.SyncAllPresets(context.Background(), boxHost, missing)
	for slot, err := range errs {
		if err == nil {
			logger.Info("preset reconcile geheilt", "slot", slot)
		}
	}
}

// fetchBoxPresetSlots liest GET /presets von der Bose API und liefert
// eine Map welcher Slot in der Box Liste gesetzt ist.
func fetchBoxPresetSlots(boxHost string) (map[int]bool, error) {
	client := http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:8090/presets", boxHost))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := map[int]bool{}
	// Bose Format: <presets><preset id="1" ...> ... </preset></presets>
	for _, m := range presetIDRegex.FindAllStringSubmatch(string(body), -1) {
		slot := 0
		fmt.Sscanf(m[1], "%d", &slot)
		if slot >= 1 && slot <= 6 {
			out[slot] = true
		}
	}
	return out, nil
}

var presetIDRegex = regexp.MustCompile(`<preset id="(\d+)"`)

// lastN gibt die letzten n Zeichen von s zurueck.
func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
