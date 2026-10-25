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
  const now = await page.evaluate(() => Date.now());
  await page.clock.pauseAt(now + 1000);
  await page.clock.runFor(ms);
}

export function runFor(page, ms) {
  return page.clock.runFor(ms);
}
