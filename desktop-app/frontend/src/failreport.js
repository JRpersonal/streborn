// Pure helpers for the copyable failure account shown after a failed install
// or update. No DOM: the views import these, and the logic that keeps being
// got wrong (which hosts go into the bundle, what the report says about the
// file that was just written) is asserted without a browser.

// REPORT_SEND_MARKER is the first line of the closing block the Go side writes
// (see formatFailureReport in desktop-app/updatereport.go). It is matched here
// so the closing wish can be replaced with the concrete path once the user has
// actually saved a bundle. Kept as one short literal on both sides on purpose:
// a longer match would break the first time the sentence under it is reworded.
export const REPORT_SEND_MARKER = 'Please send this text to str@sichtbar-app.de';

// appendSavedBundlePath rewrites the report's closing block to name the file
// that was just written.
//
// The report used to end with "together with the diagnostic file if you were
// able to save one", which is a wish rather than a path: a user whose install
// failed has no idea where that file would be, so the mails arrive without it
// and the first reply is always the same request. Once the bundle exists, its
// full path goes into the text the user is about to copy, so the attachment is
// a file they can find.
export function appendSavedBundlePath(report, path) {
  const text = String(report || '');
  const p = String(path || '').trim();
  if (!p) return text;
  const closing =
    REPORT_SEND_MARKER + ' and attach this file:\n' + p + '\n';
  const at = text.indexOf(REPORT_SEND_MARKER);
  if (at < 0) return text.replace(/\s*$/, '\n\n') + closing;
  return text.slice(0, at) + closing;
}

// failReportSaveHosts builds the host list handed to SaveDiagnosticBundle.
//
// The speaker the failure is about comes first so its box-side logs are
// collected even when the list is later capped, and the other known speakers
// follow: on a LAN where one speaker fails and five work, the five that work
// are the control group. Falsy entries and duplicates are dropped, and an
// empty result is perfectly fine - the bundle still carries README + app.log,
// which is the whole point for a speaker that never became reachable.
export function failReportSaveHosts(box, boxes) {
  const out = [];
  const push = (h) => {
    const host = typeof h === 'string' ? h.trim() : '';
    if (host && out.indexOf(host) < 0) out.push(host);
  };
  push(box && box.host);
  (Array.isArray(boxes) ? boxes : []).forEach((b) => push(b && b.host));
  return out;
}
