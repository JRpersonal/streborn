# iHeartRadio and Pandora on US speakers

Status: **automatic on US speakers, untested against the live services.**
Tracking issue #243, call for testers in idea #1101. Background on
Pandora's second route (an engine inside STR) is in
[`pandora.md`](pandora.md).

A US owner reported that iHeartRadio and Pandora still work in the Bose
SoundTouch app after the cloud shutdown. Both clients live in the speaker
firmware and talk to iHeart and Pandora directly. What they lost is the
account bookkeeping around them, and that bookkeeping now runs through
STR's stand-in for the Bose cloud. Before this change the stand-in shut
both services on every speaker the moment STR was installed.

## How the firmware handles these sources

| | Pandora | iHeartRadio |
|---|---|---|
| source type (`/sources`, `serviceAvailability`) | `PANDORA` | `IHEART` |
| provider id in the source-provider catalogue | `1` | `16` |
| id STR gives it in the account documents | `200` | `201` |

1. The firmware asks the cloud which services are available
   (`serviceAvailability`) and keeps a service off while the answer says
   so.
2. When an account is added (in the Bose app, or through the speaker's
   local `POST :8090/setMusicServiceAccount`), the firmware registers the
   source with its cloud: `POST /streaming/account/<id>/source` with the
   provider id above, the account name, and a `<credential>` element.
3. On every boot it rebuilds its source list from the account document
   (`GET /streaming/account/<id>/full`) and drops any source that
   document does not name.

STR used to answer step 1 with "Pandora unavailable, geo-restricted" (a
value copied from a German speaker) and no iHeart entry, filed step 2 as a
media server, and named neither source in step 3.

## Which speakers count as US

The agent decides from data it already has (`internal/region`):

1. the STR region, the country picked in the setup wizard or later in the
   app (`region.txt`, `PUT /api/region`). It wins whenever it is set;
2. otherwise the firmware's own `countryCode` from `:8090/info`, which
   Bose set at the factory or during the original Bose pairing.

The `countryCode` is cached in `/mnt/nv/streborn/box-country.txt`
(written only when it changes) because the firmware asks for service
availability early in its boot, before the agent has read `/info`.

`GET /api/agent/version` reports the result as `region` (for example
`US`) and `regionSource` (`str` or `box`). Both are omitted while the
country is unknown.

A US speaker owned by someone who set a different country in the wizard
is treated as that country, on purpose: Pandora and iHeart decide by the
listener's IP address, and the wizard country is where the owner says
they listen.

## What changes

On a **US speaker**, automatically:

- `serviceAvailability` reports `PANDORA` available (no geo-restriction
  reason) and adds `IHEART` as available;
- the firmware's registration of either source (step 2) is answered the
  way Bose did (`201`, `METHOD_NAME: addSource`, the source echoed back)
  and stored in `/mnt/nv/streborn/pandora-source.json` or
  `/mnt/nv/streborn/iheart-source.json` (mode 0600, on the speaker only);
- `/full` and the account source list carry the stored sources on every
  boot. The credential the firmware sent is only handed back to the
  speaker itself (loopback); any other LAN client sees it masked.

**Everywhere else** nothing changes, byte for byte, unless the opt-in
marker `/mnt/nv/streborn/pandora-optin` exists (the Pandora test from
#1140). The marker now enables both services, which lets a tester in
another iHeart country try it.

iHeart's own country lookup at `api2.iheart.com` is not touched. It goes
to iHeart directly, and answering it locally was considered and dropped
(see `docs/FIRMWARE-NOTES.md`).

## Accounts the speaker already had

The first time STR starts on a speaker it records the speaker's own
`/sources` list (`internal/boxsnapshot`). Any `PANDORA` or `IHEART`
entry in it, with its account name, is written to the reflect file
(`/mnt/nv/streborn/reflect-sources.json`), and `/full` names it from the
first boot on, the same mechanism that keeps Deezer accounts alive. The
firmware keeps its own login for the service, so nothing has to be
entered again as long as it still holds it.

The snapshot never contains a password or token (`/sources` does not
expose one). When the firmware registers the source again with the
stand-in (step 2), the stored registration replaces the carried-over
entry, so `/full` never names a service twice.

If the speaker lost the account anyway, re-link it. In the Bose app if it
still offers that, or through the agent:

```
curl -s -X POST -H "Content-Type: application/json" \
  -d '{"user":"you@example.com","password":"your-password"}' \
  http://192.0.2.1:8888/api/pandora/account

curl -s -X POST -H "Content-Type: application/json" \
  -d '{"user":"you@example.com","password":"your-password"}' \
  http://192.0.2.1:8888/api/iheart/account
```

Both call the speaker's own `setMusicServiceAccount`. The Pandora form
(`displayName="Pandora Music Service"`) is the one the firmware's own
flow uses. The iHeart form (`source="IHEART"`,
`displayName="iHeartRadio"`) is unverified: if the speaker refuses it,
the error is passed back, and linking in the Bose app is the way.

The password goes straight from the request to the speaker's `:8090` API
and is never written or logged by STR.

## Presets

A Pandora or iHeartRadio station can live on preset keys 1 to 6
(discussion #1101). STR stores it as a preset of type `native` that
carries the speaker's own ContentItem (`native`: source, sourceAccount,
location, itemType, itemName, containerArt) and the service name as the
`source` badge. STR never plays or proxies these stations; the firmware's
own client does.

- **Holding the key on the speaker** while such a station plays: the
  firmware PUTs the item to the stand-in, STR keeps it and answers with the
  preset record (its source element is the registered Pandora/iHeart source
  from `/full`). A station already on another key is refused, as for radio.
- **Hold-to-save in the desktop app or the phone remote**: the app sends
  `{"type":"native","name":...}` and the agent reads the exact item from the
  speaker's `:8090/now_playing`.
- **Pressing the key** on the speaker: the firmware plays it itself, STR
  stands back. **Tapping it in an app**: STR hands the stored item to the
  speaker's `/select`.
- **Reconcile**: a key the speaker already holds with the stored item is
  never rewritten. A missing or overwritten key is written back with
  `ws AddPreset <SOURCE> <type> <location> "<name>" <account> <slot>`, only
  while the speaker lists the source as `READY`; otherwise it is logged and
  left alone (never deleted). A refused write is retried after an hour.

The account: the speaker's own store record (the flat
`<preset><sourceid>..<name>..<username>..` body) names no account. Its
`<username>` repeats the station name (observed on an ST20, #1101: Pandora
with sourceid 100, iHeartRadio with sourceid 201), so STR stores the item
without one. Every write-back and every `/select` fills in the account the
speaker itself lists for the service in `:8090/sources` (a stored account is
kept only when the speaker lists it). v1.0.4 and v1.0.5 stored the station
name as the account, which the speaker refused on `/select` (1005
UNKNOWN_SOURCE_ERROR) and its command line could not carry, so the key was
gone after every reboot; those presets heal without being saved again.

