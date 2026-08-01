// Thumb-trigger heuristic: treating a lone userActivityUpdate frame as a
// remote thumb press (see the Handler.OnThumbActivity contract).

package boxws

import (
	"context"
	"time"
)

// thumbExplainWindow is how close an explained event (volume/preset/now
// playing) must be to a userActivity for that activity to count as "explained"
// (i.e. NOT a thumb). thumbSettle is how long we wait after a lone userActivity
// before firing, to let any sibling event arrive and cancel it.
const (
	thumbExplainWindow = 600 * time.Millisecond
	thumbSettle        = 500 * time.Millisecond
	thumbDebounce      = 2 * time.Second
	// userActivityLogDedup is the minimum gap between INFO log lines for an
	// incoming userActivity frame. A thumb press emits a single frame, so an
	// isolated press is always logged; a volume ramp emits many, which collapse
	// to one line per window so the NAND log does not churn (#187).
	userActivityLogDedup = 3 * time.Second
)

// noteExplainedActivity records that a concrete, identifiable action just
// happened (volume change, preset/now-selection, now-playing change, power).
// It cancels any pending thumb fire, because that activity explains the
// userActivity ping and it is therefore not a thumb press.
func (c *Client) noteExplainedActivity() {
	c.thumbMu.Lock()
	c.thumbExplained = time.Now()
	if c.thumbPending != nil {
		c.thumbPending.Stop()
		c.thumbPending = nil
	}
	c.thumbMu.Unlock()
}

// noteUserActivity handles a userActivityUpdate frame. If no explained event
// happened just before it, it arms a short settle timer; if no explained event
// arrives during the settle window either, it fires OnThumbActivity once
// (debounced). Both the before- and after-cases are covered, so a volume key
// (which emits volumeUpdated alongside userActivity, in either order) does not
// misfire.
func (c *Client) noteUserActivity(ctx context.Context, raw []byte) {
	c.thumbMu.Lock()
	defer c.thumbMu.Unlock()
	// Every userActivityUpdate is a physical key press (box or IR remote),
	// whether or not it is later explained by a sibling event. Stamp it for
	// LastUserActivity's spontaneous-power-off discriminator (#419).
	c.lastUserActivityAt = time.Now()
	// Record that a userActivity frame arrived at INFO (deduped), independent of
	// whether the heuristic ends up firing. A "the thumb key does nothing" report
	// (#187) is otherwise undiagnosable from a bundle: we cannot tell a frame that
	// never arrived (box sends nothing for thumbs on this firmware) from one that
	// arrived and was suppressed. The raw frame is also captured so we can see
	// whether it carries any attribute that distinguishes thumb-up from -down.
	if time.Since(c.lastUserActivityLog) > userActivityLogDedup {
		c.lastUserActivityLog = time.Now()
		c.logger.Info("box ws: user-activity frame received", "frame", preview(raw, 400))
	}
	if time.Since(c.thumbExplained) < thumbExplainWindow {
		return // explained by a recent volume/preset/now-playing event
	}
	if c.thumbPending != nil {
		return // already waiting to fire
	}
	framePrev := preview(raw, 400) // captured for the fire log below
	c.thumbPending = time.AfterFunc(thumbSettle, func() {
		c.thumbMu.Lock()
		c.thumbPending = nil
		explained := time.Since(c.thumbExplained) < thumbExplainWindow
		debounced := !c.thumbLastFire.IsZero() && time.Since(c.thumbLastFire) < thumbDebounce
		if explained || debounced {
			c.thumbMu.Unlock()
			// A lone user-activity reached the settle timer but was then
			// suppressed. Both outcomes are otherwise invisible, which makes a
			// "the thumb key does nothing" report (#187) impossible to diagnose
			// from a bundle: we cannot tell a missing frame from a suppressed
			// one. Log it at INFO. This path only runs for activity that was NOT
			// already explained at arrival (volume ramps return earlier), so it
			// stays rare and does not churn the NAND log.
			switch {
			case explained:
				c.logger.Info("box ws: user-activity settled as explained, not firing thumb")
			default:
				c.logger.Info("box ws: user-activity debounced, thumb already fired recently")
			}
			return
		}
		c.thumbLastFire = time.Now()
		c.thumbMu.Unlock()
		c.logger.Info("box ws: lone user-activity -> thumb trigger", "frame", framePrev)
		if c.handler != nil {
			c.handler.OnThumbActivity(ctx)
		}
	})
}
