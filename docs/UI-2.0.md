# UI 2.0: the new desktop layout

The desktop app grew one feature at a time, and its eight tabs follow the
technical layers more than what people do with it. In discussion #1278
DrDBanner proposed a full concept that sorts it into listening, controlling
and setting up, without dropping a single function. This page records how we
reviewed that concept and what we changed, so the decisions live next to the
code. Progress is tracked in the UI 2.0 tracking issue (label `ui-2.0`).

The images below are design mockups with sample data, not screenshots of the
app.

## How it ships

As a preview that can be switched on in the settings, next to the current
layout. The new layout is a new shell (sidebar, persistent player, settings
structure) around the existing view modules and the same state, so a feature
added during the transition lands in both layouts. When the preview covers
everything, it becomes the default and the old layout is removed one or two
releases later.

## Four rules for every view

1. **The speaker is always named.** Every action says which speaker it acts
   on.
2. **Honest feedback.** Sent is not arrived. Done only when the speaker has
   confirmed it.
3. **Errors where they happen.** A broken station reports in its row, an
   offline speaker in its card; no window-wide banner for a local problem.
4. **Nothing disappears.** Every current function has a fixed new place (see
   the mapping below).

## Navigation, current and new

| Today (8 tabs) | UI 2.0 |
|---|---|
| Listen to music | Radio |
| Library | Library |
| Recently played | Recently played |
| Spotify | Spotify |
| Multi-room | Player > Output (who plays along), Speakers > Groups (stereo pairs, fixed groups) |
| Speaker settings | Speakers > one speaker |
| Set up speaker | Speakers > Add speaker (wizard) |
| Wishes | Sidebar, bottom; Help and info |

## What we take from the concept

- Sidebar plus a player that stays in place on every view.
- Favorite and key are two named actions: saving a station is not assigning
  a hardware button.
- Technical details (codec, bitrate, stream address) live in the station
  details, not in the list.
- One volume in the player, the individual levels in the output popover.
- Restart, remove and reset sit in a separate maintenance context, confirmed,
  and the confirmation names the speaker.
- Help and info is one fixed place. Support links appear only there, never as
  a banner in the music views.
- Every empty, loading, offline and error state explains what happened and
  what can be done next. Status is never shown by color alone.

## What we change, and why

- **The speaker choice moves to the top of the sidebar.** In the concept the
  speaker only appears in the player. Keys, now playing and most settings
  belong to one speaker, so the room switcher with its live state is the
  anchor of the whole window.
- **"Which speaker am I controlling" and "who plays along" are separate.**
  The room switcher answers the first, the output popover the second.
- **Favorites are app-wide**, not per speaker (open point P1 in the concept).
- **Stereo pairs and fixed groups are set up under Speakers**, not in the
  playback popover. Taking a speaker in or out of the current playback stays
  in the player.
- **Action color with enough contrast.** White on the concept's `#0A84FF`
  reaches 3.6:1. Primary actions use `#1F6FB2` (5.3:1); STR blue `#6BD` stays
  the accent.
- **Platform-neutral shortcuts.** Most users are on Windows: Ctrl, not Cmd.

## What we add

- **Firmware check in the setup wizard.** A speaker below firmware 27.0.6 is
  updated in place before STR is installed (docs/FIRMWARE-UPDATE.md).
- **STR's real speaker states** in one status pill used everywhere: playing,
  update ready, standby, starting up, not reachable, stereo pair, Bose
  original (set up STR).
- **One place for updates**, for the app and every speaker, matching the
  speaker self-update from the phone (docs/SELF-UPDATE.md).
- **Spotify with keys:** put the playlist that is playing on a key directly.
- **Wishes stay visible**, small at the bottom of the sidebar. The Ideas vote
  is the backlog.
- **Three themes:** dark, light and high contrast; only color tokens change.
- **The phone remote follows the same structure:** room switcher on top,
  then Listen, Find, Speakers, More.

## Color tokens

| Token | Dark | Light | High contrast |
|---|---|---|---|
| `--bg` | `#17181B` | `#FFFFFF` | `#000000` |
| `--sidebar` | `#1E1F23` | `#F3F3F5` | `#000000` |
| `--surface` | `#1F2024` | `#F7F7F8` | `#0A0A0A` |
| `--selected` | `#203955` | `#DCEAF6` | `#003A70` |
| `--action` | `#1F6FB2` | `#1769AA` | `#4FC3FF` |
| `--accent` | `#66BBDD` | `#1769AA` | `#7FD8FF` |
| `--text` | `#E9EAEE` | `#16181D` | `#FFFFFF` |

Spacing on an 8 px grid, targets at least 40 px high, the system UI font.
Contrast, keyboard focus, screen reader names and enlarged text are
acceptance criteria for every phase.

## Phases

1. Tokens and components; the shell with sidebar and persistent player.
2. Radio, Library, Recently played, Spotify.
3. Speakers: list, one speaker, groups and stereo pairs.
4. Setup wizard with the firmware check.
5. States, Help and info, the phone remote.
6. Default for everyone; the old layout is removed.

Each phase ships with screenshots, the vitest suite green and every string in
all bundles.

## Mockups

Radio with the room switcher, keys and player:

![Radio](design/ui-2.0/radio.png)

Output: who plays along, with individual levels:

![Output](design/ui-2.0/output.png)

Speakers, one selected:

![Speakers](design/ui-2.0/speakers.png)

Setup wizard, check step with the firmware update:

![Setup](design/ui-2.0/setup.png)

Tokens, components and states:

![Components](design/ui-2.0/components.png)

Phone remote:

![Phone](design/ui-2.0/phone.png)
