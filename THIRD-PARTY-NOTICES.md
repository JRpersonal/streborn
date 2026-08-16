# Third-party notices

STR is licensed under the terms in [LICENSE](LICENSE). It distributes the
components below, each under its own licence.

This file covers what is **distributed**: the bundled Spotify engine, the
certificate store, and the libraries linked into the released binaries. Build
tools that never reach a user are not listed.

---

## go-librespot

**What is distributed:** `go-librespot-armv7l`, a separate executable shipped
as a release asset and embedded in the desktop app, which installs it on the
speaker.

**Licence:** GNU General Public License v3.0
(<https://www.gnu.org/licenses/gpl-3.0.html>)

**Upstream:** devgianlu/go-librespot, <https://github.com/devgianlu/go-librespot>

**The source of the binary STR ships:** STR builds it from its own fork,
<https://github.com/JRpersonal/go-librespot>, which carries the Ogg passthrough
the speaker needs. The build workflow
(`.github/workflows/go-librespot.yml`) records the exact commit in its log, and
the fork is public, so the complete corresponding source of every binary STR
distributes is available there under the same licence. Changes made in the
fork are offered upstream; see `docs/streaming/spotify.md`.

go-librespot is a **separate program**. It is not linked into the STR agent or
the desktop app: STR starts it as its own process and talks to it over a local
socket. Distributing the two together is aggregation, so the GPL applies to
go-librespot and its fork, not to STR.

---

## Mozilla CA certificate store

**What is distributed:** `desktop-app/cacert.pem` and
`internal/tlsgen/extraroots.pem`, the latter embedded in the agent binary.

**Licence:** Mozilla Public License 2.0 (<https://www.mozilla.org/MPL/2.0/>)

**Source:** Mozilla's root certificate authorities as extracted and
redistributed by the curl project,
<https://curl.se/docs/caextract.html>. Both files carry the extraction date in
their header.

`internal/tlsgen/extraroots.pem` is a pinned subset of `desktop-app/cacert.pem`,
regenerated with `make ca-roots`. The selection is in
`internal/tlsgen/extractroots.py`, so every change to the set of authorities
STR carries is a reviewed edit.

**Why STR ships them.** Some SoundTouch speakers were manufactured with a
certificate store that is missing widely used authorities, so they refuse most
https radio stations. That file belongs to the speaker's read-only firmware, a
factory reset cannot change it, and Bose's update servers are gone. The agent
composes a bundle in memory from the speaker's own store plus these roots and
points its own connections at it. Nothing on the speaker is modified, and what
the Bose firmware trusts is left exactly as Bose shipped it. See
`internal/tlsgen/extraroots.go` and [docs/THREAT-MODEL.md](docs/THREAT-MODEL.md).

---

## Libraries linked into the STR binaries

| Component | Licence | Source |
|---|---|---|
| Go standard library and toolchain | BSD-3-Clause | <https://go.dev> |
| Wails | MIT | <https://wails.io> |
| gorilla/websocket | BSD-2-Clause | <https://github.com/gorilla/websocket> |
| grandcat/zeroconf | MIT | <https://github.com/grandcat/zeroconf> |
| golang.org/x/sys | BSD-3-Clause | <https://pkg.go.dev/golang.org/x/sys> |
| Octicons | MIT | <https://github.com/primer/octicons> |

The BSD and MIT licences require their copyright notice and permission notice
to travel with binary distributions, which is what this file is for. The full
licence texts are in each project's repository at the addresses above.

---

## Services STR talks to

Not distributed, listed because the application depends on them:

- **radio-browser.info**, the community station directory,
  <https://www.radio-browser.info>
- **DuckDuckGo icon service**, used for station logos, <https://duckduckgo.com>

---

## Reverse engineering credits

STR ships no code from these projects, but several of its central mechanisms
would not exist without their published work. They are named in the
application's Open Source dialog.
