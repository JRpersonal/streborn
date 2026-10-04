# Pandora

Status: **experimental opt-in, untested against Pandora.** Tracking issue
#243, call for testers in idea #1101.

Pandora only serves listeners in the United States and checks this by
IP address. Nobody on the STR team can test it from Europe, so both
routes below need a tester with a US internet connection, a Pandora
account, and a SoundTouch speaker running STR.

There are two ways to bring Pandora back:

| Route | What plays the music | State |
|---|---|---|
| A | the speaker firmware's own Pandora client | opt-in in the agent, needs a US tester |
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
   Pandora is offered at all. STR's default answer is "unavailable,
   geo-restricted".

Without the opt-in, STR answers step 4 with "unavailable" and files any
step-2 registration as a media-server source, so Pandora can never come up.

### What the opt-in changes

The opt-in is the file `/mnt/nv/streborn/pandora-optin`. Default: absent,
nothing changes. While it exists:

- service availability reports `PANDORA` as available;
- a step-2 registration with provider id `1` is answered as Bose did
  (`201`, `METHOD_NAME: addSource`, the source echoed back) and stored in
  `/mnt/nv/streborn/pandora-source.json` (mode 0600, on the speaker only);
- `/full` and the account source list carry that source, so it survives
  reboots. The credential the firmware sent is only handed back to the
  speaker itself (loopback); any other LAN client sees it masked.

Removing the file switches all of this off again.

What STR stores is exactly what the firmware posted in step 2 (in Bose's
design that is a token the cloud kept). The password from step 1 goes
straight from the request to the speaker's `:8090` API and is never
written or logged by STR. The agent's request log (`/__spy/log`) masks
every `<credential>` value of Pandora requests.

### Agent endpoints (LAN only)

All on the agent's web port (`:8888`, or `:17008` on speakers that use
the redirect, see `docs/MODEL-VARIANTS.md`). POST bodies must be JSON.

| Call | Effect |
|---|---|
| `GET /api/pandora` | opt-in state, the stored source (account masked), and the firmware's own `PANDORA` line from `:8090/sources` |
| `POST /api/pandora` `{"enabled":true}` | create the opt-in marker (`false` removes it) |
| `POST /api/pandora/account` `{"user":"...","password":"..."}` | call `setMusicServiceAccount` on the speaker (needs the opt-in) |
| `DELETE /api/pandora/account` | call `removeMusicServiceAccount` and delete the stored source |

### Diagnostics

- Every stand-in request that names Pandora (path or body) is logged at
  INFO as `marge: Pandora request from the box` (method, path, body size,
  never the body).
- A successful registration logs `marge: Pandora source registered by the
  box` with the masked account, the credential type, and whether a
  credential was present.
- The diagnostic bundle has a `pandora` section: the stand-in's record
  (masked) and the speaker's own `PANDORA` entry from `/sources`
  (`status`, `displayName`, whether an account is attached, or `absent`).

### Unknowns this test answers

1. Does Pandora still accept the Bose partner login now that the Bose
   cloud is gone? (The firmware's login in step 1 either works or not.)
2. Does the firmware register the source with the stand-in at all, and
   in which credential shape (element text or `text=""` attribute; both
   are handled)?
3. Does the firmware ask the stand-in for anything else Pandora-related
   (station lists, `bmx` paths) that STR does not answer yet?

### Test steps for a US tester

Replace `192.0.2.1` with the speaker's address and `8888` with `17008`
if the STR app shows that port. Turn the volume down first.

1. Install the STR release that carries this opt-in (the first release
   after v1.0.2) and confirm the speaker plays internet radio.
2. Switch the test on:
   `curl -s -X POST -H "Content-Type: application/json" -d '{"enabled":true}' http://192.0.2.1:8888/api/pandora`
3. Restart the speaker from the STR app ("Restart speaker") so the
   firmware asks for service availability again.
4. Add the account:
   `curl -s -X POST -H "Content-Type: application/json" -d '{"user":"you@example.com","password":"your-password"}' http://192.0.2.1:8888/api/pandora/account`
5. Wait a minute, then:
   `curl -s http://192.0.2.1:8888/api/pandora` and
   `curl -s http://192.0.2.1:8090/sources`
   Note whether `PANDORA` appears and with which status (`READY` or
   `UNAVAILABLE`), and whether `registered` is true.
6. If it is `READY`: pick a Pandora station. The speaker's own `/select`
   needs a station token, which only the firmware knows, so try the
   preset/station list the firmware offers (`curl -s
   http://192.0.2.1:8090/now_playing` after pressing a preset that held
   Pandora before the shutdown), or tell us what you see.
7. Save a diagnostic from the STR app ("Save diagnostic log" at the
   bottom) and attach it to idea #1101. The bundle never contains your
   password; the account name is masked.
8. Undo:
   `curl -s -X DELETE http://192.0.2.1:8888/api/pandora/account` then
   `curl -s -X POST -H "Content-Type: application/json" -d '{"enabled":false}' http://192.0.2.1:8888/api/pandora`
   and restart the speaker once more.

How to read the result:

- `PANDORA` stays `UNAVAILABLE` and no `Pandora request from the box`
  line appears: the firmware did not log in. Most likely Pandora no
  longer accepts the Bose partner credentials; route A is closed.
- Requests appear but the source never reaches `READY`: the firmware
  wants something more from the stand-in; the logged paths tell us what.
- `READY` and a station plays: route A works, and the opt-in can become
  a switch in the app.

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
