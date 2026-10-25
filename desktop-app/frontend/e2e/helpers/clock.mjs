// helpers/clock.mjs: driving the page clock (the fakeClock fixture option
// installs it before the app boots, at 2026-10-23T18:00:00Z).
//
// settle pauses the clock one second past "now" in the page, then runs ms of
// timers, so the app's start-up timers (the firewall check at +3 s, the app
// update check at +8 s, the 6 s recovery sweep, the first polls) have fired
// before the test starts counting. Call it after the ready marker, never
// before page.goto.
//
// Rule: under runFor, count only window.go bindings (mock.count), never agent
// page.route calls. Those are answered in Node, asynchronously to the page
// clock, so their number at the moment runFor returns is not deterministic.

export async function settle(page, ms) {
  await pauseSoon(page);
  await page.clock.runFor(ms);
}

// pauseSoon pauses the clock one second past the page's "now". Reading now
// and pausing are two round trips, and the installed clock keeps running in
// real time in between, so on a loaded machine (CI, a parallel run) the target
// can already be in the past and pauseAt throws "Cannot fast-forward to the
// past". Read a fresh now and try again in that case.
async function pauseSoon(page) {
  for (let i = 0; ; i++) {
    const now = await page.evaluate(() => Date.now());
    try {
      await page.clock.pauseAt(now + 1000);
      return;
    } catch (e) {
      if (i >= 4 || !/to the past/i.test(String(e))) throw e;
    }
  }
}

export function runFor(page, ms) {
  return page.clock.runFor(ms);
}
