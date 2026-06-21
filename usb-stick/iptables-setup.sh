#!/bin/sh
# iptables-setup.sh: sets PREROUTING rules so Bose box traffic on
# 80 and 443 goes to our local agent ports.
#
# Called by run.sh after the agent starts, removed again on stop.
#
# Usage:
#   sh iptables-setup.sh install   # set the rules
#   sh iptables-setup.sh remove    # remove the rules
#   sh iptables-setup.sh status    # show active rules
#
# The rules carry a unique marker in the comment so they can be
# removed safely without affecting other rules.

set -u

MARKER="streborn-redirect"

# Target ports in the agent. Must match run.sh and the agent config.
# 9080 instead of 8080 because 8080 is taken by Bose's own web server.
MARGE_HTTP_PORT=9080
MARGE_HTTPS_PORT=8443
BMX_PORT=8081
WEBUI_PORT=8888

# Source ports we want to redirect.
# 80 is normally PtsServer, we redirect it to Marge HTTP.
# 443 is HTTPS for the Bose cloud domains, goes to Marge HTTPS.
HTTP_PORT=80
HTTPS_PORT=443

# INPUT_ACCEPT_PORTS list the agent ports that must be reachable from
# the LAN. On Series-I SoundTouch (older ST20 / ST10 variants with the
# SMSC-2014 + SCM components and no wlan0 interface) the Bose stock
# firmware installs an INPUT chain that REJECTs everything outside a
# tiny whitelist (8090, 8091, 8080 ...). The Go listener on :8888
# binds fine and `netstat -ltn` shows it LISTEN, but every connect
# attempt from a desktop client gets "connection refused" (TCP RST
# from the firewall). See #60: `nc -vz 192.0.2.66 8888` fails while
# `nc -vz 192.0.2.66 8091` works on an affected Series-I ST20.
# Fix: insert ACCEPT rules at the TOP of the INPUT chain so the box
# answers our ports before reaching the Bose DROP rule. On later
# (Series-II) boxes without that INPUT chain the rules are harmless.
INPUT_ACCEPT_PORTS="$WEBUI_PORT $MARGE_HTTP_PORT $BMX_PORT $MARGE_HTTPS_PORT"

install_rules() {
    # Clean up first in case old rules are still there
    remove_rules quiet

    echo "Installing iptables PREROUTING rules"

    # Try to load modules if needed (REDIRECT, DNAT, conntrack)
    for mod in xt_REDIRECT iptable_nat nf_nat_redirect xt_DNAT iptable_nat nf_nat; do
        modprobe "$mod" 2>/dev/null
    done

    # The Bose box has no REDIRECT target. We use DNAT to 127.0.0.1 instead.
    # For DNAT to localhost to work, /proc/sys/net/ipv4/conf/all/route_localnet=1 must be set first
    echo 1 > /proc/sys/net/ipv4/conf/all/route_localnet 2>/dev/null
    echo 1 > /proc/sys/net/ipv4/conf/lo/route_localnet 2>/dev/null

    # Redirect 443 (HTTPS) to Marge HTTPS via DNAT
    iptables -t nat -A PREROUTING -p tcp --dport "$HTTPS_PORT" \
        -m comment --comment "$MARKER" \
        -j DNAT --to-destination "127.0.0.1:$MARGE_HTTPS_PORT" \
        || echo "WARN: could not set 443 PREROUTING"

    # Redirect 80 (HTTP) to Marge HTTP via DNAT
    iptables -t nat -A PREROUTING -p tcp --dport "$HTTP_PORT" \
        -m comment --comment "$MARKER" \
        -j DNAT --to-destination "127.0.0.1:$MARGE_HTTP_PORT" \
        || echo "WARN: could not set 80 PREROUTING"

    # OUTPUT chain so that locally generated traffic (e.g. when the box
    # resolves the cloud hostname against localhost) is also redirected.
    iptables -t nat -A OUTPUT -p tcp --dport "$HTTPS_PORT" \
        -d 127.0.0.1 -m comment --comment "$MARKER" \
        -j DNAT --to-destination "127.0.0.1:$MARGE_HTTPS_PORT" \
        || echo "WARN: could not set OUTPUT 443"

    iptables -t nat -A OUTPUT -p tcp --dport "$HTTP_PORT" \
        -d 127.0.0.1 -m comment --comment "$MARKER" \
        -j DNAT --to-destination "127.0.0.1:$MARGE_HTTP_PORT" \
        || echo "WARN: could not set OUTPUT 80"

    # MASQUERADE on OUTPUT so reply packets from 127.0.0.1:8080 are routed
    # back to the correct sender
    iptables -t nat -A POSTROUTING -o lo -m comment --comment "$MARKER" \
        -j MASQUERADE \
        || echo "WARN: could not set MASQUERADE"

    # INPUT chain: punch holes for our agent ports so Series-I boxes
    # (SMSC/SCM, no wlan0) do not REJECT the inbound SYNs at the
    # firewall. -I inserts at the top so we win over any pre-existing
    # DROP/REJECT rules the Bose firmware installed. Best-effort: if
    # the running kernel lacks the filter table we just log and move
    # on; on Series-II boxes there is no Bose INPUT rule to fight so
    # the ACCEPT is a no-op anyway.
    for port in $INPUT_ACCEPT_PORTS; do
        iptables -I INPUT 1 -p tcp --dport "$port" \
            -m comment --comment "$MARKER" \
            -j ACCEPT 2>/dev/null \
            && echo "INPUT ACCEPT for tcp/$port installed" \
            || echo "WARN: could not set INPUT ACCEPT for tcp/$port"
    done

    echo "Done. Status:"
    show_status
}

remove_rules() {
    quiet="${1:-}"
    # Search the NAT table for our marker and delete it
    while iptables -t nat -S 2>/dev/null | grep -q "$MARKER"; do
        rule="$(iptables -t nat -S 2>/dev/null | grep "$MARKER" | head -1)"
        # rule starts with "-A CHAIN ...", we turn it into "-D CHAIN ..."
        del_rule="$(echo "$rule" | sed 's/^-A /-D /')"
        # shellcheck disable=SC2086
        iptables -t nat $del_rule 2>/dev/null || break
    done
    # Clean up the filter table (INPUT) as well, same marker logic.
    while iptables -S 2>/dev/null | grep -q "$MARKER"; do
        rule="$(iptables -S 2>/dev/null | grep "$MARKER" | head -1)"
        del_rule="$(echo "$rule" | sed 's/^-A /-D /')"
        # shellcheck disable=SC2086
        iptables $del_rule 2>/dev/null || break
    done
    if [ "$quiet" != "quiet" ]; then
        echo "iptables rules with marker '$MARKER' removed"
    fi
}

show_status() {
    echo "----- NAT PREROUTING -----"
    iptables -t nat -L PREROUTING -n -v --line-numbers 2>/dev/null \
        | grep -E "(Chain|$MARKER|num   pkts)" || echo "no rules"
    echo "----- NAT OUTPUT -----"
    iptables -t nat -L OUTPUT -n -v --line-numbers 2>/dev/null \
        | grep -E "(Chain|$MARKER|num   pkts)" || echo "no rules"
    echo "----- FILTER INPUT (full) -----"
    iptables -L INPUT -n -v --line-numbers 2>/dev/null \
        || echo "no filter table available"
}

case "${1:-install}" in
    install)  install_rules ;;
    remove|uninstall)  remove_rules ;;
    status)   show_status ;;
    *)
        echo "Usage: $0 {install|remove|status}" >&2
        exit 1
        ;;
esac
