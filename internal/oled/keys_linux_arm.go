//go:build linux && arm

package oled

import (
	"fmt"
	"runtime"
	"syscall"
)

// traceKeys follows BoseApp's key thread (DevHelperThread) syscall by syscall
// for the length of a round and reports every remote key it logs, with no
// noticeable delay. The agent's normal key reader (logread -f, see
// internal/boxlog) trails a press by up to a second, which is fine for typing
// the code but not for playing.
//
// The thread is traced, never held: holding it rebooted the speaker after
// about 25 s (it also serves BoseApp's internal eventfds and a timer), while
// tracing it ran for many minutes without effect. It keeps running normally;
// at the entry of each sendmsg/write/send/sendto its syslog line
// "IR Key event: Key()=N, State()=S" is read from its memory. The line leaves
// through sendmsg (an iovec), measured live. quit ends the trace; the
// tracer's exit releases the thread in any case.
func traceKeys(quit <-chan struct{}, out chan<- KeyPress) error {
	pid := findProcess("BoseApp")
	if pid == 0 {
		return fmt.Errorf("BoseApp not running")
	}
	tid := findThread(pid, "DevHelperThread")
	if tid == 0 {
		return fmt.Errorf("key thread not found")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, _, e := syscall.Syscall6(syscall.SYS_PTRACE, ptraceSeize, uintptr(tid), 0, optTraceSysGood, 0, 0); e != 0 {
		return fmt.Errorf("seize: %w", e)
	}
	defer ptrace(ptraceDetach, tid)
	if err := ptrace(ptraceInterrupt, tid); err != nil {
		return fmt.Errorf("interrupt: %w", err)
	}
	if _, err := waitTID(tid); err != nil {
		return err
	}
	go func() {
		<-quit
		// A signal of our own ends any syscall the thread is blocked in; its
		// delivery stop is where the loop below lets go. Swallowed on detach.
		syscall.Tgkill(pid, tid, syscall.SIGWINCH)
	}()
	quitting := func() bool {
		select {
		case <-quit:
			return true
		default:
			return false
		}
	}
	peek := func(addr uint32, n int) []byte {
		if n <= 0 {
			return nil
		}
		n = min(n, 512)
		b := make([]byte, n)
		if _, err := syscall.PtracePeekData(tid, uintptr(addr), b); err != nil {
			return nil
		}
		return b
	}
	deliver := 0
	for {
		if err := syscall.PtraceSyscall(tid, deliver); err != nil {
			return fmt.Errorf("ptrace syscall: %w", err)
		}
		deliver = 0
		ws, err := waitTID(tid)
		if err != nil {
			return err
		}
		if ws.Exited() || ws.Signaled() {
			return fmt.Errorf("key thread gone")
		}
		if !ws.Stopped() {
			continue
		}
		sig := ws.StopSignal()
		if sig == syscall.SIGTRAP|0x80 {
			var r syscall.PtraceRegs
			if err := syscall.PtraceGetRegs(tid, &r); err != nil {
				return err
			}
			if r.Uregs[12] != 0 { // ip: 0 at syscall entry, 1 at exit
				continue
			}
			var msg []byte
			switch nr, a1, a2 := r.Uregs[7], r.Uregs[1], r.Uregs[2]; nr {
			case 4, 289, 290: // write, send, sendto
				msg = peek(a1, int(a2))
			case 296: // sendmsg: msghdr iov at +8, iovlen at +12
				if h := peek(a1, 16); h != nil {
					msg = gatherIov(peek, le32(h[8:]), le32(h[12:]))
				}
			}
			if k, ok := parseKeyLine(msg); ok {
				select {
				case out <- k:
				default:
				}
			}
			continue
		}
		if sig == syscall.SIGWINCH && quitting() {
			return nil
		}
		if uint32(ws)>>16 == 0 && sig != syscall.SIGTRAP {
			deliver = int(sig) // a real signal for BoseApp: pass it on
		}
	}
}

const optTraceSysGood = 1

func waitTID(tid int) (syscall.WaitStatus, error) {
	var ws syscall.WaitStatus
	_, err := syscall.Wait4(tid, &ws, waitAll, nil)
	return ws, err
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func gatherIov(peek func(uint32, int) []byte, iov, n uint32) []byte {
	if n == 0 || n > 8 {
		return nil
	}
	vec := peek(iov, int(8*n))
	if vec == nil {
		return nil
	}
	var out []byte
	for i := uint32(0); i < n; i++ {
		out = append(out, peek(le32(vec[8*i:]), int(le32(vec[8*i+4:])))...)
	}
	return out
}
