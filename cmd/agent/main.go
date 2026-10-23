// Command streborn is the agent that runs directly on the Bose SoundTouch
// box and emulates the Bose cloud endpoints locally.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/JRpersonal/streborn/internal/internet"
	"github.com/JRpersonal/streborn/internal/netutil"
	"github.com/JRpersonal/streborn/internal/shepherd"
	"github.com/JRpersonal/streborn/internal/tlsgen"
	"github.com/JRpersonal/streborn/internal/webui"
)

// version is the semver version. The build date is set separately via
// -ldflags so that "1.0.0" can be shown while the build date is still
// available.
var (
	version    = "1.0.0"
	buildStamp = "dev"
)

func init() {
	webui.SetAgentVersion(version)
	webui.SetAgentBuild(buildStamp)
}

func main() {
	// Handle subcommands before flag.Parse() so their own flags are not
	// swallowed by the global flag set.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "shepherd":
			if err := runShepherdCmd(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			return
		}
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runShepherdCmd handles the shepherd subcommand.
// Invocations:
//
//	streborn shepherd install   -- set up /mnt/nv/shepherd
//	streborn shepherd remove    -- remove /mnt/nv/shepherd
//	streborn shepherd status    -- show the current state
func runShepherdCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: shepherd {install|remove|status}")
	}

	fs := flag.NewFlagSet("shepherd", flag.ContinueOnError)
	shepherdDir := fs.String("dir", shepherd.DefaultShepherdDir, "Shepherd override directory")
	boseDir := fs.String("bose-config", shepherd.DefaultBoseConfigDir, "Bose config directory")
	bin := fs.String("binary", shepherd.DefaultStickBin, "Path to the agent binary")
	presetsPath := fs.String("presets", shepherd.DefaultPresetsPath, "Path to presets.json")

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
		return fmt.Errorf("unknown subcommand: %s", cmd)
	}
}

// nandPresetsPath is the canonical on-NAND preset store. NAND (ubifs) survives
// reboots and the stick being removed; a stick mountpoint does not.
// powerToggle1036Window is how long the 1036 storm COUNTER is stood down after
// STR toggles the box's power. The firmware answers its own power-on
// self-resume with errorUpdate 1036 immediately; anything arriving later than
// this is the box speaking for itself and still counts.
const powerToggle1036Window = 2 * time.Second

const nandPresetsPath = "/mnt/nv/streborn/presets.json"

// canonicalPresetsPath keeps the preset store on NAND. If the configured path
// is on removable media (a USB stick under /media or /run/media, the pre-NAND
// boot-script default), it redirects to nandPresetsPath so saves persist across
// a reboot, migrating a still-readable stick copy once if NAND has none yet
// (#120). Any other path (including an explicit /mnt/nv override) is left as-is.
func canonicalPresetsPath(p string, logger *slog.Logger) string {
	clean := filepath.Clean(p)
	if !strings.HasPrefix(clean, "/media/") && !strings.HasPrefix(clean, "/run/media/") {
		return p
	}
	if _, err := os.Stat(nandPresetsPath); os.IsNotExist(err) {
		if data, rerr := os.ReadFile(p); rerr == nil && len(data) > 0 {
			if mkErr := os.MkdirAll(filepath.Dir(nandPresetsPath), 0o755); mkErr == nil {
				if werr := os.WriteFile(nandPresetsPath, data, 0o644); werr == nil {
					logger.Warn("presets: migrated stick preset store to NAND", "from", p, "to", nandPresetsPath)
				} else {
					logger.Warn("presets: NAND migration write failed", "err", werr)
				}
			}
		}
	}
	logger.Warn("presets: redirecting removable-media preset path to NAND so it survives reboot",
		"flag", p, "using", nandPresetsPath)
	return nandPresetsPath
}

