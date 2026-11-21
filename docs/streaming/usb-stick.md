# Music from a USB stick in the speaker: feasibility spike

Status: spike (discussion #1136, 27 votes). Nothing here is wired into the
product. The prototype package `internal/usbmedia` is imported by nothing.

## TL;DR

**Feasible with limits.** The speaker's own firmware has no USB music
source, but it already plays files from DLNA servers through two native
paths (the `UPNP` renderer STR's library uses, and the `STORED_MUSIC` DLNA
client). So the clean shape is: the agent serves a plain music stick as a
small, lazy UPnP ContentDirectory over plain HTTP, and the existing library
code (desktop Library, phone remote, library keys, the queue) plays it like
any NAS. No firmware bending, no new top-level UI.

The limits:

- Only ST10, ST20, ST30 and Portable have a port that hosts a stick. ST10
  and Portable need an OTG adapter.
- On the `scm` chassis (ST20/ST30 `scm`, Portable `taigan`) the LAN itself
  runs over the same USB host as the stick, and a stick left in an `scm`
  ST30 has already blackholed that speaker's network (over-current on the
  stick port, live 2026-09-01). These chassis need a hardware test before
  they get the feature, and may never get it.
- FAT32 only, 32 KB clusters at most, as far as known. Windows formats
  every stick over 32 GB as exFAT, which a 3.14 kernel almost certainly
  cannot mount.
- A music stick present at boot currently costs about 55 s of boot time
  and gets `fsck -a` run on it by `run.sh`. That needs fixing first, and is
  worth fixing even without this feature.

Rough effort: 6 to 8 days for v1, plus 2 to 3 days for the optional native
`STORED_MUSIC` stage.

## 1. Hardware and firmware per model

### Which models can host a stick at runtime

The facts come from the install and recovery history (the stick install
works on ST10, ST20, ST30 and Portable, so USB host mode works there at
least at boot), from `docs/MODEL-VARIANTS.md`, from `docs/FIRMWARE-UPDATE.md`,
and from the maintainer's live ST300 session.

| Model (variant, module) | Port | Hosts a stick? | Network shares the USB host? | Verdict |
| --- | --- | --- | --- | --- |
| ST10 (`rhino`, `sm2`) | micro-USB | yes, with an OTG adapter or OTG stick | no, LAN is `wlan0` on SDIO | **Usable.** Best candidate. |
| ST20 Series II/III (`spotty`, `sm2`) | USB-A | yes | no | **Usable.** |
| ST20 Series I | SETUP A micro-USB + SETUP B USB-A | yes, on SETUP B | depends on module | Usable on SETUP B, per module row. |
| ST20 (`spotty`, `scm`) | USB-A | yes | **yes**, `eth0` is the USB-CDC coprocessor | **Limited.** Needs the load test below. |
| ST30 (`mojo`, `sm2`) | USB-A | yes | no | **Limited.** VBUS drops under read load, seen by three users during installs (`VBUS_ERROR in a_wait_vrise`, `error -110`). Low-power stick or powered hub. |
| ST30 (`mojo`, `scm`) | USB-A | yes | **yes** | **Not recommended.** A stick left in blackholed all inbound traffic within minutes (`over-current condition on port 1`), confirmed by removing it. |
| Portable (`taigan`, `scm`) | micro-USB | yes, with an OTG adapter | **yes** | **Limited.** Does not auto-mount, so the agent mounts itself. The stick draws from the battery. |
| Wave (`lisa`), SA-4, SA-5 (`burns`), ST300 (`ginger`), CineMate, Lifestyle (`bardeen`/`lisa`), Wireless Link Adapter (`binky`) | setup/service port only | **no known host mode.** The ST300's SETUP port enumerates as a device (CDC-ACM serial, RNDIS in update mode), not as a host. None of these reads a stick at boot. | n/a | **Not usable.** |

### Does the firmware have a USB or local-file source?

No. Checked read-only on FW 27.0.6, 2026-11-21, on an ST10 (`rhino`/`sm2`),
an ST30 (`mojo`/`scm`) and a Portable (`taigan`/`scm`):

