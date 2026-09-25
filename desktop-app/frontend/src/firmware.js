// Bose firmware facts: the last version Bose shipped, the per-model support
// articles, and the version compare. Data and pure functions only.
//
// This lives outside the views on purpose. Both the Settings pane and the Setup
// wizard need it (Settings shows the outdated banner, Setup shows the route when
// an install fails on a speaker that is still stock), and a view cannot be
// imported to get at it: settings.js reads `navigator.userAgent` at module
// level, which exists in a browser and in Node 21 and later, and does not exist
// in Node 20. A test that imported the view to reach these helpers therefore
// passed on one machine and failed on the CI runner with
// "ReferenceError: navigator is not defined".

// LATEST_BOSE_FIRMWARE mirrors latestBoseFirmware in desktop-app/app_firmware.go,
// the last firmware Bose shipped for any SoundTouch (Sep 2022). Model-independent
// on purpose: the install path has to judge a CineMate or a soundbar too, and
// LATEST_FW below only lists the four speakers the settings banner covers.
export const LATEST_BOSE_FIRMWARE = '27.0.6';

export const LATEST_FW = {
  'SoundTouch 10': '27.0.6',
  'SoundTouch 20': '27.0.6',
  'SoundTouch 30': '27.0.6',
  'SoundTouch Portable': '27.0.6',
};

// Bose's own firmware-update support article, PER MODEL. Shown as a clickable
// link in the outdated-firmware banner (Jens, 2026-06-27: link the Bose support
// article).
//
// This used to be ONE link for every speaker, and that link was the SoundTouch
// 20 Series III article, on the reasoning that the steps are the same
// everywhere. They mostly are, but a SoundTouch 10 owner then opens an article
// showing a different speaker, and the owner of a Series II ST20 is sent to the
// Series III page. That came up on 2026-09-09 from a reporter whose Series II
// lost its sound after a firmware update; whatever caused that, pointing him at
// the wrong series was ours.
//
// The ST20 and the ST30 each exist in two series and the speaker does not say
// which one it is (docs/MODEL-VARIANTS.md: moduleType separates sm2 from scm,
// not the series), so those two models offer BOTH articles instead of guessing.
// Every URL below answered 200 when it was added; Bose has no model-independent
// article, that URL is a 404.
export const BOSE_FW_ARTICLES = {
  'SoundTouch 10': [
    ['', 'https://support.bose.com/s/article/soundtouch-10-updating-the-software-or-firmware-of-your-product?language=en_US'],
  ],
  'SoundTouch 20': [
    ['Series II', 'https://support.bose.com/s/article/soundtouch-20-ii-updating-the-software-or-firmware-of-your-product?language=en_US'],
    ['Series III', 'https://support.bose.com/s/article/soundtouch-20-iii-updating-the-software-or-firmware-of-your-product?language=en_US'],
  ],
  'SoundTouch 30': [
    ['Series II', 'https://support.bose.com/s/article/soundtouch-30-ii-updating-the-software-or-firmware-of-your-product?language=en_US'],
    ['Series III', 'https://support.bose.com/s/article/soundtouch-30-iii-updating-the-software-or-firmware-of-your-product?language=en_US'],
  ],
  'SoundTouch Portable': [
    ['', 'https://support.bose.com/s/article/soundtouch-portable-updating-the-software-or-firmware-of-your-product?language=en_US'],
  ],
};

// boseFwArticles returns the support articles for a speaker type, and NOTHING
// for a type that has none.
//
// It used to fall back to the SoundTouch 10 article on the reasoning that the
// steps are close enough. They are, between speakers. They are not between a
// speaker and a CineMate, a Lifestyle console, an SA-5 amplifier, a Wireless
// Link adapter or a soundbar, and all five reach this code: the install path
// reads the firmware of whatever answers on the LAN. Sending one of those owners
// to a page showing a small round speaker is the same mistake, one model family
// further out, that the comment above documents fixing for the ST10.
//
// A model with no article is not left with a dead end: the Bose Software Updater
// page (BOSE_FW_USB_URL) is model-independent and is the route that still works
// since the cloud shutdown, so it stays on screen either way.
export function boseFwArticles(type) {
  return BOSE_FW_ARTICLES[type] || [];
}

// Bose's official "Bose Software Updater" download page, referenced in step 4
// and made clickable so the user does not have to retype it. The old direct
// USB directory (downloads.bose.com/ced/soundtouch/soundtouch_usb/) went dead:
// empty listing, index answers 403 even with a browser user agent (checked
// 2026-07-31). btu.bose.com is the page Bose itself points users to.
export const BOSE_FW_USB_URL = 'https://btu.bose.com/';

// fwVersionTuple extracts the first 3 numbers from "27.0.6.46330.5043500" for
// comparison. Returns null on an unknown format.
export function fwVersionTuple(v) {
  if (!v) return null;
  const m = String(v).match(/^(\d+)\.(\d+)\.(\d+)/);
  if (!m) return null;
  return [parseInt(m[1]), parseInt(m[2]), parseInt(m[3])];
}

// firmwareOlderThanLatest reports whether a short version ("10.0.11") is behind
// the last Bose firmware. Mirrors firmwareOlder in app_firmware.go so the two
// sides of the app cannot disagree about the same speaker.
export function firmwareOlderThanLatest(short) {
  return versionOlder(fwVersionTuple(short), fwVersionTuple(LATEST_BOSE_FIRMWARE));
}

// isFwOutdated judges a speaker against the latest firmware for ITS model, which
// is what the settings banner needs: a model the app tracks no version for never
// shows the banner.
export function isFwOutdated(info) {
  return versionOlder(fwVersionTuple((info && info.version) || ''),
    fwVersionTuple(LATEST_FW[(info && info.type) || ''] || ''));
}

function versionOlder(have, want) {
  if (!have || !want) return false;
  for (let i = 0; i < 3; i++) {
    if (have[i] < want[i]) return true;
    if (have[i] > want[i]) return false;
  }
  return false;
}
