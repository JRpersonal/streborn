#!/bin/sh
# Functional test for run.sh's INPUT-chain position self-heal.
#
# Why this file exists. The 30 s firewall watchdog used `iptables -C` as its
# only check, and -C only asks whether a rule exists ANYWHERE in the chain. On
# a rhino ST10, Bose's own Firewall binary builds an INPUT chain that ends in
# `-i wlan0 -j DROP`; if that DROP lands above STR's ACCEPT rules, -C still
# answers "present", the watchdog repairs nothing, and the speaker stays
# unreachable from the LAN while outbound playback keeps working.
#
# The shim below plays the chain: it answers -S, -C, -D and -I the way
# iptables does, and records every call that changes the chain, so the test
# can assert both the resulting order and that a healthy chain is never
# touched.
#
# Runs under dash, busybox ash and bash. No arrays, no [[ ]], no local.
# Deliberately NOT in usb-stick/ itself: files.go embeds *.sh from that
# directory onto every stick.

set -u

HERE=$(dirname "$0")
RUNSH="$HERE/../run.sh"
[ -r "$RUNSH" ] || { echo "cannot read $RUNSH"; exit 1; }

FAILED=0
CASES=0

ok() {
    CASES=$((CASES + 1))
}
fail() {
    CASES=$((CASES + 1))
    FAILED=$((FAILED + 1))
    echo "FAIL: $1"
}
expect_eq() {
    # $1 = description, $2 = want, $3 = got
    if [ "$2" = "$3" ]; then
        ok
    else
        fail "$1"
        echo "--- want:"
        echo "$2"
        echo "--- got:"
        echo "$3"
    fi
}

extract() {
    awk -v fn="$1" '
        $0 == fn "() {" { inside = 1 }
        inside          { print }
        inside && $0 == "}" { exit }
    ' "$RUNSH"
}

EXTRACTED="$HERE/.extracted_fw.sh"
: > "$EXTRACTED"
for fn in fw_have fw_insert fw_str_spec fw_heal_shadowed fw_chain_evidence iptables_install_streborn_fw; do
    extract "$fn" >> "$EXTRACTED"
    if ! grep -q "^$fn() {" "$EXTRACTED"; then
        echo "FAIL: could not extract $fn from run.sh"
        rm -f "$EXTRACTED"
        exit 1
    fi
done

# shellcheck source=/dev/null
. "$EXTRACTED"
rm -f "$EXTRACTED"

# ---- stubs, defined AFTER sourcing so they win -----------------------------

INPUT_ACCEPT_PORTS="8888 9080 8081 8443"
NL='
'
LOG=""
CALLS=""
CHAIN=""

setup_log() {
    LOG="$LOG$1$NL"
}
uptime_s() {
    echo 42
}

# norm turns a rule line into a comparable form: no "-A INPUT ", no quotes,
# no implicit "-m tcp" (iptables -S adds it, the scripts never pass it).
norm() {
    printf '%s\n' "$1" | sed -e 's/^-A INPUT //' -e 's/"//g' -e 's/ -m tcp / /'
}

# iptables shim. CHAIN holds the `iptables -S INPUT` listing, one rule per
# line, policy line first.
iptables() {
    [ "$1" = "-w" ] && shift
    op=$1
    shift
    case "$op" in
        -S)
            printf '%s\n' "$CHAIN"
            return 0
            ;;
        -nL)
            return 0
            ;;
    esac
    shift # chain name
    if [ "$op" = "-I" ]; then
        shift # position; the scripts only ever insert at 1
    fi
    want=$(norm "$*")
    case "$op" in
        -C)
            printf '%s\n' "$CHAIN" | while IFS= read -r l; do
                [ "$(norm "$l")" = "$want" ] && exit 3
            done
            [ $? -eq 3 ]
            return
            ;;
        -I)
            CALLS="${CALLS}I $*$NL"
            policy=$(printf '%s\n' "$CHAIN" | grep '^-P ')
            rest=$(printf '%s\n' "$CHAIN" | grep -v '^-P ')
            CHAIN="$policy$NL-A INPUT $*"
            [ -z "$rest" ] || CHAIN="$CHAIN$NL$rest"
            return 0
            ;;
        -D)
            CALLS="${CALLS}D $*$NL"
            new=""
            done_one=0
            ifs=$IFS
            IFS=$NL
            for l in $CHAIN; do
                if [ $done_one -eq 0 ] && [ "$(norm "$l")" = "$want" ]; then
                    done_one=1
                    continue
                fi
                if [ -z "$new" ]; then new=$l; else new="$new$NL$l"; fi
            done
            IFS=$ifs
            CHAIN=$new
            [ $done_one -eq 1 ]
            return
            ;;
    esac
    return 1
}

