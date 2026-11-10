package webui

// The helper scripts an update leaves running after the agent has exited are
// generated in Go and run by the speaker's BusyBox sh. Nothing reviews them
// once they are out, so a change to the generated text must be a decision, not
// a side effect of a refactoring: these tests compare the scripts for the
// production paths byte for byte against the text that shipped in v1.5.5.

import "testing"

const goldenSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var goldenArgs = []string{"--listen-webui", ":8888", "--name", "it's"}

func checkGolden(t *testing.T, name, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	n := 0
	for n < len(got) && n < len(want) && got[n] == want[n] {
		n++
	}
	from := max(n-60, 0)
	t.Errorf("%s differs from the golden text at byte %d:\n got: %q\nwant: %q",
		name, n, got[from:min(n+80, len(got))], want[from:min(n+80, len(want))])
}

// The restart helper of a coprocessor Wi-Fi speaker (otarestart.go).
func TestAgentRestartScriptGolden(t *testing.T) {
	checkGolden(t, "restart helper", productionOTAEffects.paths.agentRestartScript(4242, agentBinNANDPath, goldenArgs), goldenRestartScript)
}

// The RAM-staged swap helper (ota_lastresort.go), ending in a reboot and, on a
// restart-only speaker, in the supervised start of the new agent.
func TestSwapHelperScriptGolden(t *testing.T) {
	reboot := swapHelper{
		agentPID: 4242, waitSec: swapHelperWaitSec,
		stage: productionOTAEffects.paths.ramStageTarget, dst: agentBinNANDPath, size: 18123456, sha: goldenSHA,
		finish:    "reboot",
		rescueBin: productionOTAEffects.paths.ramRescueBin, args: goldenArgs, paths: productionOTAEffects.paths,
	}.script()
	checkGolden(t, "swap helper (reboot)", reboot, goldenSwapReboot)
	restart := swapHelper{
		agentPID: 4242, waitSec: swapHelperWaitSec + 81,
		stage: productionOTAEffects.paths.ramStageTarget, dst: agentBinNANDPath, size: 18123456, sha: goldenSHA,
		finish:    productionOTAEffects.paths.superviseNewAgentLines(agentBinNANDPath, goldenArgs),
		rescueBin: productionOTAEffects.paths.ramRescueBin, args: goldenArgs, paths: productionOTAEffects.paths,
	}.script()
	checkGolden(t, "swap helper (restart only)", restart, goldenSwapRestart)
}

// The copy the rescue agent's own retry runs (ota_rescue.go).
func TestPlaceBinaryLinesGolden(t *testing.T) {
	checkGolden(t, "place_binary", placeBinaryLines(productionOTAEffects.paths.ramRescueBin, agentBinNANDPath, 18123456, goldenSHA), goldenPlaceBinary)
}

const goldenRestartScript = `i=0
while [ -d /proc/4242 ] && [ "$i" -lt 30 ]; do sleep 1; i=$((i+1)); done
if [ -d /proc/4242 ]; then kill -9 4242 2>/dev/null; sleep 3; fi
sleep 2
cur=$(cat "/mnt/nv/streborn/agent.pid" 2>/dev/null)
if [ -n "$cur" ] && [ "$cur" != "$$" ]; then exit 0; fi
rm -f "/tmp/streborn-agent-handover"
'/mnt/nv/streborn/bin/streborn-armv7l' '--listen-webui' ':8888' '--name' 'it'\''s' >> "/tmp/streborn-agent.log" 2>&1 &
bpid=$!
echo "$bpid" > "/mnt/nv/streborn/agent.pid"
wait "$bpid"
ec=$?
if [ "$(cat "/tmp/streborn-agent-handover" 2>/dev/null)" = "$bpid" ]; then rm -f "/tmp/streborn-agent-handover"; exit 0; fi
case "$ec" in
  0) why="clean exit (unexpected for a daemon)" ;;
  12[89]|1[3-9][0-9]) why="killed by signal $((ec-128))" ;;
  *) why="exit status $ec" ;;
esac
echo "[up=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)s] $(date): agent EXITED (started by the update restart helper, pid=$bpid): code=$ec, $why" >> "/mnt/nv/streborn/setup.log" 2>/dev/null
{
  echo "==== agent exit (update restart helper) pid=$bpid code=$ec ($why) $(date) ===="
  tail -n 40 "/tmp/streborn-agent.log" 2>/dev/null
  echo ""
} >> "/mnt/nv/streborn/logs/agent-crash.log" 2>/dev/null`

