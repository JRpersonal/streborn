# STR Threat Model

This document describes what STR protects against, what it does not,
and the security caveats inherited from the underlying speaker
firmware. It is written for users deciding whether to install STR
and for contributors touching the parts of the codebase that affect
the trust boundary.

For the user-facing reporting process, see
[`SECURITY.md`](../SECURITY.md). This file is the technical
companion.

## Scope

STR runs in a residential LAN and consists of:

1. A small Go agent installed onto a Bose SoundTouch speaker,
   normally over the network by the desktop app (via the speaker's
   setup service on `:17000`); a USB stick remains available as the
   fallback and recovery install path. After the install the agent
   runs from the speaker's own persistent storage. Agent updates are
   delivered over the LAN by the desktop app (a stick that is still
   inserted gets refreshed so it cannot revert the update on the
   next boot).
2. A cross-platform desktop application that discovers running
   agents via mDNS and offers a web UI.
3. A static website that hosts downloads and documentation.

The agent emulates a handful of cloud endpoints which the speaker
reaches via `127.0.0.1`: a small set of Bose hostnames is pinned to
loopback in the speaker's `/etc/hosts`, and the speaker accepts a
locally generated TLS certificate for them. The stand-in listeners
themselves bind all interfaces and are LAN-reachable like the rest of
the agent surface. STR does not modify the broader LAN, does not act
as a DNS server for any other device, and does not phone home.

## Trust boundaries

- **Binary release** the user downloads.
- **Code that runs on the speaker** with root privileges.
- **Local TLS certificate authority** that the agent installs in the
  speaker's own trust store.
- **GitHub repository** and its CI build artifacts.
- **Website** that hosts download links.
- **Third-party radio catalogue (radio-browser.info).** Station
  entries (name, tags, homepage, favicon URL, stream URL) are
  community-submitted and fully untrusted. They flow into the desktop
  app's webview and into the speaker (playback + presets), so they are
  treated as an attacker-controlled input boundary.
- **Bundled go-librespot sidecar and its credentials.** The Spotify
  Connect integration runs a third-party GPL binary on the speaker,
  built by this repo's CI from a pinned fork and Sigstore-attested,
  and persists Spotify credentials on the speaker's NAND for preset
  recall. Anyone with root SSH access (open while the install stick
  is inserted, or when the persistent opt-in marker is set) can read
  those credentials, which is part of why SSH is kept opt-in.

Each boundary is covered in detail in
[`SECURITY.md`](../SECURITY.md).

## What STR mitigates

- The discontinued Bose cloud cannot phone home, exfiltrate, or be
  impersonated by a third party against the speaker: the speaker
  resolves the Bose hostnames to `127.0.0.1` via pinned `/etc/hosts`
  entries and only ever reaches STR's local stand-in.
- The locally issued TLS certificate is generated on first boot,
  stored in NAND, never transmitted, and is only valid for the
  hostnames the speaker resolves to `127.0.0.1`.
- All audio traffic uses the speaker's existing UPnP AVTransport
  endpoint. STR never proxies audio through an external service.
- Untrusted radio-catalogue fields are escaped before they reach the
  desktop app's DOM (HTML/attribute escaping), the favicon URL is only
  ever placed in escaped data-attributes and the live `<img src>` is set
  to a Go-validated URL, and a strict Content-Security-Policy (no
  `unsafe-inline` script) means an injected handler could not reach the
  Wails Go bindings even if escaping were bypassed.
- The stream proxy refuses to fetch loopback/link-local/unspecified
  addresses (incl. the 169.254.169.254 cloud-metadata IP) at dial time,
  so a malicious station stream URL cannot turn the agent into an SSRF
  vector against the speaker's own privileged services. Private LAN
  ranges stay reachable so a user's local Icecast/DLNA stream still
  plays.
- Every URL the agent hands to the speaker's UPnP renderer or fetches
  through the stream proxy (including HLS playlist segments) is
  restricted to `http`/`https` schemes (`safeHTTPURL`), so `file://`
  and friends never reach the Bose renderer.
- State-changing and diagnostic webui endpoints (agent OTA update,
  reboot, WLAN reconfigure, AirPlay toggle, debug state/probe) only
  accept requests from private LAN source addresses (`isLocalLAN`);
  there is still no client authentication (see below), but these
  endpoints are at least source-gated.

## Inherited speaker firmware caveats

STR runs on top of stock Bose SoundTouch firmware. STR does not
remove pre-existing weaknesses of that firmware. Users on a shared
or untrusted LAN should be aware of the following before installing
STR:

- The speaker's stock firmware exposes a small number of services on
  the LAN (HTTP control on `:8090`, WebSocket on `:8080`, UPnP on
  `:8091`) with no client authentication. Any device on the LAN can
  control playback. This is unchanged by STR.
- The speakers ship with a passwordless `root` account, and Bose's
  init starts an SSH service when a `remote_services` marker is
  present on a mounted USB stick. STR follows that gate: SSH is open
  while the STR stick is inserted (install / recovery / diagnostics)
  and closes again on the next stickless reboot; STR no longer
  forces SSH open on every boot. A maintainer who needs persistent
  debug SSH on a stickless box can opt back in by creating
  `/mnt/nv/streborn/enable-ssh` (or a `/mnt/nv/remote_services`
  marker); a reboot does not close SSH in that case. The desktop
  app's yellow banner reflects the actual SSH state the agent
  reports: it recommends a restart while SSH is still open after the
  stick was pulled, and shows a distinct notice when the persistent
  marker is set.

