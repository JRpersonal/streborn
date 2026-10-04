import { describe, it, expect, vi } from 'vitest';
import { maybeShowStableNameNotice } from './stablenamenotice.js';

const t = (k) => k;

describe('maybeShowStableNameNotice', () => {
  it('shows the note when the update renamed the app', async () => {
    const showToast = vi.fn();
    const shown = await maybeShowStableNameNotice({ consume: async () => true, showToast, t });
    expect(shown).toBe(true);
    expect(showToast).toHaveBeenCalledWith('update.stableNameNotice', 20000);
  });

  it('stays silent without a rename', async () => {
    const showToast = vi.fn();
    expect(await maybeShowStableNameNotice({ consume: async () => false, showToast, t })).toBe(false);
    expect(showToast).not.toHaveBeenCalled();
  });

  it('stays silent when the backend call fails', async () => {
    const showToast = vi.fn();
    const consume = async () => { throw new Error('no binding'); };
    expect(await maybeShowStableNameNotice({ consume, showToast, t })).toBe(false);
    expect(showToast).not.toHaveBeenCalled();
  });
});
