// helpers/ui2radio.mjs: the UI 2.0 preview's Radio starts with an empty
// station list, as the current layout does (discussion #1587: a top list
// fetched at every start read as the last search coming back). A spec that
// needs station rows opens the top list the way a user does.
//
// With a fake clock, call it after settle: the button only shows once a
// speaker is chosen.

import { expect } from '@playwright/test';

export async function openTopList(page) {
  const top = page.locator('#topBtn');
  await top.click();
  await expect(top).toHaveAttribute('aria-pressed', 'true');
}
