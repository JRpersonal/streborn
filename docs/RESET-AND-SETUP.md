# Factory reset and setup in the Bose app

**What this is, and do I need it?** Sometimes a SoundTouch speaker has to
be set up once with Bose's own SoundTouch app before ST Reborn can be
installed on it. You need this page when:

- ST Reborn's install stops with **"The speaker turned down ST Reborn's
  stand-in account itself (Bose error 1038)"**. The speaker was reset at
  some point and never set up again, and in that state it refuses the
  install. Pressing Install again does not change that.
- You got the speaker second hand and it still carries the previous
  owner's setup.
- Your speaker's firmware is older than 27.0.6. The Bose app offers the
  update once the speaker is set up in it
  ([`FIRMWARE-UPDATE.md`](./FIRMWARE-UPDATE.md)).

It takes about 15 minutes. You need a phone or tablet with the **Bose
SoundTouch app** (App Store or Google Play) on the same Wi-Fi the speaker
should use.

## 1. Reset the speaker to factory settings

The reset clears the speaker's Wi-Fi, its name and its keys. Afterwards
it waits for setup.

**SoundTouch 10, 20, 30 and Portable:** hold the **1** key and the
**volume down** key together for about 10 seconds, until the display or
the lights show the speaker restarting.

**Wave SoundTouch music system (III and IV):** the reset sits on the
SoundTouch pedestal.

1. Check that the pedestal cable sits in the Bose link connector.
2. Unplug the Wave from power.
3. Press and hold the **Control** button on the back of the pedestal.
4. Keep holding it and plug the power back in.
5. Hold it five more seconds, then release. The system restarts.

Bose's guide, under "Resetting the SoundTouch pedestal":
[manualslib.com/manual/1067384/Bose-Wave-Soundtouch.html?page=22](https://www.manualslib.com/manual/1067384/Bose-Wave-Soundtouch.html?page=22)

**Lifestyle 535:** reset the SoundTouch adapter at the console, as
Bose's reset page describes:
[support.bose.de/article/lifestyle-soundtouch-535-resetting-your-product](https://support.bose.de/article/lifestyle-soundtouch-535-resetting-your-product)

**Any other model:** search Bose's support site for your model's name
and "factory reset" ([support.bose.com](https://support.bose.com)).

## 2. Set it up in the Bose SoundTouch app

1. Open the Bose SoundTouch app and add the speaker as a **new speaker**
   (or new system).
2. Follow the app until the speaker is on your Wi-Fi.

<details>
<summary>The app stops at the Bose account sign-in</summary>

Bose switched off its cloud in May 2026, so the account step may fail.
The speaker is often on your Wi-Fi by then anyway: open ST Reborn and
see whether it finds the speaker. If it does, carry on with step 4. If
not, send us the diagnostic log (step 5) and tell us how far the app got.

</details>

## 3. Take the firmware update, if the app offers one

When the speaker is set up, the app may show a firmware update. Take it
and let the speaker restart by itself. That can take up to 20 minutes;
do not unplug it meanwhile. Bose's steps for the Wave SoundTouch IV, as
an example:
[support.bose.co.uk/article/wave-soundtouch-iv-updating-the-software-or-firmware-of-your-product](https://support.bose.co.uk/article/wave-soundtouch-iv-updating-the-software-or-firmware-of-your-product)

A speaker that already runs 27.0.6 gets no update, which is fine.

## 4. Install ST Reborn

Open ST Reborn ([st-reborn.de](https://st-reborn.de)), let it find the
speaker and press **Install**.

## 5. If it still does not work

Use **Save diagnostic log** at the very bottom of the ST Reborn window
right after the failed attempt, and attach the file to a
[GitHub issue](https://github.com/JRpersonal/streborn/issues) or a mail.
Say which step above you got to.

<details>
<summary>More detail: why a reset speaker refuses the install</summary>

ST Reborn opens a speaker by giving it a stand-in account in place of the
dead Bose cloud. A speaker that was reset and never set up again answers
that request with its own error 1038 within a tenth of a second, without
contacting the PC at all, even after a restart onto ST Reborn's local
stand-in. The check sits inside the speaker's firmware. One setup in the
Bose app takes the speaker out of that state. First seen on a Wave IV,
2026-11-18 to 11-23.

</details>
