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

1. A small Go agent installed onto a Bose SoundTouch speaker via a
   USB stick. After the install the agent runs from the speaker's
   own persistent storage; the stick is only needed for setup and
   later for OTA agent updates.
2. A cross-platform desktop application that discovers sticks via
   mDNS and offers a web UI.
3. A static website that hosts downloads and documentation.

The agent emulates a handful of cloud endpoints on the loopback
interface of the speaker, so the speaker accepts a locally generated
TLS certificate and resolves a small set of hostnames to
`127.0.0.1`. STR does not modify the broader LAN, does not act as a
DNS server for any other device, and does not phone home.

## Trust boundaries

- **Binary release** the user downloads.
- **Code that runs on the speaker** with root privileges.
- **Local TLS certificate authority** that the agent installs in the
  speaker's own trust store.
- **GitHub repository** and its CI build artifacts.
- **Website** that hosts download links.

Each boundary is covered in detail in
[`SECURITY.md`](../SECURITY.md).

## What STR mitigates

- The discontinued Bose cloud cannot phone home, exfiltrate, or be
  impersonated by a third party against the speaker: the agent
  serves the stand-in endpoints only on the loopback interface of
  the speaker.
- The locally issued TLS certificate is generated on first boot,
  stored in NAND, never transmitted, and is only valid for the
  hostnames the speaker resolves to `127.0.0.1`.
- All audio traffic uses the speaker's existing UPnP AVTransport
  endpoint. STR never proxies audio through an external service.

## Inherited speaker firmware caveats

STR runs on top of stock Bose SoundTouch firmware. STR does not
remove pre-existing weaknesses of that firmware. Users on a shared
or untrusted LAN should be aware of the following before installing
STR:

- The speaker's stock firmware exposes a small number of services on
  the LAN (HTTP control on `:8090`, WebSocket on `:8080`, UPnP on
  `:8091`) with no client authentication. Any device on the LAN can
  control playback. This is unchanged by STR.
- Some firmware revisions ship with administrative remote-access
  facilities enabled by default. These exist independently of STR
  and are out of scope of this project to enable, document in
  detail, or distribute credentials for.

If your speaker is on a network with untrusted devices (guest Wi-Fi,
shared student housing, public-facing IoT segment), put it on a
dedicated VLAN or trusted SSID before installing STR. The same
advice applies whether or not STR is installed.

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
- STR does not authenticate clients of the stick web UI. Any device
  on the LAN that can reach the stick on `:8888` can edit presets
  and trigger playback.
- STR does not sandbox the desktop application. It runs with the
  privileges of the user who launched it.
- STR does not attempt to verify the integrity of the stock speaker
  firmware. If the firmware is compromised, STR cannot detect it.

These are documented limitations, not bugs. If you need any of these
properties, isolate the speaker on its own network segment.

## Reporting

Security reports go to the address listed in
[`SECURITY.md`](../SECURITY.md), not to a public GitHub issue.