func run() error {
	var (
		presetsPath     = flag.String("presets", "/media/sda1/presets.json", "Path to presets.json on the USB stick")
		webuiAddr       = flag.String("listen-webui", ":8888", "Address for the config web UI")
		margeAddr       = flag.String("listen-marge", ":80", "Address for the marge emulation HTTP (streaming.bose.com)")
		margeTLSAddr    = flag.String("listen-marge-tls", ":8443", "Address for the marge emulation HTTPS")
		bmxAddr         = flag.String("listen-bmx", ":81", "Address for the BMX emulation HTTP (content.api.bose.io)")
		hostsPath       = flag.String("hosts", "/etc/hosts", "Path to the hosts file")
		applyHosts      = flag.Bool("apply-hosts", true, "Modify the hosts file on start and restore it on stop")
		tlsDir          = flag.String("tls-dir", tlsgen.DefaultCADir, "Directory for the CA and server certificate")
		tlsEnabled      = flag.Bool("tls", true, "Enable TLS termination on listen-marge-tls")
		logLevel        = flag.String("log-level", "info", "Log level: debug, info, warn, error")
		boxHost         = flag.String("box-host", "127.0.0.1", "Bose box IP for UPnP calls (webui /api/play). 127.0.0.1 when the agent runs on the box, otherwise the LAN IP.")
		regionFile      = flag.String("region-file", "", "Path to region.txt with the ISO country code (from the setup wizard). The default radio country and language are derived from it.")
		pendingNameFile = flag.String("pending-name-file", "", "Path to name.txt from the setup wizard. Its contents are applied once as the box name, verbatim, and the file is deleted afterwards.")
		printVersion    = flag.Bool("version", false, "Print the version and exit")
	)
	flag.Parse()

	if *printVersion {
		fmt.Println(version)
		return nil
	}

	logger := newLogger(*logLevel)
	// The internet verdict logs its transitions from the very first clock
	// sync attempt on, which runs before the debug sections are registered.
	internet.SetLogger(logger.With("comp", "internet"))
	// The process default routes into the same destination as the named logger.
	// Library code falls back to slog.Default() when no logger was handed to it
	// (the boxapi client's 2xx error-envelope survey is the case that exposed
	// this), and without this line that output went to bare stderr, of which a
	// diagnostic bundle keeps almost nothing. The survey exists to be read out
	// of bundles across the fleet, so it has to land where bundles look.
	slog.SetDefault(logger)
	logger.Info("streborn starting", "version", version)

	// The one root context, created before the first goroutine starts: every
	// background loop below derives from it, so a SIGTERM (or a listener
	// failure, which cancels it too) reaches all of them. Only the signal
	// context itself is allowed to start from context.Background().
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	a := &agent{
		flags: agentFlags{
			presetsPath:     *presetsPath,
			webuiAddr:       *webuiAddr,
			margeAddr:       *margeAddr,
			margeTLSAddr:    *margeTLSAddr,
			bmxAddr:         *bmxAddr,
			hostsPath:       *hostsPath,
			applyHosts:      *applyHosts,
			tlsDir:          *tlsDir,
			tlsEnabled:      *tlsEnabled,
			boxHost:         *boxHost,
			regionFile:      *regionFile,
			pendingNameFile: *pendingNameFile,
		},
		logger: logger,
		ctx:    ctx,
		cancel: cancel,
		errs:   make(chan error, 3),
	}
	if err := runPhases(a.phases(), nil); err != nil {
		return err
	}
	return a.wait()
}

// routableIPv4 returns the address this speaker is reachable on, which is the
// address its own mDNS name has to resolve to. Chosen by walking the interfaces
// rather than by asking the kernel for a route, because a SoundTouch 30 keeps
// an ethernet port that is up and disconnected while it runs on Wi-Fi, and that
// port is exactly the wrong answer.
func routableIPv4() net.IP {
	return netutil.FirstLANIPv4()
}

// mdnsHostLabel is this speaker's own mDNS label, set only once the responder
// for it is live. Empty means every announcement falls back to the box's Linux
// hostname exactly as before, which is the safe half of this change.
var mdnsHostLabel string