- `/sources` lists `AUX`, `BLUETOOTH`, `AIRPLAY`, `QPLAY`, `UPNP`,
  `STORED_MUSIC_MEDIA_RENDERER`, `STORED_MUSIC` (one per registered DLNA
  server), `SPOTIFY`, `DEEZER`, `ALEXA`, `NOTIFICATION` and
  `LOCAL_INTERNET_RADIO`. There is no USB or local-file source.
- `/supportedURLs` is the same 105 endpoints on all three. Nothing in it
  touches local storage. `/listMediaServers` is the DLNA discovery cache.
- Bose only ever documented the port for setup, service and the USB
  firmware update (`Update.stu` with 4 + Volume minus at power-on).

What the firmware does have is two native ways to play a file from a DLNA
server:

1. **`UPNP` renderer** (AVTransport on `:8091`). This is how STR's library
   plays today: the agent pushes a plain-HTTP file URL with a DIDL `res`
   protocolInfo and a duration, and the box range-reads it.
2. **`STORED_MUSIC`**, the firmware's own DLNA client. It discovers
   servers over SSDP (`/listMediaServers`) and browses and plays them
   natively once STR registers the server as an account
   (`internal/webui/mediaservers.go`, `internal/boxapi/mediaservers.go`).

A stick served as a DLNA MediaServer fits both without bending anything.
To the firmware, STR is then just one more media server, like the NAS.

## 2. Mounting today

- **Bose udev** mounts the first USB block device at `/media/sda1`, `vfat`,
  `rw`, `errors=remount-ro` on the ST10, ST20 and ST30 (`/media` is a tmpfs).
  Whether this also happens when a stick is **hot-plugged** into a running
  box has never been checked. That is test T1.
- **The Portable does not auto-mount** (live 2026-06-11). `/dev/sda1`
  exists but nothing is mounted, and `/media/sda1` is a stale directory.
- **FAT is left dirty**, because the box never unmounts cleanly. A dirty
  FAT plus a large write flips the mount read-only mid-write. A read-only
  music mount avoids that whole class of problem: nothing is ever written,
  so the FAT never gets dirty.