reset() {
    LOG=""
    CALLS=""
    STR_FW_COMMENT=""
    STR_FW_PRIMED=1
    STR_FW_LAST_SIG=""
    STR_FW_CHAIN_SIG=""
    CHAIN=$1
}

STR_RULES='-A INPUT -p icmp -m comment --comment "streborn-fw" -j ACCEPT
-A INPUT -p tcp -m tcp --dport 8888 -m comment --comment "streborn-fw" -j ACCEPT
-A INPUT -p tcp -m tcp --dport 9080 -m comment --comment "streborn-fw" -j ACCEPT
-A INPUT -p tcp -m tcp --dport 8081 -m comment --comment "streborn-fw" -j ACCEPT
-A INPUT -p tcp -m tcp --dport 8443 -m comment --comment "streborn-fw" -j ACCEPT'
BOSE_HEAD='-A INPUT -i wlan0 -p tcp -m tcp --dport 17008 -j DROP
-A INPUT -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
-A INPUT -s 192.0.2.0/24 -i wlan0 -j ACCEPT'
BOSE_DROP='-A INPUT -i wlan0 -j DROP'

# Normalised view of the chain, so the order assertion does not depend on
# whether the shim kept the quotes of a rule it re-inserted.
view() {
    ifs=$IFS
    IFS=$NL
    for l in $CHAIN; do norm "$l"; done
    IFS=$ifs
}
mutations() {
    printf '%s' "$CALLS" | grep -c '^[DI] '
}

# ---- (a) STR rules pushed below Bose's DROP rules move to the top ----------

reset "-P INPUT ACCEPT$NL$BOSE_HEAD$NL$STR_RULES$NL$BOSE_DROP"
iptables_install_streborn_fw
want_chain="-P INPUT ACCEPT$NL$STR_RULES$NL$BOSE_HEAD$NL$BOSE_DROP"
expect_eq "(a) shadowed STR rules moved to the top in order" \
    "$(CHAIN=$want_chain; view)" "$(view)"
expect_eq "(a) one delete and one insert per shadowed rule" "10" "$(mutations)"
# The comment's quotes from -S must not reach iptables as literal characters,
# or the re-inserted rule would carry the comment '"streborn-fw"'.
expect_eq "(a) no literal quotes passed to -D/-I" "0" "$(printf '%s' "$CALLS" | grep -c '"')"
expect_eq "(a) deletes keep the comment match" "5" \
    "$(printf '%s' "$CALLS" | grep -c '^D .*-m comment --comment streborn-fw -j ACCEPT$')"
expect_eq "(a) exactly one self-heal line naming the DROP" "1" \
    "$(printf '%s' "$LOG" | grep -c "^firewall self-heal: STR rule was shadowed by '-i wlan0 -p tcp -m tcp --dport 17008 -j DROP', moved to the top (moved=5 failed=0)")"
expect_eq "(a) no presence reinstall on top of the move" "0" \
    "$(printf '%s' "$LOG" | grep -c 'reinstalled')"

# A second pass on the healed chain is a no-op.
CALLS=""
iptables_install_streborn_fw
expect_eq "(a) second pass changes nothing" "0" "$(mutations)"

# Bare rules (kernel without xt_comment) are recognised too, and only the
# shadowed ones move; the unshadowed one stays put.
BARE_ABOVE='-A INPUT -p icmp -j ACCEPT'
BARE_BELOW='-A INPUT -p tcp -m tcp --dport 8888 -j ACCEPT
-A INPUT -p tcp -m tcp --dport 9080 -j ACCEPT
-A INPUT -p tcp -m tcp --dport 8081 -j ACCEPT
-A INPUT -p tcp -m tcp --dport 8443 -j ACCEPT'
reset "-P INPUT ACCEPT$NL$BARE_ABOVE$NL$BOSE_DROP$NL$BARE_BELOW"
iptables_install_streborn_fw
want_chain="-P INPUT ACCEPT$NL$BARE_BELOW$NL$BARE_ABOVE$NL$BOSE_DROP"
expect_eq "(a) bare-form shadowed rules moved, order kept" \
    "$(CHAIN=$want_chain; view)" "$(view)"
