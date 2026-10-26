# Bringing a speaker to firmware 27.0.6

STR is built and tested against Bose firmware **27.0.6.46330.5043500**,
the last release Bose ever published (September 2022). A speaker that
never got that far can still be brought to it, and this page is how.

Bose's own end-of-life page says "firmware updates are no longer
available now that the cloud is offline". That is true of the update
button in the Bose app, which asked a server that is gone. It is not
true of the images themselves: they are still served from Bose's own
download host, and only the web pages that used to link them were taken
down. Verified on 2026-09-08.

STR does not host, mirror or modify any Bose image. Every link below
points at Bose's own server, and the checksums below are Bose's own.

---

## The easy way: let the app do it

When the STR app sees a speaker on your home network whose firmware is
older than 27.0.6, the firmware notice (in the speaker's settings, and on
the setup screen while it waits for a stock speaker) has an **Update now**
button. The app then:

1. downloads Bose's file for exactly this model from Bose's own host into
   the app's cache folder on your computer, and checks it against the
   size and CRC in Bose's catalogue (a file that does not match is thrown
   away, never sent);
2. uploads it to the speaker's own update page
   (`http://<speaker>:17008/update.html`, the page Bose's support
   articles use), over your home network, with no setup mode, no USB
   stick and no change of Wi-Fi;
3. waits for the speaker to restart and reports the version it comes
   back with.

It takes about ten minutes: roughly five for the upload over Wi-Fi, then
the speaker installs and restarts. Leave it powered the whole time; a
Portable belongs on its power supply, not on battery.

This works on a speaker that already runs STR too. STR occupies the
update page's port on several models, so the app hands the file to STR
on the speaker, which passes it on to Bose's page locally. A firmware
update rewrites Bose's firmware only: STR, its settings, the speaker's
name, Wi-Fi and presets stay as they were (verified on a Portable on
2026-10-13, 27.0.3 to 27.0.6 on a stock speaker, and 27.0.6 over 27.0.6
with STR installed).

The button only appears when the app can name one file for the speaker
for certain. Otherwise, or if the update page does not answer (very old
firmware may not have it), use the manual routes below.

## 1. Which firmware is on the speaker now

In the STR app, open the speaker's settings; the firmware version is
listed there. Without STR, hold **5** and **volume down** together on
the speaker until the display or the app shows the version.

Anything that does not start with `27.` needs this page. `27.0.6` is the
end of the line, there is nothing newer to chase.

## 2. Download the right image

Bose publishes one catalogue for every model, and it names the file,
its exact length and its CRC:

    https://downloads.bose.com/ced/soundtouch/downloads_stockholm/index.xml

Find your model in the table below, download the file, and check that
the size matches to the byte before you go near the speaker. Two
generations of the same model exist and they take **different images**:
the newer chassis has `sm2` in its path. If you are unsure, the STR app
names the chassis on the speaker's settings page, and
[`docs/MODEL-VARIANTS.md`](MODEL-VARIANTS.md) explains the difference.

All paths below are relative to `https://downloads.bose.com`, and all
carry version `27.0.6.46330.5043500`.

