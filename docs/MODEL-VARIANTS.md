# Hardware variant matrix

Per-variant fingerprint of every Bose SoundTouch box STR has been
observed running on. New rows land here as soon as a diagnostic
bundle (`box-N.json` inside the desktop app's "Save diagnostic logs"
zip, plus its `setup.log`) brings a previously unseen combination of
`moduleType`, Bose firmware version, or component layout.

No reporter identifiers (names, handles, real LAN IPs, MAC hashes,
serial hashes) appear in this file. Each row is anonymised hardware
fact only.

For the user-facing "which release asset do I download" mapping, see
[`MODELS.md`](MODELS.md).

## Reference: last official Bose firmware per model

The final firmware Bose shipped before the cloud shutdown on 2026-02. Anything older than this means the speaker missed at least one published update and may behave differently from samples captured here.

| Model | Latest Bose `softwareVersion` (SCM) | Build date |
| --- | --- | --- |
| SoundTouch 10 | `27.0.6.46330.5043500 epdbuild.trunk.hepdswbld04.2022-08-04T11:20:29` | **2022-08-04** |
| SoundTouch 20 | `27.0.6.46330.5043500 epdbuild.trunk.hepdswbld04.2022-08-04T11:20:29` | **2022-08-04** |
| SoundTouch 30 | `27.0.6.46330.5043500 epdbuild.trunk.hepdswbld04.2022-08-04T11:20:29` | **2022-08-04** |
| SoundTouch Portable | (to be confirmed from a diagnostic) | (to be confirmed) |

## SoundTouch 20

| `moduleType` | SCM `softwareVersion` | Build year | Latest official? | `variant` | `variantMode` | Components present | WLAN interfaces | `countryCode` / `regionCode` samples |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `sm2` | `27.0.6.46330.5043500 epdbuild.trunk.hepdswbld04.2022-08-04T11:20:29` | 2022 | **yes** | `spotty` | `normal` | SCM, PackagedProduct | `wlan0`, `wlan1` | `GB` / `GB` |
| `scm` | `27.0.6.46330.5043500 epdbuild.trunk.hepdswbld04.2022-08-04T11:20:29` | 2022 | **yes** | `spotty` | `normal` | SCM, PackagedProduct, **Lightswitch**, **SMSC** | varies (ethernet-only observed) | `EU` / `` (empty) |

### Critical differences (what actually changes STR's code path)

1. **`moduleType=sm2` vs `scm`** — wireless module generation. Same `type="SoundTouch 20"` label hides the split. Check this first.
2. **WLAN interface presence varies on `scm`** — a `scm` box can boot with **no `wlan0`/`wlan1` at all** (only `eth0 lo usb0`). Every WLAN-provisioning approach on the stick is wasted effort against such a box; STR must fall back to ethernet-only mode. `sm2` boxes consistently expose both `wlan0` and `wlan1`.
3. **Extra components on `scm`** — `Lightswitch` (LED ring / touch panel, empty serial) and `SMSC` (Microchip SMSC2014 USB-Ethernet bridge IC with its own firmware string `I<imageDate>; B<buildDate> <buildCode> <imageID>`, image 2014-10-20, build 2013-06-11). STR does not talk to either directly today, but their presence is the cleanest indicator of the older hardware revision.
4. **`regionCode` may be empty on EU `scm` samples** — Bose did not populate the field on newer EU shipments. Any STR code path that reads `regionCode` must fall back to `countryCode`, and ultimately to STR's own `region.txt`.
5. **`margeAccountUUID` populated vs empty** — populated means the box has at least once been paired against a marge endpoint (Bose's, or STR's stub). Empty means jungfräulich / post-factory-reset / never reached the pair flow.
6. **`margeURL` state** — `https://streaming.bose.com` is the Bose factory default; STR's autopair flow rewrites it to `http://no-streaming.bose.com` via `setMargeAccount` + the marge stub's `adddeviceresponse`. The field is a reliable post-install truth-check for "did pairing actually land".

### Common to both ST20 variants

- Bose `/etc/version`: `201507061523`
- Kernel: `Linux spotty 3.14.43+ #137 Wed Oct 25 21:06:53 EDT 2017 armv7l`
- `MemTotal`: ~122 MB (`122 484 kB`)
- Last Bose firmware build epoch: 2022-08-04 (the final SoundTouch firmware before Bose cloud shutdown on 2026-02)
- `networkInfo` emits two entries (SCM + SMSC) with separate MACs but the same Layer-3 IP. The speaker bridges internally; STR sees one address per box.
- Root `/etc` is read-only (ubifs `ro,relatime`); `/mnt/nv` is read-write (ubifs `rw,relatime`); `/tmp` is tmpfs.


### Variant-specific notes

**`sm2`** — newer wireless module.
- `wlan0` and `wlan1` always present in the kernel interface list.

