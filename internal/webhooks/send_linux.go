//go:build linux

package webhooks

import (
	"context"
	"net"
	"strconv"
	"syscall"
	"time"
)

// sendUDPPacket sends payload to host:port over UDP with SO_BROADCAST enabled, so
// the one path serves both a unicast target (generic UDP action) and a
// Wake-on-LAN broadcast to 255.255.255.255 / a subnet broadcast (#187). The
// SO_BROADCAST option is harmless for unicast and required for broadcast on
// Linux, where a plain send to a broadcast address otherwise returns EACCES.
// Returns the number of bytes written.
func sendUDPPacket(host string, port int, payload []byte) (int, error) {
	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
			}); err != nil {
				return err
			}
			return serr
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	pc, err := lc.ListenPacket(ctx, "udp4", ":0")
	if err != nil {
		return 0, err
	}
	defer pc.Close()
	raddr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return 0, err
	}
	_ = pc.SetWriteDeadline(time.Now().Add(4 * time.Second))
	return pc.WriteTo(payload, raddr)
}
