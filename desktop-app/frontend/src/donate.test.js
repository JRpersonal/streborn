import { describe, it, expect, vi, beforeEach } from 'vitest';

const opened = [];
vi.mock('./api.js', () => ({
  BrowserOpenURL: (url) => { opened.push(url); },
}));
vi.mock('./i18n/index.js', () => ({
  t: (key) => key,
}));

const { donateButtonsHTML, wireDonateButtons, donateFooterLinkHTML, DONATE_URLS } = await import('./donate.js');

// A minimal stand-in for the bit of the DOM these functions touch, so the test
// needs no browser environment.
function fakeRoot(html) {
  const keys = [...html.matchAll(/data-donate="([a-z]+)"/g)].map(m => m[1]);
  const nodes = keys.map(k => ({ dataset: { donate: k }, onclick: null }));
  return { nodes, querySelectorAll: () => nodes };
}

beforeEach(() => { opened.length = 0; });

describe('donate buttons', () => {
  it('renders one button per provider', () => {
    const html = donateButtonsHTML();
    for (const key of Object.keys(DONATE_URLS)) {
      expect(html).toContain(`data-donate="${key}"`);
    }
  });

  // Identified by attribute, not by id: the same markup is on the page twice,
  // once in the rail and once in the footer dialog, and two elements sharing an
  // id would make the second one unreachable.
  it('uses no ids, so the rail and the dialog can both render it', () => {
    expect(donateButtonsHTML()).not.toContain('id="donate');
  });

  it('opens the right provider link for every button', () => {
    const root = fakeRoot(donateButtonsHTML());
    wireDonateButtons(root);
    for (const node of root.nodes) {
      expect(typeof node.onclick).toBe('function');
      node.onclick();
    }
    expect(opened).toEqual([DONATE_URLS.gh, DONATE_URLS.paypal, DONATE_URLS.kofi]);
  });

  it('survives a missing container instead of throwing', () => {
    expect(() => wireDonateButtons(null)).not.toThrow();
    expect(() => wireDonateButtons({})).not.toThrow();
  });
});

describe('donate footer link', () => {
  // The reason this link exists: the donate rail is display:none below 62em and
  // that threshold scales with the text size, so on a narrow window or a large
  // font it is gone. The footer is on screen at every size, so this link is the
  // path that cannot disappear.
  it('carries the id the footer wiring looks for', () => {
    expect(donateFooterLinkHTML()).toContain('id="footerDonate"');
  });

  it('is a footer link like its neighbours', () => {
    expect(donateFooterLinkHTML()).toContain('class="footer-link"');
  });

  it('uses the existing translated strings rather than inventing new keys', () => {
    const html = donateFooterLinkHTML();
    expect(html).toContain('credits.donate');
    expect(html).toContain('footer.donateSlogan');
  });
});
