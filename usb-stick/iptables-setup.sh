#!/bin/sh
# iptables-setup.sh: setzt PREROUTING Regeln damit Bose Box Traffic auf
# 80 und 443 zu unseren lokalen Agent Ports geht.
#
# Wird vom run.sh nach Agent Start aufgerufen, beim Stop wieder entfernt.
#
# Aufruf:
#   sh iptables-setup.sh install   # Regeln setzen
#   sh iptables-setup.sh remove    # Regeln entfernen
#   sh iptables-setup.sh status    # Aktive Regeln zeigen
#
# Die Regeln tragen einen unique Marker im Kommentar, damit sie sicher
# wieder entfernt werden koennen ohne andere Regeln zu treffen.

set -u

MARKER="streborn-redirect"

# Zielports im Agent. Muessen mit run.sh und der Agent Konfig matchen.
# 9080 statt 8080 weil 8080 von Bose's eigenem WebServer belegt ist.
MARGE_HTTP_PORT=9080
MARGE_HTTPS_PORT=8443
BMX_PORT=8081

# Quellports die wir umleiten wollen.
# 80 ist normalerweise PtsServer, wir leiten es zu Marge HTTP um.
# 443 ist HTTPS fuer die Bose Cloud Domains, geht zu Marge HTTPS.
HTTP_PORT=80
HTTPS_PORT=443

install_rules() {
    # Erst aufraeumen falls alte Regeln noch da
    remove_rules quiet

    echo "Installiere iptables PREROUTING Regeln"

    # Module versuchen zu laden falls noetig (REDIRECT, DNAT, conntrack)
    for mod in xt_REDIRECT iptable_nat nf_nat_redirect xt_DNAT iptable_nat nf_nat; do
        modprobe "$mod" 2>/dev/null
    done

    # Bose Box hat kein REDIRECT Target. Wir nutzen DNAT auf 127.0.0.1 stattdessen.
    # Damit DNAT auf localhost klappt muss erst /proc/sys/net/ipv4/conf/all/route_localnet=1
    echo 1 > /proc/sys/net/ipv4/conf/all/route_localnet 2>/dev/null
    echo 1 > /proc/sys/net/ipv4/conf/lo/route_localnet 2>/dev/null

    # 443 (HTTPS) auf Marge HTTPS umleiten via DNAT
    iptables -t nat -A PREROUTING -p tcp --dport "$HTTPS_PORT" \
        -m comment --comment "$MARKER" \
        -j DNAT --to-destination "127.0.0.1:$MARGE_HTTPS_PORT" \
        || echo "WARN: konnte 443 PREROUTING nicht setzen"

    # 80 (HTTP) auf Marge HTTP umleiten via DNAT
    iptables -t nat -A PREROUTING -p tcp --dport "$HTTP_PORT" \
        -m comment --comment "$MARKER" \
        -j DNAT --to-destination "127.0.0.1:$MARGE_HTTP_PORT" \
        || echo "WARN: konnte 80 PREROUTING nicht setzen"

    # OUTPUT chain damit auch lokal generierter Traffic (z.B. wenn die Box
    # gegen localhost den Cloud Hostnamen aufloest) umgeleitet wird.
    iptables -t nat -A OUTPUT -p tcp --dport "$HTTPS_PORT" \
        -d 127.0.0.1 -m comment --comment "$MARKER" \
        -j DNAT --to-destination "127.0.0.1:$MARGE_HTTPS_PORT" \
        || echo "WARN: konnte OUTPUT 443 nicht setzen"

    iptables -t nat -A OUTPUT -p tcp --dport "$HTTP_PORT" \
        -d 127.0.0.1 -m comment --comment "$MARKER" \
        -j DNAT --to-destination "127.0.0.1:$MARGE_HTTP_PORT" \
        || echo "WARN: konnte OUTPUT 80 nicht setzen"

    # MASQUERADE auf OUTPUT damit Antwort Pakete von 127.0.0.1:8080 zurueck
    # an den richtigen Absender geroutet werden
    iptables -t nat -A POSTROUTING -o lo -m comment --comment "$MARKER" \
        -j MASQUERADE \
        || echo "WARN: konnte MASQUERADE nicht setzen"

    echo "Fertig. Status:"
    show_status
}

remove_rules() {
    quiet="${1:-}"
    # Wir greppen die NAT Tabelle nach unserem Marker und loeschen
    # jede Zeile mit -D
    while iptables -t nat -S 2>/dev/null | grep -q "$MARKER"; do
        rule="$(iptables -t nat -S 2>/dev/null | grep "$MARKER" | head -1)"
        # rule beginnt mit "-A CHAIN ...", wir machen daraus "-D CHAIN ..."
        del_rule="$(echo "$rule" | sed 's/^-A /-D /')"
        # shellcheck disable=SC2086
        iptables -t nat $del_rule 2>/dev/null || break
    done
    if [ "$quiet" != "quiet" ]; then
        echo "iptables Regeln mit Marker '$MARKER' entfernt"
    fi
}

show_status() {
    echo "----- NAT PREROUTING -----"
    iptables -t nat -L PREROUTING -n -v --line-numbers 2>/dev/null \
        | grep -E "(Chain|$MARKER|num   pkts)" || echo "keine Regeln"
    echo "----- NAT OUTPUT -----"
    iptables -t nat -L OUTPUT -n -v --line-numbers 2>/dev/null \
        | grep -E "(Chain|$MARKER|num   pkts)" || echo "keine Regeln"
}

case "${1:-install}" in
    install)  install_rules ;;
    remove|uninstall)  remove_rules ;;
    status)   show_status ;;
    *)
        echo "Verwendung: $0 {install|remove|status}" >&2
        exit 1
        ;;
esac
