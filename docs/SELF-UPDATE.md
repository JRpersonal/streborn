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

The server needs GitHub's layout: `latest/download/update.json(.sig)` and
`download/<tag>/<file>`. Sign with
`updatesign sign -dir <dir> -version vX.Y.Z-test -trust <test public key>`
and the seed in `STR_UPDATE_SIGNING_KEY`.
