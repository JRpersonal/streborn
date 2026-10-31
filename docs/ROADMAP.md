# STR Roadmap

What comes next is decided by the wish list, not by this file.

- **[Ideas](https://github.com/JRpersonal/streborn/discussions/categories/ideas)**: every wish is one discussion, and the votes set the order. The website shows the same list with its votes and what has shipped (st-reborn.de, section "Planned").
- **[Feature backlog #1591](https://github.com/JRpersonal/streborn/discussions/1591)**: the index of every idea and its vote thread.
- **Larger work I track myself**, each as one discussion: [UI 2.0 #1587](https://github.com/JRpersonal/streborn/discussions/1587), [speaker display #1588](https://github.com/JRpersonal/streborn/discussions/1588), [agent API protection #1589](https://github.com/JRpersonal/streborn/discussions/1589), [Wi-Fi provisioning #1590](https://github.com/JRpersonal/streborn/discussions/1590).
- **Bugs** are GitHub issues. Since 2026-10-28 Issues hold bug reports only; a wish that arrives as an issue moves to Ideas.

Per-model support state lives in [`MODELS.md`](MODELS.md).

## The 1.0 bar

The bar I set myself for 1.0 was deliberately low and measurable. All five are met:

1. **Two models verified end to end.** The SoundTouch 10 and the Portable are verified on hardware; the ST20, ST30, ST300, Wave, SA-4, SA-5, the CineMate and Lifestyle systems and the Wireless Link Adapter run it in the field (see [`docs/MODELS.md`](MODELS.md)).
2. **Hardware presets survive a cold boot, a standby cycle and a Wi-Fi outage** without a manual reset. The agent re-registers the keys whenever the speaker reports an empty list, and tests pin the empty-list, standby-exit and reconnect paths (`internal/boxws`).
3. **An honest first install.** Both desktop builds are signed (Windows by Certum, macOS notarized by Apple), so there is no SmartScreen or Gatekeeper workaround to explain. The website names the one prompt that does appear (the Windows firewall asking for home-network access), and its Verify section links the SHA256 sums and the Sigstore attestation.
4. **Threat model published**, including what a Bose factory reset does and does not remove ([`docs/THREAT-MODEL.md`](THREAT-MODEL.md)).
5. **Legal pages complete.** Imprint and privacy policy, in German and English, without placeholder text.
