# Administrator access prototype

This uncommitted prototype adds an optional administrator password to the
speaker API. The phone page shows password status but does not accept
credentials. The desktop app can change speaker settings without signing in;
its Advanced settings still provide optional password setup and recovery.

Playback, volume, and existing preset recall remain open. The app sends
protected settings requests over TLS and the speaker accepts the
`X-STR-Desktop-Admin: 1` marker without asking for the administrator password.
The same marker lets the app reset a password that was set from the phone,
without a recovery key.

## Security tradeoff

**The desktop marker is not authentication.** Any client that can reach the
speaker's TLS API can send the same marker, change protected settings, or reset
the password. The password lock therefore does not protect a speaker from
other clients on the home network. This shortcut is only suitable for local
testing where that tradeoff is acceptable.

Admin password, login, and recovery requests are still rejected on HTTP. The
desktop app automatically pins the speaker's first TLS certificate and rejects
a later certificate change. First contact is trust on first use.

The speaker stores a salted PBKDF2-SHA256 password hash, and sessions expire
after 12 hours or an agent restart.

## Manual check

1. Open a speaker's settings and confirm the desktop app works without a
   password, including when the optional password is enabled.
2. Confirm playback, volume, preset recall, preset edits, and alarm saves work
   from the desktop app.
3. Confirm the phone page has no password field and HTTP login/setup requests
   are rejected.
4. This bypass has not been tested on real hardware. It is for local testing,
   not a production security guarantee.
