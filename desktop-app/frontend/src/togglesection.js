// The On/Off pair a speaker setting is switched with.
//
// Eight blocks of Speaker settings (resume on power-on, the display track and
// its clock, the display messages, the splash, the notices, the AirPlay
// optimisation, the SSH port) grew the same shape one after another: two
// toggle buttons, a paint that lights the one matching the speaker's state, an
// async read that hides the section when the speaker has no such setting, and
// a write followed by a toast or an error dialog. Each copy drifted a little,
// and none of them painted the OLD state back when the write failed: the
// highlight moved on the press, the error dialog came and went, and the
// section kept claiming a value the speaker had refused. The clock toggle got
// that repaint on its own in September; this module gives it to the rest.
//
// Pure on purpose: the element lookup, the i18n lookup and the toasts come in
// as deps, so vitest exercises the success path, the failed write and the
// hidden section without a DOM, the same way the *State() readers in
// boxstate.js are tested.
//
// opts:
//   sectionId        the element hidden (class "hidden") when the speaker has
//                    no such setting; a whole section, or one row of it
//   onId, offId      the two toggle buttons
//   get              async () => the agent's answer
//   stateFn          answer => { show, enabled, ...extra }: show false hides
//                    the section; enabled is true, false, or null for "the
//                    speaker did not say", which lights neither button
//   set              async (enabled) => the agent's answer
//   toastKey         the i18n key shown after a successful write, or a
//                    function of enabled returning one
//   onLabelKey, offLabelKey  optional i18n keys for the button labels; left
//                    out, the markup's labels stay
//   confirm          optional async (enabled) => boolean, asked before the
//                    highlight moves; false cancels the press
//   onLoaded         optional (state) => void after a successful read, for
//                    the extras a section shows next to the toggle
//   onPaint          optional (enabled) => void after every paint, for rows
//                    that follow the state
//   afterSet         optional (answer, enabled) => void after a successful
//                    write, before the toast
//   onGetError       optional state to paint when the read throws; left out,
//                    the read failure hides the section
// deps: { $, t, showToast, showError }
//
// Returns { paint, press, load, apply, ready, state }, or null when one of the
// three elements is missing (the section is not in this render).
export function wireToggleSection(opts, deps) {
  const { $, t, showToast, showError } = deps;
  const {
    sectionId, onId, offId, get, set, stateFn, toastKey,
    onLabelKey, offLabelKey, confirm, onLoaded, onPaint, afterSet, onGetError,
  } = opts;
  const section = $(sectionId);
  const onBtn = $(onId);
  const offBtn = $(offId);
  if (!section || !onBtn || !offBtn) return null;
  if (onLabelKey) onBtn.textContent = t(onLabelKey);
  if (offLabelKey) offBtn.textContent = t(offLabelKey);

  // What the highlight shows right now: true, false, or null for neither.
  let current = null;
  const paint = (enabled) => {
    current = enabled === true ? true : (enabled === false ? false : null);
    onBtn.classList.toggle('active', current === true);
    offBtn.classList.toggle('active', current === false);
    if (onPaint) onPaint(current);
  };
  const apply = (st) => {
    section.classList.toggle('hidden', !st.show);
    paint(st.enabled);
    if (onLoaded) onLoaded(st);
  };
  const load = async () => {
    let st;
    try {
      st = stateFn(await get());
    } catch {
      if (onGetError) apply(onGetError);
      else section.classList.add('hidden');
      return;
    }
    apply(st);
  };
  // The highlight moves before the speaker has answered, so the panel feels
  // immediate. It moves BACK when the write fails, or the panel claims a state
  // the speaker refused.
  const press = async (enabled) => {
    if (confirm && !(await confirm(enabled))) return;
    const before = current;
    paint(enabled);
    try {
      const r = await set(enabled);
      if (afterSet) afterSet(r, enabled);
      showToast(t(typeof toastKey === 'function' ? toastKey(enabled) : toastKey));
    } catch (e) {
      paint(before);
      showError(e);
    }
  };
  onBtn.onclick = () => press(true);
  offBtn.onclick = () => press(false);
  const ready = load();
  return { paint, press, load, apply, ready, state: () => current };
}
