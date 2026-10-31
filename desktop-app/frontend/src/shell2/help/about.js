// shell2/help/about.js: the links of the About section, as pure functions.
//
// The release-notes logic lived inside main.js renderFooter. It moved here so
// the classic footer and the UI 2.0 Help and info page open the same page for
// the same build: renderFooter calls releaseNotesURL too, with identical
// output. Pure: no DOM, no state, nothing at import.

export const DEFAULT_REPO = 'https://github.com/JRpersonal/streborn';
const SITE = 'https://st-reborn.de';

// repoURL is the project's GitHub address without a trailing slash, from the
// githubUrl AppInfo reports, the project's own address when it reports none.
export function repoURL(githubUrl) {
  return (githubUrl || DEFAULT_REPO).replace(/\/+$/, '');
}

// releaseNotesURL is where the version opens. For a clean tagged build that is
// the matching GitHub release page (which carries the generated "What's
// changed" notes); for a dev build (a version like v0.6.21-3-gabc-dirty) there
// is no tag page, so it falls back to the releases list.
export function releaseNotesURL(info) {
  const i = info || {};
  const repo = repoURL(i.githubUrl);
  const isTag = /^v\d+\.\d+\.\d+$/.test(i.version || '');
  return isTag ? `${repo}/releases/tag/${i.version}` : `${repo}/releases`;
}

// The website's legal pages. German is a first-class translation of the site;
// every other language reads the English pages.
export function privacyURL(locale) {
  return locale === 'de' ? `${SITE}/de/datenschutz` : `${SITE}/privacy`;
}

export function imprintURL(locale) {
  return locale === 'de' ? `${SITE}/de/impressum` : `${SITE}/imprint`;
}

// websiteURL is the site AppInfo names, the project site when it names none.
export function websiteURL(info) {
  return (info && info.websiteUrl) || SITE;
}
