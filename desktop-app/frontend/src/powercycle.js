// powercycle.js: the words of the "unplug it" banner (#boxWedgeBanner).
//
// Two agent verdicts end in the same advice, a power cycle (boxHealth,
// needsPowerCycle in boxes.js):
//
//   wedged                 the speaker accepts playback and never plays
//   firmware-unresponsive  the speaker's own Bose software (its :8090
//                          webserver) stopped answering while STR runs on
//                          (internal/webui/boseapp_health.go)
//
// A SoundTouch Portable runs on a battery that cannot be taken out, so
// unplugging it restarts nothing; its sentence names its own restart (AUX held
// on the charger, the same one the speaker check gives).
//
// Pure: banners/speaker.js renders the lines, the tests read them.

import { t } from './i18n/index.js';
import { isPortableSpeaker } from './speakercheck.js';

// wedgeBannerLines is the banner's words as data, one { title, text } per
// sentence: the wedged speakers in one, the speakers whose Bose software
// stopped answering in another, and the Portables among those in a third.
// label names a box (getBoxLabel).
export function wedgeBannerLines(wedged, fwDead, label) {
  const names = (list) => list.map((b) => label(b)).join(', ');
  const lines = [];
  if (wedged && wedged.length) {
    lines.push({ title: t('speaker.wedgedTitle'), text: t('speaker.wedgedBanner', { name: names(wedged) }) });
  }
  const dead = fwDead || [];
  const mains = dead.filter((b) => !isPortableSpeaker(b));
  const battery = dead.filter((b) => isPortableSpeaker(b));
  if (mains.length) {
    lines.push({ title: t('speaker.fwDeadTitle'), text: t('speaker.fwDeadBanner', { name: names(mains) }) });
  }
  if (battery.length) {
    lines.push({ title: t('speaker.fwDeadTitle'), text: t('speaker.fwDeadBannerPortable', { name: names(battery) }) });
  }
  return lines;
}
