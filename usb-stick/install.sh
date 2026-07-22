#!/bin/sh
# install.sh: run once manually on the box to enable the autostart.
#
# Phase 1 (rc.local directly):
#   sh /media/sda1/install.sh             # enable phase 1
#   sh /media/sda1/install.sh remove      # disable phase 1
#   sh /media/sda1/install.sh status      # show status
#
# Phase 2 (integration into shepherdd):
#   sh /media/sda1/install.sh shepherd install   # enable phase 2
#   sh /media/sda1/install.sh shepherd remove    # disable phase 2
#   sh /media/sda1/install.sh shepherd status    # shepherd status
#
# Phase 1 and phase 2 are mutually exclusive. Enabling phase 2 disables
# phase 1 automatically, and vice versa.
#
# Note: requires root. On the ST10 we are root anyway.

set -u

# STICK defaults to the USB mount, but can be overridden via STR_STICK so the
# desktop app's SSH repair fallback can stage install.sh + run.sh + rc.local +
# the agent binary in a NAND directory and install from there when the USB
# stick itself is unreadable (large-cluster/faulty stick, exit 126; ST30 #119).
STICK="${STR_STICK:-/media/sda1}"
RC_SRC="$STICK/rc.local"
RC_DST="/mnt/nv/rc.local"
RUN_SRC="$STICK/run.sh"
RUN_OVERRIDE="/mnt/nv/streborn/run-override.sh"
PRESETS_SRC="$STICK/presets.json"
PRESETS_DST="/mnt/nv/streborn/presets.json"
BIN="$STICK/streborn-armv7l"
[ -x "$BIN" ] || BIN="$STICK/streborn"
# NAND binary cache run.sh actually boots from (CACHED_BIN in run.sh).
# phase1_install seeds it directly from the stick over the live SSH session, so
# a later stick read failure at boot cannot leave the box with no agent to run.
CACHED_BIN="/mnt/nv/streborn/bin/streborn-armv7l"

# Make sure the NAND persist directory exists.
mkdir -p /mnt/nv/streborn/bin 2>/dev/null

# heal_sdk_cloud_urls rewrites any placeholder host left in the Bose SDK config
# back to the real stock hostnames. A stick-free :17000 unlock
# (desktop-app/telnet_enable_ssh.go) points bmxRegistryUrl + statsServerUrl at the
# placeholder host str-setup.invalid to fire the SSH injection, and normally
# restores them afterwards. But when STR is installed via a path that skips that
# restore (SSH already open -> straight to RepairInstallViaSSH), the box boots with
# bmxRegistryUrl=https://str-setup.invalid, which NXDOMAINs, so the box can NEVER
# reach the SoundTouch service registry and its light bar sweeps forever (live
# ST300 2026-07-07). STR redirects only the STOCK hosts (streaming.bose.com,
# content.api.bose.io) via /etc/hosts, so the box MUST carry the stock hostnames
# for the redirect to catch them. Run this before the post-install reboot so the
# SDK reads healed URLs on its very next boot. Idempotent: a fully stock (or
# empty) config is never touched, and re-running the heal reproduces the same
# canonical stock values.
heal_sdk_cloud_urls() {
    sdk_cfg="/mnt/nv/OverrideSdkPrivateCfg.xml"
    [ -f "$sdk_cfg" ] || return 0
    # Heal ANY non-stock host in the three cloud tags, not just STR's own
    # str-setup.invalid placeholder: third-party mods (TechEndure) reroute the
    # same tags to their servers, and a box carrying such a host can never be
    # caught by STR's /etc/hosts redirect of the STOCK hostnames - the cloud
    # icon stays amber and the marge login never sticks, even after the user
    # deleted the mod's files (live report 2026-07-22). A non-empty, non-stock
    # value in ANY of the three tags triggers the heal, and the sed then
    # rewrites ALL non-empty tags to their canonical stock values (an
    # already-stock tag is rewritten to the same value, so the heal stays
    # idempotent; a fully stock or empty config is never touched at all).
    needs_heal=0
    if grep -q '<bmxRegistryUrl>[^<]' "$sdk_cfg" 2>/dev/null && \
       ! grep -q '<bmxRegistryUrl>https://content\.api\.bose\.io' "$sdk_cfg" 2>/dev/null; then
        needs_heal=1
    fi
    if grep -q '<statsServerUrl>[^<]' "$sdk_cfg" 2>/dev/null && \
       ! grep -q '<statsServerUrl>https://events\.api\.bosecm\.com' "$sdk_cfg" 2>/dev/null; then
        needs_heal=1
    fi
    if grep -q '<margeServerUrl>[^<]' "$sdk_cfg" 2>/dev/null && \
       ! grep -q '<margeServerUrl>https://streaming\.bose\.com' "$sdk_cfg" 2>/dev/null; then
        needs_heal=1
    fi
    [ "$needs_heal" = "1" ] || return 0
    sed -e 's#<bmxRegistryUrl>[^<][^<]*</bmxRegistryUrl>#<bmxRegistryUrl>https://content.api.bose.io/bmx/registry/v1/services</bmxRegistryUrl>#g' \
        -e 's#<statsServerUrl>[^<][^<]*</statsServerUrl>#<statsServerUrl>https://events.api.bosecm.com</statsServerUrl>#g' \
        -e 's#<margeServerUrl>[^<][^<]*</margeServerUrl>#<margeServerUrl>https://streaming.bose.com</margeServerUrl>#g' \
        "$sdk_cfg" > "$sdk_cfg.new" 2>/dev/null && mv "$sdk_cfg.new" "$sdk_cfg" 2>/dev/null
    echo "Healed non-stock cloud URLs in $sdk_cfg back to stock (redirect-catchable)"
}

