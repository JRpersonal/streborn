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
   restarts on its own.
5. **Leave the power alone until it is finished.** This is the only
   genuinely dangerous moment in the whole procedure.

Afterwards, check the version again as in step 1. Then install STR.

**Install STR only after the firmware is right.** STR's agent occupies
the port the speaker's own setup and update pages live on, so updating
afterwards is harder than updating first.

## 4. When 27.0.6 will not take

Speakers on a much older firmware have been reported to refuse the jump
straight to 27.0.6. The route that worked in the field was to go **down**
first and then up: install `6.02.01` from the same catalogue path shape,
then `27.0.6`. That is what got a SoundTouch 20 on firmware 7.0.37 from
"STR will not install" to a working speaker (discussion #447 and #476,
July 2026).

Two things reported alongside it, worth knowing before you start:

- On firmware **7.2.21 and older** the setup access point is opened with
  **2** and **volume down**, not the combination the current manuals
  name.
- A factory reset with **1** and **volume down** held for ten seconds
  before the update clears a half-configured state that can otherwise
  make the speaker refuse the image.

## 5. What is verified here and what is not

Verified by this project, on 2026-09-08: the catalogue and the images
are still served by Bose (`Update.stu` answers with the full payload),
and the table above is taken directly from that catalogue.

Reported by users and not reproduced by this project: the button
combinations, the downgrade route, and the behaviour of the speaker's
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
  and [#476](https://github.com/JRpersonal/streborn/discussions/476).
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
