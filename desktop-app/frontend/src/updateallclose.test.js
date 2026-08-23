// The Update-all progress window must always be dismissable.
//
// Closing it only hides the overlay; it does not cancel the run, which keeps
// going and still reports its outcome in the final toast. The button was
// nevertheless greyed out while any speaker's row had no final outcome, and a
// speaker that drops off the Wi-Fi mid-update never gets one. The window then
// could not be closed at all until the app was restarted. Reported 2026-08-23
// by an owner whose speakers were dropping off a congested network.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');

describe('the update-all window', () => {
  it('renders its Close button enabled', () => {
    const m = main.match(/<button[^>]*id="uaClose"[^>]*>/);
    expect(m, 'the uaClose button is gone').not.toBeNull();
    expect(m[0]).not.toContain('disabled');
  });

  it('never disables that button afterwards', () => {
    expect(main).not.toMatch(/uaClose'\)\.disabled\s*=/);
    expect(main).not.toMatch(/closeBtn\.disabled\s*=/);
  });

  it('still wires it to hide the overlay', () => {
    expect(main).toMatch(/uaClose'\)\.onclick/);
  });
});
