//go:build linux

package oled

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	ptraceSeize     = 0x4206
	ptraceInterrupt = 0x4207
	ptraceDetach    = 17
	waitAll         = 0x40000000 // __WALL: the flip task is a thread, not a child

	flipThread = "DirectFBFlipTas"
	fbDevice   = "/dev/fb0"
	fbSysfs    = "/sys/class/graphics/fb0/"
	frameRate  = 30
)

// panelSupported recognises the SoundTouch panel (ST20, ST30, Portable) by
// driver and geometry. Every other display is left alone, and so is the
// Wave, whose firmware reports the same framebuffer for a segment display
// that cannot show it (model.go).
func panelSupported() bool {
	if PhantomPanel() {
		return false
	}
	read := func(name string) string {
		b, _ := os.ReadFile(fbSysfs + name)
		return strings.TrimSpace(string(b))
	}
	return strings.Contains(read("name"), "ssdspi") &&
		read("virtual_size") == fmt.Sprintf("%d,%d", Width, Height) &&
		read("bits_per_pixel") == "8"
}

func boseAppDisplayReady() bool {
	pid := findProcess("BoseApp")
	return pid != 0 && findThread(pid, flipThread) != 0
}

// boseAppAge is how long BoseApp has been running, from its start time in
// /proc/<pid>/stat against /proc/uptime; zero when it is not running or
// either read fails. The kernel on every SoundTouch chassis ticks at 100 Hz.
func boseAppAge() time.Duration {
	pid := findProcess("BoseApp")
	if pid == 0 {
		return 0
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	up, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	return processAge(string(stat), string(up), 100)
}

// processAge computes a process age from the stat and uptime texts; the
// start time is the 22nd stat field, in clock ticks since boot.
func processAge(stat, uptime string, hz int) time.Duration {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 || hz <= 0 {
		return 0
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 20 {
		return 0
	}
	var startTicks float64
	if _, err := fmt.Sscan(fields[19], &startTicks); err != nil {
		return 0
	}
	var upSec float64
	if _, err := fmt.Sscan(strings.Fields(uptime)[0], &upSec); err != nil {
		return 0
	}
	age := upSec - startTicks/float64(hz)
	if age <= 0 {
		return 0
	}
	return time.Duration(age * float64(time.Second))
}

// ProcessCPUms is the CPU time (user plus system) the named process has used
// since it started, in milliseconds; -1 when it is not running. For the
// display_cost debug section: the panel hold stops BoseApp's flip thread, and
// whether BoseApp spends more or less while it is held is the question.
func ProcessCPUms(name string) int64 {
	pid := findProcess(name)
	if pid == 0 {
		return -1
	}
	return statCPUms(fmt.Sprintf("/proc/%d/stat", pid))
}

// SelfCPUms is the agent's own CPU time in milliseconds.
func SelfCPUms() int64 { return statCPUms("/proc/self/stat") }

// statCPUms reads utime + stime from a /proc stat file (USER_HZ 100).
func statCPUms(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	// the command name is in parentheses and may hold spaces
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return -1
	}
	f := strings.Fields(s[i+1:])
	// after the name: state is field 3, utime 14, stime 15 (1-based)
	if len(f) < 13 {
		return -1
	}
	var ut, st int64
	if _, err := fmt.Sscan(f[11], &ut); err != nil {
		return -1
	}
	if _, err := fmt.Sscan(f[12], &st); err != nil {
		return -1
	}
	return (ut + st) * 10
}

func findProcess(name string) int {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		var pid int
		if _, err := fmt.Sscan(e.Name(), &pid); err != nil {
			continue
		}
		if b, err := os.ReadFile("/proc/" + e.Name() + "/comm"); err == nil && strings.TrimSpace(string(b)) == name {
			return pid
		}
	}
	return 0
}

func findThread(pid int, comm string) int {
	dir := fmt.Sprintf("/proc/%d/task", pid)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if b, err := os.ReadFile(dir + "/" + e.Name() + "/comm"); err == nil && strings.TrimSpace(string(b)) == comm {
			var tid int
			fmt.Sscan(e.Name(), &tid)
			return tid
		}
	}
	return 0
}

func ptrace(req, tid int) error {
	if _, _, e := syscall.Syscall6(syscall.SYS_PTRACE, uintptr(req), uintptr(tid), 0, 0, 0, 0); e != 0 {
		return e
	}
	return nil
}

// panelHold is the panel taken from BoseApp: its flip thread held and
// /dev/fb0 mapped. Every ptrace request must come from the thread that
// attached, so takePanel and release run on a goroutine locked to its OS
// thread.
type panelHold struct {
	mem   []byte
	out   []byte // the frame last written, as panel bytes
	tid   int
	shown bool // a first frame was written whole
}

