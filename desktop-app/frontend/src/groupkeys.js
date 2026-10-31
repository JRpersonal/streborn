// groupkeys.js — pure helpers for the group keys section of the Multi-Room
// tab (#863): a saved group (template) on a thumbs key of one speaker's
// remote. The document lives on the speaker whose remote is used, so every
// helper here works on ONE speaker's document, never merges across speakers.
//
// Document shape (the agent's /api/groupkeys):
//   { templates: [{ name, master: {deviceID, ip, name}, members: [{deviceID, ip, name}], permanent }],
//     bindings: { thumbsUp: name, thumbsDown: name } }
//
// No DOM access, no imports from the views (the view-extraction trap).

export const GROUP_KEY_IDS = ['thumbsUp', 'thumbsDown'];

// normalizeDoc turns whatever the agent (or an older agent, or nothing)
// answered into a well-formed document the view can edit.
export function normalizeDoc(raw) {
  const templates = Array.isArray(raw && raw.templates) ? raw.templates : [];
  const bindings = (raw && raw.bindings && typeof raw.bindings === 'object') ? raw.bindings : {};
  const out = { templates: [], bindings: {} };
  for (const t of templates) {
    if (!t || !t.name) continue;
    out.templates.push({
      name: String(t.name),
      master: member(t.master),
      members: (Array.isArray(t.members) ? t.members : []).map(member).filter(m => m.deviceID || m.ip),
      permanent: !!t.permanent,
    });
  }
  for (const key of GROUP_KEY_IDS) {
    const name = bindings[key];
    if (name && out.templates.some(t => sameName(t.name, name))) out.bindings[key] = String(name);
  }
  return out;
}

function member(m) {
  return {
    deviceID: String((m && m.deviceID) || ''),
    ip: String((m && m.ip) || ''),
    name: String((m && m.name) || ''),
  };
}

function sameName(a, b) {
  return String(a || '').trim().toLowerCase() === String(b || '').trim().toLowerCase();
}

// templateFromBoxes builds a template from the master box and the member
// boxes the user composed on the tab. label(box) gives the display name.
// gkMembersForSave decides which speakers "Save the current group" means.
//
// Three sources, and the order is the whole point:
//
//   picked   what the user ticked in the picker. An explicit instruction, so
//            it wins over anything the speakers are doing.
//   live     who is in the group playing right now. This is what the button
//            says on the tin, and it was the one missing: with nothing ticked
//            the composer fell straight through to the stored group, found
//            none, and returned null, so the save was refused and nothing was
//            written. A field bundle of eleven speakers had an empty group-key
//            store on every one of them after exactly that (2026-09-13).
//   stored   the saved permanent group, for a master whose group is at rest
//            and therefore not live anywhere.
//
// Returns the members and which source they came from, so the caller can take
// the permanent flag from the same place rather than from a stale checkbox.
export function gkMembersForSave({ picked, live, stored }) {
  if (picked && picked.length) return { members: picked, from: 'picked' };
  if (live && live.length) return { members: live, from: 'live' };
  if (stored && stored.length) return { members: stored, from: 'stored' };
  return { members: [], from: '' };
}

export function templateFromBoxes({ name, master, members, permanent, label }) {
  const lbl = label || ((b) => (b && (b.friendlyName || b.name || b.host)) || '');
  const asMember = (b) => ({ deviceID: String(b.deviceID || ''), ip: String(b.host || ''), name: String(lbl(b) || '') });
  return {
    name: String(name || '').trim(),
    master: asMember(master),
    members: (members || []).map(asMember),
    permanent: !!permanent,
  };
}

// templateFromStored builds a template from a stored permanent group entry
// (groups.js storedPermanentGroupsOf): { masterBox, members: [{box, ip, name}] }.
export function templateFromStored(group, name, label) {
  const lbl = label || ((b) => (b && (b.friendlyName || b.name || b.host)) || '');
  return {
    name: String(name || '').trim(),
    master: { deviceID: String(group.masterBox.deviceID || ''), ip: String(group.masterBox.host || ''), name: String(lbl(group.masterBox) || '') },
    members: (group.members || []).map(m => m.box
      ? { deviceID: String(m.box.deviceID || ''), ip: String(m.box.host || ''), name: String(lbl(m.box) || '') }
      : { deviceID: '', ip: String(m.ip || ''), name: String(m.name || '') }),
    permanent: true,
  };
}