**`scm`** — older SMSC2014-based hardware.
- SMSC component has its own `softwareVersion` of the form `I<imageDate>; B<buildDate> <buildCode> <imageID>`, e.g. `I2014102015199423; B201306111041 081008C 15199423`. The "SMSC" name refers to the Microchip SMSC2014 USB-to-Ethernet bridge IC. Image date 2014-10-20, build date 2013-06-11.
- The `Lightswitch` component is also present with an empty serial; this is the touch-panel / LED-ring controller.
- WLAN interface presence is **not** guaranteed. At least one observed scm box exposes `eth0 lo usb0` only and no `wlan0`/`wlan1` at all, locking the speaker into ethernet-only mode.

## SoundTouch 10

| `moduleType` | SCM `softwareVersion` | Build year | Latest official? | `variant` | `variantMode` | Components present | `networkInfo` entries | WLAN interfaces | `countryCode` / `regionCode` samples |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `sm2` | `27.0.6.46330.5043500 epdbuild.trunk.hepdswbld04.2022-08-04T11:20:29` | 2022 | **yes** | `rhino` | `normal` | SCM, PackagedProduct | 1 (SCM only) | `wlan0`, `wlan1` | `GB` / `GB` |
| `sm2` | `27.0.6.46330.5043500 epdbuild.trunk.hepdswbld04.2022-08-04T11:20:29` | 2022 | **yes** | `rhino` | `normal` | SCM, PackagedProduct | **2 (SCM + SMSC)** | `wlan0`, `wlan1` | `GB` / `GB` |

- Kernel: `Linux rhino 3.14.43+ #137 Wed Oct 25 21:06:53 EDT 2017 armv7l` (same kernel as ST20/ST30).
- Row 1: reference target, confirmed from #123 box-1 (2026-06-10). Permissive chipset: STR's `:8888` is reached directly once the iptables INPUT ACCEPT rule opens it; `is_series_one=0`, no REDIRECT needed.

### Critical difference: the SMSC-bridge `rhino` (2026-06-17 mail bundle)

Row 2 is a SoundTouch 10 that is **byte-for-byte identical** to the reference `rhino` in `moduleType` (`sm2`), `variant` (`rhino`), components (`SCM, PackagedProduct`) **and** WLAN interfaces (`wlan0` + `wlan1`). The **only** distinguishing fingerprint is a **second `networkInfo` entry with `type="SMSC"`** (the Microchip SMSC2014 USB-Ethernet bridge, same IC as the older `scm` ST20).

That bridge whitelists external TCP to Bose-binary-bound listeners only, so the symptom is:

- the agent binds `:8888` fine (`webui: ListenTCP succeeded`),
- but the box's **own self-probe to its LAN IP fails** (`self-probe: connect failed target=webui addr=<lanip>:8888`), and the desktop app cannot reach `:8888` either, so the box never classifies as STR (`strHits` short by one).

Because `moduleType=sm2` (not `scm`) and a `wlan0` interface exists, **neither** `detect_series_one` **nor** `BCO_MODE` fired, so pre-v0.8.1 the `:17008` PREROUTING REDIRECT was skipped and the box was unreachable. **Fixed v0.8.1:** `run.sh` now also sets `REDIRECT_ELIGIBLE` when `/info` contains the `SMSC` marker, installing the harmless, additive `:17008 -> :8888` REDIRECT so the desktop app finds the box on `:17008`. The narrow `IS_SERIES_ONE` gate (which also controls the boot-hang-prone LD_PRELOAD shim) is deliberately **not** widened.

> Lesson for the matrix: `moduleType` + `variant` + `components` + WLAN topology can all be identical between a permissive box and a chipset-whitelisted one. The presence of the **`SMSC` networkInfo entry** is the reliable discriminator for "chipset blocks STR's own ports, needs the REDIRECT". Likely also explains a `sm2` box that drops out of a multi-room group because its `:8888` is unreachable.

## SoundTouch 30

