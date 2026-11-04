package boxlog

import (
	"fmt"
	"testing"
	"time"
)

// The line shapes of the #830 field bundle (ST10 rhino, FW 27.0.6,
// 2026-11-03): the speaker's own resume of a directly played file, which the
// listener heard play on while the UPnP account refused every status. Host and
// pids are placeholders.
func staleRunLine(stamp time.Time, state string) string {
	return stamp.Format("Jan _2 15:04:05") + ` rhino local0.info STSCertified[1990]: [(002300):UpnpState:INFO]CommonHandleAudioStatus: Audio Path state '` + state + ` error #0 for stale UrlID 1`
}

func staleMismatchLine(stamp time.Time) string {
	return stamp.Format("Jan _2 15:04:05") + ` rhino local0.info STSCertified[1990]: [(002300):STS:INFO]STSIPC(QPLAY, UPnPUserName)::IsCurrentUrlId(urlId:1) - urlId mismatch. m_urlId:2`
}

func stallLine(stamp time.Time) string {
	return stamp.Format("Jan _2 15:04:05") + ` rhino local0.info APServer[1877]: [(002107):RBDecoded:INFO]ReadyToWrite Interrupted, bufferDepth: 385618, capSize: 384000, totalSize: 1572864, minBufferDepth: 1.500000, need: 4608`
}

func offsetLine(stamp time.Time, sec int) string {
	return stamp.Format("Jan _2 15:04:05") + fmt.Sprintf(` rhino local0.info STSCertified[1990]: [(002300):UpnpAccount:INFO]HandleAudioStatus: Audio path offset %d`, sec)
}

// TestStalePlayRunFollowsTheBundlesResume replays the window of the bundle:
// the pause's buffer stalls, the re-open, then one stale-id PLAYING status a
// second. The run starts at the first PLAYING line, counts every one, and the
// stalls before it do not mark it.
func TestStalePlayRunFollowsTheBundlesResume(t *testing.T) {
	r := &Reader{forensics: newForensics()}
	if _, _, n, _ := r.StalePlayRun(); n != 0 {
		t.Fatalf("no line read yet, run has %d lines", n)
	}
	pause := time.Date(2026, 11, 3, 20, 55, 38, 400*int(time.Millisecond), time.Local)
	r.forensics.observe(stallLine(pause), pause)
	r.forensics.observe(stallLine(pause), pause.Add(time.Second))
	resume := pause.Add(2*time.Second + 29*time.Millisecond)
	r.forensics.observe(lineSelect, resume)
	r.forensics.observe(staleMismatchLine(resume), resume)
	for i := 0; i < 13; i++ {
		at := resume.Add(time.Duration(i) * time.Second)
		r.forensics.observe(staleMismatchLine(at), at)
		r.forensics.observe(staleRunLine(at, "PLAYING"), at)
	}
	first, last, n, stalled := r.StalePlayRun()
	if n != 13 || !first.Equal(resume) || !last.Equal(resume.Add(12*time.Second)) || stalled {
		t.Fatalf("run = %d lines %v..%v stalled=%v, want 13 lines %v..%v unstalled",
			n, first, last, stalled, resume, resume.Add(12*time.Second))
	}
	// A buffer stall while the run goes on marks it.
	r.forensics.observe(stallLine(last), last.Add(200*time.Millisecond))
	if _, _, _, stalled := r.StalePlayRun(); !stalled {
		t.Fatal("a buffer stall during the run did not mark it")
	}
	// The restart's Stop ends it.
	r.forensics.observe(staleRunLine(last, "STOPPED"), last.Add(time.Second))
	if _, _, n, _ := r.StalePlayRun(); n != 0 {
		t.Fatalf("a STOPPED status did not end the run (%d lines)", n)
	}
	var nilReader *Reader
	if _, _, n, _ := nilReader.StalePlayRun(); n != 0 {
		t.Fatal("nil reader must report no run")
	}
}

// TestStalePlayRunEndsAndRestarts: an offset line (the account counts again)
// ends a run, a gap longer than stalePlayRunGap starts a new one, and a line
// logread replays from the ring never counts.
func TestStalePlayRunEndsAndRestarts(t *testing.T) {
	r := &Reader{forensics: newForensics()}
	t0 := time.Date(2026, 11, 3, 20, 56, 13, 0, time.Local)
	for i := 0; i < 3; i++ {
		at := t0.Add(time.Duration(i) * time.Second)
		r.forensics.observe(staleRunLine(at, "PLAYING"), at)
	}
	r.forensics.observe(offsetLine(t0.Add(3*time.Second), 876), t0.Add(3*time.Second))
	if _, _, n, _ := r.StalePlayRun(); n != 0 {
		t.Fatalf("an offset line did not end the run (%d lines)", n)
	}

	r.forensics.observe(staleRunLine(t0.Add(4*time.Second), "PLAYING"), t0.Add(4*time.Second))
	later := t0.Add(4*time.Second + stalePlayRunGap + time.Second)
	r.forensics.observe(staleRunLine(later, "PLAYING"), later)
	if first, _, n, _ := r.StalePlayRun(); n != 1 || !first.Equal(later) {
		t.Fatalf("after a gap the run = %d lines from %v, want a new run of 1 from %v", n, first, later)
	}

	// Replayed from the ring at a logread restart: stamped a minute back.
	fresh := &Reader{forensics: newForensics()}
	restart := t0.Add(time.Minute)
	fresh.forensics.observe(staleRunLine(t0, "PLAYING"), restart)
	fresh.forensics.observe(staleRunLine(t0.Add(time.Second), "PLAYING"), restart)
	if _, _, n, _ := fresh.StalePlayRun(); n != 0 {
		t.Fatalf("replayed ring lines became a run of %d", n)
	}
}
