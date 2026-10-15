// The Wishes tab lists every open feature wish with its votes and links each
// one to its GitHub discussion, where the vote happens. It replaced the
// "Podcasts (planned)" placeholder tab.
import { describe, it, expect, vi } from 'vitest';

vi.mock('./api.js', () => ({ BrowserOpenURL: () => {}, FeatureWishes: async () => null }));

const { wishesHTML, wishTitle, wishMatches, doneMonth } = await import('./views/wishes.js');

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

// The website translates the titles at build time; the tab picks the app's
// language and falls back to the English original.
describe('wishTitle', () => {
  const w = { title: 'Clock on the display while streaming', titles: { de: 'Uhr auf dem Display beim Streamen', 'zh-tw': '串流時在螢幕上顯示時鐘' } };

  it('shows the title in the app language', () => {
    expect(wishTitle(w, 'de')).toBe('Uhr auf dem Display beim Streamen');
  });

  it('maps the app zh-Hant to the site zh-tw', () => {
    expect(wishTitle(w, 'zh-Hant')).toBe('串流時在螢幕上顯示時鐘');
  });

  it('falls back to English when a language is missing or the list is old', () => {
    expect(wishTitle(w, 'fr')).toBe(w.title);
    expect(wishTitle({ title: 'X' }, 'de')).toBe('X');
  });

  it('is what the rows render', () => {
    const html = wishesHTML({ ok: true, wishes: [{ ...w, url: 'https://x', votes: 8 }] }, (k) => k, 'de');
    expect(html).toContain('Uhr auf dem Display beim Streamen');
  });
});

// A search box above the list filters it while typing (Jens, 2026-10-11).
describe('wishes search', () => {
  const clock = { number: 1098, title: 'Clock on the display while streaming', titles: { de: 'Uhr auf dem Display beim Streamen', es: 'Reloj en la pantalla' }, url: 'https://x/1098', votes: 8 };
  const usb = { number: 1136, title: 'Play music from a USB stick plugged into the speaker', titles: { de: 'Musik vom USB-Stick am Lautsprecher' }, url: 'https://x/1136', votes: 5 };

  it('matches the translated title, the English one and the number', () => {
    expect(wishMatches(clock, 'de', 'uhr')).toBe(true);
    expect(wishMatches(clock, 'de', 'clock')).toBe(true);
    expect(wishMatches(clock, 'de', '1098')).toBe(true);
    expect(wishMatches(usb, 'de', 'uhr')).toBe(false);
  });

  it('needs every word and ignores case and accents', () => {
    expect(wishMatches(clock, 'de', 'DISPLAY streamen')).toBe(true);
    expect(wishMatches(clock, 'de', 'display usb')).toBe(false);
    expect(wishMatches({ ...clock, titles: { es: 'Música en la pantalla' } }, 'es', 'musica')).toBe(true);
  });

  it('matches everything when empty', () => {
    expect(wishMatches(usb, 'de', '   ')).toBe(true);
  });

  it('renders the box, hides rows that do not match and says so when none do', () => {
    const tr = (k) => k;
    const html = wishesHTML({ ok: true, wishes: [clock, usb] }, tr, 'de', 'uhr');
    expect(html).toContain('id="wishesSearch"');
    expect(html).toContain('value="uhr"');
    expect(html).toMatch(/data-wish-num="1136" hidden/);
    expect(html).not.toMatch(/data-wish-num="1098" hidden/);
    expect(html).toMatch(/id="wishesNoMatch" hidden/);
    expect(wishesHTML({ ok: true, wishes: [clock, usb] }, tr, 'de', 'zzz')).not.toMatch(/id="wishesNoMatch" hidden/);
  });
});

// Built wishes stay listed as "Recently done" for three months, so voters see
// their votes lead somewhere (Jens, 2026-10-14).
describe('recently done', () => {
  const ios = {
    number: 1182,
    title: 'iOS app for the phone <remote>',
    titles: { de: 'iOS App für die Handy Fernbedienung' },
    url: 'https://github.com/JRpersonal/streborn/discussions/1182',
    votes: 5,
    doneAt: '2026-10-10T21:37:53Z',
  };
  const WITH_DONE = { ...LIST, done: [ios] };

  it('renders a done section below the open wishes with badge, date and link', () => {
    const html = wishesHTML(WITH_DONE, tr, 'en');
    expect(html).toContain('wishes.doneTitle');
    expect(html).toContain('wishes.doneBadge');
    expect(html).toContain('data-wish-open="https://github.com/JRpersonal/streborn/discussions/1182"');
    expect(html).toContain('datetime="2026-10-10T21:37:53Z"');
    expect(html).toContain('October 2026');
    expect(html).toContain('iOS app for the phone &lt;remote&gt;');
    expect(html.indexOf('Update speakers')).toBeLessThan(html.indexOf('wishes.doneTitle'));
  });

  it('shows the release a wish shipped in, and only a real version', () => {
    expect(wishesHTML({ ...LIST, done: [{ ...ios, version: 'v1.0.13' }] }, tr, 'en')).toContain('<span class="wish-done-version small">v1.0.13</span>');
    expect(wishesHTML(WITH_DONE, tr, 'en')).not.toContain('wish-done-version');
    expect(wishesHTML({ ...LIST, done: [{ ...ios, version: '<b>x</b>' }] }, tr, 'en')).not.toContain('wish-done-version');
  });

  it('uses the translated title and the app language for the month', () => {
    const html = wishesHTML(WITH_DONE, tr, 'de');
    expect(html).toContain('iOS App für die Handy Fernbedienung');
    expect(html).toContain('Oktober 2026');
  });

  it('renders nothing extra without done wishes or with an older list', () => {
    expect(wishesHTML(LIST, tr, 'en')).not.toContain('wishes.doneTitle');
    expect(wishesHTML({ ...LIST, done: [] }, tr, 'en')).not.toContain('wishes.doneTitle');
  });

  it('still shows done wishes when no wish is open', () => {
    const html = wishesHTML({ ...WITH_DONE, wishes: [] }, tr, 'en');
    expect(html).toContain('wishes.empty');
    expect(html).toContain('wishes.doneTitle');
  });

  it('obeys the search like the open rows', () => {
    const html = wishesHTML(WITH_DONE, tr, 'en', 'ios');
    expect(html).toMatch(/data-wish-num="1182"\s*>/);
    expect(html).toMatch(/data-wish-num="1091" hidden/);
    expect(html).toMatch(/id="wishesNoMatch" hidden/);
    expect(wishesHTML(WITH_DONE, tr, 'en', 'zzz')).toMatch(/data-wish-num="1182" hidden/);
  });

  it('formats the month in UTC and skips an unreadable date', () => {
    expect(doneMonth('2026-10-31T23:30:00Z', 'en')).toBe('October 2026');
    expect(doneMonth('nope', 'en')).toBe('');
    expect(doneMonth('', 'en')).toBe('');
    expect(wishesHTML({ ...LIST, done: [{ ...ios, doneAt: 'nope' }] }, tr, 'en')).not.toContain('<time');
  });
});