- **More than 32 GB:** FAT32 with 64 KB clusters lists directories but
  fails every data read (`Input/output error`, #119). STR's own formatter
  (`cmd/winformat`) caps clusters at 32 KB. Windows formats sticks over
  32 GB as exFAT by default.
- **exFAT and NTFS:** unknown. The kernel is `3.14.43+`. exFAT only entered
  mainline in 5.4, so it is almost certainly absent. A read-only NTFS
  driver may or may not be built in. `/proc/filesystems` answers this, and
  it is not in the diagnostic yet. That is test T3.
- **Character set:** the mount options in `/proc/mounts` (`iocharset`,
  `codepage`) decide whether file names with umlauts arrive as UTF-8. The
  debug state already carries `proc_mounts`, so test T1 shows this too.

Code that already touches sticks, and has to stay out of a music stick's way:

| Where | What it does | Effect on a music stick |
| --- | --- | --- |
| `internal/webui/debug.go` `stickMountDir` / `stickReallyMounted` | finds the STR stick by marker files | ignores it (no marker), correct |
| `internal/webui/ota_stick.go` `refreshStickAgentBinary` | writes the new agent onto an inserted STR stick before the post-OTA reboot | ignores it (keyed on `stickMountDir`), correct |
| `desktop-app/ota.go` `mountStick` (over SSH, before an OTA) | for every `sd*` device: **unmounts it first**, `fsck.vfat -a`, mounts at `/tmp/str-stick`, checks markers, unmounts again | **unmounts and fscks a music stick**, then leaves it unmounted. Needs a marker check before the unmount. Only runs while SSH is open. |
| `usb-stick/rc.local` | if any USB storage is seen, waits up to 30 s for `run.sh`/`streborn-armv7l` | **waits the full 30 s** at every boot |
| `usb-stick/run.sh` (stick wait, around line 979) | waits up to 25 s for markers, then runs `fsck.vfat -a` on every `sd*` partition and mounts each one `rw` at `/media/sda1` | **waits 25 s more, fscks the stick, can mount it a second time over Bose's own mount.** A double `vfat` mount is exactly what `mountStick` warns corrupts writes. |
| `cmd/agent/boxlanguage.go`, `bootstrap.go` | read `lang.conf` / `run.sh` on `/media/sda1` | harmless |

There is no USB event listener in the agent today (no netlink uevent, no
inotify), and per the project rules there must not be a poll.

## 3. The install-stick collision

Three things make a stick left in a speaker dangerous today:

- with an STR stick inserted, the boot sync copies the stick's binary over
  NAND on **every** boot (the stick wins, no version check);
- a `remote_services` marker opens root SSH;
- on the `scm` ST30, the left-in stick took the network down.

The rule this design uses is **one stick, one purpose, decided by marker
files, with doubt resolved towards "not a music stick"**:

1. **A music stick is a stick with none of these at its top level**
   (case-insensitive, because FAT is): `install.sh`, `run.sh`, `rc.local`,
   `streborn-armv7l`, `streborn`, `version.txt`, `wlan.conf`, `wlan.ssid`,
   `wlan.pass`, `Update.stu`, `remote_services`. This is the same marker
   set the boot scripts and the agent already use, plus the two Bose
   markers. It is implemented and tested in `internal/usbmedia.Classify`.
2. **A combined stick (install files plus music) is an install stick.**
   It is never served. Otherwise the Wi-Fi password in `wlan.pass` would be
   one path-traversal bug away from the LAN. The library would then show "This
   is the STR install stick. Use a separate stick for music."
3. **The music side never writes.** It mounts read-only at its own mount
   point (`/tmp/str-music`), or remounts Bose's `/media/sda1` read-only
   once the stick is classified as music. No fsck, no marker files, no
   `setup.log`.
4. **The install side must ignore a music stick quickly.** This is a
   prerequisite change to `rc.local` and `run.sh`: when a `/media/sd*`
   mount exists and carries no STR marker, the stick wait stops at once,
   and the fsck/manual-mount recovery runs only for a device that is
   **not** mounted. The SSH `mountStick` gets the same "check markers
   before you unmount" order. Without this, a music stick left in costs
   about 55 s of every boot and gets `fsck -a` run on it.
5. **Bose's own USB firmware update** needs `Update.stu` plus a key combo
   at power-on, so a music stick never triggers it. A stick that carries
   `Update.stu` is refused by rule 1 anyway.

## 4. Playback path and formats

### What the renderer accepts

`ConnectionManager#GetProtocolInfo` on `:8091` returns the same sink list
on ST10, ST30 and Portable (live, read-only, 2026-11-21):

| Format | Sink MIME types | Notes |
| --- | --- | --- |
| MP3 | `audio/mpeg` (DLNA `MP3`), `audio/mp3` | yes |
| AAC / M4A | `audio/mp4`, `audio/aac`, `audio/x-aac`, `audio/x-m4a`, `audio/aacp`, `audio/vnd.dlna.adts`, `audio/3gpp` | yes. ALAC inside `.m4a` is unknown. |
| FLAC | `audio/flac`, `audio/x-flac` | yes. 24-bit or 96 kHz is unknown. |
| Ogg Vorbis | `audio/ogg`, `application/ogg`, `audio/vorbis` | yes. Opus is not listed. |
| WAV / LPCM | `audio/wav`, `audio/x-wav`, `audio/L16` 44.1 and 48 kHz, mono and stereo | no 88.2/96 kHz LPCM sink |
| WMA | `audio/x-ms-wma`, `audio/wma` | WMA Pro/Lossless unknown |
| Playlists | HLS / m3u MIME types | not needed for files |

Not listed: Opus, AIFF, APE, WavPack, DSD. These are simply not shown.

Two renderer quirks the server has to respect:

- **No HTTPS.** The agent serves plain HTTP on the LAN, which is what
  `:8888` already is.
- **The renderer judges playability by the URL extension** (the reason the
  Spotify URL ends in `.ogg`). File URLs therefore end in the real file
  name: `/usb/file/<folder>/<name>.flac` (see `usbmedia.FileURL`).

### Address the renderer is handed

The box's own renderer must fetch from `http://127.0.0.1:8888/...`, like
every other agent URL (`internal/boxurl`). On a whitelisted chassis a
local connection to the box's own LAN IP does not pass the `:17008`
PREROUTING REDIRECT (that rule is `! -i lo`), and the agent's self-probe
to its own LAN IP on `:8888` already fails on some boxes. Other speakers
and the desktop app need the LAN form (`:8888` on `sm2`, `:17008` on
whitelisted chassis). So:

- ContentDirectory `res` URLs are built from the request's `Host` header,
  so every client gets an address it can actually reach;
- when the agent pushes a URL whose host is its own LAN address to its own
  renderer, it rewrites the authority to `127.0.0.1:8888`. This is one
  helper next to `queueBoxURL`. The stream proxy refuses loopback, which is
  fine because a stick file is always played directly, never proxied.

### Shape: MediaServer or bespoke endpoint

| | Agent as a UPnP MediaServer (ContentDirectory) | Bespoke `/api/usb/...` |
| --- | --- | --- |
| Desktop Library, phone remote browser, library search walk, library keys, queue, Recently played | work unchanged; the stick is one more server | each needs a second code path |
| Native `STORED_MUSIC` (Bose app, other speakers natively) | possible in stage 2 via SSDP | impossible |
| Other speakers playing this stick | yes, by URL | yes, by URL |
| Test oracle | the repo's own `dlna` client parses it, and `dlna/dlnatest` shows the shapes | none |
| New code on the box | Browse SOAP, DIDL, device description and SCPD, file serving | listing JSON and file serving |
| New UI | none beyond one server entry and one refusal line | a new view in both UIs |

**Recommendation: MediaServer, discovered through STR's own channel in v1.**
The agent serves `ContentDirectory:1` (Browse, GetSystemUpdateID,
GetSearchCapabilities, GetSortCapabilities; Search answers a 501 fault,
which the library already handles by falling back to its bounded walk)
under `:8888/usb/`. In v1 it does **not** speak SSDP. Instead:

- the agent lists "USB stick" in `/api/library/servers` while a music
  stick is mounted, with a fixed local resolution (no discovery round);
- the desktop app reads the same entry from each agent and merges it like
  a manual server (`desktop-app/library_manual.go`).

Why not SSDP in v1: the box's BoseApp already owns UDP 1900 as a
MediaRenderer, and whether the agent can share it (`SO_REUSEADDR`) is
unknown. SSDP also requires a periodic `NOTIFY` (max-age at least 1800 s),
which is a new timer on every speaker. Stage 2 can add M-SEARCH answers
plus a `NOTIFY` only on mount and unmount, if the firmware turns out to
list a server on its own host (open question Q4). That stage buys the
native `STORED_MUSIC` source and the stick in the original Bose app.

## 5. Constraints on the box

Measured live 2026-11-21: the agent's RSS is about 17 to 18 MB, the Spotify
engine's about 12 MB. NAND free space is 8.7 MB on the ST10 and the
Portable, but only **3.7 MB on the `scm` ST30** (27.4 MB partition). The
agent binary is about 20 MB.

- **NAND:** nothing of the music touches NAND. No index, no cover cache,
  no tag cache. The only NAND cost is binary growth. The prototype adds
  64 KiB to a stripped `GOARM=5` binary. The full v1 (SOAP, DIDL,
  description, mount handling) is estimated at 150 to 300 KB, mostly XML
  templates. No new dependency: `encoding/xml` and `net/http` are already
  linked. This matters on the ST30, where the OTA already drops the engine
  to fit.
- **RAM:** one directory is read per Browse. 5,000 entries come to about
  0.5 MB transient. Cap a single directory at 10,000 entries and say so in
  the listing. File serving goes through `http.ServeContent` with a small
  buffer and supports Range.
- **CPU:** the agent never decodes; the renderer does. Serving a FLAC file
  is a byte copy at about 1 Mbit/s.
- **No timers, no polling.** The stick is mounted **on demand**: when a
  client asks for the server list or opens the stick, the agent looks for
  a removable USB disk in `/sys/block` (a stat, no I/O on the stick), then
  classifies and mounts it. It unmounts on a failed read (stick pulled) or
  on an explicit eject. A netlink uevent socket (a blocking read, zero cost
  while idle) is an optional refinement for prompt unmounts. It is not a
  poll, but it is a new long-lived listener, so it is Jens' call.
- **Lazy listing, no full index.** Browse reads exactly one directory and
  stats nothing below it. Titles come from file names and albums from
  folder names. Cover art is `folder.jpg` / `cover.jpg` in the folder, one
  stat per Browse. Tags (ID3v2, FLAC Vorbis comment, MP4 `ilst`) are
  deferred. If they come later, read them only for the visible page, cap
  each read at the first 64 KB, and never during a walk.
- **Search** uses the existing bounded walk (`dlna/walksearch.go`, depth
  and hit ceilings), started only by a user's query.

### Security

- **Path traversal:** every object id and file path is resolved through
  `os.Root` (Go 1.24+), which refuses `..`, absolute paths and symlinks
  that point out of the root. Ids are additionally validated with
  `fs.ValidPath`. Symlinks and device nodes are never listed. All of this
  is tested in `internal/usbmedia`; the symlink test runs on Linux CI.
- **Only audio files are served:** an extension allow-list (the sink table
  above). A text file on the stick is not downloadable.
- **LAN-only:** the `/usb/` routes take the existing `isLocalLAN` source
  gate. As with any DLNA server, anyone on the LAN can play and download
  the music on the stick. That goes into `docs/THREAT-MODEL.md` when it
  ships.
- **Read-only mount**, `nosuid,nodev,noexec`.
- **An install stick is never served** (section 3).

## 6. Effort and UI surface

| Piece | Days |
| --- | --- |
| Prerequisite: `rc.local`/`run.sh` ignore a non-STR mount at once, no fsck of a mounted device, SSH `mountStick` marker-first; shell tests in `usb-stick/tests` | 0.5 to 1 |
| Agent: detect, classify, mount read-only on demand, unmount on read failure, debug-state section (`usb_music`: kind, fs, mount options, refusal reason) | 1 to 1.5 |
| Agent: ContentDirectory (Browse, DIDL, description, SCPD) plus file serving, tested against the `dlna` client | 1.5 to 2 |
| Agent: own-host to `127.0.0.1:8888` rewrite for the box's own renderer | 0.5 |
| Desktop app and phone remote: list the stick as a server; the refusal line (install stick, not FAT32, unreadable) | 1 |
| i18n (EN/DE first-class, others follow), docs, threat-model line | 0.5 |
| Hardware validation (T1 to T7) | 1 to 2 |
| **v1 total** | **6 to 8** |
| Stage 2 (optional): SSDP on mount/unmount, native `STORED_MUSIC` registration | +2 to 3 |

**UI surface: no new control.** The stick shows up as one more entry in the
existing media-server list in the desktop Library and the phone remote,
named "USB stick (speaker name)". Everything else (folders, play, queue,
library keys) is the existing library view. The only new text is one
refusal or hint line in place of the folder list. No setting toggle: it is
there when a music stick is in, and absent otherwise.

## 7. Risks

1. **USB power on `scm` chassis and the `sm2` ST30.** The stick and, on
   `scm`, the network share the USB host. Continuous reads are exactly the
   load that collapsed VBUS during installs. A speaker whose network
   drops while it plays from the stick is worse than no feature. Gate the
   feature per chassis on test T2.
2. **exFAT sticks.** Most sticks over 32 GB come exFAT-formatted, and the
   most common failure will be "my 64 GB stick does not show up". The
   refusal line has to name the fix (reformat FAT32 with STR's Format
   button, or use a stick of 32 GB or less).
3. **Boot cost and stick writes** from today's boot scripts (section 3,
   point 4) until the prerequisite ships.