const goldenSwapReboot = `i=0
while [ -d /proc/4242 ] && [ "$i" -lt 90 ]; do sleep 1; i=$((i+1)); done
if [ -d /proc/4242 ]; then kill -9 4242 2>/dev/null; sleep 3; fi
if [ -d /proc/4242 ]; then
  echo "swap aborted: agent PID 4242 still alive after kill -9" > "/mnt/nv/streborn/ota-swap-failed"; sync
  rm -f "/dev/shm/streborn-ota.stage"
  exit 1
fi
verify_flash() {
  sync
  { echo 3 > /proc/sys/vm/drop_caches; } 2>/dev/null
  if command -v sha256sum >/dev/null 2>&1; then
    [ "$(sha256sum "$1" 2>/dev/null | cut -d' ' -f1)" = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" ]
  elif command -v cmp >/dev/null 2>&1; then
    cmp -s "/dev/shm/streborn-ota.stage" "$1"
  else
    [ "$(wc -c < "$1" 2>/dev/null | tr -d ' ')" = "18123456" ]
  fi
}
place_binary() {
  rm -f "/mnt/nv/streborn/bin/streborn-armv7l.new"
  avail=$(df -k "/mnt/nv/streborn/bin" 2>/dev/null | awk 'NR>1 && NF>=4 {a=$(NF-2)} END {print a+0}')
  if [ "${avail:-0}" -ge 18723 ] 2>/dev/null; then
    if cp "/dev/shm/streborn-ota.stage" "/mnt/nv/streborn/bin/streborn-armv7l.new" && chmod 755 "/mnt/nv/streborn/bin/streborn-armv7l.new" && verify_flash "/mnt/nv/streborn/bin/streborn-armv7l.new" && mv -f "/mnt/nv/streborn/bin/streborn-armv7l.new" "/mnt/nv/streborn/bin/streborn-armv7l"; then
      if verify_flash "/mnt/nv/streborn/bin/streborn-armv7l"; then return 0; fi
    fi
    rm -f "/mnt/nv/streborn/bin/streborn-armv7l.new"
    sync
  fi
  rm -f "/mnt/nv/streborn/bin/streborn-armv7l"
  sync
  cp "/dev/shm/streborn-ota.stage" "/mnt/nv/streborn/bin/streborn-armv7l" || return 1
  chmod 755 "/mnt/nv/streborn/bin/streborn-armv7l" 2>/dev/null
  verify_flash "/mnt/nv/streborn/bin/streborn-armv7l"
}
killall -9 streborn-armv7l 2>/dev/null
ok=0
if place_binary; then
  ok=1
else
  killall -9 streborn-armv7l 2>/dev/null
  sync
  { echo 3 > /proc/sys/vm/drop_caches; } 2>/dev/null
  sleep 2
  if place_binary; then ok=1; fi
fi
if [ "$ok" != 1 ]; then
  { echo "swap failed: the new agent could not be written to the speaker's storage (flash verify failed twice); STR keeps running from RAM until a retry writes it" > "/mnt/nv/streborn/ota-swap-failed"; } 2>/dev/null
  killall -9 streborn-armv7l 2>/dev/null
  rm -f "/mnt/nv/streborn/bin/streborn-armv7l" "/mnt/nv/streborn/bin/streborn-armv7l.new"
  sync
  run="/dev/shm/streborn-ota.stage"
  if mkdir -p "/dev/shm/streborn-rescue" 2>/dev/null && mv -f "/dev/shm/streborn-ota.stage" "/dev/shm/streborn-rescue/streborn-armv7l" 2>/dev/null; then run="/dev/shm/streborn-rescue/streborn-armv7l"; fi
  { echo "$$" > "/mnt/nv/streborn/agent.pid"; } 2>/dev/null
  sync
rm -f "/tmp/streborn-agent-handover"
"$run" '--listen-webui' ':8888' '--name' 'it'\''s' >> "/tmp/streborn-agent.log" 2>&1 &
bpid=$!
echo "$bpid" > "/mnt/nv/streborn/agent.pid"
wait "$bpid"
ec=$?
if [ "$(cat "/tmp/streborn-agent-handover" 2>/dev/null)" = "$bpid" ]; then rm -f "/tmp/streborn-agent-handover"; exit 0; fi
case "$ec" in
  0) why="clean exit (unexpected for a daemon)" ;;
  12[89]|1[3-9][0-9]) why="killed by signal $((ec-128))" ;;
  *) why="exit status $ec" ;;
esac
echo "[up=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)s] $(date): agent EXITED (started by the update restart helper, pid=$bpid): code=$ec, $why" >> "/mnt/nv/streborn/setup.log" 2>/dev/null
{
  echo "==== agent exit (update restart helper) pid=$bpid code=$ec ($why) $(date) ===="
  tail -n 40 "/tmp/streborn-agent.log" 2>/dev/null
  echo ""
} >> "/mnt/nv/streborn/logs/agent-crash.log" 2>/dev/null
  exit 1
fi
rm -f "/dev/shm/streborn-ota.stage" "/mnt/nv/streborn/ota-swap-failed"
sync
sleep 1
reboot`