Confirmed from a diagnostic bundle (2026-06-10, #123 box-0):

| `moduleType` | SCM `softwareVersion` | Build year | Latest official? | `variant` | `variantMode` | Components present | WLAN interfaces | `countryCode` / `regionCode` samples |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `sm2` | `27.0.6.46330.5043500 epdbuild.trunk.hepdswbld04.2022-08-04T11:20:29` | 2022 | **yes** | `mojo` | `normal` | SCM, PackagedProduct | `wlan0`, `wlan1` | `GB` / `GB` |

- Kernel: `Linux mojo 3.14.43+ #137 Wed Oct 25 21:06:53 EDT 2017 armv7l` (same kernel as ST10/ST20).
- `is_series_one=0`: like the ST10 (`rhino`), the ST30 (`mojo`) is **not** chipset-whitelisted. STR's `:8888` is reached directly once the iptables INPUT ACCEPT rule opens it; the LD_PRELOAD SoftwareUpdate shim is unnecessary and is skipped for `sm2` chassis (the `.so` cannot even load on `mojo`). See `usb-stick/run.sh` `shim_stage_wrapper`.

## SoundTouch Portable

Not yet observed in a diagnostic bundle.

## Wave SoundTouch IV

Observed live in a 2026-07-04 diagnostic bundle (#182): `moduleType=sm2`,
`variant=lisa`, `variantMode=NoAP`, SCM firmware 27.0.6.46330.5043500 (the
same final build as every supported model) plus a `PackagedProduct`
component (04.04.08) and an SCM + SMSC dual-MAC `networkInfo`. This
supersedes the earlier "likely different CPU" assumption: the module IS an
`sm2`, so the ARMv7l GOARM=5 agent binary executes. The stick-free
`:17000` network install (v0.9.0+) is the validated first-install route:
the maintainer and two independent #182 users installed end to end
(2026-07-09 and 2026-07-11), with presets, NAS/DLNA playback, and ST20
grouping working, and the IR remote's preset keys 1-6 recalling STR
presets under the SoundTouch source. The Wave never reads a USB stick at
boot, so the network install is the only path. See the `lisa` table below.

## How to read a new bundle

The relevant fields are inside each `box-N.json` under `boseInfoXml`. Decode the XML and extract:

- `<type>` — Bose model name
- `<moduleType>` — hardware revision (`sm2`, `scm`, ...)
- `<variant>` and `<variantMode>` — Bose-internal variant markers
- `<components>` list — component categories and each component's `<softwareVersion>` and `<serialNumber>` (anonymised)
- `<networkInfo>` block count — SCM-only vs. SCM + SMSC
- `<countryCode>` and `<regionCode>`
- `<margeURL>` — `streaming.bose.com` (no STR redirect active) or `no-streaming.bose.com` (STR `internal/hosts` hijack landed)

When the agent is up on `:8888`, the matching `setup.log` (in `debugState.setup_log`, or via the SSH fallback in `pullSSHFallback`) carries:

- `interfaces:` line listing kernel-visible NICs
- `kernel:` line with full `uname -a`
- `meminfo:` line for `MemTotal`
- `bose /etc/version:` line for the deep Bose build stamp
- `bose /etc/Variant:` line for the Bose variant marker (often blank if the file is unreadable; populated on intact boxes)
- `writable:` lines for `/etc`, `/mnt/nv`, `/tmp`, `/media/sda1`

## The `lisa` variant (SA-4, Wave SoundTouch, CineMate)

Seen in the 2026-06-28 triage bundles (#273 SA-4, plus a Wave SoundTouch)
and installed end to end since:

| `moduleType` | `variant` | `type` | Components | First-install state |
| --- | --- | --- | --- | --- |
| `scm` | `lisa` | `SoundTouch SA-4` | SCM, PackagedProduct, **Lightswitch**, **SMSC** | **validated** — a user network-installed STR end to end 2026-07-09. The stick was never an option (it does not read USB at boot); the stick-free `:17000` network install is the path. |
| `scm` | `lisa` | `Wave SoundTouch` | SCM, PackagedProduct, Lightswitch, SMSC | **validated** — the agent runs via the `:17008` REDIRECT like the `scm` ST20; stick-free network install confirmed end to end (#182, 2026-07-09 and 2026-07-11). |
| `sm2` | `lisa` | `Wave SoundTouch` | SCM, PackagedProduct, SMSC | **validated** — seen 2026-07-04 (#182, `variantMode=NoAP`): the Wave ships on BOTH module types. Stick-free network install confirmed end to end (#182, 2026-07-09 and 2026-07-11: presets, NAS/DLNA playback, ST20 grouping). |
| `sm2` | `burns` | `SoundTouch SA-5` | SCM, PackagedProduct, SMSC | seen 2026-07-04 (#274 fleet bundle): fw 27.0.6, no STR, stick never read. Agent should run, first-install path still unvalidated. |
| `sm2` | `lisa` | `CineMate 520` | SCM, PackagedProduct | **validated** — a user network-installed STR end to end 2026-07-09. Other CineMate models remain untested. |

The Wave, SA-4, and CineMate 520 are now **Working** (docs/MODELS.md); the untested-model UI warning was removed in v0.9.2 (#283 follow-up). The SA-5 (`burns`) remains unvalidated. Note: in a diagnostic a stock `scm/lisa` box shows `reachable8888=true` because that field probes **:17008**, where Bose's own SoftwareUpdate answers; the authoritative "STR present" signal is the `strDetected` field, not `reachable8888`.

## Why this matters for STR

Code paths that diverge between hardware revisions (WLAN provisioning, USB-Ethernet bridge handling, watchdog behaviour, TLS bundle generation, hosts-file bind-mount) sit on top of these facts. New failure reports get their root cause matched against this matrix before we start speculating; if a row is missing we ask for a diagnostic before promising a fix.