Known limit: an iHeartRadio location is an XML fragment
(`<IHeartCILocation id=".." locationType="LIVE_STATION" />`) with spaces and
quotes, which `ws AddPreset` cannot carry. Such a key cannot be written back
after the speaker drops its presets on a reboot; tapping it in an app still
works through `/select`.

Every store request for a source other than STR's own radio is logged at
INFO as `marge preset store: the box asked to keep a non-radio item` with
all fields (account masked).

## Agent endpoints (LAN only)

All on the agent's web port (`:8888`, or `:17008` on speakers that use
the redirect, see `docs/MODEL-VARIANTS.md`). POST bodies must be JSON.

| Call | Effect |
|---|---|
| `GET /api/us-services` | region, the availability list as served, the stored sources (masked), the recent registrations, and the firmware's own `PANDORA` and `IHEART` lines from `:8090/sources` |
| `POST /api/us-services` `{"enabled":true}` | create the opt-in marker (`false` removes it); only needed outside the US |
| `POST /api/pandora/account`, `POST /api/iheart/account` `{"user":"...","password":"..."}` | call `setMusicServiceAccount` on the speaker (needs a US speaker or the opt-in) |
| `DELETE /api/pandora/account`, `DELETE /api/iheart/account` | call `removeMusicServiceAccount` and delete the stored source |

`/api/pandora` is the older name of `/api/us-services` and still works.

## Diagnostics

- The diagnostic bundle has a `usServices` section: `region` (effective
  country, its source, and both raw inputs), `standIn` (`served`, the
  availability list exactly as answered; per service its `mode`, `us`,
  `optin` or `off`, the stored registration masked, and `carriedOver`;
  `posts`, the last 16 registrations the firmware made for either
  service, masked, with `accepted` false when the service was off), and
  `boxSources` (the firmware's own `PANDORA` and `IHEART` entries:
  `status`, `displayName`, whether an account is attached, or `absent`).
- Every stand-in request that names either service (path or body) is
  logged at INFO as `marge: US service request from the box` (service,
  method, path, body size, never the body).
- Every registration logs `marge: US service source registration from
  the box` with the service, the masked account, the credential type,
  whether a credential was present, and whether it was accepted.
- The agent's request log (`/__spy/log`) masks every `<credential>`
  value of these requests.

## What a US tester should check

Replace `192.0.2.1` with the speaker's address and `8888` with `17008`
if the STR app shows that port. Turn the volume down first.

1. Install the STR release that carries this change (the first release
   after v1.0.2) and confirm the speaker plays internet radio.
2. Check the region: `curl -s http://192.0.2.1:8888/api/agent/version`
   should show `"region":"US"`. If it shows another country or none, set
   it: `curl -s -X PUT -H "Content-Type: application/json" -d
   '{"country":"US"}' http://192.0.2.1:8888/api/region`.
3. Restart the speaker from the STR app ("Restart speaker") so the
   firmware asks for service availability again.
4. `curl -s http://192.0.2.1:8090/sources` and note the `PANDORA` and
   `IHEART` lines and their status (`READY` or `UNAVAILABLE`).
5. Open the Bose SoundTouch app: are iHeartRadio and Pandora listed, do
   your old presets for them play, can you browse and pick a station?
6. If a service is missing or `UNAVAILABLE`, link it again (Bose app, or
   the `curl` calls under "Accounts the speaker already had"), wait a
   minute, and repeat step 4.
7. Restart the speaker once more and check that the services are still
   there (step 4) and still play.
8. Save a diagnostic from the STR app ("Save diagnostic log" at the
   bottom) and attach it to idea #1101. It never contains a password; the
   account name is masked.

How to read the result:

- Both `READY` and playing after a restart: done, nothing else is needed.
- A service stays `UNAVAILABLE` and `posts` is empty: the firmware never
  registered it. Most likely the service no longer accepts the Bose
  partner login.
- `posts` has entries but the source does not reach `READY`: the firmware
  wants something more from the stand-in; the logged paths tell us what.
