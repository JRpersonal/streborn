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

## SoundTouch 20

| `moduleType` | Bose `softwareVersion` (SCM) | `variant` | `variantMode` | Components present | WLAN interfaces | `countryCode` / `regionCode` samples |
| --- | --- | --- | --- | --- | --- | --- |
| `sm2` | `27.0.6.46330.5043500 epdbuild.trunk.hepdswbld04.2022-08-04T11:20:29` | `spotty` | `normal` | SCM, PackagedProduct | `wlan0`, `wlan1` | `GB` / `GB` |
| `scm` | `27.0.6.46330.5043500 epdbuild.trunk.hepdswbld04.2022-08-04T11:20:29` | `spotty` | `normal` | SCM, PackagedProduct, Lightswitch, SMSC | varies (ethernet-only observed) | `EU` / `` (empty) |

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

Reference target. Detailed fingerprint not yet captured from a diagnostic bundle in this format; expected to be `moduleType=sm2`, `variant=rhino` based on prior bench observation.

## SoundTouch 30

Not yet observed in a diagnostic bundle.

## SoundTouch Portable

Not yet observed in a diagnostic bundle.

## Wave SoundTouch IV

Not yet observed in a diagnostic bundle. Likely different CPU; do not assume the ARMv7l agent binary applies.

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

## Why this matters for STR

Code paths that diverge between hardware revisions (WLAN provisioning, USB-Ethernet bridge handling, watchdog behaviour, TLS bundle generation, hosts-file bind-mount) sit on top of these facts. New failure reports get their root cause matched against this matrix before we start speculating; if a row is missing we ask for a diagnostic before promising a fix.
