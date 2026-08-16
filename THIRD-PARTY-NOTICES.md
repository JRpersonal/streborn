# Third-party notices

STR is licensed under the terms in [LICENSE](LICENSE). It ships a small number
of files that carry their own licence, listed here.

## Mozilla CA certificate store

**Files:** `desktop-app/cacert.pem`, `internal/tlsgen/extraroots.pem`

**Licence:** Mozilla Public License 2.0
(<https://www.mozilla.org/MPL/2.0/>)

Mozilla's set of root certificate authorities, as extracted and redistributed
by the curl project (<https://curl.se/docs/caextract.html>). Both files carry
the extraction date in their header.

`internal/tlsgen/extraroots.pem` is a pinned subset of `desktop-app/cacert.pem`
and is regenerated with `make ca-roots`. The selection is in
`internal/tlsgen/extractroots.py`, so every change to the set of authorities
STR carries is a reviewed edit.

The MPL is file-scoped: it applies to these files and to modifications of them,
not to the rest of this project.

**Why STR ships them at all.** Some SoundTouch speakers were manufactured with
a certificate store that is missing widely used authorities, so they refuse
most https radio stations. That file belongs to the speaker's read-only
firmware, a factory reset cannot change it, and Bose's update servers are gone.
The agent therefore composes a bundle in memory from the speaker's own store
plus these roots, and points its own connections at it. Nothing on the speaker
is modified, and what the Bose firmware itself trusts is left exactly as Bose
shipped it. See `internal/tlsgen/extraroots.go` and
[docs/THREAT-MODEL.md](docs/THREAT-MODEL.md).
