// helpers/mockclient.mjs: the Node side of window.__strMock (testkit/desktop-mock.mjs).
//
// Predicates cannot be shipped into page.evaluate, so every filter runs here:
// the calls are pulled into Node and filtered there.
//
//   const n = await mock.count('Status', onHost('192.0.2.52'));
//   await mock.expectCalled('PlaySlot', { pred: (c) => c.args[2] === 1, times: 1 });

import { expect } from '@playwright/test';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

// onHost matches a binding call whose first argument (the speaker) is host.
export const onHost = (host) => (c) => c.args[0] === host;

export function mockClient(page) {
  const api = {
    // Every recorded call, or those of one binding: [{ name, args, t, seq }].
    async calls(name) {
      const all = await page.evaluate(() => window.__strMock.calls.slice());
      return name ? all.filter((c) => c.name === name) : all;
    },
    async count(name, pred) {
      return (await api.calls(name)).filter((c) => !pred || pred(c)).length;
    },
    async last(name, pred) {
      const list = (await api.calls(name)).filter((c) => !pred || pred(c));
      return list.length ? list[list.length - 1] : null;
    },
    // Forget the calls so far (the model state stays).
    resetCalls: () => page.evaluate(() => window.__strMock.resetCalls()),
    // Override a binding from now on; spec as in testkit/scenario.mjs.
    set: (name, spec) => page.evaluate(([n, s]) => window.__strMock.set(n, s), [name, spec]),
    // Deliver a backend event to the app's EventsOn listeners; resolves to
    // the number of listeners that got it.
    emit: (event, ...payload) => page.evaluate(([e, p]) => window.__strMock.emit(e, ...p), [event, payload]),
    // The most calls of one binding that were in flight at the same time.
    maxOpen: (name) => page.evaluate((n) => window.__strMock.maxOpen[n] || 0, name),
    // Listener counts per event name.
    events: () => page.evaluate(() => ({ ...window.__strMock.events })),
    // The model state: playing, item, volume, presets, zones per host.
    state: () => page.evaluate(() => window.__strMock.state),
    // Binding names called with no override and no model (strict scenarios).
    unmocked: () => page.evaluate(() => window.__strMock.unmocked.slice()),
    // Errors the page itself caught (window error / unhandledrejection).
    pageErrors: () => page.evaluate(() => window.__strMock.pageErrors.slice()),
    // Polls until the binding was called (times given: exactly that often).
    async expectCalled(name, { pred, times, timeout, message } = {}) {
      const poll = expect.poll(() => api.count(name, pred), { timeout, message: message || `${name} calls` });
      if (typeof times === 'number') await poll.toBe(times);
      else await poll.toBeGreaterThan(0);
    },
  };
  return api;
}

// expectDriftList compares the names a run hit with a checked-in allowlist
// (baselines/unmocked-*.json, a sorted string array). A name hit that is not
// listed fails: model it in testkit, or list it on purpose. A listed name that
// was not hit fails as stale: take it out. E2E_UPDATE_UNMOCKED=1 rewrites the
// file instead. Reviewers accept a rewrite only when the list got shorter.
export function expectDriftList(hit, baselineFile) {
  const file = fileURLToPath(new URL(`../${baselineFile}`, import.meta.url));
  const actual = [...new Set(hit)].sort();
  if (process.env.E2E_UPDATE_UNMOCKED) {
    writeFileSync(file, JSON.stringify(actual, null, 2) + '\n');
    return;
  }
  let listed = [];
  try { listed = JSON.parse(readFileSync(file, 'utf8')); } catch { listed = []; }
  const added = actual.filter((n) => !listed.includes(n));
  const stale = listed.filter((n) => !actual.includes(n));
  expect(added, `unmocked names not in ${baselineFile}: model them in testkit/ or list them (E2E_UPDATE_UNMOCKED=1)`).toEqual([]);
  expect(stale, `${baselineFile} is stale: these were not hit any more, remove them`).toEqual([]);
}