// validateTemplate returns '' when the template can be saved, else the i18n
// key of the reason.
export function validateTemplate(tpl, doc) {
  if (!tpl || !tpl.name) return 'multiroom.groupKeysNameRequired';
  if (!tpl.master || !(tpl.master.deviceID || tpl.master.ip) || !tpl.members || !tpl.members.length) {
    return 'multiroom.groupKeysNeedGroup';
  }
  if (doc && (doc.templates || []).some(t => sameName(t.name, tpl.name))) return 'multiroom.groupKeysDuplicateName';
  return '';
}

// withTemplate returns a new document with tpl appended.
export function withTemplate(doc, tpl) {
  return { templates: [...(doc.templates || []), tpl], bindings: { ...(doc.bindings || {}) } };
}

// withoutTemplate returns a new document without the named template and
// without any binding that pointed at it.
export function withoutTemplate(doc, name) {
  const out = { templates: (doc.templates || []).filter(t => !sameName(t.name, name)), bindings: {} };
  for (const [k, v] of Object.entries(doc.bindings || {})) {
    if (!sameName(v, name)) out.bindings[k] = v;
  }
  return out;
}

// keyOf returns the key the named template is bound to, or ''.
export function keyOf(doc, name) {
  for (const k of GROUP_KEY_IDS) {
    if (sameName((doc.bindings || {})[k], name)) return k;
  }
  return '';
}

// bindKey returns a new document where the named template sits on key ('' =
// no key). A template carries at most one key and a key at most one
// template, so the previous holder of the key and the template's previous
// key are both cleared.
export function bindKey(doc, name, key) {
  const bindings = {};
  for (const [k, v] of Object.entries(doc.bindings || {})) {
    if (sameName(v, name)) continue; // the template's previous key
    if (key && k === key) continue;   // the key's previous template
    bindings[k] = v;
  }
  if (key && GROUP_KEY_IDS.includes(key)) bindings[key] = name;
  return { templates: (doc.templates || []).slice(), bindings };
}

// describeTemplate renders "Main + member, member" from the template's own
// stored names, refreshed from the discovered boxes where one matches (a
// renamed speaker shows its new name).
export function describeTemplate(tpl, boxes, label) {
  const lbl = label || ((b) => (b && (b.friendlyName || b.name || b.host)) || '');
  const up = (s) => String(s || '').toUpperCase();
  const nameOf = (m) => {
    const box = (boxes || []).find(b => b && ((m.deviceID && up(b.deviceID) === up(m.deviceID)) || (m.ip && b.host === m.ip)));
    return (box && lbl(box)) || m.name || m.ip || m.deviceID || '?';
  };
  const members = (tpl.members || []).map(nameOf);
  return nameOf(tpl.master) + (members.length ? ' + ' + members.join(', ') : '');
}

// validWolMac reports whether mac is a 6-byte hardware address in a form the
// agent's parser accepts (AA:BB:CC:DD:EE:FF, AA-BB-..., or AABB.CCDD.EEFF).
// A shorter one never fires, so it must not pass as configured.
export function validWolMac(mac) {
  const s = String(mac || '').trim();
  return /^[0-9a-f]{2}([:-])[0-9a-f]{2}(\1[0-9a-f]{2}){4}$/i.test(s)
    || /^[0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}$/i.test(s);
}

// webhookConfigured reports whether a webhook action is enabled and has what
// its transport needs to fire, the same test the agent applies.
export function webhookConfigured(a) {
  return !!a && a.enabled === true && (
    a.type === 'wol' ? validWolMac(a.mac) : a.type === 'udp' ? (!!a.host && a.port > 0) : !!a.url);
}

// webhookPresent reports whether a webhook action is enabled and filled in,
// whether or not it can fire. A broken entry (a Wake-on-LAN address that does
// not parse) is still something on the key the user must be able to see, open
// and switch off: hidden, it sat on the key unreachable (2026-10-27).
export function webhookPresent(a) {
  return !!a && a.enabled === true && (
    a.type === 'wol' ? !!String(a.mac || '').trim() : a.type === 'udp' ? (!!a.host && a.port > 0) : !!a.url);
}

