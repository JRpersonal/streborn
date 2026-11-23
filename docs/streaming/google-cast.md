# Google Cast (Chromecast)

Status: **researched, not buildable as a shipped feature.** A Google Cast
receiver is only accepted by stock senders (Chrome, Android, the YouTube
and Deezer apps through the Cast SDK) if it proves, on every connection,
that it holds a device key whose certificate Google signed. Google issues
those keys only to licensed manufacturers, provisioned at the factory. Every
open-source receiver that works with stock senders today does so with
signatures that come from a Google key taken out of someone else's device.
STR cannot ship that. Vote thread: idea #2013 (duplicate #2022, closed).

Researched 2026-11-23 from the code and docs in this repository, a
read-only `GET :8090/sources` on one STR Portable, both vote threads, and
public sources (linked inline). Nothing was played, no receiver was run.

There are three ways "cast from an app to a SoundTouch" could work:

| Route | What it is | Feasibility | Legal exposure |
|---|---|---|---|
| A | a Google Cast receiver inside STR (speaker or PC) | technically small for plain URLs; blocked by device authentication | high: needs signatures from an extracted Google device key |
| B | a YouTube-only "Link with TV" (DIAL / lounge) receiver | works for some, fragile; audio still has to be pulled from YouTube | high: YouTube stream deciphering, the youtube-dl class |
| C | casting to the speaker as the UPnP renderer it already is, AirPlay, Bluetooth | works today, per app | none for STR |

Recommendation: close #2013 as "not possible without Google's licence",
pointing at route C per service. Worth one FAQ answer and nothing in the
apps. This matches the reply already posted in #2013 on 2026-11-21, with
one precision: it is the sender platform (Chrome, Google Play services)
that checks the certificate, not each app.

## Route A: a Google Cast receiver

### How Cast device authentication works

