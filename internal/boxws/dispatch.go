// Frame dispatch: handleMessage routes one incoming gabbo frame to the
// Handler hooks (scan-before-switch ordering is load-bearing, do not reorder).

package boxws

import (
	"context"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (c *Client) handleMessage(ctx context.Context, data []byte) {
	c.logger.Debug("box ws frame", "bytes", len(data), "preview", preview(data, 400))

	s := string(data)

	// Remote next/prev keys: the box cannot skip a UPnP source itself, so it
	// emits a QPLAY_SKIP_*_FAILED error. These are firmware error codes that
	// never appear in user-supplied text, so a whole-frame match is safe and
	// they short-circuit before the typed parse.
	switch {
	case strings.Contains(s, "QPLAY_SKIP_NEXT_FAILED"):
		// A skip key is a CONCRETE event, so the userActivityUpdate the firmware
		// sends alongside it is explained and must not also be read as a thumb
		// press. Without this the remote's skip keys fired the thumb webhook as
		// well: reported on 2026-08-06, "my ST20s do skip through songs using
		// the remote, but doing so also triggers the webhook" (#536).
		c.noteExplainedActivity()
		if c.handler != nil {
			c.handler.OnRemoteSkip(ctx, true)
		}
		return
	case strings.Contains(s, "QPLAY_SKIP_PREV_FAILED"):
		c.noteExplainedActivity()
		if c.handler != nil {
			c.handler.OnRemoteSkip(ctx, false)
		}
		return
	}

	var f gabboFrame
	if err := xml.Unmarshal(data, &f); err != nil {
		// Not parseable as an <updates> envelope. Still try the source/AUX
		// attribute scan below (it is attribute-only and cannot be fooled by
		// text), then log it as unrecognized.
		c.logger.Debug("box ws xml parse error", "err", err)
	}

	// AUX webhook (beta): fire once when the active source transitions to AUX.
	// STR never selects AUX itself, so this is always a user press (front panel
	// or remote; app recalls use a different path). source is read as a raw
	// attribute (source="..."), which only ever appears in markup, never in
	// escaped text content, so this scan is not subject to the title
	// false-positive that the typed parse fixes elsewhere. Tracking lastSource
	// means it fires on the change, not on every AUX frame.
	if c.handler != nil {
		if src := attrValue(s, "source"); src != "" {
			c.mu.Lock()
			changed := src != c.lastSource
			prev := c.lastSource
			c.lastSource = src
			// Stamp every flap to INVALID_SOURCE (the box's failed self-activation
			// of STR's UPNP source): a STOP_STATE within a moment of it is that
			// teardown, not a user stop. See stopStateIsTeardown.
			if src == "INVALID_SOURCE" {
				c.lastInvalidSourceAt = time.Now()
			}
			// Track the whole UPNP-driven INVALID_SOURCE episode, not just its
			// first seconds: a long dwell before the STANDBY entry must still
			// count as "STR's playback is what powered off" (see upnpEpisode).
			switch src {
			case "INVALID_SOURCE":
				if prev == "UPNP" {
					c.upnpEpisode = true
				}
			case "STANDBY":
				// keep: the episode's terminal state is what we classify
			default:
				c.upnpEpisode = false
			}
			// Stamp every flap to OR from STANDBY: the spontaneous-off oscillation
			// (#419) carries a STOP_STATE on its STANDBY->UPNP leg whose source reads
			// UPNP, so this is the only trace that the STOP_STATE belongs to the
			// bounce and is not a deliberate user stop. See stopStateIsTeardown.
			if src == "STANDBY" || prev == "STANDBY" {
				c.lastStandbyFlapAt = time.Now()
			}
			// Stamp when STR's own source (UPNP) STOPS being active: the box's
			// give-up after a failed self-activation reaches STANDBY through
			// INVALID_SOURCE (UPNP -> INVALID_SOURCE -> STANDBY), and the
			// standby handling below must still recognise that STR-driven
			// playback is what just powered off.
			if prev == "UPNP" {
				c.lastUpnpActiveAt = time.Now()
			}
			upnpRecently := prev == "UPNP" ||
				(!c.lastUpnpActiveAt.IsZero() && time.Since(c.lastUpnpActiveAt) < upnpFlapWindow) ||
				c.upnpEpisode
			c.mu.Unlock()
			if changed {
				// Log every source transition at INFO (rare by construction: only
				// on change). This is how we learn the exact label the firmware
				// uses for external inputs like AirPlay on each model (#122), so a
				// diagnostic bundle pins down what the box actually reports when
				// the app cannot tell it is playing.
				c.logger.Info("box ws: source changed", "from", prev, "to", src)
				if src == "AUX" {
					c.handler.OnSourceAux(ctx)
				}
				// A native radio station the box abandons on its own. Measured on
				// a SoundTouch 20 (2nd gen, v0.9.30): every one of twelve native
				// presses ended LOCAL_INTERNET_RADIO -> INVALID_SOURCE within a
				// few seconds, and only the re-push recovery got audio back,
				// while the same build on the owner's ST30 dropped once in eight.
				// The write succeeded on both, so the write-side latch never saw
				// anything wrong. This is the playback-side counterpart: a box
				// that will not KEEP a native station has to fall back to the
				// UPnP form, which works there.
				// The discriminator is reaching INVALID_SOURCE, not leaving the
				// native source: a normal preset change also goes
				// LOCAL_INTERNET_RADIO -> UPNP and back within a few hundred
				// milliseconds, and counting that would disable native presets
				// on a perfectly healthy speaker. The failure route observed on
				// the ST20 was LOCAL_INTERNET_RADIO -> UPNP -> INVALID_SOURCE a
				// few seconds after the station started, so the stamp below
				// covers the direct and the indirect route alike.
				if src == "LOCAL_INTERNET_RADIO" {
					c.mu.Lock()
					c.nativeStartedAt = time.Now()
					c.mu.Unlock()
				}
				if prev == "LOCAL_INTERNET_RADIO" {
					c.mu.Lock()
					c.lastNativeActiveAt = time.Now()
					c.mu.Unlock()
				}
				if src == "INVALID_SOURCE" {
					c.mu.Lock()
					recent := !c.lastNativeActiveAt.IsZero() &&
						time.Since(c.lastNativeActiveAt) < nativeDropWindow
					c.mu.Unlock()
					if recent {
						c.fireNativeDropped()
					}
				}
				// The other way a speaker abandons a native station: straight to
				// STANDBY, never touching INVALID_SOURCE. The guard above missed
				// that route entirely, so a speaker that cannot keep native
				// stations never learned it and went silent on every single press
				// (field 2026-08-06, a newly added ST10: "normal playback does not
				// work either, the device switches to standby immediately").
				//
				// STANDBY is ambiguous where INVALID_SOURCE is not, because a user
				// switching the speaker off looks the same. The discriminator is
				// TIME, not the activity frame: that frame is known to fire from
				// STR's own writes and to appear with nobody near the box. Nobody
				// powers a speaker off within a breath of starting a station, so a
				// station that lasted less than nativeStandbyDropWindow was
				// dropped by the firmware. The reported case lasted 862 ms.
				if src == "STANDBY" && prev == "LOCAL_INTERNET_RADIO" {
					c.mu.Lock()
					started := c.nativeStartedAt
					c.mu.Unlock()
					if !started.IsZero() && time.Since(started) < nativeStandbyDropWindow {
						c.logger.Warn("box ws: the speaker dropped a native station to standby right after starting it, counting it as a native failure",
							"lastedMs", time.Since(started).Milliseconds())
						c.fireNativeDropped()
					}
				}
				// #197: some ST20 (scm) firmware oscillates UPNP->STANDBY->UPNP on a
				// power-off, re-selecting STR's UPnP source so the speaker switches
				// itself back on. When STR's own source (UPNP) drops to STANDBY, give
				// the handler a chance to clear the transport so the box has nothing to
				// bounce back to. Optional interface so handlers that do not need it
				// (tests) are unaffected. Gated so it only fires for STR-driven
				// playback, never an AUX/Spotify power-off: either a direct
				// UPNP->STANDBY drop, or the give-up flap UPNP->INVALID_SOURCE->
				// STANDBY the firmware performs after a failed self-activation -
				// that flap used to bypass the entire standby machinery, so no
				// classification/recovery ran while the box switched itself off
				// (field bundles 2026-07-22, all standby entries on taigan/spotty/
				// lisa took this route).
				// Any wake OUT of standby re-registers the hardware keys. The
				// v0.9.21 keepalive+skip fixes removed the accidental every-11-min
				// re-sync that had been silently healing boxes whose firmware
				// de-registers the key layer without a trace (#487: 70 minutes
				// of steady state, zero press frames, remote dead while /presets
				// still listed all slots). A standby exit is the moment the user
				// is back, and a write to an awake box never touches the
				// deep-standby countdown. Rate-limited inside the request.
				if prev == "STANDBY" && src != "STANDBY" {
					if h, ok := c.handler.(interface{ OnStandbyExit(context.Context) }); ok {
						h.OnStandbyExit(ctx)
					}
				}
				if src == "STANDBY" && upnpRecently {
					if h, ok := c.handler.(interface{ OnEnterStandby(context.Context) }); ok {
						h.OnEnterStandby(ctx)
					}
				}
			}
		}
	}

	// known tracks whether this frame matched any gabbo type STR understands.
	// Frames that match nothing are logged in full at INFO at the end so the
	// genuinely new, user-initiated events we are still mapping out (the preset
	// long-press "store" gesture and the remote's thumbs keys) can be identified
	// from a real box. These are rare, so logging them fully does not churn the
	// NAND log the way the periodic connectionState/nowPlaying frames would.
	// connectionState and nowPlaying fire every few seconds on some boxes (the
	// Portable flaps GOOD_SIGNAL<->EXCELLENT_SIGNAL constantly), so they stay at
	// DEBUG; powerState transitions are rare and useful and stay at INFO.
	known := false
	switch {
	case f.PowerState != nil:
		known = true
		c.noteExplainedActivity()
		standby := strings.Contains(f.PowerState.Inner, "STANDBY")
		// INFO with the resolved direction: a real power press surfaces here, while
		// a self-wake (zone / stereo pair) surfaces instead as the DO_NOT_RESUME
		// now-selection restore below. This split is the discriminator the power-on
		// resume relies on, so keep it visible in bundles for the hardware check.
		c.logger.Info("box ws phase: powerState event", "standby", standby, "preview", preview(data, 200))
		if c.handler != nil {
			if standby {
				// power webhook (beta): fire only on the transition to standby (power
				// off). STR never powers the box off itself (it only wakes it for a
				// recall), so a standby event is always a user press; this avoids the
				// webhook false-firing on STR's own wake. The STANDBY match is bounded
				// to the powerState element body. Rate-limited per id downstream.
				c.handler.OnPowerKey(ctx)
			} else {
				// A real power-ON: the box left standby. A self-wake does NOT arrive as
				// a powerState (it comes as the DO_NOT_RESUME restore -> OnSelfWake), so
				// this is the verified user-wake the optional power-on resume binds to.
				// The resume is gated by a per-box setting AND suppressed if a
				// DO_NOT_RESUME was seen in the same window, so it can never resume a
				// self-wake.
				c.handler.OnPowerWake(ctx)
			}
		}
	case f.ConnectionState != nil:
		known = true
		c.logger.Debug("box ws phase: connectionState event", "preview", preview(data, 200))
		// Capture the Wi-Fi signal class; on BCO boxes this is the only place it
		// is reported (/networkInfo has no signal there). Attribute-only scan.
		if sig := attrValue(s, "signal"); sig != "" {
			c.mu.Lock()
			c.lastSignal = sig
			c.lastSignalAt = time.Now()
			c.mu.Unlock()
		}
	case f.BassUpdated != nil:
		// Same reasoning as volume: a bass change is a person at the speaker, so
		// it explains the userActivity ping that arrives with it and must not be
		// mistaken for a thumb press.
		known = true
		c.noteExplainedActivity()
	case f.VolumeUpdated != nil:
		// A volume change is identifiable activity: the box emits a
		// userActivityUpdate alongside it, so mark this as "explained" and the
		// thumb heuristic will not treat that ping as a thumb press.
		known = true
		c.noteExplainedActivity()
	case f.UserActivity != nil:
		// The remote thumbs keys surface ONLY as this generic ping (no up/down
		// identity). Treat a lone one as a thumb press; noteUserActivity
		// debounces it and suppresses it when an explained event bracketed it.
		known = true
		c.noteUserActivity(ctx, data)
	case f.NowPlaying != nil && f.NowSelection == nil:
		known = true
		c.noteExplainedActivity()
		c.logger.Debug("box ws phase: nowPlaying event", "preview", preview(data, 200))
		// STOP_STATE in a nowPlaying update is the box reporting that playback
		// was stopped (the user pressed stop on the remote/box). Read from the
		// typed playStatus, so a track title containing "STOP_STATE" can no
		// longer trip it. INFO, not DEBUG: stops are rare and this is the signal
		// the re-push decision hinges on, so it must be visible in a bundle.
		if f.NowPlaying.playStatus() == "STOP_STATE" && c.handler != nil {
			if teardown, why := c.stopStateIsTeardown(f.NowPlaying); teardown {
				// Not a user stop: the box tore its own UPNP source down (a preset
				// switch or an involuntary stream drop). Firing OnUserStop here
				// latched a phantom user-stop that killed the drop recovery and the
				// re-press retry, so the buttons looked dead (#ST30 2026-07-11).
				c.logger.Info("box ws: STOP_STATE during a source teardown, not a user stop (recovery stays armed)", "reason", why)
			} else {
				c.logger.Info("box ws: playback stopped (STOP_STATE), treating as user stop")
				c.handler.OnUserStop(ctx)
			}
		}
	case f.PresetsUpdated != nil:
		// The box reported its own preset list (#14). Surface the full set incl.
		// foreign sources (DEEZER etc.) so STR can show/preserve/recall them
		// (Option C: the box plays a Deezer preset via its own cached account).
		known = true
		bps := make([]BoxPreset, 0, len(f.PresetsUpdated.Presets))
		slots := make([]string, 0, len(f.PresetsUpdated.Presets))
		for _, p := range f.PresetsUpdated.Presets {
			slots = append(slots, p.ID)
			slot, err := strconv.Atoi(strings.TrimSpace(p.ID))
			if err != nil || slot < 1 || slot > 6 {
				continue
			}
			ci := p.ContentItem
			bps = append(bps, BoxPreset{
				Slot: slot, Source: ci.Source, Type: ci.Type, Location: ci.Location,
				SourceAccount: ci.SourceAccount, Name: ci.ItemName,
			})
		}
		// DEBUG, not INFO: the box re-emits presetsUpdated in bursts (~20x around
		// boot / a preset sync), and the MultiWriter logger appends every line to
		// the NAND log, so an INFO burst is a stack of rapid NAND writes for no
		// diagnostic gain. The slots are still captured at DEBUG when needed.
		c.logger.Debug("box ws: presetsUpdated", "count", len(slots), "slots", strings.Join(slots, ","))
		if c.handler != nil && len(bps) > 0 {
			c.handler.OnPresetsChanged(ctx, bps)
		}
	case f.ZoneUpdated != nil:
		// The box's multiroom zone / stereo pair changed. Previously this frame
		// fell through as an "unrecognized frame" (Klaus 2026-06-12), so STR was
		// blind to box-formed groups: it could not show or dissolve them, and a
		// stereo pair sourced from STR played mono. Surface it typed so the agent
		// can track and reconcile it.
		known = true
		z := f.ZoneUpdated.toState()
		if z.Master == "" {
			c.logger.Info("box ws: zoneUpdated -> zone dissolved")
		} else {
			c.logger.Info("box ws: zoneUpdated", "master", z.Master, "senderIsMaster", z.SenderIsMaster,
				"members", len(z.Members))
		}
		if c.handler != nil {
			c.handler.OnZoneChanged(ctx, z)
		}
	case f.GroupUpdated != nil:
		// The box's stereo pair changed. Both members emit their own frame, so
		// this is where a teardown becomes visible on BOTH speakers - including
		// one done in the Bose app, which never reaches STR any other way.
		known = true
		g := f.GroupUpdated.toState()
		if !g.Paired() {
			c.logger.Info("box ws: groupUpdated -> stereo pair dissolved")
		} else {
			c.logger.Info("box ws: groupUpdated", "id", g.ID, "master", g.Master, "members", len(g.Members))
		}
		if c.handler != nil {
			c.handler.OnGroupChanged(ctx, g)
		}
	case f.LanguageUpdated != nil:
		known = true
		c.noteLanguageUpdated(strings.TrimSpace(f.LanguageUpdated.Sys))
	}

	pe := f.NowSelection
	if pe == nil {
		pe = f.PresetSelected
	}
	if pe == nil {
		if !known {
			// Some events arrive as a BARE root element, not wrapped in <updates>
			// (the box sends <userActivityUpdate/> and <errorUpdate> this way), so
			// the typed <updates> parse above leaves them nil. Recover the ones we
			// act on by the ROOT element name. This is structural (the element
			// name), NOT a content substring, so it keeps the title false-positive
			// protection the typed parse added.
			switch rootLocalName(s) {
			case "sourcesUpdated":
				// The box announces that its registered source list changed.
				// Typed rather than left to fall through as an unrecognized
				// frame, because it is the signal that decides how presets are
				// stored: the radio source registers a few seconds after the
				// agent's startup check, and without this the agent keeps a
				// stale "not available" verdict and writes UPnP presets that
				// the box then refuses to activate itself.
				c.logger.Info("box ws: the box changed its registered sources")
				c.fireSourcesChanged()
				return
			case "userActivityUpdate":
				// Lone thumb ping (see noteUserActivity). Regressed for bare frames
				// when the parser went typed; this restores it (live box log
				// 2026-06-12 showed bare <userActivityUpdate/> as unrecognized).
				c.noteUserActivity(ctx, data)
				return
			case "errorUpdate":
				// The box reports playback/source failures as a bare <errorUpdate>
				// frame (UPnP SetURI rejected, wrong state, bad URL, audio timeout).
				// These used to fall through to the generic "unrecognized frame" INFO
				// line, so real box errors were buried in diagnostics. Surface them at
				// WARN with the code/name so a bundle shows exactly what the box
				// rejected: e.g. 1036 UpnpRcvdContentItemInWrongState (SetURI raced a
				// standby wake), 3101 AUDIO_ERROR_BAD_URL (a stale/unplayable preset),
				// 3103 AUDIO_ERROR_TIMEOUT. Diagnostic only; STR's recall/verify paths
				// already recover, this just makes the cause visible.
				if v, name, sev, detail := parseBoxError(s); v != "" {
					c.logger.Warn("box ws: box reported error",
						"value", v, "name", name, "severity", sev, "detail", detail)
					if v == "1036" {
						c.note1036()
					}
					// 1036 UNABLE_TO_PROCESS_NOT_LOGGED_IN: the box refuses the UPnP
					// source because it does not think it is signed into an account.
					// Signal the agent (via the OnLoginError callback) so the recall
					// verify stands its retry down for a beat and recovers with a bare
					// stream re-push, instead of thrashing the box. The callback does
					// NOT force an account re-login: re-onboarding a live source
					// bounced it into a self-off + volume reset on taigan/scm (see
					// webui.NoteBoxLoginError / project_selfoff_login_maintenance).
					//
					// The box reuses code 1036 for two flavors that can arrive
					// SEPARATELY or TOGETHER:
					//   - name UNABLE_TO_PROCESS_NOT_LOGGED_IN: the box refuses the
					//     source because it lost its logged-in/associated state (it can
					//     keep a margeAccountUUID yet still report this). Re-asserting
					//     the marge account does NOT actually cure this (the UPnP
					//     activation login is not tied to it, proven on .79); a bare
					//     re-push of the stream is what recovers it, as in v0.9.0.
					//   - detail UpnpRcvdContentItemInWrongState: the routine race of a
					//     SetURI against a standby wake / a preset->preset teardown, and
					//     the expected teardown when /setZone kills an in-flight UPnP
					//     session during group forming (#70). By itself NOT a login
					//     problem: firing the self-heal on it killed the recall retry
					//     and forced a pointless re-pair on every wake race.
					//
					// The decisive field is the NAME (the box's authoritative reason).
					// Real hardware-preset rejections on the Portable/ST10/ST20/ST30
					// carry BOTH markers at once -
					// name=UNABLE_TO_PROCESS_NOT_LOGGED_IN detail=UpnpRcvdContentItemInWrongState
					// (53/53 field log lines) - because the box could not activate its
					// own stored ContentItem PRECISELY because it is not logged in. The
					// previous detail-first check misread every one of those as a plain
					// wake race and re-pushed the identical SetURI into a box that keeps
					// answering 1036 until a power pull, never re-registering it. So
					// classify by the name first: a not-logged-in name means re-login,
					// even when the wrong-state teardown rides along in the detail.
					notLoggedIn := strings.Contains(strings.ToUpper(name), "NOT_LOGGED_IN")
					wrongState := strings.Contains(detail, "UpnpRcvdContentItemInWrongState")
					switch {
					case notLoggedIn:
						// Root cause: the box is not logged in. Re-assert the account so
						// it accepts STR's source. Also nudge the recall's verify to
						// re-point once the re-login lands (the box hangs
						// attached-but-buffering otherwise); the self-heal is rate-limited
						// and the verify re-push is a no-op once the box plays.
						c.fireLoginError()
						if h, ok := c.handler.(interface{ OnSourceRejected(context.Context) }); ok {
							h.OnSourceRejected(ctx)
						}
					case wrongState:
						// Pure wake/teardown race (name is a plain UNABLE_TO_PROCESS, no
						// NOT_LOGGED_IN). It does NOT retry on its own and can hang
						// attached-but-buffering on the Spotify stream without reaching
						// audio, which needed a manual second preset press to clear (ST30
						// 4->5 switch, 2026-07-14). Signal the recall so its verify
						// re-points instead of trusting that stuck state. NOT a login
						// problem, so it must not fire the re-login self-heal.
						if h, ok := c.handler.(interface{ OnSourceRejected(context.Context) }); ok {
							h.OnSourceRejected(ctx)
						}
					case v == "1036":
						c.fireLoginError()
					}
					return
				}
			}
			// Surface anything still unrecognized so we can map the events STR
			// does not yet handle (the preset long-press store gesture).
			//
			// "rare" was the assumption, and it was wrong. Some of these repeat
			// forever: a Portable emits userInactivityUpdate every few minutes,
			// and sourcesUpdated / swUpdateStatusUpdated / balanceUpdated arrive
			// on every source change. Measured 2026-08-06, unrecognized frames
			// were 15.6 % of the 32 KB NAND log, the only log that survives a
			// reboot on a box with no shell, at up to 1800 bytes per line.
			//
			// The forensic value is in learning that a frame SHAPE exists, not
			// in the hundredth copy of it. So the first of each shape is logged
			// in full, and repeats are counted and reported once an hour.
			c.logUnrecognizedFrame(data)
		}
		return
	}

	// A preset / now-selection change is identifiable activity (the user
	// recalled a preset); it explains any accompanying userActivity, so the
	// thumb heuristic must not fire on a preset press.
	c.noteExplainedActivity()

	var slot int
	_, _ = fmt.Sscanf(pe.ID, "%d", &slot)
	if slot < 1 || slot > 6 {
		// id="0" + INVALID_SOURCE follows the real press when the box cannot
		// play the source itself. Ignore it for playback, but log it once on
		// INVALID_SOURCE: this is the box's own failed self-activation that
		// shows "service unavailable" on the display (#22) before STR takes
		// over. Markers are matched within this preset element only (pe.Inner),
		// so an unrelated frame's title cannot trip it.
		if strings.Contains(pe.Inner, "INVALID_SOURCE") || strings.Contains(pe.Inner, "DISABLED") {
			// A standby wake or a source teardown makes the box restore its last
			// now-selection and, because it cannot natively play STR's UPNP
			// source, mark it INVALID_SOURCE + type=DO_NOT_RESUME. STR used to
			// INVERT that signal and resume the last stream, which made boxes
			// start playing on their own after any standby wake and kept AirPlay
			// from staying stopped (Klaus + Brecht diagnostics, 2026-06-12):
			// wake -> play -> 500 -> retry, on a loop. DO_NOT_RESUME means exactly
			// what it says. STR now stands down; playback only follows an explicit
			// user action: a real preset press (slot 1-6 below) or an app recall.
			if strings.Contains(pe.Inner, "DO_NOT_RESUME") {
				// The box left standby and, unable to play its UPNP selection
				// itself, restored it as INVALID_SOURCE + DO_NOT_RESUME. On this
				// firmware that is the ONLY power-on signal: no powerStateUpdated is
				// ever sent (verified live on a Portable/taigan 2026-06-13). So this
				// is what drives the optional power-on resume. The box will NOT play
				// it natively; STR's resume decides, gated by a per-box opt-out and a
				// zone-membership self-wake guard (see webui.ResumeLastPlay), so a
				// stereo-pair self-wake never auto-resumes.
				c.logger.Info("box ws: power-on wake (DO_NOT_RESUME restore), box will not resume natively")
				if c.handler != nil {
					c.handler.OnPowerWake(ctx)
				}
			} else {
				// DEBUG: the box emits this id=0 INVALID_SOURCE self-activation after
				// EVERY hardware preset press (the actual press is logged at INFO
				// just below), so at INFO it doubled the NAND writes per press for no
				// extra signal.
				c.logger.Debug("box self-activation rejected preset (shows 'service unavailable')",
					"id", pe.ID, "source", pe.ContentItem.Source,
					"location", pe.ContentItem.Location, "preview", preview(data, 240))
			}
		}
		return
	}
	// Named for what was OBSERVED, not for what is assumed. The firmware sends
	// the same event whether the trigger was the speaker's own keys, the IR
	// remote, or the Bose app on someone's phone, and calling it a hardware
	// press invites exactly one wrong conclusion: that the owner standing in
	// the room did it. That misreading cost a round trip on a report where
	// stations kept switching by themselves (#510, 2026-08-11), and the honest
	// line is the one that keeps the question open.
	c.logger.Info("preset selected on the speaker (keys, remote or another app)",
		"slot", slot,
		"location", pe.ContentItem.Location,
		"source", pe.ContentItem.Source,
		"title", pe.ContentItem.ItemName,
	)
	// Stamp the press so the STOP_STATE this switch teardown emits a moment later
	// is recognised as teardown, not a user stop (see stopStateIsTeardown).
	c.mu.Lock()
	c.lastPresetPressAt = time.Now()
	c.mu.Unlock()
	if c.handler != nil {
		c.handler.OnPresetSelected(ctx, slot,
			pe.ContentItem.Location, pe.ContentItem.ItemName)
	}
}

// unknownSummaryEvery bounds how often the repeat counts are rolled up. Long
// enough that a chatty box costs one line an hour, short enough that a bundle
// pulled during a fault still shows what has been arriving.
const unknownSummaryEvery = time.Hour

// frameShape names a frame by its first element, which is what makes two frames
// "the same kind": the bodies differ per device and per value, the shape does
// not. Falls back to a short prefix when no element name can be read, so an
// unparseable frame still groups with its own kind instead of being unique
// every time (which would defeat the whole point).
func frameShape(data []byte) string {
	s := string(data)
	i := strings.IndexByte(s, '<')
	if i < 0 {
		return strings.TrimSpace(preview(data, 24))
	}
	rest := s[i+1:]
	end := strings.IndexAny(rest, " \t\r\n/>")
	if end <= 0 {
		return strings.TrimSpace(preview(data, 24))
	}
	name := rest[:end]
	// An <updates> wrapper says nothing; the interesting name is the child.
	if name == "updates" {
		if j := strings.IndexByte(rest, '<'); j >= 0 {
			inner := rest[j+1:]
			if e := strings.IndexAny(inner, " \t\r\n/>"); e > 0 {
				return "updates/" + inner[:e]
			}
		}
	}
	return name
}

// logUnrecognizedFrame logs the FIRST frame of each shape in full and counts
// the rest, rolling the counts up once an hour.
//
// The old behaviour logged every one at up to 1800 bytes. On a Portable that
// was 15.6 % of the 32 KB NAND ring (measured 2026-08-06), i.e. it was
// consuming the very history a post-mortem needs, to repeat facts already in
// the log. Learning that a shape exists is the whole forensic value here.
func (c *Client) logUnrecognizedFrame(data []byte) {
	shape := frameShape(data)
	c.mu.Lock()
	if c.unknownFrames == nil {
		c.unknownFrames = map[string]int{}
	}
	c.unknownFrames[shape]++
	n := c.unknownFrames[shape]
	var summary []any
	now := time.Now()
	if n > 1 && !c.unknownSummaryAt.IsZero() && now.Sub(c.unknownSummaryAt) >= unknownSummaryEvery {
		c.unknownSummaryAt = now
		for k, v := range c.unknownFrames {
			summary = append(summary, k, v)
		}
	} else if c.unknownSummaryAt.IsZero() {
		c.unknownSummaryAt = now
	}
	c.mu.Unlock()

	if n == 1 {
		c.logger.Info("box ws unrecognized frame (first of this shape)",
			"shape", shape, "bytes", len(data), "body", preview(data, 1800))
	} else {
		c.logger.Debug("box ws unrecognized frame", "shape", shape, "count", n)
	}
	if len(summary) > 0 {
		c.logger.Info("box ws unrecognized frames so far", summary...)
	}
}
