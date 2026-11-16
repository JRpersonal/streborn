// The restart pre-flight of both update runners (preflight.js mayStayDark): a
// speaker is flagged only when the Go side says the old agent's restart is
// likely and cannot be routed around (BoxRestartPlan mode "warn").
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { readFileSync } from 'node:fs';

const api = vi.hoisted(() => ({ BoxRestartPlan: vi.fn() }));
vi.mock('../api.js', async (importOriginal) => ({ ...(await importOriginal()), ...api }));

import { mayStayDark } from './preflight.js';

const BOX = { host: '192.0.2.20', port: 8888 };

describe('mayStayDark', () => {
  beforeEach(() => { api.BoxRestartPlan.mockReset(); });

  it('is true only for the warn plan', async () => {
    api.BoxRestartPlan.mockResolvedValue({ mode: 'warn', chassis: 'unknown', agentVersion: 'v1.2.1' });
    expect(await mayStayDark(BOX)).toBe(true);
    expect(api.BoxRestartPlan).toHaveBeenCalledWith('192.0.2.20', 8888);
  });

  it('is false where the app routes around the restart, or nothing needs it', async () => {
    for (const mode of ['agent-restart', 'reboot', '', undefined]) {
      api.BoxRestartPlan.mockResolvedValue({ mode });
      expect(await mayStayDark(BOX), String(mode)).toBe(false);
    }
    api.BoxRestartPlan.mockResolvedValue(null);
    expect(await mayStayDark(BOX)).toBe(false);
  });

  it('throws when the speaker cannot be asked, which both runners swallow', async () => {
    api.BoxRestartPlan.mockRejectedValue(new Error('timeout'));
    await expect(mayStayDark(BOX)).rejects.toThrow('timeout');
  });
});

describe('both runners ask, and never block', () => {
  const src = (p) => readFileSync(new URL(p, import.meta.url), 'utf8').replace(/\r\n/g, '\n');

  it('the single update asks in a confirmation the owner can pass', () => {
    const single = src('./single.js');
    const at = single.indexOf('if (await mayStayDark(targetBox)) {');
    expect(at).toBeGreaterThan(0);
    const block = single.slice(at, at + 400);
    expect(block).toContain("confirmWarn(t('update.darkRiskTitle'), t('update.darkRiskBody', { name: getBoxLabel(targetBox) }))");
    // Inside a try whose catch lets the update go on.
    expect(single.slice(at, at + 600)).toContain('} catch { /* plan unknown: do not block the update */ }');
    // Before the run starts.
    expect(at).toBeLessThan(single.indexOf('await RunSpeakerUpdate('));
  });

  it('Update all collects a note for its one confirmation', () => {
    const all = src('./all.js');
    const at = all.indexOf('if (await mayStayDark(b)) notes.push(t(\'updateAll.noteDarkRisk\', { name: getBoxLabel(b) }));');
    expect(at).toBeGreaterThan(0);
    expect(all.slice(at, at + 200)).toContain('} catch { /* plan unknown: never block a batch */ }');
    expect(at).toBeLessThan(all.indexOf('const confirmed = await confirmWarn(confirmTitle, body'));
  });
});