| Model | Chassis | Path | Bytes | CRC |
|---|---|---|---|---|
| SoundTouch 10 | sm2 | `/ced/soundtouch/downloads_stockholm/stu/r/sm2/Update.stu` | 95 906 856 | `0xf18fe026` |
| SoundTouch 20 | older | `/ced/soundtouch/downloads_stockholm/stu/s/Update.stu` | 105 879 988 | `0x2d5a971e` |
| SoundTouch 20 | sm2 | `/ced/soundtouch/downloads_stockholm/stu/s/sm2/Update.stu` | 99 445 800 | `0x536e6d6f` |
| SoundTouch 30 | older | `/ced/soundtouch/downloads_stockholm/stu/s/Update.stu` | 105 879 988 | `0x2d5a971e` |
| SoundTouch 30 | sm2 | `/ced/soundtouch/downloads_stockholm/stu/s/sm2/Update.stu` | 99 445 800 | `0x536e6d6f` |
| SoundTouch Portable | older | `/ced/soundtouch/downloads_stockholm/stu/s/Update.stu` | 105 879 988 | `0x2d5a971e` |
| SoundTouch 300 | sm2 | `/ced/soundtouch/downloads_stockholm/stu/g/sm2/Update.stu` | 102 384 736 | `0xed21efd3` |
| Wave SoundTouch | older | `/ced/soundtouch/downloads_stockholm/stu/n/Update.stu` | 112 367 416 | `0x49d88de4` |
| Wave SoundTouch | sm2 | `/ced/soundtouch/downloads_stockholm/stu/n/sm2/Update.stu` | 100 297 132 | `0x2e2fd417` |
| SA-4, Stereo JC | older | `/ced/soundtouch/downloads_stockholm/stu/l/Update.stu` | 110 991 796 | `0x01e35713` |
| SA-4, Stereo JC | sm2 | `/ced/soundtouch/downloads_stockholm/stu/l/sm2/Update.stu` | 98 921 512 | `0x07521bda` |
| SA-5 | sm2 | `/ced/soundtouch/downloads_stockholm/stu/b/sm2/Update.stu` | 100 957 944 | `0x0b99190b` |
| CineMate | older | `/ced/soundtouch/downloads_stockholm/stu/t/Update.stu` | 114 125 624 | `0x562de6f1` |
| CineMate | sm2 | `/ced/soundtouch/downloads_stockholm/stu/t/sm2/Update.stu` | 102 700 460 | `0xfbcab635` |
| Lifestyle, VideoWave | older | `/ced/soundtouch/downloads_stockholm/stu/m/Update.stu` | 215 402 464 | `0xd48b1181` |
| Lifestyle, VideoWave | sm2 | `/ced/soundtouch/downloads_stockholm/stu/m/sm2/Update.stu` | 203 977 300 | `0x3f489bd5` |
| Wireless Link adapter | sm2 | `/ced/soundtouch/downloads_stockholm/stu/s/sm2/Update.stu` | 99 445 800 | `0x536e6d6f` |

Bose's own support pages name a different path for the SA-5. Where they
disagree, the catalogue above is the one the speaker itself uses.

### Downloading and checking it, step by step

Checking the size is the important part and takes a second: a truncated
download is the one failure mode that can leave a speaker mid-flash.

**Windows, PowerShell.** Replace the path with the one for your model:

```powershell
$url = 'https://downloads.bose.com/ced/soundtouch/downloads_stockholm/stu/r/sm2/Update.stu'
Invoke-WebRequest $url -OutFile Update.stu
(Get-Item Update.stu).Length          # must match the table exactly
```

**macOS or Linux:**

```bash
curl -L -o Update.stu https://downloads.bose.com/ced/soundtouch/downloads_stockholm/stu/r/sm2/Update.stu
stat -f %z Update.stu                 # macOS
stat -c %s Update.stu                 # Linux
```

If the number does not match the table, delete the file and download it
again; a resumed or interrupted download is worse than no download. The
CRC in the table is Bose's own and can be checked with any CRC-32 tool if
you want the second confirmation.

Plain `http://` works too if your network breaks the TLS connection.

## 3. Put it on the speaker with a USB stick

This is the route Bose built into the speaker itself, and it needs no
server and no app.

1. **Format a stick as FAT32.** Use one of **32 GB or smaller**. A
   larger stick formatted FAT32 by a third-party tool usually ends up
   with 64 KB clusters, which the speaker's own kernel cannot read: the
   file appears in the directory and then fails to open.
2. **Copy the file to the root of the stick as `Update.stu`.** Do not
   rename it to anything else and do not put it in a folder.
