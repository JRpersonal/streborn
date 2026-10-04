# Pandora

Status: **route A is automatic on US speakers, untested against
Pandora.** Tracking issue #243, call for testers in idea #1101.

Pandora only serves listeners in the United States and checks this by
IP address. Nobody on the STR team can test it from Europe, so both
routes below need a tester with a US internet connection, a Pandora
account, and a SoundTouch speaker running STR.

There are two ways to bring Pandora back:

| Route | What plays the music | State |
|---|---|---|
| A | the speaker firmware's own Pandora client | automatic on US speakers, needs a US tester |
| B | an engine in STR (like go-librespot for Spotify) | research only |

## Route A: the speaker's own Pandora client

### How it works

Firmware 27.0.6 still contains Bose's Pandora client (source `PANDORA`,
source provider id `1` in the provider catalogue). It talks to Pandora
directly with Bose's partner credentials. What it lost with the cloud is
the account bookkeeping around it:

1. An account is added through the speaker's own local API on `:8090`:

   ```
   POST /setMusicServiceAccount
   <credentials source="PANDORA" displayName="Pandora Music Service">
     <user>user@example.com</user><pass>password</pass>
   </credentials>
   ```

   (`/removeMusicServiceAccount` with the same body and an empty
   `<pass>` removes it.)

2. The firmware logs in and then registers the source with its cloud,
   which is STR's marge stand-in: `POST /streaming/account/<id>/source`
   with a `<source>` document carrying `<sourceproviderid>1</sourceproviderid>`,
   `<sourcename>PANDORA</sourcename>`, the `<username>`, and a
   `<credential>` element.

3. From then on the firmware rebuilds its source list from
   `GET /streaming/account/<id>/full` on every boot and drops any source
   that document does not name.

4. Separately it asks the cloud's service-availability endpoint whether
   Pandora is offered at all. Outside the US, STR's default answer is
   "unavailable, geo-restricted".

On a speaker outside the US without the opt-in, STR answers step 4 with
"unavailable" and files any step-2 registration as a media-server source,
so Pandora cannot come up there.

### What STR does

Route A now covers iHeartRadio too, and it is on automatically for a
US speaker: [`us-services.md`](us-services.md) describes how the agent
decides a speaker is in the US, what the stand-in answers, how an
account the speaker already had is carried over, the agent endpoints,
the diagnostics, and the test steps for a US tester. Outside the US the
opt-in marker `/mnt/nv/streborn/pandora-optin` still enables it.

## Route B: an engine in STR

If route A is closed, the alternative is the Spotify pattern: STR runs
its own Pandora client and hands the audio to the speaker over UPnP.

Candidate clients:

| Project | Language | License |
|---|---|---|
| gopiano | Go | BSD-2-Clause |
| pydora | Python | MIT |
| libpiano (pianobar's library, and the libpiano-nx fork) | C | MIT |

All of them speak Pandora's unofficial JSON API (`tuner.pandora.com`)
using partner keys extracted from Pandora's own device apps. That is a
terms-of-service grey area, and the keys can be revoked at any time.
Pandora's free tier inserts ads as ordinary tracks in the playlist; an
engine would play them like any other track.

Route B is research only. It would need the same US tester and a
decision on the partner-key question before any code is written.
