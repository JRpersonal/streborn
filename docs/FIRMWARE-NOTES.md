# Firmware notes: living with the stock SoundTouch firmware

STR runs on top of the unmodified Bose SoundTouch firmware. Reviving
the speakers after the cloud shutdown meant learning a lot about how
that firmware behaves once its cloud is gone. This page collects the
hard-won, reproducible findings so other people working on these
speakers do not have to rediscover them.

Everything here is observed runtime behaviour (service names, local
port numbers, process states). It contains **no** Bose code, no
firmware binaries, and no decompilation. All device identifiers, IPs,
and MACs are placeholders.

In August 2026 these notes were cross-checked against the official
Bose SoundTouch Web API reference (v1.1, dated 2026-04-01). Entries
from that pass carry one of two provenance markers. **Doc-confirmed**
means that reference states the same thing; only its identifiers are
reproduced here, never its text. **Field wins** means the doc says one
thing, the live FW 27.0.6 fleet demonstrably does another, and STR
follows the field observation, with the citation given. The field-wins
entries are the valuable ones: do not "fix" STR toward the doc on any
of them.

## The Portable ~27-minute reboot loop (and how STR fixes it)

**Symptom.** On the SoundTouch Portable, internet radio would stop and
the speaker would reboot itself roughly every 27 minutes. Other models
(ST10/20/30) were not affected.

**Root cause, pinned live with `strace` + `/proc`:**

1. The Portable's battery service, `BatteryMonitor` (the local Bose
   service registered at `127.0.0.1:17002` in
   `/opt/Bose/etc/services.json`), reads the battery's identity chip
   over I2C and looks up a matching "battery personality" module. The
   battery on the test unit reports type `BOSE_A`, which this firmware
   build has no personality for (it knows `BOSE_ICC`, `BOSE_SANYO`,
   `BOSE_SERVICES`). It logs `CRITICAL: No battery personality module
   for BOSE_A` and its main thread then parks on a futex forever. The
   `:17002` listener is never opened. This is deterministic: killing the
   process makes the supervisor respawn it, and it re-deadlocks at once.
2. `BoseApp` (the main firmware app) runs a battery UI client that wants
   `:17002`. With nothing listening it retries `connect()` in a tight
   loop (~137 failed attempts/second), and each failed attempt leaves a
   new client thread pair blocked in `poll`, each holding one eventfd +
   one timerfd that is never reaped.
3. That leaks ~30 file descriptors/minute. When `BoseApp`'s open-fd
   count reaches its internal ~1024 `select()`/`FD_SETSIZE` ceiling, its
   `:8090` HTTP API deadlocks and the Bose watchdog reboots the box.
   ~27 minutes per cycle.

**The fix (STR v0.6.18).** The retry storm is driven purely by
`connect()` *failing*. The instant anything accepts on `:17002`,
`BoseApp`'s client connects and blocks reading instead of spawning a
new leaking thread, the fd/thread count plateaus, and the box stays up.
So the STR agent itself listens on `127.0.0.1:17002` as a fallback when
the port is unserved, accepts the battery client, and drains the
connection. It waits a short grace period and only binds when the port
is free, so on a box whose `BatteryMonitor` is healthy the real service
keeps the port and the agent stays out of the way. On models with no
battery, nothing connects and the listener sits idle. See
`cmd/agent/boseapp_recovery.go`.

**Ruled out along the way** (so nobody re-derives them): it is **not**
STR's `/etc/hosts` cloud redirect (with the redirect off the leak rate
and reboot interval were identical), **not** the STR agent / gabbo
connection (killing the agent did not change the leak), and **not**
diagnostic probing. It is the stock firmware reacting to an
unrecognised battery, which STR papers over.

This also explains the "battery always shows 50%" cosmetic issue on the
same unit: with `BatteryMonitor` dead, `BoseApp` never receives real
battery data. Restoring the real percentage would require replaying the
proprietary `:17002` push protocol; the reboot fix does not attempt it.