3. **Plug it into the speaker's service port.** SoundTouch 10 and
   Portable have a micro-USB port, so they need an OTG-capable stick or
   a micro-USB OTG adapter with a normal stick; a plain micro-USB stick
   will not be seen. SoundTouch 20 and 30 of the Series II and III
   generation have a normal USB-A port on the back. The oldest Series I
   units may again need the OTG route.
4. **Start the update.** With the speaker unplugged, hold **4** and
   **volume down** while reconnecting power, and keep holding until the
   speaker shows it is updating. It takes a few minutes and the speaker
   restarts on its own. The Portable's battery cannot be removed, so
   unplugging does not switch it off: leave it on its power adapter,
   switch it off with the power button, then hold **4** and **volume
   down** while switching it on. That combination is not confirmed on
   the Portable yet.
5. **Leave the power alone until it is finished.** This is the only
   genuinely dangerous moment in the whole procedure.

Afterwards, check the version again as in step 1. Then install STR.

**Bring the firmware to 27.0.6 before installing STR if you can.** The
app's **Update now** button also works on a speaker with STR, but STR is
tested on 27.0.6 only, so updating first saves an install that fails.

## 4. When 27.0.6 will not take

Speakers on very old firmware sometimes refuse the jump straight to
27.0.6. After the hand symbol the display shows an empty bar that never
fills, even with a stick that is known to be good. Going up in smaller
steps has helped.

### The step-by-step guide in the app

For the first generation SoundTouch 20, 30 and Portable (the older
chassis, `scm`), the app does the stepping for you. When such a speaker
reports a firmware older than 15, the firmware notice turns into a
numbered list of updates instead of the single one. For each step the
app:

1. downloads the step's zip from the Internet Archive, on your computer,
   only when you click **Prepare stick** (or takes a zip or `Update.stu`
   you downloaded yourself in the browser);
2. checks the zip's size and sha1 against the Internet Archive's own
   metadata, then the `Update.stu` inside against its size, CRC-32 and,
   where known, sha1 (a file that does not match is never written);
3. copies only `Update.stu` to the root of the stick you picked, flushes
   it, reads it back and checks the CRC again, and ejects the stick;
4. shows the button steps for the speaker (4 and volume down while
   plugging the power back in; on the Portable, which cannot be unplugged,
   while switching it on with the power button, marked as not confirmed)
   and, after **Done, check again**, reads the version and moves on.

The last step is Bose's own 27.0.6 file, with the normal **Update now**
over the network, or by stick if the update page does not answer. The
app remembers the step per speaker, so it picks up again after a restart.

