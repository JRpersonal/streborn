// Discussion #1101: a Pandora station held onto key 1 on a US speaker. The
// agent now keeps such stations as type "native" with the speaker's own item.
// These tests pin how the app shows and saves them: the service name as the
// badge, no stream-shaped extras, the key lit while the speaker plays it, and
// a hold-to-save that asks the speaker for the item instead of saving a URL.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  nativeServiceLabel,
  isNativeServicePreset,
  nativeServiceSaveable,
  nativeServiceBadge,
  nativeServiceActive,
} from './nativeservice.js';

const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');
const api = readFileSync(new URL('./api.js', import.meta.url), 'utf8');

const pandoraKey = {
  slot: 1, name: 'Little Big Town Radio', type: 'native', source: 'Pandora', stream_url: '',
  native: { source: 'PANDORA', location: '4071226281950183516', itemType: 'stationurl' },
};

describe('native service presets', () => {
  it('names the services STR keeps and nothing else', () => {
    expect(nativeServiceLabel('PANDORA')).toBe('Pandora');
    expect(nativeServiceLabel('iheart')).toBe('iHeartRadio');
    expect(nativeServiceLabel('IHEARTRADIO')).toBe('iHeartRadio');
    expect(nativeServiceLabel('DEEZER')).toBe('');
    expect(nativeServiceLabel(undefined)).toBe('');
    expect(nativeServiceSaveable('PANDORA')).toBe(true);
    expect(nativeServiceSaveable('LOCAL_INTERNET_RADIO')).toBe(false);
    expect(nativeServiceSaveable('SPOTIFY')).toBe(false);
  });

  it('recognises the key type and its badge', () => {
    expect(isNativeServicePreset(pandoraKey)).toBe(true);
    expect(isNativeServicePreset({ type: 'radio' })).toBe(false);
    expect(isNativeServicePreset(null)).toBe(false);
    expect(nativeServiceBadge(pandoraKey)).toBe('Pandora');
    expect(nativeServiceBadge({ ...pandoraKey, source: '' })).toBe('Pandora');
    expect(nativeServiceBadge({ type: 'radio', source: 'NAS' })).toBe('');
  });

  it('is lit exactly while the speaker plays its location', () => {
    expect(nativeServiceActive(pandoraKey, '4071226281950183516')).toBe(true);
    expect(nativeServiceActive(pandoraKey, 'http://127.0.0.1:8888/stream/1')).toBe(false);
    expect(nativeServiceActive(pandoraKey, '')).toBe(false);
    expect(nativeServiceActive({ type: 'radio', stream_url: 'x' }, 'x')).toBe(false);
  });
});

describe('the app wires native keys', () => {
  it('saves a hold while Pandora or iHeartRadio plays through SaveNativePreset, before any URL path', () => {
    const fn = main.slice(main.indexOf('async function saveCurrentToSlot'));
    const native = fn.indexOf('nativeServiceSaveable(state.nowSource)');
    const urlCase = fn.indexOf('savePresetCase(');
    expect(native).toBeGreaterThan(-1);
    expect(native).toBeLessThan(urlCase);
    expect(fn.slice(native, urlCase)).toContain('saveNativeToSlot(slot)');
    const helper = main.slice(main.indexOf('async function saveNativeToSlot'), main.indexOf('async function saveCurrentToSlot'));
    expect(helper).toContain('SaveNativePreset(');
    expect(helper).toContain('showPresetSaveError(err, slot)');
  });

  it('imports the binding as an optional one', () => {
    expect(api).toContain("callOptionalBinding('SaveNativePreset'");
    expect(main).toMatch(/\bSaveNativePreset,/);
  });

  it('shows no bitrate line and runs no directory logo lookup on a native key', () => {
    const tile = main.slice(main.indexOf('function renderPresets()'));
    expect(tile).toContain("isNativeServicePreset(p) ? '' : `<div class=\"preset-bitrate\">");
    const heal = main.slice(main.indexOf('async function healPresetLogos()'));
    expect(heal.slice(0, heal.indexOf('RadioSearch('))).toContain('!isNativeServicePreset(p)');
  });

  it('never flattens a native key with a metadata self-heal', () => {
    const guard = main.slice(main.indexOf('const flattenable ='));
    expect(guard.slice(0, 200)).toContain('isNativeServicePreset(live)');
  });
});
