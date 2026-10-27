// How a user reaches a view in the UI 2.0 preview, for the specs that open a
// view by its classic name. Since phase 3 three classic tabs live under one
// sidebar entry, Speakers (src/shell2/speakers/routes.js):
//
//   settings   Speakers > the current speaker's row (its page)
//   multiroom  Speakers > Groups
//   setup      Speakers > Add speaker
//
// Every other view has its own sidebar entry. The caller waits for what the
// view shows once loaded (each spec knows its own marker); with a fake clock
// it advances the clock between the clicks (tick).

const AREA_PLACE = {
  settings: '#view-speakers .spl-open[aria-current="true"]',
  multiroom: '#ui2SpkSubnav .spa-sub[data-route="groups"]',
  setup: '#ui2SpkSubnav .spa-sub[data-route="add"]',
};

export async function openUi2View(page, view, { tick } = {}) {
  const place = AREA_PLACE[view];
  await page.locator(`#ui2Sidebar .ui2-nav-item[data-view="${place ? 'speakers' : view}"]`).click();
  if (!place) return;
  if (tick) await tick();
  await page.locator(place).click();
}
