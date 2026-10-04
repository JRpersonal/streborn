package webui

import (
	"fmt"
	"time"
)

// debugSectionTimeout is how long the registered sections of /api/debug/state
// may take, together, before the state goes out without the ones still running.
//
// The sections ran one after another with no limit at all. Several of them ask
// the speaker's firmware, and the speaker a bundle has to describe is the one
// that is misbehaving: on a SoundTouch 10 stuck as half of an old stereo pair
// the whole debug state never arrived, the desktop's 20 s client gave up, and
// the bundle carried that speaker's version and nothing else (mail,
// 2026-10-04). That is the one bundle where the log mattered most.
const debugSectionTimeout = 4 * time.Second

// collectDebugSections runs every registered section in parallel and waits at
// most timeout for all of them together. A section that has not answered by
// then is reported as timed out, and one that panics is reported as such; the
// rest are returned as they are. A section still running is left to finish on
// its own: it cannot be cancelled, but it no longer holds up the answer.
func collectDebugSections(fns map[string]func() any, timeout time.Duration) map[string]any {
	type result struct {
		key string
		val any
	}
	ch := make(chan result, len(fns))
	for k, fn := range fns {
		go func(k string, fn func() any) {
			defer func() {
				if r := recover(); r != nil {
					ch <- result{k, fmt.Sprintf("ERR: provider panicked: %v", r)}
				}
			}()
			ch <- result{k, fn()}
		}(k, fn)
	}
	out := make(map[string]any, len(fns))
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for len(out) < len(fns) {
		select {
		case r := <-ch:
			out[r.key] = r.val
		case <-deadline.C:
			for k := range fns {
				if _, ok := out[k]; !ok {
					out[k] = fmt.Sprintf("ERR: section did not answer within %s", timeout)
				}
			}
			return out
		}
	}
	return out
}
