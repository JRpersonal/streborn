// Pandora stations were left out of Recently played, because no card could
// replay them (#1101). The speaker's own Pandora client plays them through
// /api/pandora/play now, so the agent records them and both the app and the
// phone page replay them. These tests pin the card fields and the replay call.
import { describe, it, expect, vi } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { srcOf } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
const phone = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8').replace(/\r\n/g, '\n');

const BOX = { host: '192.0.2.10', port: 17008 };

describe('a Pandora card in Recently played', () => {
  it('replays through the speaker with the stored station and type', async () => {
    const play = vi.fn().mockResolvedValue(undefined);
    // eslint-disable-next-line no-new-func
    const replay = new Function('PlayPandoraStation',
      `${srcOf('replayPandoraCard')}\nreturn replayPandoraCard;`)(play);
    await replay({ url: '111', name: 'Jazz Radio', itemType: 'stationurl' }, BOX);
    expect(play).toHaveBeenCalledWith('192.0.2.10', 17008, '111', 'Jazz Radio', 'stationurl');
  });

  it('keeps the item type when the history is grouped into cards', () => {
    // eslint-disable-next-line no-new-func
    const group = new Function(`${srcOf('groupRecentCards')}\nreturn groupRecentCards;`)();
    const cards = group([{ _boxKey: 'b', source: 'pandora', cardKey: 'pandora:111', cardName: 'Jazz Radio', cardURL: '111', itemType: 'stationurl', ts: '2026-10-17T08:00:00Z' }]);
    expect(cards[0].itemType).toBe('stationurl');
    expect(cards[0].url).toBe('111');
  });

  it('gets play and save-to-key, but no radio favourite', () => {
    const html = srcOf('recentCardHTML');
    expect(html).toContain("c.source !== 'pandora'");
    expect(html).toContain("c.source === 'pandora' ? !!c.url");
  });

  it('is replayable on the phone page too', () => {
    expect(phone).toContain("e.source === 'pandora' && e.cardURL");
    expect(phone).toContain('async function startPandora(st, btn)');
  });
});