// webhookSourceOnKey says where the webhook on key comes from: 'key' for the
// key's own trigger, 'thumb' for the legacy shared thumbs action (it fires for
// either thumbs key), '' for none. A present but broken entry counts, so the
// key map lights it and a click opens it.
export function webhookSourceOnKey(cfg, key) {
  if (!cfg) return '';
  if (webhookPresent(cfg.buttons && cfg.buttons[key])) return 'key';
  return GROUP_KEY_IDS.includes(key) && webhookPresent(cfg.thumb) ? 'thumb' : '';
}

// webhookOnKey reports whether the speaker's webhook config carries an
// enabled trigger for key (the legacy shared thumbs action counts for both
// thumbs keys). A bound group wins over it, so the view warns.
export function webhookOnKey(cfg, key) {
  return webhookSourceOnKey(cfg, key) !== '';
}

// keyMapTarget is the webhook entry a click on key in the remote key map
// opens. A thumbs key lit only by the shared thumbs action opens that shared
// entry: opening the key's own (empty) form left a lit key with nothing on it
// to change or remove (2026-10-27).
export function keyMapTarget(cfg, key) {
  return webhookSourceOnKey(cfg, key) === 'thumb' ? 'thumb' : key;
}

// sharedThumbFiresOn reports whether the agent would fire the shared thumbs
// action for a press of key: it has to be valid, and the key must carry no
// group, no key action and no working webhook of its own (the agent's
// FireKey order).
export function sharedThumbFiresOn(cfg, key, opts) {
  const o = opts || {};
  if (!cfg || !webhookConfigured(cfg.thumb) || !GROUP_KEY_IDS.includes(key)) return false;
  if (o.bindings && o.bindings[key]) return false;
  if (typeof o.keyActionOn === 'function' && o.keyActionOn(key)) return false;
  return !webhookConfigured(cfg.buttons && cfg.buttons[key]);
}

// groupKeyWebhookWarnKey picks the Multi-room warning for a webhook on a
// group-bound key. The shared thumbs action needs its own sentence, and which
// one depends on whether it still fires on the other thumbs key: claiming it
// does when the other key carries a group or a key action (or the entry is
// broken) confused the user it was meant to help. opts: { bindings,
// keyActionOn(key) }.
export function groupKeyWebhookWarnKey(cfg, key, opts) {
  switch (webhookSourceOnKey(cfg, key)) {
    case 'key': return 'multiroom.groupKeysWebhookWarn';
    case 'thumb': {
      const other = key === 'thumbsUp' ? 'thumbsDown' : 'thumbsUp';
      return sharedThumbFiresOn(cfg, other, opts)
        ? 'multiroom.groupKeysWebhookWarnShared' : 'multiroom.groupKeysWebhookWarnSharedUnused';
    }
    default: return '';
  }
}

// groupKeyErrorBannerKey picks the banner sentence for a refused group-key
// press by the agent's stable kind. The kinds the user can act on get their
// own sentence; any other (or none, from an older agent) keeps the generic
// one with the speaker's reason in it.
export function groupKeyErrorBannerKey(kind) {
  switch (kind) {
    case 'master-unresponsive': return 'speaker.groupKeyErrorUnresponsive';
    case 'member-unresolved': return 'speaker.groupKeyErrorMoved';
    default: return 'speaker.groupKeyErrorBanner';
  }
}

// groupKeyErrorBanners turns the speakers that report a refused group-key
// press into banner lines: one per sentence kind the user can act on (with
// every speaker of that kind named), and one per speaker for the generic
// sentence, which quotes that speaker's own reason. Mixing kinds under the
// first speaker's sentence told a speaker whose form was refused that it had
// moved. Each line keeps its raw reasons for display under the sentence.
export function groupKeyErrorBanners(boxes, label) {
  const lbl = label || ((b) => (b && (b.friendlyName || b.name || b.host)) || '');
  const out = [];
  const byKey = new Map();
  for (const b of boxes || []) {
    const key = groupKeyErrorBannerKey(b.groupKeyErrorKind);
    const reason = String(b.groupKeyError || '');
    if (key === 'speaker.groupKeyErrorBanner') {
      out.push({ key, names: lbl(b), reason, reasons: [reason] });
      continue;
    }
    let line = byKey.get(key);
    if (!line) {
      line = { key, names: [], reason, reasons: [] };
      byKey.set(key, line);
      out.push(line);
    }
    line.names.push(lbl(b));
    if (reason && !line.reasons.includes(reason)) line.reasons.push(reason);
  }
  return out.map(l => (Array.isArray(l.names) ? { ...l, names: l.names.join(', ') } : l));
}