const goldenSwapRestart = `i=0
while [ -d /proc/4242 ] && [ "$i" -lt 171 ]; do sleep 1; i=$((i+1)); done
if [ -d /proc/4242 ]; then kill -9 4242 2>/dev/null; sleep 3; fi
if [ -d /proc/4242 ]; then
  echo "swap aborted: agent PID 4242 still alive after kill -9" > "/mnt/nv/streborn/ota-swap-failed"; sync
  rm -f "/dev/shm/streborn-ota.stage"
  exit 1
fi
verify_flash() {
  sync
  { echo 3 > /proc/sys/vm/drop_caches; } 2>/dev/null
  if command -v sha256sum >/dev/null 2>&1; then
    [ "$(sha256sum "$1" 2>/dev/null | cut -d' ' -f1)" = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" ]
  elif command -v cmp >/dev/null 2>&1; then
    cmp -s "/dev/shm/streborn-ota.stage" "$1"
  else
    [ "$(wc -c < "$1" 2>/dev/null | tr -d ' ')" = "18123456" ]
  fi
}
place_binary() {
  rm -f "/mnt/nv/streborn/bin/streborn-armv7l.new"
  avail=$(df -k "/mnt/nv/streborn/bin" 2>/dev/null | awk 'NR>1 && NF>=4 {a=$(NF-2)} END {print a+0}')
  if [ "${avail:-0}" -ge 18723 ] 2>/dev/null; then
    if cp "/dev/shm/streborn-ota.stage" "/mnt/nv/streborn/bin/streborn-armv7l.new" && chmod 755 "/mnt/nv/streborn/bin/streborn-armv7l.new" && verify_flash "/mnt/nv/streborn/bin/streborn-armv7l.new" && mv -f "/mnt/nv/streborn/bin/streborn-armv7l.new" "/mnt/nv/streborn/bin/streborn-armv7l"; then
      if verify_flash "/mnt/nv/streborn/bin/streborn-armv7l"; then return 0; fi
    fi
    rm -f "/mnt/nv/streborn/bin/streborn-armv7l.new"
    sync
  fi
  rm -f "/mnt/nv/streborn/bin/streborn-armv7l"
  sync
  cp "/dev/shm/streborn-ota.stage" "/mnt/nv/streborn/bin/streborn-armv7l" || return 1
  chmod 755 "/mnt/nv/streborn/bin/streborn-armv7l" 2>/dev/null
  verify_flash "/mnt/nv/streborn/bin/streborn-armv7l"
}
killall -9 streborn-armv7l 2>/dev/null
ok=0
if place_binary; then
  ok=1
else
  killall -9 streborn-armv7l 2>/dev/null
  sync
  { echo 3 > /proc/sys/vm/drop_caches; } 2>/dev/null
  sleep 2
  if place_binary; then ok=1; fi
fi
if [ "$ok" != 1 ]; then
  { echo "swap failed: the new agent could not be written to the speaker's storage (flash verify failed twice); STR keeps running from RAM until a retry writes it" > "/mnt/nv/streborn/ota-swap-failed"; } 2>/dev/null
  killall -9 streborn-armv7l 2>/dev/null
  rm -f "/mnt/nv/streborn/bin/streborn-armv7l" "/mnt/nv/streborn/bin/streborn-armv7l.new"
  sync
  run="/dev/shm/streborn-ota.stage"
  if mkdir -p "/dev/shm/streborn-rescue" 2>/dev/null && mv -f "/dev/shm/streborn-ota.stage" "/dev/shm/streborn-rescue/streborn-armv7l" 2>/dev/null; then run="/dev/shm/streborn-rescue/streborn-armv7l"; fi
  { echo "$$" > "/mnt/nv/streborn/agent.pid"; } 2>/dev/null
  sync
rm -f "/tmp/streborn-agent-handover"
"$run" '--listen-webui' ':8888' '--name' 'it'\''s' >> "/tmp/streborn-agent.log" 2>&1 &
bpid=$!
echo "$bpid" > "/mnt/nv/streborn/agent.pid"
wait "$bpid"
ec=$?
if [ "$(cat "/tmp/streborn-agent-handover" 2>/dev/null)" = "$bpid" ]; then rm -f "/tmp/streborn-agent-handover"; exit 0; fi
case "$ec" in
  0) why="clean exit (unexpected for a daemon)" ;;
  12[89]|1[3-9][0-9]) why="killed by signal $((ec-128))" ;;
  *) why="exit status $ec" ;;
esac
echo "[up=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)s] $(date): agent EXITED (started by the update restart helper, pid=$bpid): code=$ec, $why" >> "/mnt/nv/streborn/setup.log" 2>/dev/null
{
  echo "==== agent exit (update restart helper) pid=$bpid code=$ec ($why) $(date) ===="
  tail -n 40 "/tmp/streborn-agent.log" 2>/dev/null
  echo ""
} >> "/mnt/nv/streborn/logs/agent-crash.log" 2>/dev/null
  exit 1
fi
rm -f "/dev/shm/streborn-ota.stage" "/mnt/nv/streborn/ota-swap-failed"
sync
sleep 1
cur=$(cat "/mnt/nv/streborn/agent.pid" 2>/dev/null)
if [ -n "$cur" ] && [ "$cur" != "$$" ]; then exit 0; fi
rm -f "/tmp/streborn-agent-handover"
'/mnt/nv/streborn/bin/streborn-armv7l' '--listen-webui' ':8888' '--name' 'it'\''s' >> "/tmp/streborn-agent.log" 2>&1 &
bpid=$!
echo "$bpid" > "/mnt/nv/streborn/agent.pid"
wait "$bpid"
ec=$?
if [ "$(cat "/tmp/streborn-agent-handover" 2>/dev/null)" = "$bpid" ]; then rm -f "/tmp/streborn-agent-handover"; exit 0; fi
case "$ec" in
  0) why="clean exit (unexpected for a daemon)" ;;
  12[89]|1[3-9][0-9]) why="killed by signal $((ec-128))" ;;
  *) why="exit status $ec" ;;
esac
echo "[up=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)s] $(date): agent EXITED (started by the update restart helper, pid=$bpid): code=$ec, $why" >> "/mnt/nv/streborn/setup.log" 2>/dev/null
{
  echo "==== agent exit (update restart helper) pid=$bpid code=$ec ($why) $(date) ===="
  tail -n 40 "/tmp/streborn-agent.log" 2>/dev/null
  echo ""
} >> "/mnt/nv/streborn/logs/agent-crash.log" 2>/dev/null`