expect_eq "(a) bare form: 4 moves" "8" "$(mutations)"

# A REJECT counts as a DROP.
reset "-P INPUT ACCEPT$NL-A INPUT -i wlan0 -j REJECT --reject-with icmp-port-unreachable$NL$STR_RULES"
iptables_install_streborn_fw
expect_eq "(a) REJECT shadows too" "10" "$(mutations)"

# ---- (b) already-correct chain is left untouched ---------------------------

reset "-P INPUT ACCEPT$NL$STR_RULES$NL$BOSE_HEAD$NL$BOSE_DROP"
before=$CHAIN
iptables_install_streborn_fw
expect_eq "(b) correct chain: no delete/insert calls" "0" "$(mutations)"
expect_eq "(b) correct chain unchanged" "$before" "$CHAIN"
expect_eq "(b) no self-heal line" "0" "$(printf '%s' "$LOG" | grep -c 'firewall self-heal')"

# A shadowed duplicate whose twin sits above the DROP is left alone: the twin
# already accepts the traffic, and -D by spec would remove the twin.
reset "-P INPUT ACCEPT$NL$STR_RULES$NL$BOSE_DROP$NL-A INPUT -p tcp -m tcp --dport 8888 -m comment --comment \"streborn-fw\" -j ACCEPT"
iptables_install_streborn_fw
expect_eq "(b) shadowed duplicate with a live twin: untouched" "0" "$(mutations)"

# Bose's own ACCEPTs below a DROP are never ours to move.
reset "-P INPUT ACCEPT$NL$STR_RULES$NL$BOSE_DROP$NL-A INPUT -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT$NL-A INPUT -p tcp -m tcp --dport 8090 -j ACCEPT"
iptables_install_streborn_fw
expect_eq "(b) foreign ACCEPTs below a DROP: untouched" "0" "$(mutations)"

# ---- (c) chain with no DROP rules is unchanged -----------------------------

reset "-P INPUT ACCEPT$NL-A INPUT -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT$NL$STR_RULES"
before=$CHAIN
iptables_install_streborn_fw
expect_eq "(c) policy ACCEPT, no DROP: no delete/insert calls" "0" "$(mutations)"
expect_eq "(c) chain unchanged" "$before" "$CHAIN"

# The existing presence path still works on such a chain (empty chain: every
# rule gets inserted once, no self-heal move line).
reset "-P INPUT ACCEPT"
iptables_install_streborn_fw
expect_eq "(c) empty chain: five inserts, no deletes" "5" "$(printf '%s' "$CALLS" | grep -c '^I ')"
expect_eq "(c) empty chain: no move line" "0" "$(printf '%s' "$LOG" | grep -c 'firewall self-heal')"

# ---- (d) chain evidence is edge-triggered ----------------------------------

reset "-P INPUT ACCEPT$NL$STR_RULES$NL$BOSE_HEAD$NL$BOSE_DROP"
iptables_install_streborn_fw
iptables_install_streborn_fw
iptables_install_streborn_fw
expect_eq "(d) stable chain: one dump over three passes" "1" \
    "$(printf '%s' "$LOG" | grep -c '^iptables INPUT chain changed')"
expect_eq "(d) the dump carries the rules" "1" \
    "$(printf '%s' "$LOG" | grep -c '^  -A INPUT -i wlan0 -j DROP$')"

CHAIN="$CHAIN$NL-A INPUT -i eth0 -j DROP"
iptables_install_streborn_fw
iptables_install_streborn_fw
expect_eq "(d) one more dump after a change" "2" \
    "$(printf '%s' "$LOG" | grep -c '^iptables INPUT chain changed')"

# A heal pass: the shadowed chain is dumped, then the healed one, then quiet.
reset "-P INPUT ACCEPT$NL$BOSE_DROP$NL$STR_RULES"
iptables_install_streborn_fw
iptables_install_streborn_fw
iptables_install_streborn_fw
iptables_install_streborn_fw
expect_eq "(d) heal: shadowed dump + healed dump, then quiet" "2" \
    "$(printf '%s' "$LOG" | grep -c '^iptables INPUT chain changed')"
expect_eq "(d) heal logged once" "1" "$(printf '%s' "$LOG" | grep -c '^firewall self-heal')"

echo "firewall_position_test: $CASES cases, $FAILED failed"
[ "$FAILED" -eq 0 ]
