// Presets for music services the speaker plays by itself (Pandora,
// iHeartRadio). The agent stores them as type "native" with the speaker's own
// ContentItem in `native`; STR has no stream for them, so a tile shows the
// name and the service and offers no stream-shaped actions (bitrate, logo
// lookup in the radio directory, metadata self-heals).

// The source enums STR keeps on a key, with the service name the tile shows.
// Brand names, so they are the same in every language.
const NATIVE_SERVICE_LABELS = {
  PANDORA: 'Pandora',
  IHEART: 'iHeartRadio',
  IHEARTRADIO: 'iHeartRadio',
};

// nativeServiceLabel returns the service name for a source enum, '' when STR
// does not keep that source on a key.
export function nativeServiceLabel(source) {
  return NATIVE_SERVICE_LABELS[String(source || '').trim().toUpperCase()] || '';
}

// isNativeServicePreset reports whether a stored preset is one of these keys.
export function isNativeServicePreset(p) {
  return !!p && p.type === 'native';
}

// nativeServiceSaveable reports whether a hold-to-save while the speaker plays
// `source` should store a native-service key.
export function nativeServiceSaveable(source) {
  return nativeServiceLabel(source) !== '';
}

// nativeServiceBadge is the service name a native tile shows: the label the
// agent stored, else the one derived from the stored item.
export function nativeServiceBadge(p) {
  if (!isNativeServicePreset(p)) return '';
  if (p.source) return String(p.source);
  return nativeServiceLabel(p.native && p.native.source);
}

// nativeServiceActive reports whether a native key is what the speaker plays:
// its stored location is the speaker's now-playing location.
export function nativeServiceActive(p, nowLocation) {
  return isNativeServicePreset(p) && !!nowLocation && !!p.native &&
    !!p.native.location && p.native.location === nowLocation;
}
