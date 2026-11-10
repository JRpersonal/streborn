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
| Multi-room | Sidebar speaker list (who plays along: `+` on a speaker, `−` to take it out), Speakers > Groups (stereo pairs, fixed groups) |
| Speaker settings | Speakers > one speaker |
| Set up speaker | Speakers > Add speaker (wizard) |
| Wishes | Tab row, small at the far end; Help and info |

## What we take from the concept

- Sidebar plus a player that stays in place on every view (the navigation
  became a tab row above the page on 2 November, see below).
- Favorite and key are two named actions: saving a station is not assigning
  a hardware button.
- Technical details (codec, bitrate, stream address) live in the station
  details, not in the list.
- Group volume in the player, the individual levels in a compact panel
  opened from it (Levels).
- Restart, remove and reset sit in a separate maintenance context, confirmed,
  and the confirmation names the speaker.
- Help and info is one fixed place for help. Support is always on screen:
  the three donate buttons sit at the foot of the sidebar under "Support
  STR" on every view (Jens, 2026-11-02: the preview had hidden them in Help
  and info, while the classic layout keeps its rail). Never a banner, never a
  prompt in a success moment.
- Every empty, loading, offline and error state explains what happened and
  what can be done next. Status is never shown by color alone.

## What we change, and why

- **Every speaker in the sidebar** (DrDBanner's proposal in the #1284
  discussion, adopted on 14 October). In the concept the speaker only
  appears in the player; keys, now playing and most settings belong to one
  speaker, so the speakers are the anchor of the whole window. One list
  answers both questions: which speaker you control (one click on its row)
  and who plays along (`+` on an idle speaker adds it to what's playing,
  `−` takes a member out again). Choosing a row and its `+` are two
  different clicks. A stereo pair is one row, "Living room (stereo)",
  because it only plays as a whole. The playing group stays pinned on
  top (the one you control first when several play; with nothing playing,
  the speaker you control, so a group can be put together before Play);
  with many speakers the rest of the list scrolls on its own. Offline
  speakers are greyed and say "Offline"; Bose originals say "Ready for STR"
  and open the setup. This
  replaces the room switcher and the separate output popover of the first
  draft.
- **The sidebar belongs to the speakers, the navigation is a row of tabs
  above the page** (2 November 2026). Until then the sidebar stacked the
  speaker list and the navigation, each in its own scroll box: on Windows
  two scrollbars were always on screen, in the default 1240 x 740 window the
  menu was already cut off (Spotify half visible, Speakers hidden), and at
  the smallest 1000 x 600 only two entries showed. Now the sidebar holds the
  logo, the speaker section, "Support STR" with the three donate buttons
  side by side, and a foot with the text size (Aa) and the language in one
  row. The speaker section takes all the height left: the
  pinned group keeps its cap and scrolls on its own beyond it, the other
  speakers fill the rest and are the one scroll box (thin scrollbars, as in
  the main pane). The navigation is a tab row in its own grid row above the
  main pane, so the page scrolls below it and the tabs never scroll away:
  Radio, Library, Recently played, Spotify, Speakers (marked current on all
  four Speakers places), then Wishes and Help and info small and muted at
  the far end. The open tab has a 3 px accent underline and
  `aria-current="page"`; Left and Right move between the tabs. The row fits
  at 1000 px in English and German at the default text size and wraps onto a
  second line at a larger one, so no tab is ever cut off. The app's version
  line left the sidebar for Help and info, About; only while an app update
  waits and its notice was dismissed does one small line in the sidebar's
  foot say so, and pressing it brings the notice back.
