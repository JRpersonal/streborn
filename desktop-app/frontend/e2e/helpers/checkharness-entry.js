// checkharness-entry.js: the browser side of helpers/checkharness.mjs.
//
// Bundled on its own (not part of the app build) and injected into the running
// app on the mock backend, so the setup wizard's check step
// (src/shell2/setup/check.js) is driven against the same Wails mock and CSS as
// the app while the wizard that hosts it is built in parallel. It mounts the
// step in an overlay and records what it reports.

import { renderCheckStep } from '../../src/shell2/setup/check.js';

window.__checkStep = {
  result: null,
  states: [],
  controller: null,
  mount(box) {
    let host = document.getElementById('e2eCheckHost');
    if (!host) {
      host = document.createElement('main');
      host.id = 'e2eCheckHost';
      host.setAttribute('aria-label', 'Check step');
      host.style.cssText = 'position:fixed;inset:0;z-index:99;overflow:auto;padding:24px 32px;background:var(--u-bg);';
      // Beside the app's confirmation modal (z-index 100), in the same
      // stacking context, so the modal opens above the step.
      const modal = document.getElementById('warnModal');
      if (modal && modal.parentNode) modal.parentNode.insertBefore(host, modal);
      else document.body.appendChild(host);
    }
    this.result = null;
    this.states = [];
    this.controller = new AbortController();
    renderCheckStep(host, {
      box,
      signal: this.controller.signal,
      onState: (s) => { this.states.push(s); },
    }).then((r) => { this.result = r; });
  },
  abort() {
    if (this.controller) this.controller.abort();
  },
};