const goldenPlaceBinary = `verify_flash() {
  sync
  { echo 3 > /proc/sys/vm/drop_caches; } 2>/dev/null
  if command -v sha256sum >/dev/null 2>&1; then
    [ "$(sha256sum "$1" 2>/dev/null | cut -d' ' -f1)" = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" ]
  elif command -v cmp >/dev/null 2>&1; then
    cmp -s "/dev/shm/streborn-rescue/streborn-armv7l" "$1"
  else
    [ "$(wc -c < "$1" 2>/dev/null | tr -d ' ')" = "18123456" ]
  fi
}
place_binary() {
  rm -f "/mnt/nv/streborn/bin/streborn-armv7l.new"
  avail=$(df -k "/mnt/nv/streborn/bin" 2>/dev/null | awk 'NR>1 && NF>=4 {a=$(NF-2)} END {print a+0}')
  if [ "${avail:-0}" -ge 18723 ] 2>/dev/null; then
    if cp "/dev/shm/streborn-rescue/streborn-armv7l" "/mnt/nv/streborn/bin/streborn-armv7l.new" && chmod 755 "/mnt/nv/streborn/bin/streborn-armv7l.new" && verify_flash "/mnt/nv/streborn/bin/streborn-armv7l.new" && mv -f "/mnt/nv/streborn/bin/streborn-armv7l.new" "/mnt/nv/streborn/bin/streborn-armv7l"; then
      if verify_flash "/mnt/nv/streborn/bin/streborn-armv7l"; then return 0; fi
    fi
    rm -f "/mnt/nv/streborn/bin/streborn-armv7l.new"
    sync
  fi
  rm -f "/mnt/nv/streborn/bin/streborn-armv7l"
  sync
  cp "/dev/shm/streborn-rescue/streborn-armv7l" "/mnt/nv/streborn/bin/streborn-armv7l" || return 1
  chmod 755 "/mnt/nv/streborn/bin/streborn-armv7l" 2>/dev/null
  verify_flash "/mnt/nv/streborn/bin/streborn-armv7l"
}`
