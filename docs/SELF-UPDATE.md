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

The speaker the phone talks to drives the run (`POST {"action":"plan"}`
lists, `POST {"action":"install-all"}` starts, `GET` carries the run as
`fleet`):

- **One speaker at a time.** For each peer that is older than the release,
  it asks the peer to check and then to install through the peer's own
  `/api/agent/self-update`, so each speaker verifies the signature, version
  and hashes itself. It then polls the peer (every 5 s, at most 12 minutes,
  plus up to 4 minutes for the Spotify engine) until the peer is back on
  the new version, and only then moves on.
- **This speaker last.** Its own update reboots it and cuts the phone off.
  The run travels in `selfupdate-pending.json` across that reboot, and the
  card shows the summary once the speaker is back.
- **Skipped, never forced:** speakers that are already current, speakers
  that cannot be reached, and speakers whose agent predates the
  self-update endpoint. Those are listed as "needs one update from the
  desktop app" and are not touched in any other way.
- **Who is reached:** only entries of the peer list, only bare private LAN
  addresses, only the agent ports (`:8888` on sm2, `:17008` on BCO chassis,
  recognised by a JSON `/api/agent/version` so the Bose service on an sm2's
  `:17008` is never mistaken for the agent), through a dialer that refuses
  loopback and link-local, and without following redirects. Each peer
  applies its own LAN gate to the request.

The run state lives in the driving speaker's memory, so reloading the
phone page, or opening it on another phone, picks up the run.

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
