#!/bin/sh
# provision-wlan.sh: liest /media/sda1/wlan.conf und uebermittelt das WLAN
# Profil an die BoseApp HTTP API damit sich die Box automatisch verbindet.
#
# wlan.conf Format (key=value):
#   ssid=MyHomeWifi
#   security=wpa2     (oder wpa, wpa_or_wpa2, wep, open)
#   passphrase=secret-password
#
# Aufruf:  sh /media/sda1/provision-wlan.sh
# Logs:    /mnt/nv/streborn/wlan-provision.log

set -u

CONF="/media/sda1/wlan.conf"
LOG="/mnt/nv/streborn/wlan-provision.log"
BOSE_API="http://127.0.0.1:8090"

log() {
    echo "$(date): $*" >> "$LOG"
}

if [ ! -r "$CONF" ]; then
    log "wlan.conf nicht lesbar, beende"
    exit 0
fi

# Konfiguration einlesen (key=value Format, Kommentare mit # erlaubt)
ssid=""
security=""
passphrase=""
while IFS='=' read -r key value; do
    # Kommentare und leere Zeilen ignorieren
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
    log "FEHLER ssid oder passphrase fehlt in wlan.conf"
    exit 1
fi
if [ -z "$security" ]; then
    security="wpa_or_wpa2"
fi

log "Provisioning Profile fuer SSID '$ssid' security $security"

# Pruefen ob die Box schon im WLAN ist. Wenn ja, machen wir nichts.
i=0
while [ $i -lt 30 ]; do
    if wget -qO- -T 2 "$BOSE_API/networkInfo" 2>/dev/null | grep -q "NETWORK_WIFI_CONNECTED"; then
        log "Box ist schon im WLAN, kein Provisioning noetig"
        exit 0
    fi
    # BoseApp noch nicht erreichbar?
    if ! wget -qO- -T 2 "$BOSE_API/info" >/dev/null 2>&1; then
        sleep 2
        i=$((i+1))
        continue
    fi
    break
done

if [ $i -ge 30 ]; then
    log "BoseApp nicht erreichbar nach 60s, gebe auf"
    exit 1
fi

log "BoseApp reagiert, sende addWirelessProfile"

# Mehrere Body Formate ausprobieren. Wir loggen welcher klappt.
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
    log "Versuche Body: $body"
    resp=$(echo "$body" | wget -qO- -T 5 --header="Content-Type: application/xml" \
        --post-data="$body" "$BOSE_API/addWirelessProfile" 2>&1)
    log "Response: $resp"
    case "$resp" in
        *Error* | *error* | *FAIL*)
            log "Format fail, naechstes probieren"
            ;;
        *)
            log "Format akzeptiert"
            success=true
            break
            ;;
    esac
done
IFS="$IFS_OLD"

if ! $success; then
    log "Kein Body Format hat geklappt"
    exit 1
fi

log "Warte bis Box im WLAN ist"
i=0
while [ $i -lt 30 ]; do
    if wget -qO- -T 2 "$BOSE_API/networkInfo" 2>/dev/null | grep -q "NETWORK_WIFI_CONNECTED"; then
        log "Erfolg, Box ist im WLAN"
        exit 0
    fi
    sleep 2
    i=$((i+1))
done

log "WLAN nicht aktiv nach 60s, eventuell falsche Credentials oder anderes Problem"
exit 1
