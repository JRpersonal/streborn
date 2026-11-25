// desktop-preset-address: a radio key names the address it plays when the
// pointer rests on its bitrate line (discussion #2103), in both layouts. The
// tile itself shows nothing new.

import { test, expect } from '../fixtures.mjs';
import { PRESETS } from '../../testkit/demo-data.mjs';

for (const ui2 of [false, true]) {
  test.describe(ui2 ? 'UI 2.0 layout' : 'classic layout', () => {
    test.use({ ui2 });

    test('the bitrate line of a radio key carries the station address as its title', async ({ app }) => {
      const { page } = app;
      const radio = PRESETS.find((p) => p.type !== 'spotify' && /^https?:\/\//.test(p.stream_url || ''));
      const tile = page.locator('#presets .preset', { hasText: radio.name }).first();
      await expect(tile.locator('.preset-bitrate')).toHaveAttribute('title', radio.stream_url);
    });
  });
}
