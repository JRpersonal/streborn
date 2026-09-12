// The SEND half of the box WebSocket, and the one write that needs it.
//
// boxws was a pure listener until 2026-09-10: it consumed the gabbo stream and
// the only bytes it ever wrote were keepalive pings. That was not an oversight,
// it was the whole design. STR writes to the speaker over the plain HTTP API on
// :8090, which is documented, easy to reason about, and answers with a status.
//
// The stereo BALANCE of a pair is the one setting that API will not take.
// Measured live on two SoundTouch 10s (2026-08-04): the read works, and every
// POST /balance hung the endpoint until the speaker was woken again, including
// the exact body the widely-copied community reference sends. STR has shown the
// balance read-only ever since, and the comment on the read handler said the
// firmware accepted no write that sticks.
//
// That was one interface short of the truth. On 2026-09-09 gesellix reported on
// #70, and then in gesellix/Bose-SoundTouch#699 with the source of Stockholm
// (the web app Bose ships ON the speaker), that Bose's own client never writes
// balance over HTTP either. It sends it here, on this socket, and only here:
//
//	function y(e,t){ d({uID:e, path:"balance", rootNode:"balanceSet",
//	                    obj:{VALUE:t.toString()}}) }   // setBalance
//
// So the negative result was about HTTP, not about the firmware, and the reason
// nobody had tried the socket is in the first paragraph: there was no code path
// from which a write could be attempted. This file is that path.
package boxws

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxwrites"
	"github.com/gorilla/websocket"
)

// ErrNotConnected is returned when a send is attempted with no live socket:
// before Run started, or in the gap between two reconnects. Callers surface it
// as "not right now" rather than as a failure of the write itself.
var ErrNotConnected = errors.New("box websocket not connected")

// wsSendTimeout bounds one application write. Short on purpose: a send is
// always driven by somebody waiting in the app, and a half-dead socket must not
// hold that request open for the keepalive's much longer budget.
const wsSendTimeout = 5 * time.Second

func (c *Client) setSendConn(conn *websocket.Conn) {
	c.sendMu.Lock()
	c.sendConn = conn
	c.sendMu.Unlock()
}

// Send writes one raw gabbo message on the live socket.
//
// Exported so a spike can drive an arbitrary envelope against a real speaker
// without a build of its own. Everything STR itself sends goes through a named
// method like SetBalance, so the envelope stays in one place next to the
// evidence for it.
func (c *Client) Send(payload string) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	conn := c.sendConn
	if conn == nil {
		return ErrNotConnected
	}
	if err := conn.SetWriteDeadline(time.Now().Add(wsSendTimeout)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, []byte(payload))
}

// nextReqID hands out the request number for the next frame. Separate lock
// acquisition from Send on purpose: building the frame and writing it are two
// steps, and nesting them under one lock would deadlock.
func (c *Client) nextReqID() uint64 {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.reqID++
	return c.reqID
}

// BalanceFrame builds the balance-write envelope, in the exact shape Stockholm
// sends it (gesellix/Bose-SoundTouch#699):
//
//	<msg><header deviceID="{master}" url="balance" method="POST">
//	  <request requestID="{n}"><info mainNode="balanceSet" type="new"/></request>
//	</header><body><balance><targetBalance>-3</targetBalance></balance></body></msg>
//
// Separated from SetBalance so a test can assert the bytes without a socket.
func BalanceFrame(deviceID string, target int, reqID uint64) string {
	return fmt.Sprintf(
		`<msg><header deviceID="%s" url="balance" method="POST">`+
			`<request requestID="%d"><info mainNode="balanceSet" type="new"/></request>`+
			`</header><body><balance><targetBalance>%d</targetBalance></balance></body></msg>`,
		xmlAttr(deviceID), reqID, target)
}

// SetBalance moves the left/right balance of this speaker's stereo pair.
//
// deviceID is the SoundTouch deviceID the frame is addressed to: the firmware
// keys on the ID rather than on the socket the frame arrived on, so it has to be
// filled in even though the agent is talking to its own speaker. In practice it
// is the id of the speaker this agent runs on, resolved from its own /info.
//
// Either member of a pair can be addressed. Measured 2026-09-10 on a live pair:
// both report the same balance document and a write to either one moves the pair,
// which both then report. STR addresses the master anyway, so that reading and
// writing are one behaviour rather than two.
//
// target is written through unclamped: the CALLER clamps against the Min/Max
// the speaker itself reports. They are not the -50..+50 the community reference
// assumes; two SoundTouch 10s answer -7..+7, and gesellix has since confirmed
// that his own repo's range was the invented one.
//
// Nothing is awaited here. The firmware answers with a RESPONSE frame and then
// broadcasts balanceUpdated, and neither is correlated to the request: a send
// that returns nil means the bytes left, not that the balance moved. The one
// piece of evidence that it took is reading the balance back, which is the
// caller's job (see webui.writeBoxBalance).
func (c *Client) SetBalance(deviceID string, target int) error {
	dev := strings.TrimSpace(deviceID)
	if dev == "" {
		return errors.New("balance: no deviceID to address the frame to")
	}
	if err := c.Send(BalanceFrame(dev, target, c.nextReqID())); err != nil {
		return err
	}
	// Ledger it like every other write against the speaker's own firmware. The
	// source is unknown from here (boxws does not track it), which the ledger
	// records as "unknown" so the gap is visible rather than guessed at.
	boxwrites.Note("setbalance", "")
	// Debug, not Info. The webui side logs the OUTCOME at Info ("balance: set",
	// or the read-back miss), which is the line worth keeping, and a slider
	// releases often enough that two Info lines per move is NAND churn on a
	// 32 KB ring shared with every other subsystem.
	c.logger.Debug("box ws: balance write sent", "target", target, "deviceID", dev)
	return nil
}

// xmlAttr escapes a value for use inside a double-quoted XML attribute. A
// SoundTouch deviceID is a MAC in hex and needs none of this; it is here so a
// caller that ever passes something else cannot break the frame.
func xmlAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
