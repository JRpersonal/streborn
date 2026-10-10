// The Wishes tab lists every open feature wish with its votes and links each
// one to its GitHub discussion, where the vote happens. It replaced the
// "Podcasts (planned)" placeholder tab.
import { describe, it, expect, vi } from 'vitest';

vi.mock('./api.js', () => ({ BrowserOpenURL: () => {}, FeatureWishes: async () => null }));

const { wishesHTML } = await import('./views/wishes.js');

const tr = (k, p) => (p ? `${k}:${JSON.stringify(p)}` : k);
const LIST = {
  ok: true,
  updatedAt: '2026-10-10T13:56:34Z',
  voteUrl: 'https://github.com/JRpersonal/streborn/discussions/categories/ideas',
  suggestUrl: 'https://github.com/JRpersonal/streborn/discussions/new?category=ideas',
  wishes: [
    { number: 1091, title: 'Update speakers from the phone', url: 'https://github.com/JRpersonal/streborn/discussions/1091', votes: 10 },
    { number: 1110, title: '12 presets <via> double-press', url: 'https://github.com/JRpersonal/streborn/discussions/1110', votes: 8 },
  ],
};

describe('wishesHTML', () => {
  it('lists every wish with its votes and a vote link, in the given order', () => {
    const html = wishesHTML(LIST, tr, 'en');
    expect(html.indexOf('Update speakers')).toBeLessThan(html.indexOf('12 presets'));
    expect(html).toContain('data-wish-open="https://github.com/JRpersonal/streborn/discussions/1091"');
    expect(html).toMatch(/wish-votes[^>]*>.*10<\/span>/s);
    expect(html).toContain('wishes.asOf');
  });

  it('escapes titles from the network', () => {
    expect(wishesHTML(LIST, tr, 'en')).toContain('12 presets &lt;via&gt; double-press');
  });

  it('keeps the suggest and vote buttons when the list is missing or loading', () => {
    for (const list of [null, { ok: false }]) {
      const html = wishesHTML(list, tr, 'en');
      expect(html).toContain('id="wishesSuggestBtn"');
      expect(html).toMatch(/id="wishesSuggestBtn"|data-wish-open="https:\/\/github.com\/JRpersonal\/streborn\/discussions\/new/);
      expect(html).toContain('discussions/new?category=ideas');
      expect(html).not.toContain('wish-row');
    }
    expect(wishesHTML(null, tr, 'en')).toContain('wishes.loading');
    expect(wishesHTML({ ok: false }, tr, 'en')).toContain('wishes.unavailable');
  });

  it('says so when there are no open wishes', () => {
    expect(wishesHTML({ ...LIST, wishes: [] }, tr, 'en')).toContain('wishes.empty');
  });
});
