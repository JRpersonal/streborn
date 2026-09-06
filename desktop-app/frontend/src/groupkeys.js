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

// webhookOnKey reports whether the speaker's webhook config carries an
// enabled, configured trigger for key (the legacy shared thumbs action counts
// for both thumbs keys). A bound group wins over it, so the view warns.
export function webhookOnKey(cfg, key) {
  const configured = (a) => !!a && a.enabled === true && (
    a.type === 'wol' ? !!a.mac : a.type === 'udp' ? (!!a.host && a.port > 0) : !!a.url);
  if (!cfg) return false;
  if (configured(cfg.buttons && cfg.buttons[key])) return true;
  return GROUP_KEY_IDS.includes(key) && configured(cfg.thumb);
}
