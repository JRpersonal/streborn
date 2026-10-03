// The banner's "Remove leftovers" button only switched to the settings page, so
// an owner pressed it twice and nothing happened (#1083). Both buttons now run
// one flow; these tests drive it with fakes.
import { describe, it, expect, vi } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { runConflictCleanup } from './conflictcleanup.js';

const here = dirname(fileURLToPath(import.meta.url));
const main = readFileSync(join(here, 'main.js'), 'utf8');
const settings = readFileSync(join(here, 'views', 'settings.js'), 'utf8');

const box = { host: '192.0.2.1', port: 8888, friendlyName: 'Office', conflictingMod: 'OpenCloudTouch' };

function fakes({ confirm = [true, false], reply = { removed: ['a', 'b'] } } = {}) {
  const answers = [...confirm];
  return {
    t: (k) => k,
    confirmWarn: vi.fn(async () => answers.shift()),
    showToast: vi.fn(),
    showError: vi.fn(),
    removeConflictingMod: vi.fn(async () => JSON.stringify(reply)),
    rebootBox: vi.fn(async () => {}),
    rediscover: vi.fn(),
    setBusy: vi.fn(),
  };
}

describe('runConflictCleanup', () => {
  it('removes the leftovers after the confirmation and reports the count', async () => {
    const d = fakes();
    expect(await runConflictCleanup(box, d)).toBe(true);
    expect(d.removeConflictingMod).toHaveBeenCalledWith('192.0.2.1', 8888);
    expect(d.showToast).toHaveBeenCalledWith('settingsView.removeConflictDoneToast');
    expect(d.showError).not.toHaveBeenCalled();
    expect(d.setBusy.mock.calls).toEqual([[true], [false]]);
    // Restart declined: the list is refreshed right away.
    expect(d.rebootBox).not.toHaveBeenCalled();
    expect(d.rediscover).toHaveBeenCalledWith(0);
  });

  it('does nothing at all when the owner cancels', async () => {
    const d = fakes({ confirm: [false] });
    expect(await runConflictCleanup(box, d)).toBe(false);
    expect(d.removeConflictingMod).not.toHaveBeenCalled();
    expect(d.setBusy).not.toHaveBeenCalled();
  });

  it('restarts the speaker when asked and refreshes once it is back', async () => {
    const d = fakes({ confirm: [true, true] });
    await runConflictCleanup(box, d);
    expect(d.rebootBox).toHaveBeenCalledWith('192.0.2.1', 8888);
    expect(d.rediscover).toHaveBeenCalledWith(35000);
  });

  it('reports a cleanup that removed nothing as a problem', async () => {
    const d = fakes({ reply: { removed: [] } });
    await runConflictCleanup(box, d);
    expect(d.showError).toHaveBeenCalled();
    expect(d.showToast).not.toHaveBeenCalledWith('settingsView.removeConflictDoneToast');
  });

  it('shows the error and releases the button when the speaker refuses', async () => {
    const d = fakes();
    d.removeConflictingMod = vi.fn(async () => { throw new Error('boom'); });
    expect(await runConflictCleanup(box, d)).toBe(false);
    expect(d.showError).toHaveBeenCalled();
    expect(d.setBusy.mock.calls.at(-1)).toEqual([false]);
  });
});

describe('both buttons run the same flow', () => {
  it('the banner button removes, it does not just switch pages', () => {
    const wiring = main.slice(main.indexOf("const cb = $('boxIssueConflictBtn')"), main.indexOf("const d = $('boxIssueDismissBtn')"));
    expect(wiring).toContain('runConflictCleanup(conflict[0]');
    expect(wiring).not.toContain("switchView('settings')");
  });

  it('the settings button uses it too', () => {
    expect(settings).toContain('runConflictCleanup(box,');
  });
});
