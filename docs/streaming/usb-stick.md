# Music from a USB stick in the speaker

Status: **v1 implemented; run on the ST10 and the Portable (2026-11-21),
not yet confirmed on the ST20 and the ST30** (Ideas #1136, 27 votes). Section `v1` below says what ships, on which models, and how to
test it on hardware. Sections 1 to 9 are the feasibility spike (draft PR
#2018) the design came from, kept as the background for every decision.

## TL;DR (spike)

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
- A music stick present at boot cost about 55 s of boot time and got
  `fsck -a` run on it by `run.sh`. Fixed in #2027, before v1.
- v1 serves the `scm` chassis too, so they can be tested (T2 decides; see
  `Per model` below).

Rough effort: 6 to 8 days for v1, plus 2 to 3 days for the optional native
`STORED_MUSIC` stage.

## v1: what ships

The agent serves a FAT32 music stick plugged into the speaker as one more
media server. It shows up in the desktop Library and in the phone remote's
library as "USB stick (speaker name)", and everything the Library does with a
NAS works with it: browse folders, play a song, play a folder (queue), save a
song or a folder on a key, Recently played, and the whole-library search in
the desktop app (a browse walk, the stick has no Search action). There is no
new control and no setting: the entry is there while a music stick is in, and
gone when it is out.

### Per model

| Model (variant) | v1 | How the stick is read |
| --- | --- | --- |
| ST10 (`rhino`) | **served** | the firmware's own mount at `/media/sda1` (OTG adapter needed) |
| ST20 (`spotty`, `sm2` and `scm`) | **served** | the firmware's own mount; Series I on SETUP B |
| ST30 (`mojo`, `sm2` and `scm`) | **served, caution** | the firmware's own mount. VBUS drops under load (`sm2`) and a left-in stick once took an `scm` ST30 off the network: hardware test T2 decides whether it stays on |
| Portable (`taigan`) | **served, caution** | the firmware's own mount at `/media/sda1` (live 2026-11-21, against the spike's expectation). Where nothing mounted it, the agent mounts it **read-only** at `/tmp/str-music` when the Library is opened (OTG adapter needed, draws on the battery) |
| Wave (`lisa`), SA-4/SA-5, ST300, CineMate, Lifestyle, Link Adapter | not served | no USB host port. If such a speaker ever reports a USB disk, the Library says "this speaker cannot play music from a USB stick" |

The gate is one table, `usbMusicChassis` in `internal/webui/usbmusic.go`. If
T2 shows that a chassis loses its network while the stick is read, that
chassis comes out of the table (a one-line change) and the speaker then says
it cannot play from a stick, instead of failing.

### How it works

- **Looking for a stick** reads `/sys/block/sd*`, `/proc/mounts`, and the
  first 512 bytes of the partition (to tell FAT32 from exFAT and NTFS, and to
  read the cluster size). It happens once when the agent starts (detection
  and a log line only, the agent mounts nothing at boot), when the kernel
  logs a USB disk coming or going (below), and when a client asks: the
  server list (`/api/library/servers`), a browse, a file, the debug state.
  No timer, no poll, no netlink listener. A stick plugged in while the
  Library is open shows up the next time the list is loaded ("Search again"
  in the app, reopening the library on the phone).
- **The kernel's own lines are the trigger.** The kernel logs through klogd
  into the busybox syslog ring the agent already follows with `logread -f`
  (`internal/boxlog`; a rhino capture of 2026-09-06 carries
  `kern.info kernel: usb 1-1: new high-speed USB device number 4 using musb-hdrc`).
  `boxlog` classifies three kernel shapes as `usb_disk`
  (`internal/boxlog/usbdisk.go`): `sd 0:0:0:0: [sda] Attached SCSI removable disk`
  (or `Attached SCSI disk`) and `FAT-fs (sda1): ...` (a FAT warning at mount
  time) mean attached, `usb 1-1: USB disconnect` means gone. Lines that
  `logread` replays from the ring when it (re)starts are not acted on. An
  attach starts one round of at most four looks over about ten seconds (1,
  2, 3, 4 s apart), because the firmware mounts the stick a moment after the
  kernel line; the round ends at the first look that finds the stick served
  or refused, and further lines of the same insert start nothing. A
  disconnect is one look, which stops serving and takes away a mount the
  agent made. Not checked live yet: that these exact lines reach the ring on
  every chassis (the attach line itself has not been captured on a speaker;
  the boxlog `usb_disk` events in a bundle answer it). Where they do not,
  the other looks still find the stick.
- **Only the first partition** of the first USB disk counts.
- **STR reads, never writes, and makes the mount read-only.** Where the
  firmware mounted the stick (ST10, ST20, ST30 and, live, the Portable too)
  that mount is **read-write** (see Live results), and a stick pulled from
  it came back with a dirty FAT. So as soon as the agent has classified the
  stick as a music stick (no marker, see below), it runs
  `mount -o remount,ro /media/sda1` (busybox) on the firmware's mount and
  reads `/proc/mounts` back to see that it took (`remounted_ro` in the debug
  state). Only a music stick: an install or recovery stick keeps its `rw`
  mount for STR's own scripts, and a stick whose classification failed is
  left as it is. The firmware's mount is never unmounted, and nothing is
  held open on it beyond the one directory handle serving needs. A remount
  that fails is logged once, the stick is served from the `rw` mount as
  before, and it is tried again on a later look at most once a minute
  (`remount_error`). Every look remounts a mount that is still `rw`: the
  kernel line, the agent start, the server list, a browse, a file. Where
  nothing mounted the stick (a hot-plug the firmware did not mount), the
  agent mounts it itself with `ro,nosuid,nodev,noexec` (UTF-8 names first,
  plain if the kernel lacks the codepage) at `/tmp/str-music`, on tmpfs, when
  the Library is opened. Nothing is ever written, renamed or deleted on a
  stick by STR, and no fsck is run.
- **File names are decoded from the mount's `iocharset`**
  (`internal/usbmedia/charset.go`). The firmware's mount is
  `iocharset=iso8859-1`, so `01 Ärger.mp3` arrives as the Latin-1 byte
  `0xC4`. Titles and object ids are the UTF-8 text (`01 Ärger`); file and
  cover URLs carry the on-disk bytes (`01%20%C4rger.mp3`), so the renderer
  fetches exactly what the filesystem holds. A URL in the UTF-8 form is
  found too. A `utf8` mount has both forms equal; a mount whose charset is
  not listed reads valid UTF-8 as UTF-8 and anything else as Latin-1.
- **Install sticks are never served**: any of the names in
  `usbmedia.InstallMarkers` at the top level (case-insensitive) makes it an
  install, recovery or Bose firmware stick. The list is the same as
  `stick_has_str_marker` in `usb-stick/run.sh` and `usb-stick/rc.local`
  (merged in #2027); `TestInstallMarkersMatchTheBootScripts` fails when the
  two drift. A mount the agent made on an install stick is taken away again
  at once. The desktop app's SSH `mountStick` uses its own, shorter
  `has_marker` (it only needs to recognise an STR stick it may write to); it
  never serves anything, so it is not part of this rule.
- **Refusals** are one line in the Library instead of a server, per stick:
  `install-stick` (use a separate stick for music), `not-fat32` (exFAT,
  NTFS, anything else: format as FAT32), `big-clusters` (FAT with clusters
  over 32 KB, which list but fail every read, #119), `unreadable` (no boot
  sector, mount failed, root not readable) and `unsupported-model`.
- **Browsing** is a ContentDirectory under `:8888/usb/` (`device.xml`,
  `cds.xml`, `control`): Browse (children and metadata),
  GetSystemUpdateID, GetSearchCapabilities, GetSortCapabilities. Search
  answers UPnP 401, and the clients walk instead. One Browse reads one
  directory (at most 10,000 entries), never stats or opens the files in it:
  titles are file names without the extension, the album is the folder's
  name, the cover is `cover`/`folder`/`front`/`albumart` `.jpg/.jpeg/.png`
  in the folder. No tags are read, so there are no durations either (see
  Deferred). Order is natural (`2 x` before `10 x`), folders first. Dot
  files, `._*` and the Windows system folders are hidden.
- **Files** only by extension: `.mp3 .flac .m4a .aac .ogg .oga .wav .wma`,
  served with `http.ServeContent` (Range, for seeking and FLAC headers),
  the MIME from the extension, and the DLNA `contentFeatures` /
  `transferMode` headers.
- **Hi-res files are listed, and refused when played.** The speaker plays
  FLAC and WAV up to 48 kHz only (Live results). Listing reads no file, so
  a 96 kHz FLAC is listed like any other. When one is played, the agent
  reads the sample rate from the header (`usbmedia.ReadAudioFormat`: FLAC
  STREAMINFO, after an optional ID3v2 tag; the WAV `fmt ` chunk; a few dozen
  bytes, at most 256 KB into the file). M4A keeps its rate in the `moov`
  box, which may sit at the end of the file, so it is not read and an M4A
  plays as before. Above 48 kHz the file is not handed to the speaker:
  a single play (`/api/play`, a song key from the app or the phone) is
  refused with `code:"library-file-hires"` and "This speaker cannot play
  this file: it has a sample rate above 48 kHz."; a folder steps over the
  track (`queueItem.Skip`, the track stays in the list so the key still
  matches its folder) and says how many it skipped (`skippedHiRes`); a
  folder of nothing else is refused like a single file; a hardware key
  plays nothing and the log says why (`internal/webui/usbhires.go`).
- **A file the speaker still cannot decode.** When the speaker answers a
  stick file with `AUDIO_ERROR_DECODER` (a format the header check does not
  read), the agent notes it, and `GET /api/queue` carries `usbRefused`
  while that file is the last play. The phone page shows "The speaker could
  not decode this file from the USB stick. It plays MP3, AAC, Ogg, WMA, and
  FLAC or WAV up to 48 kHz." where "Stopped" would stand; the desktop app
  shows the same instead of its generic format guess. Every path goes through `os.Root`, so `..`,
  absolute paths and symlinks cannot leave the stick, and only audio and
  cover names can be fetched at all. `/usb/` is LAN-only (`isLocalLAN`).
- **URLs.** A client is handed URLs built from the Host it used, so the
  desktop app gets `http://<speaker>:8888/usb/file/...` (or `:17008` on a
  whitelisted chassis). When such a URL reaches the speaker that holds the
  stick (`/api/play`, `/api/queue`, saving a key), the agent rewrites it to
  `http://127.0.0.1:8888/usb/file/...` (`usbLoopbackURL`), because a
  whitelisted chassis cannot reach its own LAN address on `:17008`. The
  phone page browses the stick through the agent itself over loopback, so it
  gets the loopback form directly. Another speaker plays the stick by its
  LAN URL, while the speaker holding it is on.
- **A stick file always plays directly.** A URL on this speaker's own stick
  is handed to the renderer over UPnP with the MIME of its extension
  (`usbFileMIME`), whether the caller sent a `mime` or not: `/api/play`,
  `/api/queue`, a folder key and a song key alike. Without that, a play with
  no `mime` went the radio way, through the stream proxy, whose SSRF guard
  refuses the speaker's own address (`stream target ... blocked
  (loopback/link-local`), and the speaker answered `AUDIO_ERROR_BAD_URL`.
  The proxy's guard is unchanged.
- **The UDN** is `uuid:str-usb-<device id>`, stable per speaker, so a key
  saved from the stick and the app's last-opened server survive a restart.

### What the user sees

- Desktop Library: "USB stick (Kitchen)" in the server list, browsed like any
  server. No "put on the speakers" button for it (the other speakers cannot
  take it as a native source in v1). A refused stick is one line above the
  list, for example "The USB stick in Kitchen is not formatted as FAT32.
  Format it as FAT32, then copy the music onto it again."
- Phone remote: a "USB stick" button next to the media servers, or the
  refusal line under them.
- No hint about pulling the stick out: the agent switches the firmware's
  mount of a music stick to read-only (How it works), so the stick can be
  pulled at any time. (A first version told the user to switch the speaker
  to standby first; it was dropped for the remount.)
- A hi-res file: "This speaker cannot play this file: it has a sample rate
  above 48 kHz." (`library.usbHiRes` in the app, a short "Above 48 kHz" on a
  key tile, `usbHiRes` on the phone). A folder with such files: "Songs in
  this folder with a sample rate above 48 kHz are skipped, because this
  speaker cannot play them (2 skipped)." (`library.usbHiResSkipped`,
  `usbHiResSkipped`).
- Keys: a song or a folder from the stick saved on a key plays the loopback
  URL. Such a key belongs to the speaker that holds the stick; copied to
  another speaker it points at that speaker's own stick and fails per track.
  With the stick pulled, the key fails per track like a switched-off NAS.

### Debug state

`/api/debug/state` carries `usb_music`: `present`, `variant`, `device`,
`filesystem`, `fat_type`, `cluster_bytes`, `mount_dir`, `mounted_by`
(`firmware` or `agent`), `mount_options`, `name_charset` (`utf8`,
`iso8859-1`, `iso8859-15` or `unknown`, while served), `classification` (`music` or
`install`), `served`, `refusal`, `waiting_for_library` (a FAT stick nothing
mounted yet), the lazily known `root_listed` / `root_folders` / `root_files`,
`browses`, `file_reads`, `last_error`, `remounted_ro` (STR switched the
firmware's mount to read-only and `/proc/mounts` shows it), `remount_error`
(why the last attempt failed, while the mount is still `rw`),
`last_kernel_event` / `last_kernel_event_at` (`attached` or `gone`, the last
kernel USB disk line), `last_decoder_error` (the last stick file the speaker
could not decode), `checked_at`, `checks`. The kernel lines themselves are in
`box_syslog_events` as class `usb_disk`. The agent log has one INFO line per
change of state (`usb music: ...`), one for the remount (or one WARN for its
failure), and one per kernel attach round, never one per request.

### Size

A stripped `GOARM=5` agent grows by 128 KiB (19,333,282 to 19,464,354
bytes). Nothing touches NAND at run time.

### Deferred

- **Durations and tags.** No ID3/FLAC/MP4 tag is read, so a track has no
  length in the DIDL. A single song still gets one from the existing length
  read on a key press, and the queue pre-arms the next MP3's length from its
  header (both allowed to read the stick over loopback); FLAC/M4A/OGG in a
  folder fall back to the frozen-position end detection, which can leave a
  gap of up to about 15 s between tracks. Reading the first 64 KB of the
  visible page's files is the next step if that gap is a complaint.
- **Phone remote search** does not include the stick yet (browsing does).
- **SSDP and the native `STORED_MUSIC` source** (stage 2, Q4): the stick is
  not visible to the Bose app or to other speakers' own menus.
- **Hot-plug events.** No netlink listener; the kernel's own syslog lines
  are the trigger (How it works), and a client's next look is the fallback.
- **More than one partition**, exFAT/NTFS support (the kernel has none,
  T3), and the sample rate of an M4A (ALAC above 48 kHz still reaches the
  speaker and fails per track).

### Live results, 2026-11-21

ST10 (`rhino`) and Portable (`taigan`), FW 27.0.6, stick A, PR #2028.

- **Both automount the stick** at `/media/sda1`, `vfat`, read-write. The
  spike expected the Portable not to (section 2, live 2026-06-11); on this
  run it did. `/proc/mounts` on both:
  `rw,relatime,fmask=0022,dmask=0022,codepage=437,iocharset=iso8859-1,shortname=mixed,errors=remount-ro`.
- **`iocharset=iso8859-1`** made `01 Ärger` show as `01 �rger` and broke the
  file URL, because the names were taken as UTF-8. Fixed: names are decoded
  from the mount's `iocharset` (How it works).
- **The mount is the firmware's and read-write.** STR never writes to it,
  but after stick A was pulled from the speaker, Windows reported its FAT as
  dirty ("Warning"; chkdsk found no damage), and sticks have been damaged
  that way before. A first fix told the user to switch to standby before
  pulling; the maintainer decided against it, and the agent now remounts the
  firmware's mount of a music stick read-only (How it works). Whether
  `remount,ro` takes on these boxes is the first thing to check on the next
  run (`remounted_ro`, `mount_options`).
- **A play without `mime`** went through the stream proxy and failed
  (`AUDIO_ERROR_BAD_URL`, the proxy log said `stream target 192.0.2.1
  blocked (loopback/link-local`); with `mime` it played over UPnP from
  `127.0.0.1:8888`. Fixed: a stick file always plays directly (How it
  works).
- **Formats on the Portable, volume 5** (answers Q5):

  | File | Speaker | STR since this run |
  | --- | --- | --- |
  | MP3 | plays | plays |
  | FLAC 16 bit / 44.1 kHz | plays (heard) | plays |
  | M4A (AAC) | plays (heard) | plays |
  | Ogg Vorbis | plays (heard) | plays |
  | WAV 44.1 kHz | plays (heard) | plays |
  | ALAC `.m4a` | plays (heard) | plays |
  | FLAC 24 bit / 96 kHz | fails, `AUDIO_ERROR_DECODER` | listed; refused when played, skipped in a folder |
  | WAV 96 kHz | stays `BUFFERING`, never plays | listed; refused when played, skipped in a folder |

  The MP3 (`01 Ärger.mp3`) first answered 404, the umlaut path bug fixed
  above. Hi-res files stay listed (the listing reads no file); the sample
  rate is read from the header when one is played (How it works), so a
  queue no longer reaches a 96 kHz WAV that would stay `BUFFERING`.
- **The speaker name** is UTF-8 on the wire. On the live Portable,
  `USB stick (BüroPortable)` was checked byte for byte in
  `/api/library/servers`; the ST10's name `Küche` was checked through
  `/api/agent/version` and `/info`, which carry the same name the stick's
  entry is built from.
  A reading of `KÃ¼che` comes from a client that decodes the answer in a
  Windows code page instead of UTF-8, not from the agent;
  `TestTheStickNameKeepsANonASCIISpeakerName` pins the bytes.

### v1 test steps on a speaker

Stick A as in section 9 (16 GB FAT32 from STR's Format button, no STR
files). Speaker addresses are placeholders; `:8888` for `sm2`, `:17008` for
whitelisted boxes. Audible playback on the Portable only, at volume 5; on the
ST10 and the ST30 check `now_playing` silently (volume 0 or muted).

1. **ST10 (`sm2`), hot-plug.** Agent running, insert stick A, wait 30 s.
   `curl http://192.0.2.10:8888/api/library/servers`: the last entry is
   `{"kind":"usb","udn":"uuid:str-usb-...","location":"http://192.0.2.10:8888/usb/device.xml",...}`.
   `curl .../api/debug/state` and read `usb_music`: `served:true`,
   `mounted_by:"firmware"`, `mount_dir:"/media/sda1"`, `fat_type:"FAT32"`,
   `cluster_bytes:32768`, `remounted_ro:true`, `mount_options` starting
   with `ro,`, and `proc_mounts` shows the `iocharset` and `ro`.
   `last_kernel_event:"attached"` says the kernel line arrived; if it is
   empty, the attach line did not reach the ring and the stick was found by
   the server list instead. If the firmware did NOT mount it (T1 answers
   no), `mounted_by:"agent"`, `mount_dir:"/tmp/str-music"`,
   `mount_options:"ro"`.
2. **Browse.** Desktop app (a build from this branch): Library, Search
   again. "USB stick (<name>)" is listed; open `Album 1`. Check: `01 Ärger`
   shows its umlaut, order is 01, 02, 03, 04, 05, the cover shows, the
   `.opus` file is not listed, `alac.m4a`, `hires-2496.flac`, `wav-96k.wav`
   are listed. Phone remote on the same speaker: the "USB stick" button,
   same folder.
3. **Play, silently on the ST10.** Play `02 Test.flac` from the app on the
   ST10 at volume 0. `curl .../api/status`: the location is
   `http://127.0.0.1:8888/usb/file/Album%201/02%20Test.flac` (loopback, not
   the LAN address), play state PLAY. `usb_music.file_reads` goes up. Then
   "Play folder" on `Album 1`: the queue advances (watch `now_playing`).
4. **Key.** Save `Album 1` on key 6 of the ST10 from the app, press key 6
   on the speaker (volume 0): the folder starts. Remove the stick, press key
   6: it fails per track like a switched-off NAS, the speaker does not hang.
   `usb_music.present:false`, and the server list has no stick entry.
5. **ST30 (`scm`), the risky one.** Same as 1 to 3, at volume 0, with the
   port `:17008`. In step 1 the `location` is on `:17008`; in step 3 the
   location is still the loopback `127.0.0.1:8888` form (this is the
   chassis the rewrite exists for). While the 300 MB file plays (or during
   T2's `dd` if SSH is open), poll `curl -m 3 http://192.0.2.11:17008/api/agent/version`
   by hand every few seconds for two minutes. If the speaker drops off the
   network: pull the stick, note it in the PR, and `mojo` leaves
   `usbMusicChassis`.
6. **Portable, audible at volume 5.** Insert stick A (OTG adapter). On
   2026-11-21 the firmware mounted it (`mounted_by:"firmware"`,
   `name_charset:"iso8859-1"`); the rest of this step is for a stick it does
   not mount. Before opening the Library, `usb_music` (reading it only detects) says
   `waiting_for_library:true` and `mounted_by` is empty. Open the Library in the app: `mounted_by:"agent"`,
   `mount_options:"ro"`, and on the speaker (SSH, read-only)
   `grep str-music /proc/mounts` shows `ro,nosuid,nodev,noexec`. Play
   `01 Ärger.mp3`, `02 Test.flac`, `03.m4a`, `04.ogg`, `05.wav` at volume 5
   and note per file: plays, refused, or hangs. Then `hires-2496.flac` and
   `wav-96k.wav`: the app says the speaker cannot play them and the speaker
   is not touched; "Play folder" on `Album 1` skips both and says "(2
   skipped)". Pull the stick: `/tmp/str-music` is unmounted
   (`usb_music.mount_dir` empty), and on a firmware mount the stick shows no
   dirty FAT on a PC.
7. **Refusals.** Stick B (exFAT) in the ST10: the Library line "is not
   formatted as FAT32", `usb_music.refusal:"not-fat32"`. An STR install
   stick in the ST10: "is an ST Reborn install stick", nothing listed, and
   on the Portable `/tmp/str-music` is not left mounted.
8. **Boot with stick A in** (ST10): the boot is not slower than without it
   (the #2027 fix), the agent log has `usb music: FAT stick found` or
   `serving the music stick` once, and nothing is mounted by the agent
   before the Library is opened.

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
| Portable (`taigan`, `scm`) | micro-USB | yes, with an OTG adapter | **yes** | **Limited.** Did not auto-mount on 2026-06-11, did on 2026-11-21 (`/media/sda1`, `rw`); where nothing mounts it the agent mounts it itself. The stick draws from the battery. |
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
- **The Portable did not auto-mount** on 2026-06-11 (`/dev/sda1` existed,
  nothing was mounted, `/media/sda1` was a stale directory). On 2026-11-21
  it **did**: `/media/sda1`, `vfat`, `rw`, the same options as the ST10 (see
  v1, Live results). The agent's own read-only mount stays as the fallback
  for a stick nothing mounted.
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
  `codepage`) decide whether file names with umlauts arrive as UTF-8. Live
  on ST10 and Portable they do not: `codepage=437,iocharset=iso8859-1`, so
  names arrive as Latin-1 bytes and the agent decodes them (v1, How it
  works).

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
   about 55 s of every boot and gets `fsck -a` run on it. **Done in #2027**:
   the boot scripts and `mountStick` now leave a stick without a marker alone.
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
  Answered for the ST10 and the Portable (2026-11-21): yes, at `/media/sda1`,
  `rw`, `codepage=437,iocharset=iso8859-1`. ST20 and ST30 are still open.
- **Q2** Does continuous stick reading on `scm` (ST30, Portable) or on the
  `sm2` ST30 trip VBUS or over-current, and does the LAN survive it?
- **Q3** Which filesystems does the kernel offer (`/proc/filesystems`,
  `/lib/modules`)? exFAT? NTFS read-only?
- **Q4 (stage 2)** Can the agent share UDP 1900 with BoseApp? Does
  `/listMediaServers` list a server on the box's own IP, and does
  `STORED_MUSIC` play from it on a whitelisted chassis?
- **Q5** 24-bit/96 kHz FLAC, ALAC `.m4a`, 96 kHz WAV: play, fail cleanly,
  or wedge the renderer? Answered on the Portable, 2026-11-21: FLAC 24/96
  fails with `AUDIO_ERROR_DECODER`, WAV 96 kHz stays `BUFFERING`, ALAC
  plays (heard). STR now refuses FLAC and WAV above 48 kHz itself.
- **Q6** Does `umount` of a read-only stick work on these boxes? (A `rw`
  `umount /media/sda1` answered "Invalid argument" once.)
- **Q7** Does `mount -o remount,ro /media/sda1` take on the firmware's
  `vfat` mount (ST10, Portable, then ST20/ST30), and does the firmware mind
  a read-only stick? `remounted_ro` and `remount_error` in `usb_music`
  answer the first part.
- **Q8** Do the kernel's `Attached SCSI removable disk` and `USB disconnect`
  lines reach the syslog ring on every chassis? The `usb_disk` events in
  `box_syslog_events` and `last_kernel_event` answer it.

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

## 10. Code

- `internal/usbmedia`: `Classify` (the marker rule), `Library` (Browse,
  Lookup, OpenFile, OpenArt, all through `os.Root`), `detect.go`
  (partitions from sysfs, `/proc/mounts`, the boot-sector sniff),
  `cds.go` (device and service description, the Browse control endpoint,
  file serving), `charset.go` (file names from the mount's `iocharset` to
  UTF-8 and back). Tested against the repo's own `dlna` client, which is
  what the desktop app and the phone page browse with.
- `internal/usbmedia/samplerate.go`: the sample rate from a FLAC or WAV
  header.
- `internal/boxlog/usbdisk.go`: the kernel's USB disk lines (`usb_disk`) and
  the hook the agent looks at the stick from.
- `internal/webui/usbmusic.go`: the stick state (detect, mount read-only
  where needed, remount the firmware's mount read-only, classify, serve,
  drop), `OnUSBDiskLine`, the `/usb/` routes, the stick's entry in
  `/api/library/servers`, the loopback resolve that lets every existing
  library path browse the stick, `usbLoopbackURL`, `usbFileMIME` (a stick
  file always plays directly), and the `usb_music` debug section. Registered
  in `cmd/agent/agent.go`.
- `internal/webui/usbhires.go`: the hi-res refusal, the folder skip, the
  decoder note.
- `desktop-app/library_usb.go`: asks every STR speaker for its stick during
  the Library scan; `LibraryUSBNotices` for the refusal lines.
- Frontend: `views/library.js` (`localizeUSBServers`, `libraryUSBNote`,
  `isUSBFileURL`, `usbHiResSkippedNote`), `utils.js` (`friendlyErrorText`
  for `library-file-hires`) and the phone page's `loadBrowseServers`,
  `playRefusal` and status line with their `I18N6` strings.

The spike prototype's size measurement (a throwaway `main`, GOARM=5,
`-s -w`) was +64 KiB; the finished v1 adds 128 KiB to the agent.