# Bringing a speaker to firmware 27.0.6

STR is built and tested against Bose firmware **27.0.6.46330.5043500**,
the last release Bose ever published (September 2022). A speaker that
never got that far can still be brought to it. This page shows how,
including the in-between steps very old speakers need.

Bose's end-of-life page says "firmware updates are no longer available
now that the cloud is offline". That is true of the update button in the
Bose app, which asked a server that is gone. It is not true of the files
themselves: Bose's own download host still serves them. Only the web
pages that used to link them were taken down. Verified on 2026-09-08.

STR does not host, mirror or change any firmware file. Every link on
this page points at Bose's own server or at the Internet Archive.

**Contents**

1. [Find the version your speaker runs now](#1-find-the-version-your-speaker-runs-now)
2. [Pick your route](#2-pick-your-route)
3. [Route A: one click in ST Reborn](#3-route-a-one-click-in-st-reborn)
4. [Route B: Bose's update page on the speaker's own Wi-Fi](#4-route-b-boses-update-page-on-the-speakers-own-wi-fi)
5. [Route C: USB stick](#5-route-c-usb-stick)
6. [What you see while it updates](#6-what-you-see-while-it-updates)
7. [Very old firmware: the step-by-step ladder](#7-very-old-firmware-the-step-by-step-ladder)
8. [Checking a download](#8-checking-a-download)
9. [Bose's files for 27.0.6, per model](#9-boses-files-for-2706-per-model)
10. [If you are stuck, send us this](#10-if-you-are-stuck-send-us-this)
11. [What is verified and what is not](#11-what-is-verified-and-what-is-not)
12. [Every source](#12-every-source)

---

## 1. Find the version your speaker runs now

Use whichever of these you can reach. You need the first two or three
numbers, for example `7.0.37` or `27.0.3`.

- **ST Reborn app.** Open the speaker's settings. The firmware version
  is listed there. If the speaker is not found on your network, the
  setup screen also shows the version once it reaches the speaker.
- **Bose SoundTouch app**, if you still have it. Open the settings,
  pick the speaker and look at its speaker information.
- **The speaker's info page.** In a browser on the same network, open
  `http://<speaker address>:8090/info`. Look for `softwareVersion`
  next to `SCM`. Your router's device list shows the speaker's address.
  If the speaker is not on your network, put it into setup mode
  ([section 4](#4-route-b-boses-update-page-on-the-speakers-own-wi-fi),
  steps 2 and 3), join its own Wi-Fi and open
  `http://192.0.2.1:8090/info` (or `http://192.168.1.1:8090/info`).
  Unverified on very old firmware.
- **On the speaker itself.** Hold **5** and **Volume minus** together
  until the display shows the version. Reported by owners, not tested
  by this project.

Anything that does not start with `27.` needs this page. `27.0.6` is the
end of the line. There is nothing newer.

## 2. Pick your route

| Your version | Route |
|---|---|
| 27.0.6 | Nothing to do. |
| 15.x or newer | **Route A**, one click in ST Reborn. Untested on 15.x so far. If it fails, Route B or C with Bose's file from [section 9](#9-boses-files-for-2706-per-model). |
| 14.x or older | **The ladder in [section 7](#7-very-old-firmware-the-step-by-step-ladder)**, one USB stick step at a time. Do not jump straight to 27.0.6. |
| Unknown | Find it first. Hold **5** and **Volume minus**, or open the info page in setup mode ([section 1](#1-find-the-version-your-speaker-runs-now)). If you still cannot see it, ask us ([section 10](#10-if-you-are-stuck-send-us-this)). Do not put 27.0.6 on a stick blindly: on a 14.x speaker that jump can end in a restart loop. |

Why not jump from 14.x or older to 27.0.6 in one go? Readers tried. On
one speaker, both from 5.x and again from 06.02.01, the progress bar
stayed empty. On a 14.0.15 speaker the bar filled, then the speaker
restarted in a loop
([issue #854](https://github.com/JRpersonal/streborn/issues/854)).

**Update the firmware before you install STR.** STR is tested on 27.0.6
only. Three field reports failed at install on older firmware.

## 3. Route A: one click in ST Reborn

For speakers on **15.x or newer** that ST Reborn finds on your home
network.

When the app sees a speaker older than 27.0.6, the firmware notice (in
the speaker's settings, and on the setup screen) has an **Update now**
button. The app then:

1. downloads Bose's file for exactly this model from Bose's own host and
   checks its size and checksum against Bose's catalogue. A file that
   does not match is thrown away, never sent;
2. sends it to the speaker's own update page over your home network.
   No setup mode, no USB stick, no change of Wi-Fi;
3. waits for the speaker to restart and shows the version it comes back
   with.

It takes about ten minutes. Leave the speaker powered the whole time.
A Portable belongs on its power supply, not on battery.

This also works on a speaker that already runs STR. A firmware update
replaces Bose's firmware only. STR, its settings, the speaker's name,
Wi-Fi and presets stay as they were (verified on a Portable on
2026-10-13: 27.0.3 to 27.0.6 on a stock speaker, and 27.0.6 over 27.0.6
with STR installed).

The button only appears when the app can name one file for the speaker
for certain. If it does not appear, or the upload fails, use Route B or
C.

## 4. Route B: Bose's update page on the speaker's own Wi-Fi

This is the route Bose's own support articles describe. It needs a
computer with Wi-Fi and a browser.

1. Download Bose's file for your model ([section 9](#9-boses-files-for-2706-per-model))
   and check it ([section 8](#8-checking-a-download)). Do not rename it.
2. Put the speaker into setup mode:
   - SoundTouch 10, 20, 30, Portable: hold **2** and **Volume minus**
     until the Wi-Fi light glows amber.
   - SoundTouch SA-5: hold the Control button for about 3 seconds.
   - SoundTouch 300: press SoundTouch on the remote, then hold **9**.
   - Wave SoundTouch: hold Control on the back of the pedestal for about
     3 seconds.
   - Wireless Link adapter: hold the button on the back for about 10
     seconds.
3. On the computer, join the speaker's own Wi-Fi. Its name starts with
   "Bose". Stay connected even if the computer says there is no
   internet.
4. Open `http://192.168.1.1:17008/update.html`. If that does not open,
   try `http://192.0.2.1:17008/update.html` (Bose's articles name both).
5. Click **Choose File**, pick the file, click **Upload**. Do not close
   the page or unplug the speaker until it restarts by itself.
6. Switch the computer back to your home Wi-Fi.

Bose also describes a USB cable route: connect the computer to the
speaker's USB port (the smaller one if there are two) and open
`http://203.0.113.1:17008/update.html` instead of steps 2 to 4.

Very old firmware may not have this page. Then use Route C.

## 5. Route C: USB stick

The speaker can install an update from a USB stick by itself. No app,
no network.

### The stick

- **Small: 2 to 8 GB is best.** Larger sticks often fail. Over 32 GB
  almost always fails, because the speaker cannot read the large
  clusters such sticks get.
- **Formatted as FAT32.** Not exFAT, not NTFS. Windows offers FAT32
  only for sticks up to 32 GB, one more reason to use a 2 to 8 GB stick.
- **Only one file on it: `Update.stu`, at the top level** (the root),
  not in a folder.
- **Keep the exact name `Update.stu`.** Do not rename it. A browser that
  saves it as `Update(1).stu` or `Update.stu.zip` breaks it. Check
  that file extensions are visible on your computer.
- **No other files.** Delete everything else, including hidden folders.
  If you can, format the stick fresh before you copy the file. On
  Windows: right-click the stick in File Explorer, choose **Format**,
  pick **FAT32**. On a Mac: Disk Utility, **Erase**, format **MS-DOS
  (FAT)**.
- **Formatting erases everything on the drive.** Before you click,
  check that the drive letter, name and size are the USB stick's, not
  another disk.
- **Eject the stick properly** before you pull it from the computer.

For the ladder steps from the Internet Archive, the download is a zip.
Open the zip and copy only the `Update.stu` inside it to the stick.

### The port

- SoundTouch 20 and 30 of Series II and III have a normal USB-A port
  on the back.
- SoundTouch 10 and Portable have a small micro-USB port. You need an
  OTG adapter (micro-USB to USB-A) or an OTG stick. A plain micro-USB
  cable will not do.
- The oldest Series I units may also need the OTG route. Reported once,
  not confirmed.

### Step by step

1. **Battery models (Portable): plug into the power supply first** and
   keep it plugged in until the end.
2. **Reset the speaker first.** Hold **1** and **Volume minus** together
   for about 10 seconds, until the speaker restarts. **This is Bose's
   factory reset.** It erases the presets, the speaker's name and its
   Wi-Fi, and you have to set the speaker up again afterwards. STR, if
   it is already installed, survives it. The reset clears a
   half-finished setup that can make the speaker refuse the file. If
   you want to keep your presets, try steps 3 to 8 without it first.
3. **Switch the speaker off.** SoundTouch 10, 20, 30: unplug it from
   power. Insert the stick.
4. **Hold 4 and Volume minus**, and while you hold them, plug the power
   back in.

   **Portable:** its battery cannot be removed, so unplugging does not
   switch it off. Keep the power supply plugged in. Switch it off with
   its power button (or hold **AUX** for about 10 seconds). Insert the
   stick. Then hold **4** and **Volume minus** and, while you hold
   them, press the power button. Unverified.
5. **Let go when the display shows a hand or a progress bar** (or the
   lights change on models without a display).
6. **Wait.** Up to **45 minutes**. The bar can stand still for a long
   time. That alone is not a failure.
7. **Never unplug it while it updates.** This is the one dangerous
   moment of the whole procedure.
8. When the speaker has restarted, pull the stick and check the version
   again ([section 1](#1-find-the-version-your-speaker-runs-now)).

The button combinations in steps 2 and 4 come from owners and community
guides. They worked on SoundTouch 20 Series I and II. The Portable
variant of step 4 is untested. On other models
they are not confirmed. If yours does not react, look at Bose's update
article for your model ([section 12](#12-every-source)).

## 6. What you see while it updates

| What you see | What it means | What to do |
|---|---|---|
| A hand symbol, then a bar that fills | The update runs. | Wait. Do not touch the power. |
| The bar stands still for a few minutes | Normal. | Keep waiting, up to 45 minutes in total. |
| The bar stays **empty** for 30 minutes or more | The speaker did not accept this file. Seen on one speaker given 27.0.6 directly, from 5.x and again from 06.02.01. | Unplug power, wait 10 seconds, pull the stick, plug power back in. Check the version. If it did not change, use a smaller step from [section 7](#7-very-old-firmware-the-step-by-step-ladder), or try another, smaller stick. |
| The bar fills, then the speaker restarts again and again | Seen once when 14.0.15 got 27.0.6 directly. | Pull the stick, unplug power, then try the ladder step for your version. Send us the details ([section 10](#10-if-you-are-stuck-send-us-this)). |
| After the update: an **orange Wi-Fi symbol and a book icon** | **The update worked.** The speaker has forgotten its Wi-Fi and waits for setup. | Pull the stick. Set up Wi-Fi again with the Bose SoundTouch app (or ST Reborn's setup). Then check the version. |
| Nothing at all after plugging power in | The speaker did not see the stick or the combination. | Check the stick rules above, the name `Update.stu`, the OTG adapter, and try the combination again. |

Models without a display (SoundTouch 10, SA-5, Wireless Link) show the
update with their lights only. We do not have a confirmed description of
the light pattern per model yet. Photos are welcome.

## 7. Very old firmware: the step-by-step ladder

A speaker on 14.x or older needs several updates in a row. Each step is
one USB stick update (Route C), and the version must be checked after
every step before the next one.

### Where the in-between files come from

Bose still serves 26.0.1, 27.0.1, 27.0.2, 27.0.3 and 27.0.6. Its paths
for 14.0.33 up to 25.0.0 answer 403 (checked 2026-10-25). For older
steps the only source we know is a community collection on the Internet
Archive:
<https://archive.org/details/bose-soundtouch-software-and-firmware>.

> **Read this before you use an archive file.** These copies are not
> from Bose. Somebody uploaded them in 2024. STR cannot check them
> beyond the size and sha1 the archive itself publishes, and the size
> and CRC32 of the `Update.stu` inside. Where an archived Bose catalogue
> lists the same file, we say so in the table. You use them at your own
> risk. An interrupted or wrong update can leave the speaker unusable.
> STR links these files and never copies or hosts them.

Two more rules:

- **Pick the folder for your speaker's generation.** First generation
  SoundTouch 20, 30 and Portable (no Bluetooth) take the
  `Airplay-1_ST20-30-Portable` files. The Bluetooth folders are for the
  newer chassis. The archive also contains files filed under the wrong
  model (listed below). Never use those.
- **The last step to 27.0.6 always comes from Bose**, never from the
  archive.

### SoundTouch 20, 30 and Portable, first generation (no Bluetooth)

Archive folder:
<https://archive.org/download/bose-soundtouch-software-and-firmware/Firmware/2013-2015_No_Bluetooth/Airplay-1_ST20-30-Portable/>

**Which steps your speaker needs:**

| You are on | Steps, in this order |
|---|---|
| 5.x | 06.02.01, 07.00.37, 14.00.33, 15.00.20, 27.0.6 |
| 2.x or older | Unverified. See the note on 2.x below the tables. |
| 6.x | 07.00.37, 14.00.33, 15.00.20, 27.0.6 |
| 7.x up to 14.0.15 | 14.00.33, 15.00.20, 27.0.6 |
| 14.0.33 | 15.00.20, 27.0.6 |
| 15.x or newer | 27.0.6 (Route A, untested on 15.x; Route C if it fails) |

**The files.** "Zip" is what you download. "Inside" is the `Update.stu`
in the zip, the one file that goes onto the stick.

| Step | File to download | Zip bytes | Zip sha1 | Inside: bytes / CRC32 | Status |
|---|---|---|---|---|---|
| 06.02.01 | `Airplay-1_ST20-30-Portable_06.02.01.zip` | 56 867 812 | `70c0cb5b436c1b33fb9e52371f53f7b2b32b9fdb` | 80 559 847 / `a70e1564` | **Live-verified**: 5.x to 06.02.01 on SoundTouch 20 Series I. A field report (not reproduced) says it also installed from 7.0.37 on a second SoundTouch 20. |
| 07.00.37 | `Airplay-1_ST20-30-Portable_07.00.37.zip` | 65 800 764 | `e2b2e7e0e23c7820e42f08e214c56a61c7ea20b4` | 93 032 539 / `a46e94d0` | **Live-verified**: 06.02.01 to 07.00.37 on SoundTouch 20 Series I |
| 14.00.33 | `Airplay-1_ST20-30-Portable_14.00.33.zip` | 67 614 484 | `2d9898399328fab3bb79612a20bfdd42cac6dd60` | 93 114 711 / `d4dc260b` | Unverified. Matches Bose's archived 14.0.33 catalogue. |
| 15.00.20 | `Airplay-1_ST20-30-Portable_Update_ti_15.00.20.37272.2363762.scm.stu.zip` | 75 030 888 | `da0adbc64ef2b3362d9f49e778c52a68c75a3d5b` | 101 642 152 / `f8648557` | Unverified |
| 27.0.6 | **From Bose**: `https://downloads.bose.com/ced/soundtouch/downloads_stockholm/stu/s/Update.stu` (no zip) | | | 105 879 988 / `2d5a971e` | Unverified from 15.00.20. Live-verified by this project from 27.0.3. |

Full sha1 of the inner `Update.stu`, if your tool shows it:

| Step | Inner `Update.stu` sha1 |
|---|---|
| 06.02.01 | `20058e4632b866bf08f61593e09ab2823a36b886` |
| 07.00.37 | `0586eddfe502d324ac2836998b2271b89eb927fd` |
| 14.00.33 | `b3b563a4de548632a066f768edf92a1b35bd2095` |
| 15.00.20 | `7a03fa3b37210b96b2bb852ee5ca710143767839` |

**If 07.00.37 to 14.00.33 also leaves the bar empty**, try two smaller
steps in between: 07.00.37, then 09.00.41, then 12.00.09, then 14.00.33.
Both are unverified, and no Bose catalogue lists them to compare with.

| Step | File to download | Zip bytes | Zip sha1 | Inside: bytes / CRC32 | Status |
|---|---|---|---|---|---|
| 09.00.41 | `Airplay-1_ST20-30-Portable_09.00.41.zip` | 67 402 612 | `8854dd251aec11630367b0cbc629b0bbbdc27c80` | 92 333 191 / `c4fd3a70` | Unverified fallback |
| 12.00.09 | `Airplay-1_ST20-30-Portable_12.00.09.zip` | 67 590 860 | `c6e659517509c306a00d9774497d2b5d21742a7a` | 93 083 567 / `c4d62d01` | Unverified fallback |

**On 2.x?** Start with 06.02.01. If the speaker refuses it, use 05.00.23
first, then continue with 06.02.01. Unverified.

| Step | File to download | Zip bytes | Zip sha1 | Inside: bytes / CRC32 | Status |
|---|---|---|---|---|---|
| 05.00.23 | `Airplay-1_ST20-30-Portable_05.00.23.zip` | 54 117 996 | `70b09185de8b027ff6bcdcd1f76374b48286e2b3` | 76 821 995 / `3bae21ab` | Unverified. Inner sha1 `107166e5cb7b2993376add269792f19e854a9227` |

A step that worked can be repeated. 06.02.01 re-installed fine from a
stick on a speaker where 27.0.6 had just failed.

### Other models: no checked ladder yet

For these we know the route in outline, but we have not yet pulled the
file names and checksums for every step. Until we have, treat this as a
pointer, not a recipe. If your speaker is one of these and on 14.x or
older, please get in touch ([section 10](#10-if-you-are-stuck-send-us-this))
before you start.

| Speaker | Probable route | Known values | Never use |
|---|---|---|---|
| Wave SoundTouch, first generation (folder `Airplay-1_Wave_SoundTouch`) | 06.02.01, 07.00.37, 14.00.33, 15.00.20, then 27.0.6 from Bose (`stu/n/Update.stu`) | 15.00.20 inside: 107 842 648 / `686cad33`. Bose 27.0.6: 112 367 416 / `49d88de4` | 16.00.09 and 18 to 24 from that folder: they are the SoundTouch 20/30 file |
| SA-4, Stereo JC, first generation (folder `Airplay-1_SA4_or_STJC`) | Below 14: no source known. From 14.x: 14.0.33, 15.0.20, then 27.0.6 from Bose (`stu/l/Update.stu`) | 15.0.20 inside: 106 491 816 / `f6ef441d` | 14.0.15, 16.0.09, 17.0.08, 18, 19, 22, 23, 24 from that folder: they are the SoundTouch 20/30 file |
| CineMate adapter, first generation | 06.00.91, 07.00.37, 14.00.33, 15.00.20, then 27.0.6 from Bose (`stu/t/Update.stu`) | | |
| Bluetooth chassis: SoundTouch 10, SoundTouch 20/30 with Bluetooth, SA-5, 300, Wave IV, Wireless Link | Below 14: 14.00.33, 15.00.20, then 27.0.6 from Bose (the `sm2` path in section 9). SoundTouch 10 below 15: 15.00.20, then 27.0.6. | | Wave IV 14.00.33 from the archive: it is the SA-5's file |

All of this table is unverified.

## 8. Checking a download

A short or damaged download is the one thing that can leave a speaker
stuck halfway. Check every file before it goes near the speaker. It
takes a few seconds.

**1. The size, to the byte.** Compare with the tables above.

- Windows (PowerShell): `(Get-Item .\Update.stu).Length`
- macOS: `stat -f %z Update.stu`
- Linux: `stat -c %s Update.stu`

**2. The sha1 of an archive zip.** Compare with the "Zip sha1" column.

- Windows (PowerShell): `Get-FileHash .\Airplay-1_ST20-30-Portable_07.00.37.zip -Algorithm SHA1`
- macOS: `shasum -a 1 Airplay-1_ST20-30-Portable_07.00.37.zip`
- Linux: `sha1sum Airplay-1_ST20-30-Portable_07.00.37.zip`

Windows prints the sha1 in capitals. That is the same value.

**3. The `Update.stu` inside the zip.** Compare size and CRC32 with the
"Inside" column.

- macOS and Linux: `unzip -v Airplay-1_ST20-30-Portable_07.00.37.zip`
  lists the inner file with its size (`Length`) and its `CRC-32`,
  without unpacking it.
- Windows: open the zip in 7-Zip. The `Size` and `CRC` columns show
  both.

If any number does not match, delete the file and download it again.
Do not resume a broken download. A file that does not match must never
go on the stick.

Bose's own files come without a sha1. For those, Bose's catalogue gives
the size and the CRC32, listed in [section 9](#9-boses-files-for-2706-per-model).
Always download them over `https://`. Size and CRC32 catch a damaged
download, but not a file somebody swapped on purpose; the secure
connection is what protects you there. For the SoundTouch 20, 30 and
Portable file (`stu/s/Update.stu`) a sha1 is also known, taken from
the identical archive copy:
`e8694f872e8631118564b2354dd7e2e3047977bc`.

## 9. Bose's files for 27.0.6, per model

Bose publishes one catalogue for every model. It names the file, its
exact size and its CRC32:

    https://downloads.bose.com/ced/soundtouch/downloads_stockholm/index.xml

Two generations of the same model exist, and they take **different
files**. The newer chassis has `sm2` in its path. If you are unsure,
ST Reborn names the chassis on the speaker's settings page, and
[`MODEL-VARIANTS.md`](MODEL-VARIANTS.md) explains the difference. As a
rule of thumb: a SoundTouch 20 or 30 with a Bluetooth button is `sm2`.

All paths below start with `https://downloads.bose.com`. All files are
version `27.0.6.46330.5043500`.

| Model | Chassis | Path | Bytes | CRC32 |
|---|---|---|---|---|
| SoundTouch 10 | sm2 | `/ced/soundtouch/downloads_stockholm/stu/r/sm2/Update.stu` | 95 906 856 | `f18fe026` |
| SoundTouch 20 | older | `/ced/soundtouch/downloads_stockholm/stu/s/Update.stu` | 105 879 988 | `2d5a971e` |
| SoundTouch 20 | sm2 | `/ced/soundtouch/downloads_stockholm/stu/s/sm2/Update.stu` | 99 445 800 | `536e6d6f` |
| SoundTouch 30 | older | `/ced/soundtouch/downloads_stockholm/stu/s/Update.stu` | 105 879 988 | `2d5a971e` |
| SoundTouch 30 | sm2 | `/ced/soundtouch/downloads_stockholm/stu/s/sm2/Update.stu` | 99 445 800 | `536e6d6f` |
| SoundTouch Portable | older | `/ced/soundtouch/downloads_stockholm/stu/s/Update.stu` | 105 879 988 | `2d5a971e` |
| SoundTouch 300 | sm2 | `/ced/soundtouch/downloads_stockholm/stu/g/sm2/Update.stu` | 102 384 736 | `ed21efd3` |
| Wave SoundTouch | older | `/ced/soundtouch/downloads_stockholm/stu/n/Update.stu` | 112 367 416 | `49d88de4` |
| Wave SoundTouch | sm2 | `/ced/soundtouch/downloads_stockholm/stu/n/sm2/Update.stu` | 100 297 132 | `2e2fd417` |
| SA-4, Stereo JC | older | `/ced/soundtouch/downloads_stockholm/stu/l/Update.stu` | 110 991 796 | `01e35713` |
| SA-4, Stereo JC | sm2 | `/ced/soundtouch/downloads_stockholm/stu/l/sm2/Update.stu` | 98 921 512 | `07521bda` |
| SA-5 | sm2 | `/ced/soundtouch/downloads_stockholm/stu/b/sm2/Update.stu` | 100 957 944 | `0b99190b` |
| CineMate | older | `/ced/soundtouch/downloads_stockholm/stu/t/Update.stu` | 114 125 624 | `562de6f1` |
| CineMate | sm2 | `/ced/soundtouch/downloads_stockholm/stu/t/sm2/Update.stu` | 102 700 460 | `fbcab635` |
| Lifestyle, VideoWave | older | `/ced/soundtouch/downloads_stockholm/stu/m/Update.stu` | 215 402 464 | `d48b1181` |
| Lifestyle, VideoWave | sm2 | `/ced/soundtouch/downloads_stockholm/stu/m/sm2/Update.stu` | 203 977 300 | `3f489bd5` |
| Wireless Link adapter | sm2 | `/ced/soundtouch/downloads_stockholm/stu/s/sm2/Update.stu` | 99 445 800 | `536e6d6f` |

Bose's own support pages name a different path for the SA-5. Where they
disagree, the catalogue above is the one the speaker itself uses.

The archive's copy of 27.0.6 for SoundTouch 20, 30 and Portable has the
same size and CRC32 as Bose's file. Still, take 27.0.6 from Bose.

Download example, Windows (PowerShell). Replace the path with yours:

```powershell
Invoke-WebRequest 'https://downloads.bose.com/ced/soundtouch/downloads_stockholm/stu/s/Update.stu' -OutFile Update.stu
(Get-Item Update.stu).Length
```

macOS or Linux:

```bash
curl -L -o Update.stu https://downloads.bose.com/ced/soundtouch/downloads_stockholm/stu/s/Update.stu
```

## 10. If you are stuck, send us this

Open a [discussion](https://github.com/JRpersonal/streborn/discussions)
or write to str@sichtbar-app.de. Please include:

1. **The version** the speaker showed before you started, and after
   each step ([section 1](#1-find-the-version-your-speaker-runs-now)).
2. **Your model**, and whether it has a Bluetooth button.
3. **Which file** you used for each step (the file name from the
   tables), and which route (A, B or C).
4. **A photo of the display or the lights** at the moment it got stuck.
   For an empty bar, say how long you waited.
5. **The stick**: its size and how you formatted it.
6. **The diagnostic file**, if ST Reborn can still reach the speaker:
   in the app, open the speaker's settings and save the diagnostic
   logs (the button at the bottom), or use **Save diagnostic logs** at
   the bottom of the window. The file is anonymised.

Do not post your Wi-Fi password, the speaker's serial number or your
address in a public discussion.

## 11. What is verified and what is not

**Verified by this project:**

- The catalogue and the 27.0.6 files are still served by Bose
  (2026-09-08). The table in section 9 is taken directly from that
  catalogue.
- Route A on a Portable, from 27.0.3 and again over 27.0.6 with STR
  installed (2026-10-13).
- The archive zips for 05.00.23, 06.02.01, 07.00.37, 14.00.33, 15.00.20
  and 27.0.6 were downloaded in full. Their sha1 matched what the
  archive publishes, and the files inside were consistent.

**Live-verified by one reader, on one first generation SoundTouch 20**
(one speaker unless a line says otherwise):

- 5.x to 06.02.01 by USB stick.
- 06.02.01 to 07.00.37 by USB stick.
- 06.02.01 straight to 27.0.6 stalled on an empty bar.
- 07.00.37 to the next step is not tried yet.

**Field reports, not reproduced:**

- July 2026 ([discussion #447](https://github.com/JRpersonal/streborn/discussions/447),
  [issue #476](https://github.com/JRpersonal/streborn/issues/476)): a
  SoundTouch 20 on 7.0.37. 27.0.6 from the first stick did not install.
  With a different stick, 6.02.01 and then 27.0.6 worked. The stick and
  the route changed at the same time, so either may have been the fix.
- October 2026: a first generation SoundTouch 20 on 5.x. 27.0.6 hung on
  an empty bar for over 20 minutes with a checked 16 GB FAT32 stick.
  06.02.01 installed fine. 27.0.6 after that hung again.
- [Issue #854](https://github.com/JRpersonal/streborn/issues/854): a
  SoundTouch 20 on 14.0.15 filled the bar with 27.0.6 and then
  restarted in a loop.

**Unverified:**

- Every ladder step from 07.00.37 upward, the fallback steps, the 2.x
  route, and every model other than the first generation SoundTouch 20.
- Whether Route A or B works on 15.x (the update page exists on 27.x;
  on 15.x it is expected but untested).
- The button combinations (1, 4 and 5 with Volume minus) on any model
  other than SoundTouch 20 Series I and II.
- How long each step takes, and what the lights show on models without
  a display.
- Whether the speaker checks a signature on the file. If it does, a
  damaged file is simply refused. If it does not, the size and checksum
  checks in section 8 are what protect you.

STR itself has never been tested below firmware 27.0.6.

## 12. Every source

**Bose**

- End-of-life page: <https://www.bose.com/soundtouch-end-of-life>
- The model catalogue with sizes and CRCs:
  <https://downloads.bose.com/ced/soundtouch/downloads_stockholm/index.xml>
- Bose's update articles, still online, only their download links are
  gone:
  - SoundTouch 10: <https://support.bose.com/s/article/soundtouch-10-updating-the-software-or-firmware-of-your-product?language=en_US>
  - SoundTouch 20 Series II: <https://support.bose.com/s/article/soundtouch-20-ii-updating-the-software-or-firmware-of-your-product?language=en_US>
  - SoundTouch 20 Series III: <https://support.bose.com/s/article/soundtouch-20-iii-updating-the-software-or-firmware-of-your-product?language=en_US>
  - SoundTouch 30 Series II: <https://support.bose.com/s/article/soundtouch-30-ii-updating-the-software-or-firmware-of-your-product?language=en_US>
  - SoundTouch 30 Series III: <https://support.bose.com/s/article/soundtouch-30-iii-updating-the-software-or-firmware-of-your-product?language=en_US>
  - SoundTouch Portable: <https://support.bose.com/s/article/soundtouch-portable-updating-the-software-or-firmware-of-your-product?language=en_US>

**Internet Archive** (not from Bose, see the warning in section 7)

- The collection: <https://archive.org/details/bose-soundtouch-software-and-firmware>
- Its file list with the sizes and sha1 values used above:
  <https://archive.org/metadata/bose-soundtouch-software-and-firmware>

**Community**

- The downgrade guide that documents the button combinations:
  <https://bose.fandom.com/wiki/SoundTouch_Firmware_Downgrade_Guide>.
  Reported, not reproduced by this project.

**This project**

- Which chassis your speaker is: [`MODEL-VARIANTS.md`](MODEL-VARIANTS.md).
- Which models work with STR: [`MODELS.md`](MODELS.md).
- Firmware quirks STR runs into: [`FIRMWARE-NOTES.md`](FIRMWARE-NOTES.md).

## Pictures

There are none yet, on purpose. Pictures of the USB port on each model,
of the buttons held for a combination, and of the display during an
update would help. Guessing them from memory is how wrong instructions
get written. If you have a speaker in front of you and take those
photos, please share them in a discussion or a pull request. They will
be added here with credit.
