// shell2/speakers/groups.js: Speakers > Groups in the UI 2.0 preview, where
// stereo pairs and fixed (permanent) groups are SET UP (phase 3, #1287).
//
// SKELETON. What is here: nothing but the hook. The route shows the classic
// Multi-room view (#view-multiroom, views/multiroom.js) unchanged below the
// Speakers area head, with its own live poll, notes and handlers, so every
// group function works in the preview from day one.
//
// Not here, on purpose: who plays along RIGHT NOW. That stays in the sidebar
// speaker list ("+" on an idle speaker, "-" on a member; phase 1). There is no
// output popover and no room switcher (docs/UI-2.0.md, "What we change").
//
// TODO(phase 3, groups) contract for the part that fills this in:
//   receives  mountGroups(ctx), called by the router on every entry to the
//             groups route (after switchView('multiroom') has already run
//             renderMultiroom(true) and started its live poll) and on every
//             sidebar repaint while the route is open.
//               ctx   { root: #view-multiroom, open(route, opts), label(box) }
//   renders   the Groups page of the preview: the stereo pairs (create with
//             left and right, rename, balance, dissolve, the stale-pair fix)
//             and the fixed groups (create, the main speaker, remove a member,
//             forget, the group keys on a remote), each in its own card, the
//             card naming its speakers. The classic view stays the one source
//             of that UI: restyle and regroup it under html.ui2 #view-multiroom
//             (groups.css), or wrap its rendered nodes after each
//             renderMultiroom; never fork a second copy of multiroom.js.
//   reuses    views/multiroom.js (renderMultiroom and every group, pair and
//             group key binding it calls: the protocol logic lives in Go, the
//             frontend only calls what classic calls, and nothing under
//             shell2/ calls a zone binding itself, wiring.test.js) and
//             groups.js (stereoPairsOf, storedPermanentGroupsOf, ...).
//   keeps     every control of today's Multi-room tab, each with a fixed new
//             place (checklist: #zoneCreate, #zoneUngroup, #zonePermanent,
//             #zoneRefresh, #zoneCurrent, the zone cards with make main and
//             update first, the dissolve and member chips, #stereoLeft,
//             #stereoRight, #stereoName, #stereoNameSave, #stereoCreate,
//             #stereoDissolve, #pairBalance, the stale stereo fix, #gkRemote,
//             #gkName, #gkSave, the group key selects and remove buttons,
//             #gkKeyMap, and the result lines #zoneResult, #stereoResult,
//             #gkResult); a result in the card it belongs to; done only when
//             the speakers confirmed; destructive steps confirmed by name.
//
// No DOM work at import: main.js imports this before the views exist.

import './groups.css';

// mountGroups is the groups route's hook. The skeleton marks the classic view
// as shown inside the Speakers area, so groups.css can scope to it.
export function mountGroups(ctx = {}) {
  const root = ctx.root;
  if (!root) return;
  root.dataset.ui2Route = 'groups';
}
