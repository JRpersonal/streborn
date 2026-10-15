# Speaker self-update

A speaker running STR can update itself to the latest release, with no
desktop app involved. The phone remote shows it under **More > Software
update** (idea #1091).

## What the owner sees

Opening the More tab asks the speaker to check GitHub once. The card then
says which version the speaker runs and whether a newer one exists. One
tap, a confirmation, and the speaker downloads the release, installs it
and restarts. The page keeps polling through the restart and reports
"Updated to vX.Y.Z" when the speaker is back with its Spotify engine.

When the engine is missing for any reason (it is deleted to make room on
tight speakers), the card offers **Install the Spotify engine**, which
fetches the engine of the release the speaker runs.

## Updating all speakers at once

When the speaker knows other STR speakers (the peer list the desktop app
distributes and mDNS fills), the card lists every speaker with the version
it runs and whether it needs the update, and the main button is **Update
all speakers (N)**. "Only this speaker" stays available as a link.

**The phone drives the run.** Speakers often cannot reach each other
(ST10s on the rhino chassis are isolated from one another, "no route to
host" box to box), while the phone in the owner's hand reaches every one
of them. So the page does the walking:

1. `POST {"action":"plan"}` on the speaker serving the page lists the
   peers it knows (name, address, the agent port it knows them on) and
   what each runs. The page then asks every peer directly
   (`GET http://<peer>:<port>/api/agent/self-update`, `:8888` on sm2,
   `:17008` on BCO chassis) and trusts that answer over the serving
   speaker's view.
2. The tap claims the run on the serving speaker
   (`POST {"action":"run-claim","token":"<random>"}`), renewed about once
   a minute while the run goes on.
3. **One speaker at a time.** For each peer that is older than the
   release the page sends `POST check` and then `POST install` to the
   peer's own `/api/agent/self-update`, so each speaker verifies the
   signature, version and hashes itself, then polls the peer every 5 s,
   at most 12 minutes, plus up to 4 minutes for the Spotify engine, and
   only then moves on.
4. **This speaker last**, with `POST {"action":"install","token":...}`.
   The page keeps polling through its restart, then sends `run-end`.

Each row shows waiting, checking, the download percentage, restarting,
the engine fetch, done, failed (with the reason) or skipped.

- **Fallback through the speaker.** A peer the phone cannot reach (an
  older agent without the CORS answer, a browser that refuses) is handed
  to the serving speaker: `POST {"action":"install-all","peers":["<ip>"],
  "token":...}` runs the speaker-driven path for just that peer and not
  for the speaker itself; the page follows it through `GET` (`fleet`) and
  marks the row "through this speaker". Only peers on the speaker's own
  list are accepted (400 otherwise). A peer neither reaches is "Not
  reachable. Update it from the desktop app or open its own page."
- **Keep the page open.** The card says so. When the page goes to the
  background (`visibilitychange`), the run waits between steps, never in
  the middle of a speaker's update; on return it gives that speaker the
  time it was away and asks it how it is doing. The run is kept in
  `localStorage` (`str.su.run`), so a reload continues it where it stood.
- **Never two drivers.** While a phone holds the run, a second phone
  cannot claim one and sees "Another phone is updating the speakers right
  now"; `install`, `engine` and `install-all` on the serving speaker
  without the token answer 409. A claim expires 20 minutes after its last
  renewal. A run the speaker drives on its own still answers a single
  `install` with 409, and no phone can claim a run during it.
- **Skipped, never forced:** speakers that are already current, and
  speakers whose agent predates the self-update endpoint ("needs one
  update from the desktop app"). They are not touched in any other way.

### Cross-origin access

The page is served by one speaker and calls the others, a cross-origin
request. `/api/agent/self-update`, and no other path, answers CORS for an
`Origin` of `http://` plus a LAN host (private or link-local IP, an
address on one of the speaker's own subnets, or a bare local name such as
`kitchen` or `kitchen.local`). The origin is echoed exactly with
`Vary: Origin`; only `GET, POST, OPTIONS` and `Content-Type` are allowed;
a preflight that asks for Private Network Access gets
`Access-Control-Allow-Private-Network: true`. Never `*`, `null`, loopback
or a public origin. The caller's address still has to pass the LAN gate,
and a POST from any other page origin is refused with 403. See
[`THREAT-MODEL.md`](THREAT-MODEL.md).

### The speaker-driven path

`POST {"action":"install-all"}` without `peers` still lets the serving
speaker walk every peer itself (the original design, now the fallback):
same order, same waits, this speaker last, the run carried across its
own restart in `selfupdate-pending.json`. It reaches only entries of the
peer list, only bare private LAN addresses, only the agent ports
(recognised by a JSON `/api/agent/version`, so the Bose service on an
sm2's `:17008` is never mistaken for the agent), through a dialer that
refuses loopback and link-local, and without following redirects. Each
peer applies its own LAN gate to the request.

## How it works

```
phone ──POST check──▶ agent ──HTTPS──▶ github.com/.../releases/latest/download/update.json(.sig)
                         │                verify Ed25519 signature, version newer?
phone ──POST install──▶  │
                         ├──HTTPS──▶ releases/download/<tag>/streborn-armv7l
                         │    stream ─▶ heldBackBody (SHA-256 + size) ─▶ handleAgentUpdate
                         │                                               (space reclaim, RAM stage,
                         │                                                flash verify, stick refresh,
                         │                                                reboot)
                         ▼
                  new agent boots, finds selfupdate-pending.json
                         └──HTTPS──▶ releases/download/<tag>/go-librespot-armv7l
                              stream ─▶ heldBackBody ─▶ handleAgentSidecar
```

- **Same install path as the desktop app.** The downloaded body is handed
  to the existing upload handlers in-process, so every safeguard the
  desktop push has (NAND reclaim, tier-3 RAM-staged swap, flash verify,
  stick refresh, the reboot that deploys `run.sh`) applies unchanged.
- **Hash before install.** `heldBackBody` withholds the last byte until
  the whole download has the signed size and SHA-256. On a mismatch the
  handler sees a short body and discards the temp file.
- **Engine after the reboot.** The update may delete the engine to fit the
  new agent. The pending marker carries the engine's signed hash and size,
  and the new agent fetches it once its network is up (retries for about
  20 minutes). A failed update that had already deleted the engine puts it
  back at once from the same release.
- **No background traffic.** The speaker talks to GitHub only when the
  owner opens the card or taps a button, plus the engine step that
  finishes an update the owner started.
- **TLS.** The chain is checked with the clock tolerance the radio relay
  uses. A speaker whose trust store lacks the issuer still downloads,
  because what gets installed is decided by the signature, not by TLS.

## Release signing

The release workflow writes `update.json` (version, and hash and size of
`streborn-armv7l` and `go-librespot-armv7l`) and signs it into
`update.json.sig` with `cmd/updatesign`. The private key is the
`STR_UPDATE_SIGNING_KEY` Actions secret; the public key is compiled into
the agent in `internal/updatesig`. `updatesign` refuses to sign with a key
the agent would not trust, so a mismatch fails the release, not the
speakers. A real release without the secret fails too.

Setting it up (once):

```sh
go run ./cmd/updatesign keygen
# private seed -> repository secret STR_UPDATE_SIGNING_KEY
# public key   -> releaseKey1 in internal/updatesig/updatesig.go
```

Keep an offline copy of the seed. Rotating the key works through a
release: sign it with the old key and ship an agent that already trusts
the new one (`PublicKeys` holds more than one).

Speakers on releases from before this feature update once from the
desktop app; from then on the phone can do it.

## Testing against a local release

A developer can point a test speaker at a local web server with
`/mnt/nv/streborn/selfupdate-source.json` (root-only NAND, so it opens
nothing that root could not do anyway):

```json
{"base": "http://192.0.2.10:8765/", "keys": ["<test public key>"]}
```

An optional `"peers": ["192.0.2.21"]` narrows "update all speakers" to
those addresses, so the whole flow can be tried on two test speakers in a
household that has more.

The server needs GitHub's layout: `latest/download/update.json(.sig)` and
`download/<tag>/<file>`. Sign with
`updatesign sign -dir <dir> -version vX.Y.Z-test -trust <test public key>`
and the seed in `STR_UPDATE_SIGNING_KEY`.