4. **Install stick left in for music.** It is refused, but the user's
   intent ("I put my music on the stick I had") is real, so the message
   must be clear.
5. **Battery drain** on the Portable.
6. **Unknown codecs:** hi-res FLAC, ALAC, WMA Lossless. Unsupported files
   must fail per track (the queue already skips a dropped track) and
   never stall the box.

## 8. Open questions

- **Q1** Does Bose udev mount a stick hot-plugged into a running ST10,
  ST20 or ST30, and with which options (`iocharset`, `codepage`, `rw`)?
- **Q2** Does continuous stick reading on `scm` (ST30, Portable) or on the
  `sm2` ST30 trip VBUS or over-current, and does the LAN survive it?
- **Q3** Which filesystems does the kernel offer (`/proc/filesystems`,
  `/lib/modules`)? exFAT? NTFS read-only?
- **Q4 (stage 2)** Can the agent share UDP 1900 with BoseApp? Does
  `/listMediaServers` list a server on the box's own IP, and does
  `STORED_MUSIC` play from it on a whitelisted chassis?
- **Q5** 24-bit/96 kHz FLAC, ALAC `.m4a`, 96 kHz WAV: play, fail cleanly,
  or wedge the renderer?
- **Q6** Does `umount` of a read-only stick work on these boxes? (A `rw`
  `umount /media/sda1` answered "Invalid argument" once.)