| Step | Source | `Update.stu` bytes / CRC-32 | Confidence | Seen working on a speaker |
|---|---|---|---|---|
| 06.02.01 | archive, `Airplay-1_ST20-30-Portable_06.02.01.zip` | 80 559 847 / `a70e1564` | high | yes, 5.x to 6.02.01 by stick |
| 07.00.37 | archive, `Airplay-1_ST20-30-Portable_07.00.37.zip` | 93 032 539 / `a46e94d0` | high | yes, 6.02.01 to 7.00.37 by stick |
| 14.00.33 | archive, `Airplay-1_ST20-30-Portable_14.00.33.zip` | 93 114 711 / `d4dc260b` (same as Bose's catalogue) | medium | no |
| 15.00.20 | archive, `..._Update_ti_15.00.20.37272.2363762.scm.stu.zip` | 101 642 152 / `f8648557` | medium-high | no |
| 27.0.6 | Bose, catalogue path in section 2 | 105 879 988 / `2d5a971e` | high | over the network from 27.0.3 only |
| 09.00.41 (only if 14.00.33 stalls) | archive, `..._09.00.41.zip` | 92 333 191 / `c4fd3a70` | low | no |
| 12.00.09 (only if 14.00.33 stalls) | archive, `..._12.00.09.zip` | 93 083 567 / `c4d62d01` | low | no |

A speaker on 6.x skips 06.02.01, one on 7.x to 14.0.15 starts at
14.00.33, one on 14.0.33 at 15.00.20, and from 15.00.20 on it is Bose's
27.0.6 alone. A speaker on 2.x is untested: if it refuses 06.02.01 the
app offers 05.00.23 first.

Other models (Wave, SA-4, CineMate, every Bluetooth chassis) have no
step-by-step path in the app yet. They keep the single-step guide.

**Maintaining the ladder.** The table lives in
`desktop-app/bosefw/ladder_data.go`. Every zip size and sha1 comes from
the Internet Archive item's metadata JSON, every inner size and CRC-32
from the zip's own central directory, and 14.00.33 and 27.0.6 also match
Bose's catalogue. Never type a value from memory and never commit an
image. `LiveVerified` is set only after a field report with a diagnostic
bundle shows the step installed.

**Where the older images come from.** Bose still serves 26.0.1, 27.0.1,
27.0.2, 27.0.3 and 27.0.6. The paths for 14.0.33 up to 25.0.0 answer 403
(checked 2026-10-25). For everything older the only source I know is a
community collection on the Internet Archive. Those files do not come
from Bose. The app checks them against the sizes and checksums the
Internet Archive publishes and the CRC-32 inside each zip, which shows
the file is the one that was uploaded, not that Bose made it. STR links
them and never mirrors them. You use them at your own risk.

For the first generation SoundTouch 20, 30 and Portable (no Bluetooth,
chassis `older` in the table in section 2) the folder is:

    https://archive.org/download/bose-soundtouch-software-and-firmware/Firmware/2013-2015_No_Bluetooth/Airplay-1_ST20-30-Portable/

Do not take files from the Bluetooth folders for these speakers. Those
are for the newer chassis.

| Version | Source | `Update.stu` bytes | Status |
|---|---|---|---|
| 6.02.01 | archive, `Airplay-1_ST20-30-Portable_06.02.01.zip` | 80 559 847 | Reported: installed on two SoundTouch 20 |
| 7.00.37 | archive, `Airplay-1_ST20-30-Portable_07.00.37.zip` | 93 032 539 | Reported: the firmware the July speaker came from |
| 14.00.33 | archive, `Airplay-1_ST20-30-Portable_14.00.33.zip` | 93 114 711 | Untested idea |
| 26.0.1 | Bose, `/ced/soundtouch/st3_2020_a3b99494/stu/s/Update.stu` | 105 627 340 | Untested idea |
| 27.0.6 | Bose, catalogue path in section 2 | 105 879 988 | Target |

The zips contain a folder with the `Update.stu` inside. Copy only that
file to the root of the stick. The archived Bose catalogues for 14.0.33
and 26.0.1 list the same lengths and the same device ID as these files.

**Order to try.** Reset the speaker before every step (see below).

1. 27.0.6 again from a different, smaller stick (2 to 8 GB, FAT32, only
   `Update.stu` on it). Give it up to 45 minutes.
2. 6.02.01, then 27.0.6.
3. 7.00.37, then 27.0.6. To copy the July route exactly, go 7.00.37,
   6.02.01, 27.0.6.
4. 14.00.33, then 26.0.1, then 27.0.6.

A step that worked can be repeated. 6.02.01 has re-flashed fine from a
stick on a speaker where 27.0.6 had just failed.

**Field reports so far.**

- July 2026 ([discussion #447](https://github.com/JRpersonal/streborn/discussions/447),
  [issue #476](https://github.com/JRpersonal/streborn/issues/476)): a
  SoundTouch 20 on 7.0.37. 27.0.6 from the first stick did not install.
  With a different stick, 6.02.01 and then 27.0.6 worked. The stick and
  the route changed at the same time, so either may have been the fix.
- October 2026 (mail): a first generation SoundTouch 20 on 5.x. 27.0.6
  hung on an empty bar for over 20 minutes with a checked 16 GB FAT32
  stick (8 KB clusters). 6.02.01 installed fine and the speaker worked
  again. 27.0.6 after that hung again. 7.00.37 from 6.02.01 then
  installed by stick too. The next step from 7.00.37 is not tried yet.
- [Issue #854](https://github.com/JRpersonal/streborn/issues/854): a
  SoundTouch 20 on 14.0.15 filled the bar to 100 % with 27.0.6 and then
  restarted in a loop. So a jump from 14.x straight to 27.0.6 is not safe
  either.

Two more things reported alongside, worth knowing before you start:

- On firmware **7.2.21 and older** the setup access point also opens
  with **2** and **volume down**, the same combination Bose's current
  articles name (community-reported).
- A factory reset with **1** and **volume down** held for ten seconds
  before the update clears a half-configured state that can otherwise
  make the speaker refuse the image.

## 5. What is verified here and what is not

Verified by this project, on 2026-09-08: the catalogue and the images
are still served by Bose (`Update.stu` answers with the full payload),
and the table above is taken directly from that catalogue.

Verified on 2026-10-13: the network upload to `:17008/update.html` on a
Portable, from 27.0.3 and again over 27.0.6 with STR installed. Other
models use the same page; the app's own upload is being tested across the
maintainer's speakers (SoundTouch 10, 30 and Portable).

Reported by users and not reproduced by this project: the button
combinations, the step routes in section 4, and the behaviour of the speaker's
own update check now that the cloud is gone. They are written down here
because they worked for somebody, not because we tested them.

Not known: whether the speaker verifies a signature on the image. If it
does, a damaged download is simply refused. If it does not, the size
check in step 2 is what protects you.

STR itself has never been tested below firmware 27.0.6. The app will let
you try, but three field reports failed at install on older firmware, so
updating first is the better-supported path by a distance.

---

## 6. Every source, so you can check this yourself

Nothing here has to be taken on trust. These are the pages and files this
document is built from.

**Bose's own material**

- The end-of-life page, which states both the shutdown date and that
  updates are no longer available:
  <https://www.bose.com/soundtouch-end-of-life>
- The model catalogue every table entry above comes from, with the
  lengths and CRCs:
  <https://downloads.bose.com/ced/soundtouch/downloads_stockholm/index.xml>
- The images themselves, one per line in the table; for example the
  SoundTouch 10:
  <https://downloads.bose.com/ced/soundtouch/downloads_stockholm/stu/r/sm2/Update.stu>
- Bose's support article on updating a SoundTouch over USB, still online
  and still describing the procedure, only its download links are gone:
  <https://support.bose.com/s/article/soundtouch-20-iii-updating-the-software-or-firmware-of-your-product?language=en_US>

**This project**

- The field case behind section 4, a SoundTouch 20 on firmware 7.0.37
  that could not install STR until it was taken down and then up again:
  [discussion #447](https://github.com/JRpersonal/streborn/discussions/447)
  and [issue #476](https://github.com/JRpersonal/streborn/issues/476).
- The community collection of older images used in section 4:
  <https://archive.org/details/bose-soundtouch-software-and-firmware>.
  Not from Bose, checked by size only.
- Which chassis your speaker is, and why two generations of the same
  model take different images:
  [`docs/MODEL-VARIANTS.md`](MODEL-VARIANTS.md).
- Which models are known to work with STR and how far each is verified:
  [`docs/MODELS.md`](MODELS.md).

**Community**

- The downgrade guide that documents the button combinations and the
  older setup access point:
  <https://bose.fandom.com/wiki/SoundTouch_Firmware_Downgrade_Guide>.
  Reported, not reproduced by this project; see section 5.

## 7. Pictures

This page has none, on purpose. The pictures that would help are of the
service port on each model and of the button panel while a combination is
held, and photographing somebody else's hardware from memory is how wrong
instructions get written. If you have a speaker in front of you and take
those two photographs, they are welcome as a pull request or in a
discussion, and they will be added here with credit.
