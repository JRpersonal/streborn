// Recently played showed the bare Spotify glyph on every Spotify card although
// the agent stores the playlist or album cover on it (#1077). The card now
// draws that cover like a Spotify preset key, glyph first and as the fallback.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { logoImg } from './views/recent.js';
import { SPOTIFY_LOGO } from './logos.js';
import { escapeAttr } from './utils.js';

describe('Spotify card cover', () => {
  const COVER = 'https://image-cdn.example/cover/ab67616d.jpg';
  it('hands the stored cover to the logo hydration, glyph first', () => {
    const tag = logoImg({ source: 'spotify', art: COVER });
    expect(tag).toContain('class="rc-logo"');
    expect(tag).toContain(`src="${escapeAttr(SPOTIFY_LOGO)}"`);
    expect(tag).toContain(`data-logo-fav="${COVER}"`);
  });
  it('keeps the plain glyph for a card without a cover', () => {
    const tag = logoImg({ source: 'spotify', art: '' });
    expect(tag).toContain(`src="${escapeAttr(SPOTIFY_LOGO)}"`);
    expect(tag).not.toContain('data-logo-fav');
  });
  it('the Spotify branch does not go back to the bare glyph', () => {
    const src = readFileSync(new URL('./views/recent.js', import.meta.url), 'utf8');
    expect(src).toContain("return spotifyCoverImgTag(c.art, 'rc-logo');");
    expect(src).not.toContain('src="$' + '{escapeAttr(SPOTIFY_LOGO)}"');
  });
});