- **The agent can also be asked to open SSH, by anyone on the LAN.**
  `POST /api/agent/enable-ssh` writes the `remote_services` marker and
  starts sshd, and its only gate is that the caller's address is on the
  local network. There is no authentication, and the root account has no
  password, so any device already on the network can obtain a root shell
  on the speaker. The endpoint exists because the desktop app needs it to
  uninstall STR from a stickless speaker, which otherwise cannot be
  undone without opening the box up.

  This is worth stating plainly because it is easy to describe STR as
  "keeping SSH closed" and be wrong: the stick gate is one of two ways in,
  and the other one needs nothing but LAN access. It also raises what an
  attacker on the LAN gets, from controlling playback (which the stock
  firmware already allows, see above) to running code as root.

  The practical mitigation is the same one that covers the unauthenticated
  firmware services: keep the speakers on a network segment you trust. A
  segment, not a guest network, since guest networks usually isolate
  clients from one another and that breaks discovery and multiroom.

If your speaker is on a network with untrusted devices (guest Wi-Fi,
shared student housing, public-facing IoT segment), put it on a
dedicated VLAN or trusted SSID before installing STR. The same
advice applies whether or not STR is installed.

## Supplied certificate roots

Some speakers were manufactured with a certificate store that is missing widely
used authorities. On the `scm` chassis, `/etc/pki/tls/certs/ca-bundle.crt`
carries 2 certificates where the `sm2` chassis carries 166, and the thin set
lacks DigiCert Global Root G2 and ISRG Root X1. Such a speaker refuses most
https radio stations, and the Spotify engine fails on its first call. The file
belongs to the read-only firmware, a factory reset does not touch it, and
Bose's update servers no longer answer, so nothing on the speaker can restore
it.

The agent therefore carries a pinned set of public root certificates
(`internal/tlsgen/extraroots.pem`, Mozilla's roots via the curl project, see
[THIRD-PARTY-NOTICES.md](../THIRD-PARTY-NOTICES.md)). At startup it reads the
speaker's own stores, appends only the roots that are genuinely absent, writes
the result to tmpfs and points its own process at it with `SSL_CERT_FILE`. The
Spotify engine inherits that through the environment.

What this means for trust:

- **The speaker's own trust is unchanged.** Nothing is mounted, nothing under
  `/etc` is modified, nothing is written to NAND. The Bose firmware, its app
  and its native services keep exactly the certificate authorities Bose
  shipped, and remain as limited as before.
- **It is additive, never subtractive.** `SSL_CERT_FILE` replaces only the file
  half of Go's certificate loader; the directory scan is governed by
  `SSL_CERT_DIR`, which STR never sets. The composed bundle is the speaker's
  own store plus the missing roots, and it is read back and counted before it
  is used: if it would hold fewer usable certificates than the store it was
  built from, it is discarded and nothing changes.
- **The set is frozen between releases.** An authority that is distrusted after
  a build keeps being trusted by STR's own connections until the speaker is
  updated. The set is deliberately short so it can be reviewed, and
  authorities that the wider ecosystem is retiring are not included.
- **It can be refused.** A file at `/mnt/nv/streborn/no-extra-roots` stops it,
  and the diagnostic reports whether anything was added and why not.

## Hardening roadmap

The following items are planned before STR is recommended for users
who cannot evaluate the speaker firmware caveats above on their own.
Issues and PRs welcome.

- Provision a fresh credential for any administrative interface the
  stock firmware exposes, on the first boot of the agent, and store
  the resulting state inside NAND so it survives reboots.
- Where the firmware permits, disable administrative remote access
  entirely once provisioning is complete.
- Surface a clear status indicator in the desktop app for whether
  the hardening step has run successfully on each discovered
  speaker.
- Publish a step-by-step user guide for putting the speaker on its
  own VLAN, for users who prefer network-level isolation over
  agent-managed hardening.

The corresponding code lives under `internal/autopair/`,
`internal/tlsgen/`, `usb-stick/setup-tls.sh`, and
`usb-stick/iptables-setup.sh`. Changes to those paths require
coordinated review per `.github/CODEOWNERS`.

## What STR does not do

- STR does not encrypt traffic between the desktop app and the
  stick. Both sit on the same LAN, and the LAN is the trust
  boundary.
- STR does not authenticate clients of the agent web UI. Any device
  on the LAN that can reach the agent (`:8888`, or `:17008` on
  whitelisted/BCO-chassis boxes) can edit presets and trigger playback.
- STR does not sandbox the desktop application. It runs with the
  privileges of the user who launched it.
- STR does not attempt to verify the integrity of the stock speaker
  firmware. If the firmware is compromised, STR cannot detect it.

These are documented limitations, not bugs. If you need any of these
properties, isolate the speaker on its own network segment.

## Reporting

Security reports go to the address listed in
[`SECURITY.md`](../SECURITY.md), not to a public GitHub issue.