phase1_install() {
    if [ ! -f "$RC_SRC" ]; then
        echo "ERROR: $RC_SRC not found" >&2
        exit 1
    fi
    # Remove phase 2 first if active
    if [ -d /mnt/nv/shepherd ]; then
        echo "Phase 2 is active, removing it first"
        phase2_remove
    fi
    # Free a stranded SSH-repair staging dir and known regenerable junk before
    # copying, so a re-install on a tight NAND has room. A leftover
    # streborn-install (the ~28 MB SSH-repair staging set) filled a ST30 to 80%
    # so every OTA then failed with "no space left on device" (#ST30). Never
    # remove our own source ($STICK): on the SSH-repair path STR_STICK IS the
    # streborn-install staging dir.
    for d in /mnt/nv/streborn-install /mnt/nv/streborn/streborn-install; do
        [ "$d" = "$STICK" ] || rm -rf "$d" 2>/dev/null
    done
    rm -f /mnt/nv/sp-oauth.out /mnt/nv/streborn/cap*.ogg /mnt/nv/streborn/bin/*.new 2>/dev/null
    # When the NAND is still too tight for the agent copy after the cheap reclaim,
    # also drop the ~16 MB go-librespot engine: it is regenerable (re-seeded from
    # the stick / re-delivered after boot), so freeing it lets the agent fit on a
    # nearly-full ST30 instead of failing the install (#119). Gated on a df check
    # so a roomy box keeps its engine. Mirrors the agent's reclaimSpotifyEngine.
    if [ -s "$BIN" ]; then
        need_kb=$(( ( $(wc -c < "$BIN") + 2097152 ) / 1024 ))
        free_kb=$(df -k /mnt/nv 2>/dev/null | tail -1 | awk '{print $(NF-2)}')
        if [ "${free_kb:-0}" -lt "$need_kb" ]; then
            echo "NAND tight (${free_kb:-?}KB free < ${need_kb}KB): dropping regenerable go-librespot engine to make room"
            rm -f /mnt/nv/streborn/bin/go-librespot /mnt/nv/streborn/bin/go-librespot.sha256 2>/dev/null
        fi
    fi
    echo "Copying $RC_SRC to $RC_DST"
    cp "$RC_SRC" "$RC_DST" || { echo "ERROR while copying" >&2; exit 1; }
    chmod +x "$RC_DST"

    # Deploy run.sh into NAND as run-override.sh. This makes NAND the
    # source of truth for the boot - SD card I/O problems or a stale
    # run.sh on the SD card are no longer a problem.
    if [ -f "$RUN_SRC" ]; then
        echo "Copying $RUN_SRC to $RUN_OVERRIDE"
        cp "$RUN_SRC" "$RUN_OVERRIDE"
        chmod +x "$RUN_OVERRIDE"
    fi

    # Seed the NAND binary cache (CACHED_BIN) that run.sh boots from, NOW, over
    # the live SSH session, instead of leaving it to run.sh's boot-time
    # stick->NAND sync. On a flaky/failing stick (readable enough to exec this
    # small script but not to stream the ~10 MB binary) that boot-time copy hits
    # an I/O error, and on a first install there is no prior cache to fall back
    # to, so run.sh exits with "neither NAND cache nor stick binary available"
    # and the agent never starts. Copying here means the binary is on NAND
    # before the reboot, so a later stick read failure no longer blocks the
    # agent. A read failure at THIS point fails install.sh loudly with an I/O
    # marker the desktop app classifies as a stick problem (and offers the SSH
    # NAND-copy repair), instead of an opaque post-reboot "agent not up".
    if [ -s "$BIN" ]; then
        echo "Copying agent binary to $CACHED_BIN"
        if cp "$BIN" "$CACHED_BIN.new" 2>&1; then
            # chmod is part of the condition: a non-executable cache makes
            # run.sh's [ -x "$CACHED_BIN" ] test fail at boot, which would
            # surface as the misleading "neither NAND cache nor stick binary
            # available" even though the file is there. Fail loudly instead.
            if chmod +x "$CACHED_BIN.new" && mv "$CACHED_BIN.new" "$CACHED_BIN" 2>&1; then
                echo "Agent binary cached on NAND ($(wc -c < "$CACHED_BIN") bytes)"
            else
                rm -f "$CACHED_BIN.new"
                echo "ERROR: agent binary chmod/mv to NAND failed" >&2
                exit 1
            fi
        else
            rm -f "$CACHED_BIN.new"
            echo "ERROR: agent binary could not be copied from the stick to NAND (stick I/O error?)" >&2
            exit 1
        fi
    fi

    # Migrate presets.json from SD to NAND on the first install.
    # The SD card often throws an I/O error on write (FAT32), so the
    # agent keeps presets.json on NAND.
    if [ -f "$PRESETS_SRC" ] && [ ! -f "$PRESETS_DST" ]; then
        echo "Migrating presets.json to NAND"
        cp "$PRESETS_SRC" "$PRESETS_DST"
    fi

    # Undo any placeholder SDK cloud URL left by a stick-free :17000 unlock
    # BEFORE the post-install reboot, so the box never boots with
    # bmxRegistryUrl=https://str-setup.invalid (NXDOMAIN -> registry unreachable
    # -> light bar sweeps forever). Idempotent: a config with no placeholder is
    # left untouched.
    heal_sdk_cloud_urls

    ls -la "$RC_DST" "$RUN_OVERRIDE" 2>/dev/null
    echo "Phase 1 active. On the next boot $RC_DST will run."
    echo "NAND override active: agent runs via $RUN_OVERRIDE"
}

phase1_remove() {
    if [ -e "$RC_DST" ]; then
        rm -f "$RC_DST"
        echo "Phase 1 removed: $RC_DST"
    else
        echo "Phase 1 was not active"
    fi
    if [ -e "$RUN_OVERRIDE" ]; then
        rm -f "$RUN_OVERRIDE"
        echo "NAND override removed: $RUN_OVERRIDE"
    fi
}

phase1_status() {
    if [ -x "$RC_DST" ]; then
        echo "Phase 1 (rc.local direct): active"
        ls -la "$RC_DST"
    else
        echo "Phase 1 (rc.local direct): inactive"
    fi
    if [ -x "$RUN_OVERRIDE" ]; then
        echo "NAND override: active"
        ls -la "$RUN_OVERRIDE"
    else
        echo "NAND override: not installed"
    fi
    if [ -f "$PRESETS_DST" ]; then
        cnt=$(grep -c '"slot"' "$PRESETS_DST" 2>/dev/null || echo 0)
        echo "Presets on NAND: $cnt entries"
    fi
}

phase2_install() {
    if [ ! -x "$BIN" ]; then
        echo "ERROR: agent binary not executable: $BIN" >&2
        exit 1
    fi
    # Remove phase 1 first if active
    if [ -x "$RC_DST" ]; then
        echo "Phase 1 is active, removing it first"
        phase1_remove
    fi
    echo "Enabling phase 2: shepherd install via $BIN"
    "$BIN" shepherd install || { echo "ERROR" >&2; exit 1; }
    echo
    echo "Phase 2 active. On the next boot shepherdd starts our agent."
    echo "Recommendation: run 'reboot' now or 'kill -HUP \$(pgrep shepherdd)' so it takes effect immediately."
}

phase2_remove() {
    if [ -x "$BIN" ]; then
        "$BIN" shepherd remove || true
    else
        rm -rf /mnt/nv/shepherd
        echo "Phase 2 removed (via rm, binary was not present)"
    fi
}

phase2_status() {
    if [ -x "$BIN" ]; then
        "$BIN" shepherd status
    else
        if [ -d /mnt/nv/shepherd ]; then
            echo "Phase 2: directory present, but binary not executable"
            ls -la /mnt/nv/shepherd
        else
            echo "Phase 2 (shepherd integration): inactive"
        fi
    fi
}

case "${1:-install}" in
  install)        phase1_install ;;
  remove|uninstall) phase1_remove ;;
  status)         phase1_status; echo; phase2_status ;;
  shepherd)
    case "${2:-status}" in
      install)        phase2_install ;;
      remove|uninstall) phase2_remove ;;
      status)         phase2_status ;;
      *)
        echo "Usage: $0 shepherd {install|remove|status}" >&2
        exit 1
        ;;
    esac
    ;;
  *)
    echo "Usage: $0 [install|remove|status|shepherd <install|remove|status>]" >&2
    exit 1
    ;;
esac
