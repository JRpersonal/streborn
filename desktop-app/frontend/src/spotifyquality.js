// spotifyquality.js: what the per-speaker Spotify streaming-quality row shows.
//
// A speaker without an explicit choice follows the account plan: 320 kbps on
// Premium, 160 kbps otherwise (and while the plan is not known). The row says
// which bitrate is active and why, so "320 kbps (Premium default)" is visible
// where the setting lives. Kept free of DOM and API calls so it can be tested
// on its own; views/spotify.js renders it.

// The agent's reason codes (internal/spotify/quality.go) mapped to i18n keys.
const REASON_KEYS = {
  'chosen': 'spotify.qualityReasonChosen',
  'premium-default': 'spotify.qualityReasonPremium',
  'free-default': 'spotify.qualityReasonFree',
  'plan-unknown': 'spotify.qualityReasonUnknown',
};

export const QUALITY_I18N_KEYS = [
  'spotify.qualityAuto',
  'spotify.qualityActive',
  'spotify.qualityActivePlain',
  ...Object.values(REASON_KEYS),
];

// qualityReasonKey returns the i18n key for an agent reason code, or '' for
// an unknown code (a newer agent) or an older agent that sends none.
export function qualityReasonKey(reason) {
  return REASON_KEYS[reason] || '';
}

// qualityStatusText is the "Streaming quality: 320 kbps (Premium default)"
// line. Falls back to the plain form when the reason is not known.
export function qualityStatusText(st, t) {
  if (!st || !st.ok || !st.bitrate) return '';
  const key = qualityReasonKey(st.reason);
  if (!key) return t('spotify.qualityActivePlain', { kbps: st.bitrate });
  return t('spotify.qualityActive', { kbps: st.bitrate, reason: t(key) });
}

// qualityOptions lists the selector's options. An agent that knows the
// plan-dependent default offers "Automatic" (value 0) and selects the stored
// choice; an older agent keeps the original two options keyed on the bitrate.
export function qualityOptions(st, t) {
  if (st && st.autoSupported) {
    const choice = st.choice || 0;
    return [
      { value: 0, label: t('spotify.qualityAuto'), selected: choice === 0 },
      { value: 160, label: '160 kbps', selected: choice === 160 },
      { value: 320, label: '320 kbps (Premium)', selected: choice === 320 },
    ].concat(choice === 96 ? [{ value: 96, label: '96 kbps', selected: true }] : []);
  }
  const high = !!st && st.bitrate === 320;
  return [
    { value: 160, label: '160 kbps', selected: !high },
    { value: 320, label: '320 kbps (Premium)', selected: high },
  ];
}