A sender (Chrome, or Google Play services on Android on behalf of any app
using the Cast SDK) finds receivers over mDNS (`_googlecast._tcp`), opens a
TLS connection to port 8009 and sends an `AuthChallenge` on the
`urn:x-cast:com.google.cast.tp.deviceauth` namespace. The receiver answers
with an `AuthResponse`: a signature over its TLS peer certificate (and, if
the sender enforces it, the sender's nonce), made with the device's
`client_auth_certificate` key, plus the intermediate certificates. The
sender checks that the chain ends at Google's Cast root and that the
signature is valid. Source: the shanocast write-up
(https://xakcop.com/post/shanocast/, 2023-09-27), which states that a
legitimate receiver needs "a `client_auth_certificate` signed by Google"
and its private key, and the sender code in Open Screen
(`cast/sender/channel/cast_auth_util.cc` in
https://chromium.googlesource.com/openscreen/).

Two relaxations in the sender code matter, both kept "for compatibility
with legacy receivers":

- nonce checking can be off (`enforce_nonce_checking`), in which case the
  receiver may leave the nonce out and the signature covers only its own
  peer certificate, so it can be precomputed and replayed;
- revocation (CRL) failures and SHA-1 signatures are tolerated when the
  corresponding enforcement flags are off.

Those flags are Google's to flip. Chrome's values are visible in Chromium
source; Google Play services is closed source, so what Android enforces
today is not known from code.

### The open-source receivers, and how each gets past it

| Project | What it is | How it authenticates | Works with | State |
|---|---|---|---|---|
| Open Screen libcast (Google, BSD-3) | the reference Cast protocol library in Chromium; ships a standalone receiver (mirroring) | a self-generated developer certificate, which only a sender told to trust it accepts (Open Screen's own test sender) | not stock Chrome or Android | maintained, but a test tool |
| shanocast (rgerganov, https://github.com/rgerganov/shanocast) | Open Screen's standalone receiver with a patch, Linux | "precomputed signatures taken from AirReceiver": the same RSA key every time, one signature per 48-hour certificate, 795 of them harvested by changing a phone's date, covering 2023-08-15 to **2027-12-21** | Chrome tab and desktop mirroring, per the author | the author: "I don't think this hack will work for long, so enjoy it while it lasts"; "shano" is Bulgarian slang for shady or illegal |
| AirReceiver, AirScreen (commercial Android and Fire TV apps) | closed receivers that advertise Google Cast | the shanocast author suspects signatures from a rooted Chromecast; unconfirmed | advertised for Chrome and apps; reports say some apps (Disney+ and others) refuse | Fire TV stores removed earlier Cast-style apps, per AFTVnews |
| go-cast (tristanpenman, MIT, https://github.com/tristanpenman/go-cast) | Go sender and receiver | a "cert manifest" whose generation "requires access to a rooted Chromecast", or a remote "certificate service" that hands out signatures | YouTube's Cast receiver app ids; receiver is video only, "Audio is currently not supported" | 2026 |
| castv2 libraries (Node `castv2`, `pychromecast`, `go-chromecast`), mkchromecast | protocol libraries and senders | do not solve receiver auth; mkchromecast is a sender (PC to a real Chromecast) | n/a | n/a |
| leapcast and other 2013 receivers | DIAL-era emulators | none; broke when device auth arrived | nothing current | dead |

Conclusion for question 1: **no open-source receiver works with stock
senders without signatures made by a Google device key that was not issued
to its author.** The only known working source (AirReceiver's replayed
signatures) runs out on 2027-12-21 at the latest and dies earlier the day
Google turns on nonce enforcement in Chrome or Play services, which it can
do in a silent update. For a project with STR's install base that is a
fleet-wide failure on a date nobody controls.

### Legal status (not legal advice)

- **The signatures.** Replaying signatures made with a key extracted from a
  Google device, or shipping an extracted key, is using material STR has no
  licence for. It is also the same posture `CLAUDE.md` forbids for Bose
  material (no extracted keys, no decompiled code) applied to Google.
- **Circumvention.** Device authentication is what lets content apps rely on
  the receiver; for paid services it gates Widevine licences. In Germany
  §95a UrhG forbids circumventing an effective technical measure and
  distributing tools primarily designed for it, and the OLG Hamburg
  (5 U 54/23, 2024-11-21, youtube-dl) held that even an easily broken
  measure counts (see `docs/streaming/deezer.md`, route B). In the US,
  17 U.S.C. §1201 is the equivalent. Whether Cast device auth protects a
  "work" is arguable; nobody has litigated it. The risk is not worth
  arguing.
- **Trademark.** "Chromecast", "Google Cast" and "Chromecast built-in" are
  Google trademarks licensed to certified devices only. STR could not use
  the names for an unlicensed receiver, which is the only thing users
  would search for.

### Where it would run, if authentication were solved

Kept short, because it does not change the verdict.

**What a receiver must do per app.** A Cast sender asks the receiver to
launch an app id. The receiver is expected to load that app's Web Receiver
(HTML and JavaScript from Google's app registry, using the Cast Application
Framework) in a Chromium-based Cast runtime and run it
(https://developers.google.com/cast/docs/overview: "Receivers (except for
the Default Receiver) are registered using a distinct app ID"). What that
means per sender:

| Sender | Receiver app | What a minimal receiver would get |
|---|---|---|
| podcast apps, simple players using the Default Media Receiver (`CC1AD845`) or a styled receiver | Google-hosted media player | a `LOAD` with a plain media URL (MP3, AAC, sometimes HLS): playable on the speaker through STR's stream proxy, which already turns HLS into one ADTS/MP3 stream |
| YouTube, YouTube Music | YouTube's own receiver app (YouTube's Cast app ids, e.g. `233637DE`) | a video id, not a URL; the audio has to be resolved from YouTube (route B's problem) |
| Spotify | Spotify's receiver app, which hands over to Spotify Connect | nothing new: STR already is a Spotify Connect receiver |
| Deezer, Tidal, Amazon Music, Netflix and other paid services | the service's custom Web Receiver | an opaque content id; the receiver app fetches protected, often Widevine-encrypted streams itself. Without a Chromium Cast runtime plus a Widevine licence for a certified device, nothing plays |

So even a perfectly authenticated "Default Media Receiver" emulation (a
few days of Go: mDNS, TLS on 8009, the castv2 protobuf framing, the
`receiver` and `media` namespaces, handing the URL to `:8091`) would cover
podcast apps and simple players, the apps that mostly already offer UPnP
or AirPlay, and would **not** cover YouTube Music, Deezer or Tidal, the
apps users actually ask for in #2013 and #2022.

**On the speaker.** ARMv7 (AM33XX), GOARM=5 softfloat (issue #302), about
50 MB RAM available on the smaller chassis, NAND tight (agent about 11 MB,
Spotify engine about 16 MB, see `docs/streaming/spotify.md`). A
Default-Media-Receiver emulation inside the agent would fit (well under
1 MB, no decoding: the speaker decodes). A Chromium Cast runtime to run web
receivers would not, by two orders of magnitude, and neither would
Open Screen's mirroring receiver (Opus and VP8 over RTP to decode and
re-encode on a softfloat CPU). Port 8009 would need a PREROUTING redirect
on the whitelisted chassis, like `:17008`
(`docs/MODEL-VARIANTS.md`).

**On the PC.** A desktop-side receiver could run a full Chromium, but it
breaks STR's rule that the speakers work without the PC on, and it gains
nothing on the paid services (Widevine on a Cast receiver is again tied to
a certified device).

## Route B: a YouTube "Link with TV" receiver

YouTube and YouTube Music also cast to smart TVs over DIAL and YouTube's
own pairing service ("Link with TV code"), independent of Google Cast
device auth. `yt-cast-receiver` (patrickkfkan, MIT; its DIAL server is a
peer-dial fork "free for non commercial use",
https://github.com/patrickkfkan/yt-cast-receiver) implements that receiver
in Node.js and names the YouTube and YouTube Music mobile apps as senders.
Volumio ships it as a community plugin ("YouTube Cast Receiver").

Why STR should not:

- **The audio.** The library has no player; the integrator resolves the
  video id to a stream with YouTube.js. That means deciphering YouTube's
  rolling signature cipher, the exact measure the OLG Hamburg youtube-dl
  ruling treats as protected. Same legal class as route B in
  `docs/streaming/deezer.md`.
- **Fragility.** The Volumio thread is a long list of "broke after a
  YouTube change, fixed in the next release", with recent reports that
  YouTube Music fails to cast. The author calls it "work-in-progress and
  may not be reliable enough for production use".
- **Discovery is moving away.** A 2026 report says YouTube is replacing
  same-Wi-Fi discovery with account-based linking and the TV code
  (https://ppc.land/youtube-drops-shared-wi-fi-from-phone-control-of-tv-playback/);
  unconfirmed by YouTube, but it points the wrong way for a LAN receiver.
- **Runtime.** Node.js on the speaker is out (NAND, softfloat); in the
  desktop app it needs the PC on.

## The official route: Google's programmes

- **Chromecast built-in (Google Cast for audio).** A manufacturer licence:
  a contract with Google, a certified hardware or module platform (audio
  makers typically buy a pre-certified Wi-Fi module from a Google partner),
  certification testing including Google's multi-room certification at a
  partner lab (for example the StreamUnlimited leaflet,
  https://www.streamunlimited.com/wp-content/uploads/2019/07/mr_cert_leaflet.pdf),
  and per-device keys provisioned at the factory into secure storage. There
  is no retrofit path for hardware already sold, and no hobby or
  open-source tier. Bose itself never shipped Cast on SoundTouch.
- **Cast Connect** is for Android TV apps (an Android TV receiver app
  registered in the Cast Developer Console next to a Web Receiver). It is
  not a device programme and does not apply to a speaker.
- **Cast Developer Console** (USD 5 registration) registers *receiver
  apps* and lists *genuine* Cast devices by serial for testing. It does not
  certify a device.
- **Matter casting** exists for TVs (a video-player device type); Google
  Home does not use it to make third-party speakers Cast targets, and it
  would not reach the apps' Cast buttons.

Answer for question 3: **no path for STR.** The same goes for the
"group speakers of different brands" part of #2022: Google Home speaker
groups are Cast groups and need certified devices. STR's own multiroom
covers SoundTouch with SoundTouch.

## Route C: what works today, with zero build

Every SoundTouch is a UPnP MediaRenderer on `:8091` (MP3, AAC, FLAC, Ogg,
WAV over plain `http`; STR's proxy covers https and HLS for its own
sources), has Bluetooth, and some firmware variants list `AIRPLAY` in
`:8090/sources` (the probed Portable lists `AIRPLAY` `READY`; the gate is
the MFi chip, see the AirPlay notes). None of the routes below gives a
hardware key, and all need the phone or PC to stay on, except where noted.

| Service | Best way onto a SoundTouch today | Notes |
|---|---|---|
| Spotify | **Spotify Connect, built into STR** | presets, hardware keys, phone can leave |
| YouTube Music, YouTube | Bluetooth; AirPlay from iPhone, iPad, Mac where the speaker has AirPlay | no DLNA output in the app; Android audio capture (BubbleUPnP "Audio Cast" and similar, Android 10+) is possible only if the app allows capture: untested |
| Deezer | Bluetooth, AirPlay; a speaker that kept its Deezer login keeps Deezer (A1) | see `docs/streaming/deezer.md` route C |
| Tidal | **BubbleUPnP or mConnect** (Android, iOS for mConnect): log in to Tidal inside the app, play to the SoundTouch as a UPnP renderer | the phone only controls; the speaker pulls the stream. Hi-res above 16/44 may not play (Tidal spike PR #1953 measures it) |
| Qobuz | **BubbleUPnP or mConnect**, as Tidal | same caveat on hi-res |
| Podcasts | **Podcast Addict Premium**: casts to UPnP/DLNA speakers since v2026.13 (2026-10-25, "early", audio only, the app must keep running, https://podcastaddict.com/changelog/2026_13); any podcast via an RSS stream URL in STR's own station search where it plays | Pocket Casts and others: Chromecast or AirPlay only |
| Apple Music | AirPlay where the speaker has it, else Bluetooth | |
| Amazon Music | Bluetooth, AirPlay | no DLNA output |
| Own files, NAS | **STR's DLNA library** (desktop app and phone page), the USB stick (#2018), BubbleUPnP, Windows "Cast to Device" (Play To) | presets and keys work for library items |
| Anything on a Home Assistant server | **Music Assistant**: has a Bose SoundTouch player provider and a DLNA player provider, and music providers for YouTube Music, Deezer, Tidal, Qobuz, SoundCloud, podcasts and more (https://www.music-assistant.io/music-providers/) | a third-party server that must stay on; its own providers carry their own legal posture; SoundTouch over DLNA reported flaky there with stock Bose firmware; untested with STR |

### AirPlay per model, checked 2026-11-23

Read from `:8090/sources` on the maintainer's speakers, all on firmware 27.0.6:

| Model | `AIRPLAY` source |
|---|---|
| SoundTouch Portable (two units) | `READY` |
| SoundTouch 30 (scm chassis) | `READY` |
| SoundTouch 10 (two units) | `UNAVAILABLE` |

The firmware registers AirPlay only where the speaker's AirPlay authentication chip answers, so a SoundTouch 10 has Bluetooth as its only phone route. Any answer that points users to AirPlay has to name that.

## Summary for planning

**Verdict: close #2013 as not possible** (no shippable Cast receiver
without a Google licence), and **point to route C** per service. Nothing to
build in the agent or the apps. Revisit only if Google opens a licence tier
that covers existing hardware, which nothing suggests.

**User-facing outcome:** one FAQ answer on the website ("Can I cast to my
SoundTouch? Spotify: yes, via STR. Tidal, Qobuz: BubbleUPnP or mConnect.
Podcasts: Podcast Addict. YouTube Music, Deezer, Apple Music: AirPlay or
Bluetooth."), and a closing reply in #2013 with the same list. No new UI.

**Effort:** FAQ and reply about half a day. For the record: a
Default-Media-Receiver emulation in the agent would be 3 to 5 days of Go
for podcast-type apps only, and is moot without authentication. Route B
(YouTube DIAL) about a week plus permanent maintenance; ruled out.

**Legal exposure:** route A and route B high (extracted Google key
material or replayed signatures; YouTube cipher circumvention; trademark).
Route C none for STR.

**Conflicts with own posts:** none. The #2013 reply of 2026-11-21 ("blocked",
"Bluetooth is the way today") stands; the closing reply can add Tidal and
Qobuz via BubbleUPnP or mConnect, Podcast Addict, and AirPlay where present.

### Tests on real hardware

All are checks of route C for the FAQ wording; nothing here needs a test
build. Audio tests run on the Portable at volume 5 only.

1. **AirPlay presence per model (read-only, anyone).** On every speaker in
   the fleet: `GET http://192.0.2.1:8090/sources` and note whether
   `source="AIRPLAY"` is listed and its status. Gives the FAQ its "where
   the speaker has AirPlay" list per model and chassis.
2. **Tidal via BubbleUPnP (maintainer runs).** Android phone, BubbleUPnP,
   Library, add Tidal, log in with a Tidal trial. Renderer: the Portable.
   Set volume 5 in BubbleUPnP before Play. Play one album track at
   "High" (AAC 320) and one at "Max" (FLAC). Pass: both play to the end of
   the track, `GET :8090/now_playing` shows source `UPNP`; note whether the
   FLAC plays or BubbleUPnP falls back. Stop in BubbleUPnP afterwards.
3. **Qobuz via BubbleUPnP (maintainer runs).** As test 2 with a Qobuz trial,
   one CD-quality track. Pass as in test 2.
4. **Podcast Addict Premium UPnP cast (maintainer runs).** Phone, Podcast
   Addict (premium or trial), Cast button, pick the Portable, volume 5,
   play one audio episode for 2 minutes, then lock the phone for 2 minutes.
   Pass: it keeps playing while locked; note what happens when the app is
   swiped away (the changelog says playback stops after the buffer).
5. **YouTube Music audio capture (maintainer runs).** Android 10+, BubbleUPnP
   "Audio Cast" (or the free tier's limit noted) to the Portable at volume
   5, start a song in YouTube Music. Pass: audible on the speaker. Fail
   means the app blocks capture; the FAQ then says AirPlay or Bluetooth
   only.
6. **AirPlay from YouTube Music on iOS (maintainer runs).** iPhone, YouTube
   Music, AirPlay picker, the Portable (listed `AIRPLAY` in test 1), volume
   5, one song for 1 minute. Pass: plays, and STR's now-playing shows the
   AirPlay source.
7. **Optional, no audio, PC only: does a stock Android sender list an
   unauthenticated receiver at all?** Build Open Screen's standalone
   receiver on a Linux PC with a self-generated developer certificate (not
   the AirReceiver signatures), start it, open YouTube Music and Chrome's
   Cast menu on the same LAN. Record whether the receiver is listed and
   what happens on selection. Expected: listed, then refused at the device
   authentication step. Confirms the #2013 wording with a first-hand
   observation; skip if time is short.
