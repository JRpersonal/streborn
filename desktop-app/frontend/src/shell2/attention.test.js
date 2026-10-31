// What one speaker needs from you (UI 2.0 phase 5, attention.js): the lists
// main.js builds for the classic wedge and issue banners become one item list
// per speaker, worded with the classic banner's sentence for that speaker
// alone, with the same buttons, as a chip, a list-card note and an Overview
// card that names the speaker.
import { describe, it, expect, vi, afterEach } from 'vitest';
import { setLocale, t } from '../i18n/index.js';
import { escapeHtml } from '../utils.js';
import { state } from '../state.js';
import {
  ATTENTION_KINDS, attentionCardHTML, attentionChip, attentionItems, attentionNoteHTML, itemActions,
  runAttentionAction, setSpeakerAttentionRunner, speakerAttention, speakerIssuesFrom,
} from './attention.js';

setLocale('en');
afterEach(() => { state.speakerIssues = undefined; setSpeakerAttentionRunner(null); });

const kitchen = { host: '192.0.2.10', friendlyName: 'Kitchen', conflictingMod: 'AfterTouch' };
const bath = { host: '192.0.2.11', friendlyName: 'Bath', foreignCloudURL: 'http://cloud.example' };
const label = (b) => b.friendlyName;

describe('speakerIssuesFrom', () => {
  it('splits the banner lists into one ordered item list per speaker', () => {
    const issues = speakerIssuesFrom({
      wedged: [bath], conflict: [kitchen], foreignCloud: [bath], noWifi: [kitchen],
    }, label);
    expect(Object.keys(issues).sort()).toEqual([bath.host, kitchen.host].sort());
    expect(issues[kitchen.host].map((i) => i.kind)).toEqual(['conflict', 'noWifi']);
    expect(issues[bath.host].map((i) => i.kind)).toEqual(['wedged', 'foreignCloud']);
    expect(issues[kitchen.host][0]).toMatchObject({ name: 'Kitchen', mod: 'AfterTouch' });
    expect(issues[bath.host][1]).toMatchObject({ url: 'http://cloud.example' });
  });

  it('knows every classic banner kind', () => {
    expect(ATTENTION_KINDS).toEqual(['wedged', 'noNet', 'conflict', 'foreignCloud', 'noWifi', 'storm',
      'keyErr', 'holdErr', 'staleKey', 'badName']);
  });

  it('is empty without issues', () => {
    expect(speakerIssuesFrom({}, label)).toEqual({});
    expect(attentionItems(kitchen.host, {})).toEqual([]);
    expect(attentionChip([])).toBe(null);
    expect(attentionNoteHTML([])).toBe('');
    expect(attentionCardHTML([], 'Kitchen')).toBe('');
  });
});

describe('words and buttons', () => {
  it('chips a speaker that cannot play as danger, a heads-up as warn', () => {
    const issues = speakerIssuesFrom({ wedged: [bath], noWifi: [kitchen] }, label);
    expect(attentionChip(issues[bath.host])).toEqual({ kind: 'danger', label: t('ui2.attention.chip') });
    expect(attentionChip(issues[kitchen.host]).kind).toBe('warn');
  });

  it('notes the classic sentence for this one speaker, with its buttons and dismiss kind', () => {
    const issues = speakerIssuesFrom({ conflict: [kitchen], noWifi: [kitchen] }, label);
    const html = attentionNoteHTML(issues[kitchen.host]);
    expect(html).toContain(escapeHtml(t('speaker.conflictModBanner', { name: 'Kitchen', mod: 'AfterTouch' })));
    expect(html).toContain(escapeHtml(t('speaker.noWifiBanner', { name: 'Kitchen' })));
    expect(html).toContain('data-attn="removeConflict"');
    expect(html).toContain('data-attn="saveWifi"');
    expect(html).toContain('data-dismiss="conflict"');
    expect(html).toContain('data-dismiss="nowifi"');
    expect(html).toContain(`data-host="${kitchen.host}"`);
    expect(html).toContain(t('speaker.conflictRemoveBtn'));
    expect(html).toContain(t('speaker.issueDismiss'));
  });

  it('offers Put STR back for a foreign cloud address, and the restart for a storm', () => {
    expect(itemActions({ kind: 'foreignCloud' }).map((a) => a.act)).toEqual(['cloudRestore']);
    expect(itemActions({ kind: 'foreignCloud' })[0].label).toBe('Put STR back');
    expect(itemActions({ kind: 'storm' }).map((a) => a.act)).toEqual(['stormRestart']);
    expect(itemActions({ kind: 'wedged' })).toEqual([]);
  });

  it('heads the Overview card with the speaker\'s name', () => {
    const issues = speakerIssuesFrom({ wedged: [bath] }, label);
    const html = attentionCardHTML(issues[bath.host], 'Bath');
    expect(html).toContain(t('ui2.attention.heading', { name: 'Bath' }));
    expect(html).toContain('Bath needs your attention');
    expect(html).toContain(t('speaker.wedgedTitle'));
    expect(html).toContain(escapeHtml(t('speaker.wedgedBanner', { name: 'Bath' })));
    expect(html).toContain('sps-attn--danger');
  });

  it('escapes what a speaker reports', () => {
    const bad = { host: '192.0.2.12', friendlyName: '<b>x</b>', foreignCloudURL: '"><img>' };
    const html = attentionNoteHTML(speakerIssuesFrom({ foreignCloud: [bad] }, label)[bad.host]);
    expect(html).not.toContain('<b>x</b>');
    expect(html).not.toContain('"><img>');
  });

  it('reads state.speakerIssues for the sidebar', () => {
    state.speakerIssues = speakerIssuesFrom({ storm: [kitchen] }, label);
    const a = speakerAttention(kitchen.host);
    expect(a.items).toHaveLength(1);
    expect(a.chip.kind).toBe('danger');
    expect(speakerAttention(bath.host)).toEqual({ items: [], chip: null });
  });
});

describe('runAttentionAction', () => {
  it('hands a button press to the runner main.js registered', () => {
    const run = vi.fn();
    setSpeakerAttentionRunner(run);
    const btn = { dataset: { host: kitchen.host, attn: 'dismiss', dismiss: 'nowifi' }, disabled: false, getAttribute: () => null };
    const target = { closest: (sel) => (sel === '.attn-act' ? btn : null) };
    expect(runAttentionAction(target)).toBe(true);
    expect(run).toHaveBeenCalledWith(kitchen.host, 'dismiss', btn, 'nowifi');
    expect(runAttentionAction({ closest: () => null })).toBe(false);
  });
});