- **A speaker's settings live on its page, Speakers > one speaker** (the
  open question "where do speaker settings live" from the concept). The page
  always shows the speaker you control: opening a speaker from the Speakers
  list makes it the controlled one, and choosing another row in the sidebar
  while the page is open moves the page with it. The sidebar row itself stays
  one click (choose) plus `+` / `−`; it gets no second settings button.
  Its five tabs hold every section of today's Speaker settings: Overview
  (STR and Bose firmware, update, network summary, phone remote, name,
  language, sources), Sound (volume, balance, bass, levels, display and
  playback, announcements), Network (Wi-Fi, region, music libraries,
  Pandora and iHeartRadio), Keys and alarms (button repair, remote key map
  and key actions, copy keys, keys from an address, voice control) and
  Maintenance (identify, logs, support, restart, reset, remove STR, backup,
  SSH, other tools' changes). The checklist is `SPEAKER_SECTIONS` in
  `shell2/speakers/speaker.js`; every confirmation there names the speaker.
- **Volume in the player.** While a group plays, the group volume takes the
  place of the speaker volume, and Levels opens the per-speaker sliders.
  Levels only sets levels; choosing speakers stays in the sidebar.
- **Favorites are app-wide**, not per speaker (open point P1 in the concept).
- **Stereo pairs and fixed groups are set up under Speakers**, not in the
  playback popover. Taking a speaker in or out of the current playback stays
  in the sidebar speaker list (`+` / `−`).
- **Action color with enough contrast.** White on the concept's `#0A84FF`
  reaches 3.6:1. Primary actions use `#1F6FB2` (5.3:1); STR blue `#6BD` stays
  the accent.
- **Platform-neutral shortcuts.** Most users are on Windows: Ctrl, not Cmd.

## What we add

- **Firmware check in the setup wizard.** A speaker below firmware 27.0.6 is
  updated in place before STR is installed (docs/FIRMWARE-UPDATE.md).
- **STR's real speaker states** in one status pill used everywhere: playing,
  update ready, standby, starting up, offline, stereo pair, Bose original
  (set up STR).
- **One place for updates**, for the app and every speaker, matching the
  speaker self-update from the phone (docs/SELF-UPDATE.md).
- **Spotify with keys:** put the playlist that is playing on a key directly.
- **Wishes stay visible**, small and muted at the far end of the tab row,
  next to Help and info. The Ideas vote is the backlog.
- **Three themes:** dark, light and high contrast; only color tokens change.
- **The phone remote follows the same structure:** the speaker list on top,
  then Listen, Find, Speakers, More.

## Color tokens

As defined in `desktop-app/frontend/src/shell2/shell2.css`; the contrast of
every pairing is checked by `shell2/tokens.test.js`.

| Token | Dark | Light | High contrast |
|---|---|---|---|
| `--u-bg` | `#17181B` | `#FFFFFF` | `#000000` |
| `--u-sidebar` | `#1E1F23` | `#F3F3F5` | `#000000` |
| `--u-surface` | `#1F2024` | `#F7F7F8` | `#0A0A0A` |
| `--u-selected` | `#203955` | `#DCEAF6` | `#003A70` |
| `--u-action` | `#1F6FB2` | `#1769AA` | `#4FC3FF` |
| `--u-on-action` | `#FFFFFF` | `#FFFFFF` | `#000000` |
| `--u-accent` | `#66BBDD` | `#1769AA` | `#7FD8FF` |
| `--u-text` | `#E9EAEE` | `#16181D` | `#FFFFFF` |
| `--u-muted` | `#A9ADB6` | `#565B66` | `#E0E0E0` |
| `--u-border` | `#6E727C` | `#767B85` | `#FFFFFF` |
| `--u-ok` | `#5CC98A` | `#1A6B3C` | `#7CFFB2` |
| `--u-warn` | `#E8B84A` | `#8A5A00` | `#FFD54A` |
| `--u-danger` | `#FF7A6E` | `#B3261E` | `#FF8A80` |
| `--u-danger-fill` | `#CC3328` | `#B3261E` | `#FF8A80` |
| `--u-on-danger` | `#FFFFFF` | `#FFFFFF` | `#000000` |
| `--u-focus` | `#66BBDD` | `#1769AA` | `#FFFFFF` |

`--u-on-action` is the label on a button filled with `--u-action`.
`--u-danger` is the danger colour as text and as an edge; a danger button is
filled with `--u-danger-fill` and labelled in `--u-on-danger`.

The sidebar speaker list adds no colour token, only two sizes:
`--u-spk-row` (one row) and `--u-spk-pinned` (how many rows of a large
pinned group show before it scrolls on its own). The other speakers take all
the height the sidebar has left and are its one scroll box.
An offline speaker is greyed with `--u-muted`, never with opacity, so its
name keeps 4.5:1.

Targets at least 40 px high (`--u-target`), the system UI font (`--u-font`).
Contrast, keyboard focus, screen reader names and enlarged text are
acceptance criteria for every phase.

## Size tokens

The same in all three themes, defined next to the colours on `html.ui2`.
The UI 2.0 stylesheets (`shell2/**/*.css` and `views/music/ui2radio.css`)
take `padding`, `margin`, `gap`, `inset` and `font-size` from these tokens
wherever the value is one of the steps. One-off values (2 px, 6 px, 10 px,
borders, widths and heights) stay literal.

Spacing, on a 4 px base with 8 px as the grid:

| Token | Value |
|---|---|
| `--u-space-1` | 4 px |
| `--u-space-2` | 8 px |
| `--u-space-3` | 12 px |
| `--u-space-4` | 16 px |
| `--u-space-5` | 24 px |
| `--u-space-6` | 32 px |

`--u-space` (8 px) is the grid unit itself and the same as `--u-space-2`.

Type scale. It covers exactly the sizes the preview used when the tokens
were introduced, so introducing them changed no pixel. The three `-plus`
steps sit between two main steps and are the ones a later pass can fold
into a neighbour:

| Token | Value |
|---|---|
| `--u-fs-xs` | 12 px |
| `--u-fs-sm` | 13 px |
| `--u-fs-md` | 14 px (the body size) |
| `--u-fs-md-plus` | 15 px |
| `--u-fs-lg` | 16 px |
| `--u-fs-lg-plus` | 17 px |
| `--u-fs-xl` | 18 px |
| `--u-fs-2xl` | 20 px |
| `--u-fs-2xl-plus` | 22 px |
| `--u-fs-3xl` | 24 px |

Font weights: `--u-fw-regular` (400), `--u-fw-semibold` (600) and
`--u-fw-bold` (700).

Radii: `--u-radius` (8 px) for buttons, cards, rows and fields,
`--u-radius-pill` (12 px) for the status pill, badges and state chips.
Round marks use `50%`.

Focus: `--u-focus-ring` (`2px solid var(--u-focus)`) is the outline of every
keyboard focus in the preview, set off from the control by
`--u-focus-offset` (2 px) unless a control draws its ring inside its own
box.

## Phases

1. Tokens and components; the shell with the speaker sidebar (every
   speaker, `+` to play along, the playing group pinned) and the
   persistent player with group volume and per-speaker levels.
2. Radio, Library, Recently played, Spotify.
3. Speakers: list, one speaker, groups and stereo pairs.
4. Setup wizard with the firmware check.
5. States, Help and info, the phone remote.
6. Default for everyone; the old layout is removed.

Each phase ships with screenshots, the vitest suite green and every string in
all bundles. The preview is also checked in a browser against simulated
speakers in CI (`desktop-app/frontend/screenshots/ui2-check.mjs`).

Decided for later: the update banner gets its UI 2.0 styling in phase 5, and
a setting that fails on a speaker's page then shows its error in its own
section instead of a toast across the window.

## Mockups

The Radio and Output mockups predate the sidebar decision on #1284: the
room switcher and the output popover they show are replaced by the sidebar
speaker list and Levels in the player.

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
