# Proposal: first install over the stick boot path instead of app SSH

Status: Track 1 (gated in-app auto-install) shipped; Track 2 (true no-SSH first-boot bootstrap) still open, hardware experiment pending. Tracked in a GitHub issue.

## Problem

The desktop app installs the stick agent over an SSH connection to the
speaker. That depends on several fragile conditions at the same time:

1. The box is on the same LAN and reachable.
2. SSH port 22 is open. Bose only opens sshd when the box boots with the
   stick inserted (the `remote_services` marker).
3. Timing: the user has to press "Install" while that access window is
   open.

When any one of these fails, the install aborts with `ssh handshake:
exit status 255` (or similar), and the user has no way to tell which
condition was missing.

Real case (ST10, host `.11`, diagnostic 2026-06-06):
`reachable8090 = true` (box is on the network), `reachableSSH = false`,
`agentVersion = none`. So this is purely a port 22 / timing problem, not
a Wi-Fi or network problem. The install error wrongly blamed the network.
(Partly addressed already: the preflight now probes :8888 and :8090 and
distinguishes "box offline" from "box online, install window closed".)

## What the SSH install actually does today

This is the key finding from reading both paths end to end.

The **stick boot path (`usb-stick/run.sh`) is already fully autonomous.**
With no app and no SSH it: syncs the agent binary to NAND, deploys
`run-override.sh`, runs the complete Wi-Fi provisioning pipeline, applies
region, name and presets, announces over mDNS on :8888, and mirrors a
`setup.log` back to the stick every 25 s.

The **SSH `install.sh` does almost nothing on top of that.** It copies
three things to NAND: `rc.local`, `run-override.sh`, and (first install
only) `presets.json`. Everything else is the boot path's job.

But the one thing it does is the decisive one: it **seeds the first-boot
bootstrap.** Bose firmware runs `/mnt/nv/rc.local` (NAND) at boot, not
anything on the stick. On a factory box `/mnt/nv/rc.local` does not exist
yet. `install.sh` is what writes it. On the `remote_services` stick Bose
opens **sshd and nothing else**, so that SSH channel is the only
first-boot code-execution path STR has. It is not an arbitrary design
choice; it is the bootstrap mechanism.

## The architecture-doc contradiction

`docs/ARCHITECTURE.md` used to show an SSH-free first install (Bose
firmware runs `/media/sda1/rc.local` directly on seeing
`remote_services`). The shipping code does not do that, and
`usb-stick/autostart.sh` is marked deprecated in favour of the
`/mnt/nv/rc.local -> run-override.sh` chain. The diagram has been
corrected to match the real flow (app SSHes in, runs `install.sh`,
reboots, the second boot runs from NAND).

Whether Bose can be made to auto-run a stick script at all is the open
hardware question below.

## The real question

Everything reduces to one thing:

> Can we seed `/mnt/nv/rc.local` (the first-boot bootstrap) without SSH?

- If Bose firmware will autonomously execute some script from the stick
  on boot (the old ARCHITECTURE claim, currently **untested**), then SSH
  can be removed entirely and this proposal is fully viable.
- If it will not (what the shipping code implies), then SSH, or some
  equivalent code-execution channel, is irreducible for the *first*
  install, because there is no other way to run the first command on a
  pristine box.

This cannot be settled from code. It needs a hardware experiment.

## Proposed direction (two tracks)

### Track 1: remove the button and the timing, keep SSH hidden

Most of the UX pain is the manual "Install" button and the timing, not
SSH itself. The app can:

- Continuously detect a stock box with :22 open (stick inserted, OOB
  window) during discovery.
- Run `install.sh` automatically in the background the moment it is
  possible, with no user click.
- Show clear status from :8888 and the stick's `setup.log` (no SSH needed
  to read status).

Resulting UX: prepare stick, insert, power on, app shows "installing",
done. That matches the desired flow except SSH stays as the hidden
transport. This needs no new Bose mechanism and can ship independently.

### Track 2: true no-SSH install

A hardware experiment on the test box: a pristine box (no prior STR, so
`/mnt/nv/rc.local` absent), insert the stick, power on, and observe
whether the agent comes up on :8888 / mDNS **without the app ever
SSHing**. If Bose runs any stick script on its own, find the exact
filename it expects and ship the bootstrap there. If it does not, Track 1
is the ceiling and SSH stays.

### Track 2 alpha (v0.7.36): no-USB-boot via the :17000 diagnostic console

For a box that is on the LAN (Bose :8090 answers) but never opened SSH
because it did not read the stick at boot, the installer now tries the
legacy Bose telnet diagnostic console on **TCP 17000** to enable
`remote_services` without a stick boot, then continues the normal SSH
install. This targets the **SoundTouch 300** (it does not auto-read the
stick at boot) and stubborn **ST30** units. It is experimental and
best-effort: if :17000 does not open SSH the installer falls back to the
existing "boot with the stick" guidance, so nothing regresses.

Caveat: Bose **removed the `remote_services` command on firmware 27.x**
(community RE writeups and `deborahgu/soundcork` #309), so on current
firmware this likely does nothing; it is mainly a live probe. The install
logs the :17000 banner and the console's reply to `help` /
`remote_services on` / `local_services on` into the desktop app log, so a
real SoundTouch 300 test (Save logs) tells us exactly what that firmware's
console still exposes, without the user needing a terminal.

## Sub-questions from the original proposal, answered

- **Status without an SSH channel:** already solved. `run.sh` mirrors
  `setup.log` to the stick every 25 s, and the agent answers on :8888
  once up. The app can read both without SSH.
- **Security:** an automatic/boot-path install changes nothing about the
  current SSH exposure. Pre-1.0, `run.sh` (`ensure_sshd_running`) keeps
  sshd running on every boot as a deliberate debug-visibility choice (so
  diagnostics and repair work when the agent is down); the planned v1.0
  hardening flips this to opt-in via a stick marker. Track 1 and Track 2
  are both neutral to that window.
- **Fallback:** keep the manual SSH install as an expert path in case the
  automatic path does not engage.

## Expected benefit

- Removes the `ssh handshake 255` class of errors from the common path.
- Much simpler UX: insert, power on, done. No timing, no button.
- Less support load (several tickets are exactly this).