## 9. Hardware test plan (for Jens)

All read-only except plugging sticks in. Speaker addresses are
placeholders. `:8888` is for `sm2` boxes, `:17008` for whitelisted ones.
Audible playback on the Portable only, at volume 5.

**Stick A:** 16 GB, FAT32 (STR's Format button), no STR files. Folders
`Album 1/` with `01 Ärger.mp3`, `02 Test.flac` (16/44.1), `03.m4a` (AAC),
`04.ogg`, `05.wav` (44.1), plus `hires-2496.flac`, `alac.m4a`,
`wav-96k.wav`, `opus.opus`, a `cover.jpg`, and one large file (about 300 MB).
**Stick B:** 64 GB as it came from the shop (exFAT). **Stick C:** any
stick formatted NTFS.

- **T1 hot-plug mount (Q1).** ST10, then ST30 `scm`, then Portable: with
  the speaker running, insert stick A and wait 30 s. Then
  `curl http://192.0.2.10:8888/api/debug/state` (or `:17008`) and look at
  `proc_mounts` (is there a `/dev/sda1 /media/sda1 vfat ...` line, and
  with which options?) and `media_listing`. Are the umlauts intact in
  `stick_listing`? Expected: mounted on ST10 and ST30, not on the Portable.
- **T2 USB load (Q2).** Only where SSH is already open, read-only:
  `dd if=/media/sda1/<large file> of=/dev/null bs=64k`, three times in a
  row. Then `dmesg | tail -50`: any `VBUS_ERROR`, `over-current`,
  `error -110` or `USB disconnect`? During the read, from the PC, check
  `curl -m 3 http://192.0.2.10:<port>/api/agent/version` a few times by
  hand. Run this on the ST10 (reference), the ST30 `scm` and the Portable.
  If the ST30 `scm` loses the LAN: pull the stick, and that chassis is
  out.
- **T3 filesystems (Q3).** SSH read-only: `cat /proc/filesystems;
  ls /lib/modules/$(uname -r) 2>/dev/null`. Then insert stick B and stick
  C in turn and repeat T1: does either get mounted?
- **T4 boot with a music stick (section 3).** Power-cycle the ST10 with
  stick A inserted. Afterwards, in `/api/debug/state`, check `boot_log`
  for "waiting up to 30s" / "no stick after 30s" and `setup_log` for
  "trying fsck+manual mount". Measure the time until the agent answers,
  compared with a boot without the stick. Expected: about 55 s slower,
  plus an fsck of the stick. This confirms the prerequisite fix.
- **T5 formats (Q5), no new code needed.** Copy stick A's files to the NAS
  and play each one through STR's existing Library on the Portable (volume
  5). Note per file: plays, refused (box error), or box hangs. Then check
  `now_playing` silently on the ST10 and the ST30.
- **T6 (stage 2 only, needs a prototype build).** With an agent that
  serves the stick and announces it once, does `GET :8090/listMediaServers`
  on the same speaker list it?
- **T7 eject.** After T1, pull the stick while nothing plays and check
  `proc_mounts` again. Is the mount gone, or left stale?

## 10. Prototype in this branch

`internal/usbmedia` (not imported anywhere) holds:

- `Classify`: the marker rule from section 3 (case-insensitive; a combined
  stick counts as an install stick; an unreadable root is not served);
- `Library.Browse`: one directory per call, folders first, a
  case-insensitive sort, paging, hidden and system entries (`._*`,
  `System Volume Information`) skipped, playable files only by extension;
- `Library.OpenFile`: `os.Root`-confined, audio only;
- `FileURL`: an escaped URL that keeps the real extension last.

Tests cover the classification table, paging and ordering, traversal
(`..`, absolute, backslash, `./`), the symlink escape (Linux) and URL
escaping. The size cost was measured with a throwaway `main`, GOARM=5, `-s
-w`: +64 KiB.