// takePanel holds BoseApp's flip thread (PTRACE_SEIZE + PTRACE_INTERRUPT, no
// signal, every other BoseApp thread keeps running) and maps the
// framebuffer. The caller must have called runtime.LockOSThread and must call
// release on success.
func takePanel() (*panelHold, error) {
	pid := findProcess("BoseApp")
	if pid == 0 {
		return nil, errors.New("BoseApp not running")
	}
	tid := findThread(pid, flipThread)
	if tid == 0 {
		return nil, errors.New("flip thread not found")
	}
	f, err := os.OpenFile(fbDevice, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	mem, err := syscall.Mmap(int(f.Fd()), 0, FrameSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	// The mapping stays valid without the descriptor, so close it right away
	// and with its error checked: nothing is written through the file itself.
	if cerr := f.Close(); err == nil && cerr != nil {
		_ = syscall.Munmap(mem)
		return nil, fmt.Errorf("close %s: %w", fbDevice, cerr)
	}
	if err != nil {
		return nil, fmt.Errorf("mmap: %w", err)
	}
	if err := ptrace(ptraceSeize, tid); err != nil {
		_ = syscall.Munmap(mem)
		return nil, fmt.Errorf("seize: %w", err)
	}
	fail := func(err error) (*panelHold, error) {
		_ = ptrace(ptraceDetach, tid)
		_ = syscall.Munmap(mem)
		return nil, err
	}
	if err := ptrace(ptraceInterrupt, tid); err != nil {
		return fail(fmt.Errorf("interrupt: %w", err))
	}
	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(tid, &ws, waitAll, nil); err != nil {
		return fail(fmt.Errorf("wait: %w", err))
	}
	return &panelHold{mem: mem, out: make([]byte, FrameSize), tid: tid}, nil
}

// show puts a frame of grey levels 0..15 on the panel. Only the rows that
// changed since the last frame are written to the framebuffer (the first
// frame is written whole, over BoseApp's picture): a scrolling line or the
// playing symbol touches a strip of the panel, not all of it. The flip
// thread is held, so nothing else writes there in between.
func (p *panelHold) show(buf []byte) {
	for y := 0; y < Height; y++ {
		row, out := buf[y*Width:(y+1)*Width], p.out[y*Width:(y+1)*Width]
		dirty := !p.shown
		for i, v := range row {
			if b := v * 0x11; out[i] != b {
				out[i] = b
				dirty = true
			}
		}
		if dirty {
			copy(p.mem[y*Width:(y+1)*Width], out)
		}
	}
	p.shown = true
}

// release leaves a black frame (BoseApp repaints its own screen right after
// the detach), lets the flip thread go and unmaps the framebuffer.
func (p *panelHold) release() {
	clear(p.mem)
	_ = ptrace(ptraceDetach, p.tid)
	_ = syscall.Munmap(p.mem)
}

// play holds BoseApp's flip thread, renders frames until frame returns false,
// and always releases the thread again. quit (may be nil) asks for the exit:
// onQuit (may be nil) is told the time so the animation can wind down,
// otherwise the loop stops at once. After maxDur the same exit is forced, and
// a few seconds later the loop ends regardless.
func play(frame func(t float64, buf []byte) bool, onQuit func(t float64), maxDur time.Duration, quit <-chan struct{}) error {
	// The OS thread stays locked to this goroutine until after the detach.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	p, err := takePanel()
	if err != nil {
		return err
	}
	defer p.release()

	buf := make([]byte, FrameSize)
	start := time.Now()
	every := time.Second / frameRate
	quitting := false
	for {
		t := time.Since(start)
		if !quitting {
			asked := false
			select {
			case <-quit:
				asked = true
			default:
			}
			if asked || t > maxDur {
				quitting = true
				if onQuit == nil {
					break
				}
				onQuit(t.Seconds())
			}
		}
		if quitting && t > maxDur+5*time.Second {
			break
		}
		more := frame(t.Seconds(), buf)
		p.show(buf)
		if !more {
			break
		}
		time.Sleep(every - time.Since(start)%every)
	}
	return nil
}

// hold takes the panel for as long as quit stays open and draws only when
// asked: draw runs once at the start, again on every redraw signal, and when
// the wait next returns after a draw runs out (next nil or <= 0: no timed
// redraw). Nothing is drawn or copied in between, so a held panel whose
// content stands costs no CPU. The flip thread is always released on return,
// and the kernel releases it on its own if the agent dies.
func hold(draw func(buf []byte), redraw, quit <-chan struct{}, next func() time.Duration) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	p, err := takePanel()
	if err != nil {
		return err
	}
	defer p.release()
	buf := make([]byte, FrameSize)
	for {
		draw(buf)
		p.show(buf)
		var tc <-chan time.Time
		if next != nil {
			if d := next(); d > 0 {
				tc = time.After(d)
			}
		}
		select {
		case <-quit:
			return nil
		case <-redraw:
		case <-tc:
		}
	}
}
