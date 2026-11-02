import { describe, it, expect, vi } from 'vitest';
import { folderKeyRefused } from './views/library.js';

// One music-library folder, one key (#1695). The Library's "save folder to a
// key" gets the speaker's already-on-slot refusal when the folder already sits
// on another key, and answers it like a station's: the note naming that key and
// the offer to move the folder onto the key the user picked.
describe('folderKeyRefused', () => {
  it('turns the refusal into the move offer for the picked key', () => {
    const offer = vi.fn();
    const err = new Error('status 409: {"code":"already-on-slot","name":"Power Windows","slot":3}\n');
    expect(folderKeyRefused(err, 6, offer)).toBe(true);
    expect(offer).toHaveBeenCalledWith({ slot: 3, name: 'Power Windows' }, 6);
  });

  it('reads the refusal as the binding hands it over, a plain string', () => {
    const offer = vi.fn();
    expect(folderKeyRefused('status 409: {"code":"already-on-slot","name":"Rush \\"Live\\"","slot":2}', 4, offer)).toBe(true);
    expect(offer).toHaveBeenCalledWith({ slot: 2, name: 'Rush "Live"' }, 4);
  });

  it('leaves every other failure to the error it already shows', () => {
    const offer = vi.fn();
    expect(folderKeyRefused(new Error('status 422: {"code":"queue-empty"}'), 6, offer)).toBe(false);
    expect(folderKeyRefused('status 500: disk full', 6, offer)).toBe(false);
    expect(folderKeyRefused(null, 6, offer)).toBe(false);
    expect(offer).not.toHaveBeenCalled();
  });
});
