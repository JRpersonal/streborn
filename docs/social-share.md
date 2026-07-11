# Social share buttons — spec for the website

The desktop app and the phone remote show a "share ST Reborn" panel under the
donate buttons (an escalation: *you like it* -> donate, *you love it* -> share).
This document is the handoff so the website (`st-reborn.de`) can offer the same
buttons for parity. Nothing here is app-specific; it is plain share-intent URLs
plus a QR.

## Copy

Two escalating headings (localize per site language; English / German shown):

| Section | English | German |
| --- | --- | --- |
| Donate heading | `You like ST Reborn?` | `Dir gefällt STR?` |
| Share heading | `…and if you really love it?` | `…und wenn es dir so richtig gefällt?` |

Share message (localize; English default):

```
ST Reborn brings my Bose SoundTouch back to life without the Bose cloud.
```

Shared target URL (everywhere): `https://st-reborn.de`

## Platforms and share-intent URLs

URL-encode the parts: `u` = target URL, `text` = message + " " + target URL,
`title` = a short title (e.g. "ST Reborn").

| Platform | Share-intent URL |
| --- | --- |
| WhatsApp | `https://wa.me/?text=<text>` |
| Facebook | `https://www.facebook.com/sharer/sharer.php?u=<u>` |
| Telegram | `https://t.me/share/url?url=<u>&text=<title>` |
| Bluesky | `https://bsky.app/intent/compose?text=<text>` |
| LinkedIn | `https://www.linkedin.com/sharing/share-offsite/?url=<u>` |
| Reddit | `https://www.reddit.com/submit?url=<u>&title=<title>` |

Order used in the app (most-used first): WhatsApp, Facebook, Telegram, Bluesky,
LinkedIn, Reddit. Plus a "copy link" that copies `https://st-reborn.de`.

Brand colours: WhatsApp `#25D366`, Facebook `#1877F2`, Telegram `#229ED9`,
Bluesky `#0085FF`, LinkedIn `#0A66C2`, Reddit `#FF4500`.

### Instagram — not offered
Instagram has no web or QR share-intent (you cannot pre-fill an Instagram post
from a URL; their platform only allows in-app Story sharing). So Instagram can
only be a "Follow on Instagram" profile link, not a share. It is left out of the
app; the website can do the same or add a profile link.

## QR codes (desktop only)

On a desktop, each platform tile also shows a QR that encodes **that platform's
share-intent URL**. A phone scan opens that platform's post pre-filled, so a
visitor logged in on their phone just taps Send. Any QR library works
(error-correction level M); the payload is a plain URL, so the QR works offline.
The phone remote does not show QRs — the user is already on their phone there, so
the buttons open the native app directly.

## Behaviour

- The panel is not permanent: it is behind a single dashed, accent-tinted button
  and folds away until the user opens it.
- Buttons open in a new tab / the system browser.
- Theme-aware; matches the surrounding footer.
