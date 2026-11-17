// stickers/collect.js: reads what the sticker sheet needs from every speaker.
//
// One pass when the dialog opens: the presets, the address for the phone
// remote and its QR code, and a picture per key. A speaker that does not
// answer is left off the sheet and named, never a reason to have no sheet.
//
// The backend calls come in as `api` so the flow is tested without one.

import { isStr, labelOf } from '../boxes.js';
import { housingOf, logoRequest, speakerId, stickerKeys } from './data.js';

const isDataImage = (s) => typeof s === 'string' && /^data:image\//i.test(s);

// keyLogo finds the picture for one key and returns it as a data: URI, or ''
// for the letter tile. Station logos go through ResolveStationLogo, which is
// what tells a real logo from an icon service's placeholder; whatever address
// comes out of it is then fetched by Go (StickerImage), because an image the
// canvas loaded itself from another origin could not be saved.
async function keyLogo(preset, api) {
  const req = logoRequest(preset);
  if (req.inline) return req.inline;
  const fetched = async (url) => {
    try {
      const uri = await api.StickerImage(url);
      return isDataImage(uri) ? uri : '';
    } catch {
      return '';
    }
  };
  const tried = new Set();
  if (req.lookup) {
    let chain = '';
    try { chain = await api.ResolveStationLogo(req.lookup.fav, req.lookup.brand, req.lookup.hosts); } catch { chain = ''; }
    for (const cand of String(chain || '').split('|').map((c) => c.trim()).filter(Boolean)) {
      // A media server's cover already comes back as a data: URI.
      if (isDataImage(cand)) return cand;
      tried.add(cand);
      const uri = await fetched(cand);
      if (uri) return uri;
    }
  }
  // The key's own art last: for a Spotify key it is the only candidate, and
  // for a station it covers a plain-http logo the lookup does not hand out.
  if (req.direct && !tried.has(req.direct)) return fetched(req.direct);
  return '';
}

async function collectOne(box, api, storage) {
  const port = box.port || 8888;
  const presets = await api.GetPresets(box.host, port) || [];
  const keys = stickerKeys(box, presets);
  const [qr] = await Promise.all([
    (async () => {
      // PhoneAddressFor prefers a name that survives a new address and falls
      // back to the address itself; the code encodes whichever it proved. If
      // the question itself fails, the plain address still opens the remote.
      let url = `http://${box.host}:${port}/`;
      try {
        const addr = await api.PhoneAddressFor(box.host, port);
        if (addr && addr.url) url = addr.url;
      } catch { /* keep the address */ }
      const qr = await api.PhoneQR(url);
      if (!isDataImage(qr)) throw new Error('no QR code');
      return qr;
    })(),
    ...keys.map(async (k) => {
      k.logo = k.preset ? (await keyLogo(k.preset, api)) || null : null;
      delete k.preset;
    }),
  ]);
  const id = speakerId(box);
  return { id, name: labelOf(box), model: box.model || '', housing: housingOf(id, storage), keys, qr };
}

// collectSpeakers reads every STR speaker of the list. Returns the speakers
// for the sheet and the names of those left off it.
export async function collectSpeakers(boxes, api, storage) {
  const all = (boxes || []).filter((b) => isStr(b) && b.host);
  const results = await Promise.all(all.map(async (box) => {
    // A speaker the list already marks as gone is not asked again.
    if (box.offline) return null;
    try { return await collectOne(box, api, storage); } catch { return null; }
  }));
  return {
    speakers: results.filter(Boolean),
    skipped: all.filter((_, i) => !results[i]).map(labelOf),
  };
}
