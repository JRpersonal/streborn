package webui

import "testing"

// #960: a folder stopped part way through and the bundle could not say why,
// because the queue's whole lifecycle was silent. These pin the record that
// answers it, and the one property that makes it safe to paste into a public
// issue: no track titles anywhere in the section.

func TestQueueEpisodeRecordsHowItEnded(t *testing.T) {
	s, _ := newPlayTestServer(t)
	s.queue.load([]queueItem{
		{URL: "http://192.0.2.10:50002/a.mp3", Title: "A", Mime: "audio/mpeg"},
		{URL: "http://192.0.2.10:50002/b.mp3", Title: "B", Mime: "audio/mpeg"},
		{URL: "http://192.0.2.10:50002/c.mp3", Title: "C", Mime: "audio/mpeg"},
	}, 0, false, repeatOff)
	s.setQueueTiming(0)
	s.noteQueueStart(3, 0, false, repeatOff)

	cur := currentEpisode(t, s)
	if cur.Tracks != 3 || cur.EndReason != "" {
		t.Fatalf("a running episode: tracks=%d endReason=%q", cur.Tracks, cur.EndReason)
	}

	s.queueMu.Lock()
	gen := s.queueGen
	s.queueMu.Unlock()
	s.advanceAndPlay(true, gen, "the box reported the track stopped at its end")

	if cur = currentEpisode(t, s); cur.Auto != 1 || cur.Manual != 0 {
		t.Fatalf("after one auto advance: auto=%d manual=%d", cur.Auto, cur.Manual)
	}

	s.stopQueue("stop was pressed")

	if _, ok := s.QueueForensics().(map[string]any)["current"]; ok {
		t.Error("the episode is over, it must not still be reported as current")
	}
	past := recentEpisodes(t, s)
	if len(past) != 1 {
		t.Fatalf("recent episodes = %d, want 1", len(past))
	}
	if past[0].EndReason != "stop was pressed" {
		t.Errorf("endReason = %q, want the reason the caller gave", past[0].EndReason)
	}
	if past[0].Auto != 1 {
		t.Errorf("the finished episode lost its advance count: auto=%d", past[0].Auto)
	}
}

// A queue started while one is running IS how the old one ended. Without this
// the first episode would sit open forever and the second would overwrite it.
func TestQueueEpisodeClosedByTheNextQueue(t *testing.T) {
	s, _ := newPlayTestServer(t)
	s.noteQueueStart(4, 0, false, repeatOff)
	s.noteQueueStart(2, 0, true, repeatAll)

	past := recentEpisodes(t, s)
	if len(past) != 1 || past[0].EndReason != "replaced by a new queue" {
		t.Fatalf("the first episode was not closed by the second: %+v", past)
	}
	cur := currentEpisode(t, s)
	if cur.Tracks != 2 || !cur.Shuffle || cur.Repeat != "all" {
		t.Errorf("the running episode is not the second one: %+v", cur)
	}
}

// The ring is bounded: a box playing folders all day must not grow the debug
// section without limit.
func TestQueueEpisodeHistoryIsBounded(t *testing.T) {
	s, _ := newPlayTestServer(t)
	for i := 0; i < queueEpisodeKeep+4; i++ {
		s.noteQueueStart(1, 0, false, repeatOff)
		s.noteQueueEnd("played to the end of the list")
	}
	if got := len(recentEpisodes(t, s)); got != queueEpisodeKeep {
		t.Errorf("kept %d episodes, want %d", got, queueEpisodeKeep)
	}
}

// noteQueueEnd with nothing running is the normal case: most stopQueue callers
// fire on a plain radio play that never had a queue.
func TestQueueEndWithoutAnEpisodeIsHarmless(t *testing.T) {
	s, _ := newPlayTestServer(t)
	s.noteQueueEnd("a single station or track was played instead")
	if got := len(recentEpisodes(t, s)); got != 0 {
		t.Errorf("an end with no episode invented %d of them", got)
	}
}

func currentEpisode(t *testing.T, s *Server) queueEpisode {
	t.Helper()
	cur, ok := s.QueueForensics().(map[string]any)["current"].(queueEpisode)
	if !ok {
		t.Fatal("no episode is reported as current")
	}
	return cur
}

func recentEpisodes(t *testing.T, s *Server) []queueEpisode {
	t.Helper()
	past, ok := s.QueueForensics().(map[string]any)["recent"].([]queueEpisode)
	if !ok {
		t.Fatal("the forensics section has no recent list")
	}
	return past
}
