#!/bin/sh
# provision-wlan.sh: DEPRECATED.
#
# WLAN provisioning is now handled inline by run.sh: it parses the
# JSON wlan.conf written by the Desktop App setup wizard
# ({"ssid":"...","password":"..."}) and writes
# /etc/wpa_supplicant.conf directly, then restarts wpa_supplicant.
# That path is reliable and does not depend on the BoseApp HTTP API
# being up early enough.
#
# This script remains on the stick as a manual fallback only. It
# expects a key=value wlan.conf and posts an addWirelessProfile to
# the BoseApp HTTP API. The Desktop App does NOT produce that
# format. If you need to run this manually, write wlan.conf as:
#   ssid=MyHomeWifi
#   security=wpa2     (or wpa, wpa_or_wpa2, wep, open)
#   passphrase=secret-password
# Then: sh /media/sda1/provision-wlan.sh
# Logs: /mnt/nv/streborn/wlan-provision.log

set -u

CONF="/media/sda1/wlan.conf"
LOG="/mnt/nv/streborn/wlan-provision.log"
BOSE_API="http://127.0.0.1:8090"

log() {
    echo "$(date): $*" >> "$LOG"
}

if [ ! -r "$CONF" ]; then
    log "wlan.conf not readable, exiting"
    exit 0
fi

# Read the configuration (key=value format, comments with # allowed)
ssid=""
security=""
passphrase=""
while IFS='=' read -r key value; do
    # Ignore comments and empty lines
    case "$key" in
        "" | \#* | " "*) continue ;;
    esac
    case "$key" in
        ssid)        ssid="$value" ;;
        security)    security="$value" ;;
        passphrase)  passphrase="$value" ;;
    esac
done < "$CONF"

if [ -z "$ssid" ] || [ -z "$passphrase" ]; then
    log "ERROR ssid or passphrase missing in wlan.conf"
    exit 1
fi
if [ -z "$security" ]; then
    security="wpa_or_wpa2"
fi

log "Provisioning profile for SSID '$ssid' security $security"

# Check whether the box is already on Wi-Fi. If so, do nothing.
i=0
while [ $i -lt 30 ]; do
    if wget -qO- -T 2 "$BOSE_API/networkInfo" 2>/dev/null | grep -q "NETWORK_WIFI_CONNECTED"; then
        log "Box is already on Wi-Fi, no provisioning needed"
        exit 0
    fi
    # BoseApp not reachable yet?
    if ! wget -qO- -T 2 "$BOSE_API/info" >/dev/null 2>&1; then
        sleep 2
        i=$((i+1))
        continue
    fi
    break
done

if [ $i -ge 30 ]; then
    log "BoseApp not reachable after 60s, giving up"
    exit 1
fi

log "BoseApp responds, sending addWirelessProfile"

# Try several body formats. We log which one works.
BODIES="
<addWirelessProfile><ssid>${ssid}</ssid><security>${security}</security><passphrase>${passphrase}</passphrase></addWirelessProfile>
<profile ssid=\"${ssid}\" security=\"${security}\" passphrase=\"${passphrase}\"/>
<wirelessProfile><ssid>${ssid}</ssid><password>${passphrase}</password></wirelessProfile>
"

success=false
IFS_OLD="$IFS"
IFS='
'
for body in $BODIES; do
    [ -z "$body" ] && continue
    log "Trying body: $body"
    resp=$(echo "$body" | wget -qO- -T 5 --header="Content-Type: application/xml" \
        --post-data="$body" "$BOSE_API/addWirelessProfile" 2>&1)
    log "Response: $resp"
    case "$resp" in
        *Error* | *error* | *FAIL*)
            log "Format fail, trying the next one"
            ;;
        *)
            log "Format accepted"
            success=true
            break
            ;;
    esac
done
IFS="$IFS_OLD"

if ! $success; then
    log "No body format worked"
    exit 1
fi

log "Waiting until the box is on Wi-Fi"
i=0
while [ $i -lt 30 ]; do
    if wget -qO- -T 2 "$BOSE_API/networkInfo" 2>/dev/null | grep -q "NETWORK_WIFI_CONNECTED"; then
        log "Success, box is on Wi-Fi"
        exit 0
    fi
    sleep 2
    i=$((i+1))
done

log "Wi-Fi not active after 60s, possibly wrong credentials or another problem"
exit 1
