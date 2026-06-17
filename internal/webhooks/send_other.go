//go:build !linux

package webhooks

import "fmt"

// sendUDPPacket is a stub on non-Linux: the agent only runs on the speaker
// (Linux), but the package must still build on a developer host for tests/vet.
func sendUDPPacket(host string, port int, payload []byte) (int, error) {
	return 0, fmt.Errorf("udp/wol send is only supported on the speaker (linux)")
}