## Hardware preset buttons: the gabbo bus

The speaker exposes an internal WebSocket IPC bus on
`ws://127.0.0.1:8080/` with subprotocol `gabbo`. Physical preset-button
presses and connection-state changes are published there. STR subscribes
(read-only) and, on a `nowSelectionUpdated` / preset event, drives
playback over UPnP. This is how hardware buttons 1 to 6 come back to life
without any cloud. See `internal/boxws/boxws.go`.

**Connection lifetime lore (corrected 2026-07-27):** the long-standing
belief that "the firmware drops an idle gabbo socket every ~10 minutes"
was wrong on two counts. The original ~10.5 min drops were real, but the
June 2026 keepalive "fix" only appeared to help: without a pong handler,
gorilla/websocket consumed the firmware's pong replies inside
`ReadMessage`, the read deadline was never refreshed, and the CLIENT tore
down its own healthy connection every ~11.2 min (machine-regular 674.5 s
cadence in field bundles, zero `connection lost` warnings because a
client-side timeout logs differently than a peer drop). Since v0.9.21 the
pong handler refreshes the deadline and the connection is genuinely
persistent; the firmware answers protocol pings indefinitely. Every gap
had been a 10-14 s window that lost a hardware press (#435 feeder) and a
log-churn source that rotated the 32 KB NAND log in ~3.5 h.

### The official notification names (doc-confirmed)

The official doc confirms the `gabbo` subprotocol on `:8080` and maps
its internal notification names onto the wire elements STR parses:

| Doc notification name   | Wire element                |
| ----------------------- | --------------------------- |
| VolumeChange            | `volumeUpdated`             |
| BassChange              | `bassUpdated`               |
| NetworkConnectionStatus | `connectionStateUpdated`    |
| NowSelectionChange      | `nowSelectionUpdated` (a `preset` child carrying a `ContentItem`) |
| SourcesChange           | `sourcesUpdated`            |
| InfoChange              | `infoUpdated`               |
| ZoneMapChange           | `zoneUpdated`               |
| PresetsChangedNotifyUI  | `presetsUpdated`            |
| NowPlayingChange        | `nowPlayingUpdated`         |
| AcctModeChangedNotifyUI | `acctModeUpdated`           |

The doc additionally lists `swUpdateStatusUpdated`,
`siteSurveyResultsUpdated`, and `recentsUpdated`.

### The doc's blind spots (field-only frames)

The doc's notification chapter is provably not exhaustive for
FW 27.0.6. All of these are field-observed and appear nowhere in the
doc: `presetSelectionUpdated`, `powerStateUpdated`, `groupUpdated`,
`languageUpdated`, `balanceUpdated`, `userInactivityUpdate`, the
bare-root forms of `userActivityUpdate` and `errorUpdate`, and the
`signal` attribute on `connectionStateUpdated`. The blind spot runs the
other way too: no field log has ever shown the doc's value-carrying
`<updates><volume>` frame, which would surface in the
unrecognized-frame log as shape `updates/volume` and never has.
Consequence: the unrecognized-frame capture stays; the doc's list can
never replace it.

### Frame shape traps

- **`zoneUpdated` carries a body (field wins).** The doc shows the
  frame bodyless in every sample. FW 27.0.6 sends a full `<zone>`
  body, and an empty `<zone/>` is the dissolution signal (#70, field
  capture 2026-06-12). Do not simplify the parse toward the doc.
- **`playStatus` is dual-shape (field wins).** The doc shows it as a
  child element only; live firmware builds also ship it as an
  attribute. The dual capture in `wsNowPlaying` stays.
- **`ContentItem` case trap (doc-confirmed shapes).** `presetsUpdated`
  nests `ContentItem`; `recentsUpdated` nests lowercase `contentItem`.
  Go's `encoding/xml` matches case-sensitively, so a future recents
  parser must not reuse `wsPreset`.
- **`updatedOn` vs `updateOn` (doc-internal typo).** The doc's presets
  endpoint chapter spells the timestamp attribute `updateOn` while its
  notification chapter spells it `updatedOn`. STR emits `updatedOn`,
  the spelling proven against the installed base; do not "correct" it
  to the other one.

## Reaching the agent on BCO speakers (chipset whitelist)

On the newer "BCO" chassis (the Portable, and every `scm`-module
chassis observed so far — the scm revisions of the ST20, ST30 and Wave,
plus the SA-4 — as well as `sm2` boxes carrying the SMSC bridge)
the network chipset only routes inbound external TCP to listeners owned
by a Bose binary. A normal listener like the STR agent on `:8888` is not
reachable from the LAN as-is. STR works around this two ways:

- An `iptables` PREROUTING `REDIRECT` maps an externally reachable,
  Bose-owned port to the agent (the path STR uses on BCO today).
- An `LD_PRELOAD` shim (`usb-stick/shim/shim.c`, built from source on
  every release) can hook `accept()` inside a Bose process to forward
  connections. It is **skipped on every catalogued chassis today**: on
  whitelisted chassis (Portable `taigan` AND ST20 `spotty`) it races the
  firmware's service-init and wedges boot, and on the SM2 chassis
  (`rhino`, `mojo`) it is unnecessary and cannot even load on `mojo`
  (live ST30, #123). The iptables REDIRECT is the production path
  everywhere it matters; the shim remains only as a fallback for
  uncatalogued variants (`STR_FORCE_SHIM_TAIGAN=1` to force it).

The SM2 chassis (ST10 `rhino`, ST30 `mojo` — labelled "Series-II" in
`MODELS.md`, `is_series_one=0` in `run.sh`) does not need the REDIRECT;
its agent is reachable directly once `run.sh` opens `:8888` with an
`INPUT ACCEPT` rule. Note the label inversion: `run.sh`'s
`detect_series_one` returns 1 for the *whitelisted* chassis
(`taigan`/`spotty`/`scm`), not for `rhino`.

## Bose internal HTTP buffer cap

Bose's internal HTTP library (used by `BoseApp` on `:8090` and the
SoftwareUpdate service on `:17008`) caps a POST at ~1536 bytes including
the request line and headers. Any STR call routed through `:17008`
without an active shim must stay under that, which is why the agent OTA
has an SSH fallback for the binary upload. The official Web API doc
documents no request size limit anywhere; the measured cap is field
knowledge and stands.

## The `:8090` HTTP API vs the official doc

### The error envelope and the code table

An HTTP error is an `<errors deviceID="...">` envelope wrapping one or
more `<error value="..." name="..." severity="...">` elements; a
malformed request can instead get a bare `<error>` body with no
envelope (doc-confirmed). The gabbo `errorUpdate` frame shares only the
inner `<error>` element shape; the doc's own section on that frame is
empty in the text extraction we have.

The doc names exactly one error code: 1019 `CLIENT_XML_ERROR`. Every
other code STR handles is field-learned and appears nowhere in the
doc, which makes this table the authoritative list:

| Code | Name (field-learned) | Where it shows up |
| ---- | -------------------- | ----------------- |
| 1005 | `UNKNOWN_SOURCE_ERROR` | selecting a source the firmware has no live registration for |
| 1036 | `UNABLE_TO_PROCESS_NOT_LOGGED_IN` | recalling or selecting a source without a live marge login; often paired with an `UpnpRcvdContentItemInWrongState` marker |
| 3101 | `AUDIO_ERROR_BAD_URL` | stale or unplayable stream URL on recall |
| 3103 | `AUDIO_ERROR_TIMEOUT` | the stream did not start in time |
| 4502 | `BMX_JSON_PARSE_ERROR` | malformed body on the BMX JSON paths (#600) |
| 5510 | `GROUP_ALREADY_EXISTS` | `/addGroup` against a stale stereo pair |
| 5580 | `GROUP_CREATE_GROUP_ON_MARGE_ERROR` | `/addGroup` while the box's marge session is broken |

### Status enums: values doc-confirmed, one semantic field-corrected

- **`PLAY_STATUS` is a closed five-value enum** (doc-confirmed):
  `PLAY_STATE`, `PAUSE_STATE`, `STOP_STATE`, `BUFFERING_STATE`,
  `INVALID_PLAY_STATUS`. STR's busy/idle discriminators over it are
  therefore provably exhaustive, not best-effort.
- **`ART_STATUS`** (doc-confirmed): `INVALID`, `SHOW_DEFAULT_IMAGE`,
  `DOWNLOADING`, `IMAGE_PRESENT`. Field caveat: `IMAGE_PRESENT`
  promises nothing about rendering. The speaker reports it for SVG and
  ICO URLs its display cannot draw, and on native radio it reports it
  and then never fetches the image at all (see the display-logo
  section below). STR's raster preference at preset-save time stays.
- **`SOURCE_STATUS` is exactly `{UNAVAILABLE, READY}`**
  (doc-confirmed), **but its meaning is not what the doc says (field
  wins).** The doc reads the `/sources` status as availability; on
  27.0.6 it is a connection indicator. `UPNP` reports `UNAVAILABLE`
  while it is actively playing, and unpaired Bluetooth reports
  `UNAVAILABLE` too. This mismatch is the root of the native-vs-UPnP
  preset split and the whole 1036 story, so STR applies it per path:
  `READY` is required before writing native `LOCAL_INTERNET_RADIO`
  presets, and `UNAVAILABLE` is treated as dead only for
  account-linked cloud presets during write-back, because the firmware
  itself drops those. The doc also omits the `isLocal` and
  `multiroomallowed` attributes STR parses off `/sources`.

### Endpoint truths

- **`/setZone` takes a slaves-only member list (field wins).** The
  doc's zone samples put the master into the member list. FW 27.0.6 on
  `rhino` and `spotty` silently rejects that body: after commit
  df7764a shipped the doc shape, a live fleet check showed an empty
  liveMaster and zero live members everywhere, and 7e58171 reverted
  it. Do not "align" the zone body with the doc.
- **`/nowPlaying` vs `/now_playing`.** The doc spells the playback
  read `/nowPlaying` and offers `/trackInfo` as an identical-shape
  alias. The underscore `/now_playing` STR uses everywhere is a live
  firmware alias the doc omits; both spellings answer on live boxes,
  while `/trackInfo` rests on the doc's word alone, untested here.
  Renaming for conformance would be churn with zero gain.
- **`/presets` is officially GET-only** (doc-confirmed). The TAP CLI
  write path STR uses is the only preset write path there is, not a
  workaround for a missed HTTP call.
- **`/key` press-then-release with a `sender` attribute** is exactly
  what STR sends (doc-confirmed). The doc has no power endpoint beyond
  the POWER key and defines no power-state notification. The field is
  split by firmware build: some send the field-only
  `powerStateUpdated` (blind-spot list above) on a power press, while
  the Portable (taigan) on 27.0.6 sends no dedicated power frame at
  all (verified live 2026-06-13); a press then surfaces only through
  generic frames (a now-selection restore, a `userActivityUpdate`,
  varying per chassis), which is why `internal/boxws` listens for the
  dedicated frame and its stand-ins alike. The real
  power-off STR relies on is the undocumented GET `/standby`, where
  POST answers 400.
- **`/volume` POST accepts a `muteenabled` child**, applied before the
  volume value; per the doc the box unmutes only when the posted
  volume exceeds the current one. Unverified on 27.0.6: STR never
  writes mute, so nothing depends on it. Recorded so nobody trusts it
  untested.

### Endpoints the doc does not know exist

None of these appear in the doc at all; they are field knowledge:
`/standby`, `/balance`, the `/getGroup` family, `/networkInfo`,
`/clockDisplay`, `/language`, `/setup`, `/getActiveWirelessProfile`,
`/performWirelessSiteSurvey`, `/listMediaServers`,
`/setMusicServiceAccount`, `/navigate`, `/supportedURLs`,
`/setMargeAccount`.

## Discovery on the wire: mDNS and SSDP

- **mDNS `_soundtouch._tcp.local` is the sanctioned service type**
  (doc-confirmed), and STR browses it already. The
  `_bose-soundtouch._tcp` alias STR also browses appears nowhere in
  the doc: it rests on field observation alone, so keep it or drop it
  on field evidence only, never on the doc's authority.
- **SSDP identifiers** (doc-confirmed on paper, not yet
  Wireshark-verified on FW 27.0.6): speakers are providers under
  `urn:schemas-upnp-org:device:MediaRenderer:1`, send NOTIFY
  `ssdp:alive` / `ssdp:byebye`, and advertise a `CACHE-CONTROL`
  max-age of at least 1800 s. Until a packet capture on 27.0.6
  confirms this, treat it as the doc's word, not the fleet's.
- **Announce expiry (field wins, deliberately).** The doc mandates
  dropping a device when its max-age expires. STR retains expired
  announces for 24 hours on purpose, because one lost multicast
  datagram once cost a user their media library (#341). The deviation
  is intentional and stays.

## NAND override beats the SD card

The SD card the firmware boots from is unreliable for writes. STR
installs `/mnt/nv/streborn/run-override.sh` on the speaker's NAND, which
the boot path runs **in place of** the SD-based entry point. Treat the
SD card as read-only. Do not re-exec `run-override.sh` while it is
already running: it collides with the Bose service manager and leaves the
speaker in a bad state.

## A Bose "factory reset" does not remove STR

The on-device factory reset (and the Bose app's reset) clears only what
Bose tracks: pairing, account, friendly name, Wi-Fi profile. It does
**not** touch `/mnt/nv/streborn/`. After a reset, STR is still installed
and boots automatically once the brief setup-AP window times out.
Removing STR is therefore a separate, explicit "Uninstall STR" step.

## No battery-backed clock: TLS breaks after a cold boot

SoundTouch speakers have no battery-backed RTC. On a cold boot the kernel
clock starts in the firmware's build epoch (observed as mid-2015) and only
jumps forward once NTP syncs, which can be delayed or, on locked-down
networks, never happens. While the clock is stuck in the past, Go's default
TLS verifier rejects every HTTPS upstream as "certificate is not yet valid":
the cert's `NotBefore` (a real 2026 date) is in the future relative to the
box's 2015 clock. The visible symptom is that plain-HTTP radio (e.g. some BBC
streams) plays but HTTPS radio (e.g. Virgin Radio) and the Spotify sidecar's
`apresolve.spotify.com` fetch do not (#296).

The stream proxy mitigates this for radio: when the local clock is
implausibly old it still verifies the certificate chain to the system roots
and the hostname, but relaxes the time-validity window (see
`clockTolerantTLSConfig` in `internal/streamproxy/tlsclock.go`). Verification
tightens again automatically once the clock is corrected. The agent
additionally corrects an implausibly old clock at start and keeps retrying
from an HTTP Date header until a sane time is set
(`internal/clocksync`, #296/#375), which also covers the Spotify sidecar.

**A plug-pull boot can stay poisoned even after the clock heals** (#419
Finding 4, on-site ST30 capture): the Bose firmware processes start on the
2015 clock, and on such boots every playback died within 2-13 s for the
whole boot even though the clock was corrected shortly after; only a soft
reboot (API-triggered, clock stays sane) cured it, reproduced twice. The
agent logs `clock forensics` markers (implausible-at-start, healed-after-
firmware-boot) and exposes `clock_status` in `/api/debug/state` so bundles
show exactly this sequence. Practical rule: after a wall power-cycle that
misbehaves, prefer a software reboot over another plug pull.

## Deep standby: what resets the countdown

SoundTouch speakers drop into a deep standby (network fully off, woken
only at the device) after a long idle period. Which activity resets that
countdown was pinned down via #119 (ST30 fleet bundles, 2026-07-26):

- **Box-API READS do not block deep standby.** v0.9.16 speakers deep
  slept fine under 5-minute read-only heartbeats and periodic
  `GET /presets` reconciles.
- **Box-API WRITES reset the countdown.** From v0.9.17 the (then
  ~11-minute) gabbo reconnect cycle scheduled a forced key re-sync with
  two blind `AddPreset` writes per cycle; fleet boxes stopped deep
  sleeping entirely (`/proc/uptime` spanning days). v0.9.21 removes the
  reconnect churn and skips the forced re-sync while a box demonstrably
  idles in standby.

Standing rule for all future work: any feature that would add periodic
box-API writes must be checked against this countdown first. Keep-awake
mechanisms are explicitly out of scope for STR; a deep-sleeping speaker
being unreachable over the network is correct behavior, surfaced in the
UIs as a dimmed sticky tile.

## The speaker display shows the source logo, never the station logo

On a native radio preset the speaker's own display shows the STR mark for
every station. This is not a bug in the artwork URL and it is not a
substitution: **the firmware never fetches per-station art on this path.**

What it does fetch, once, 0.2 s after it reads the BMX service registry
during pairing, is the two service icons
(`/media/bmx-icons/orion/monochrome_v2.png`,
`/media/bmx-icons/tunein/monochromePng.png`), and STR serves the STR mark
there (`internal/webui/bmxicons.go`). That icon is the picture on the
display: it belongs to the SOURCE, not to what is playing.

Meanwhile `now_playing` carries a correct per-station URL and the firmware
declares `artImageStatus="IMAGE_PRESENT"` for it, then never requests it.
Measured on a Portable (taigan) and an ST30 (mojo/scm); the same two icon
fetches appear on an ST10 (rhino). Before presets became native, playback
went through UPnP, where the artwork travels inside the DIDL metadata,
which the firmware does render. That is why the logos used to be there.

Three routes have been tried and all are closed:

1. **Fix the ContentItem art URL.** Nothing to fix: fetching the stored
   URLs through the box's own proxy returns the real images, and the
   stored preset slots carry correct URLs.
2. **Serve the current station's logo at the BMX icon path.** The asset
   is fetched once per source registration. A probe build with
   `askAgainAfter: 60` and a counter in the icon path proved the firmware
   re-reads the registry on the dot every 60 s and still fetches the icon
   only for the FIRST offer, not in standby and not during playback. Only
   an unpair/re-pair triggers another fetch, which cannot be done per
   station change.
3. **Deliver art in the marge recents answer.** The per-station recents
   POST is the only message the box sends marge on a station change, so
   its answer is the only per-station channel. A probe build answered it
   with the box's own record plus five artwork fields, each pointing at a
   different URL (`art`, `imageurl`, `imageUrl`, `logo`, and a
   `ContentItem/containerArt`, the spelling the preset documents use).
   The speaker fetched none of the five, on two station changes
   (ST30, 2026-08-12). The firmware's own recents record carries no
   artwork field either.

Do not re-open any of these without new firmware evidence. Deep RE of the
native path is blocked, see the native-preset notes.

## See also

- [`ARCHITECTURE.md`](./ARCHITECTURE.md) for the component map, ports,
  and data flows.
- [`THREAT-MODEL.md`](./THREAT-MODEL.md) for the security caveats of the
  firmware STR runs on top of.
- [`MODEL-VARIANTS.md`](./MODEL-VARIANTS.md) for the per-variant
  fingerprint table.
