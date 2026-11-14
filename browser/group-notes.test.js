import test from 'node:test';
import assert from 'node:assert/strict';
import { expect } from '@playwright/test';
import { goBrowserFixture, goSavedModel, seedState } from './go-fixture.js';
import { textContrast } from './color-contrast.js';

const groupCard = (page, name) => page.locator('.model-group').filter({ has: page.getByRole('heading', { name, exact: true }) });
const groupMenu = (page, name) => groupCard(page, name).locator('.model-group-header .action-menu > summary');
const noteIcon = (page, name) => groupCard(page, name).locator('.group-note-trigger');
const noteCard = page => page.locator('.group-note-card');
const noteInput = page => page.locator('.group-note-card textarea');

async function openNote(page, name) {
  const card = groupCard(page, name);
  // Once the icon is on the header it is the shortest way in; the menu entry
  // then reads 关闭便签, so it is only used to bring the icon back.
  const icon = card.locator('.group-note-trigger');
  if (await icon.count()) {
    if (!(await page.locator('.group-note-card').isVisible())) await icon.click();
    return;
  }
  const menu = card.locator('.model-group-header .action-menu');
  if (!(await menu.evaluate(el => el.open))) await groupMenu(page, name).click();
  await card.locator('.model-group-header .action-menu-items').getByRole('button', { name: '打开便签', exact: true }).click();
}

test('group actions live in the row menu and archiving still works from it', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await f.login();
  const header = f.page.locator('.model-group-header');
  // The header only carries the menu and the collapse toggle.
  await expect(header.getByRole('button', { name: '归档分组', exact: true })).toHaveCount(0);
  await groupMenu(f.page, 'seed').click();
  const items = header.locator('.action-menu-items');
  await expect(items.getByRole('button', { name: '打开便签', exact: true })).toBeVisible();
  await expect(items.getByRole('button', { name: '归档分组', exact: true })).toBeVisible();
  await items.getByRole('button', { name: '归档分组', exact: true }).click();
  await expect(f.page.locator('.model-row[data-model-id]')).toHaveCount(0);
  await expect.poll(async () => (await f.state()).groups[0].archived).toBe(true);
  // Archived groups live under 已归档; the same menu offers the inverse action.
  await f.page.locator('#models-panel').getByRole('button', { name: '已归档', exact: true }).click();
  await groupMenu(f.page, 'seed').click();
  await expect(header.locator('.action-menu-items').getByRole('button', { name: '恢复分组', exact: true })).toBeVisible();
});

test('the menu action turns into 关闭便签 and closing it removes the icon', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await f.login();
  const items = () => f.page.locator('.model-group-header .action-menu-items');
  await groupMenu(f.page, 'seed').click();
  await items().getByRole('button', { name: '打开便签', exact: true }).click();
  await expect(noteCard(f.page)).toBeVisible();
  await expect(noteIcon(f.page, 'seed')).toHaveCount(1);
  // Reopening the menu must not dismiss the pinned memo, so the action reads
  // as its inverse and closing it is one more click.
  await groupMenu(f.page, 'seed').click();
  await expect(noteCard(f.page)).toBeVisible();
  await items().getByRole('button', { name: '关闭便签', exact: true }).click();
  await expect(noteCard(f.page)).toBeHidden();
  // Closing from the menu means "I do not want this on screen": the icon goes
  // away with the card, and the decision survives a reload.
  await expect(noteIcon(f.page, 'seed')).toHaveCount(0);
  await f.page.reload();
  await expect(f.page.locator('#model-list .model-row').first()).toBeVisible();
  await expect(noteIcon(f.page, 'seed')).toHaveCount(0);
  await groupMenu(f.page, 'seed').click();
  await expect(items().getByRole('button', { name: '打开便签', exact: true })).toBeVisible();
  await items().getByRole('button', { name: '打开便签', exact: true }).click();
  await expect(noteIcon(f.page, 'seed')).toHaveCount(1);
});

test('the menu reads 关闭便签 whenever the icon is present, even with the card closed', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await f.login();
  const items = () => f.page.locator('.model-group-header .action-menu-items');
  await groupMenu(f.page, 'seed').click();
  await items().getByRole('button', { name: '打开便签', exact: true }).click();
  await expect(noteIcon(f.page, 'seed')).toHaveCount(1);
  // Dismiss the card but keep the icon: the menu must still offer 关闭便签.
  await f.page.keyboard.press('Escape');
  await expect(noteCard(f.page)).toBeHidden();
  await groupMenu(f.page, 'seed').click();
  await expect(items().getByRole('button', { name: '关闭便签', exact: true })).toBeVisible();
  // That entry removes the icon rather than opening the card again.
  await items().getByRole('button', { name: '关闭便签', exact: true }).click();
  await expect(noteIcon(f.page, 'seed')).toHaveCount(0);
  await expect(noteCard(f.page)).toBeHidden();
  await groupMenu(f.page, 'seed').click();
  await expect(items().getByRole('button', { name: '打开便签', exact: true })).toBeVisible();
});

test('closing the card with Escape or a click outside keeps the icon', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await f.login();
  const items = () => f.page.locator('.model-group-header .action-menu-items');
  await groupMenu(f.page, 'seed').click();
  await items().getByRole('button', { name: '打开便签', exact: true }).click();
  await expect(noteCard(f.page)).toBeVisible();
  await f.page.keyboard.press('Escape');
  await expect(noteCard(f.page)).toBeHidden();
  await expect(noteIcon(f.page, 'seed')).toHaveCount(1);
  await noteIcon(f.page, 'seed').click();
  await expect(noteCard(f.page)).toBeVisible();
  await f.page.locator('.workbench-heading h2').click();
  await expect(noteCard(f.page)).toBeHidden();
  // Only the menu's 关闭便签 hides the icon; an incidental dismissal must not.
  await expect(noteIcon(f.page, 'seed')).toHaveCount(1);
});

test('a note icon appears only after opening the note, and the choice survives a reload', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await f.login();
  await expect(noteIcon(f.page, 'seed')).toHaveCount(0);
  await openNote(f.page, 'seed');
  await expect(noteIcon(f.page, 'seed')).toHaveCount(1);
  await f.page.keyboard.press('Escape');
  await f.page.reload();
  await expect(f.page.locator('#model-list .model-row').first()).toBeVisible();
  await expect(noteIcon(f.page, 'seed')).toHaveCount(1);
  // Another group is unaffected: the choice is per group.
  await goSavedModel(f, { group: 'other', name: 'second' });
  await f.page.reload();
  await expect(noteIcon(f.page, 'other')).toHaveCount(0);
});

test('hovering the icon shows the memo and leaving hides it', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await goSavedModel(f, { group: 'other', name: 'second' });
  await f.login();
  const response = await f.api('/api/groups/' + (await f.state()).groups[0].id + '/note', 'PUT', { note: '晚高峰会排队\n账单按月结算' });
  assert.equal(response.status, 200, await response.text());
  await f.page.reload();
  // A group that already carries a memo shows its icon in any browser, so the
  // text stays discoverable instead of being hidden behind a one-time choice.
  await expect(noteIcon(f.page, 'seed')).toHaveCount(1);
  await expect(noteCard(f.page)).toBeHidden();
  await noteIcon(f.page, 'seed').hover();
  await expect(noteCard(f.page)).toBeVisible();
  await expect(noteInput(f.page)).toHaveValue('晚高峰会排队\n账单按月结算');
  // Moving into the card keeps it open so the text stays selectable.
  await noteInput(f.page).hover();
  await expect(noteCard(f.page)).toBeVisible();
  // Leaving both closes it.
  await f.page.locator('.workbench-heading h2').hover();
  await expect(noteCard(f.page)).toBeHidden();
});

test('the popover is editable directly and saves shortly after typing stops', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await f.login();
  const groupId = (await f.state()).groups[0].id;
  await openNote(f.page, 'seed');
  await f.page.keyboard.press('Escape');
  await noteIcon(f.page, 'seed').hover();
  await noteInput(f.page).click();
  await noteInput(f.page).fill('第一行评价\n第二行续写');
  await expect(noteInput(f.page)).toBeFocused();
  // No explicit save button is needed; waiting past the debounce persists it.
  await expect.poll(async () => (await f.state()).groups.find(g => g.id === groupId).note, { timeout: 8000 }).toBe('第一行评价\n第二行续写');
  await expect(f.page.locator('.group-note-status')).toHaveText('已保存');
  await f.page.reload();
  await expect(noteIcon(f.page, 'seed')).toHaveCount(1);
  await noteIcon(f.page, 'seed').hover();
  await expect(noteInput(f.page)).toHaveValue('第一行评价\n第二行续写');
});

test('moving the pointer away mid-typing keeps the memo open until it is left', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await f.login();
  await openNote(f.page, 'seed');
  await f.page.keyboard.press('Escape');
  await noteIcon(f.page, 'seed').hover();
  await noteInput(f.page).click();
  await noteInput(f.page).fill('正在写的草稿');
  await f.page.locator('.workbench-heading h2').hover();
  // The pointer left before the debounce finished, but the field still has focus.
  await expect(noteCard(f.page)).toBeVisible();
  await expect(noteInput(f.page)).toHaveValue('正在写的草稿');
  await expect.poll(async () => (await f.state()).groups[0].note, { timeout: 8000 }).toBe('正在写的草稿');
});

test('an open memo survives the periodic state refresh without losing the draft', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await goSavedModel(f, { group: 'other', name: 'second' });
  await f.login();
  await openNote(f.page, 'seed');
  await f.page.keyboard.press('Escape');
  await noteIcon(f.page, 'seed').hover();
  await noteInput(f.page).click();
  await noteInput(f.page).fill('刷新时不该丢的字');
  await noteInput(f.page).evaluate(el => el.setSelectionRange(3, 3));
  const response = f.page.waitForResponse(r => r.url().endsWith('/api/state'));
  await f.page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
  await response;
  await expect(noteCard(f.page)).toBeVisible();
  await expect(noteInput(f.page)).toHaveValue('刷新时不该丢的字');
  assert.equal(await noteInput(f.page).evaluate(el => el.selectionStart), 3);
});

test('the memo popover is keyboard reachable and closes on Escape', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await f.login();
  await openNote(f.page, 'seed');
  const icon = noteIcon(f.page, 'seed');
  await icon.focus();
  await expect(noteCard(f.page)).toBeVisible();
  await expect(icon).toHaveAttribute('aria-expanded', 'true');
  await f.page.keyboard.press('Tab');
  await expect(noteInput(f.page)).toBeFocused();
  await f.page.keyboard.type('键盘写入');
  await f.page.keyboard.press('Escape');
  await expect(noteCard(f.page)).toBeHidden();
  await expect(icon).toBeFocused();
  await expect.poll(async () => (await f.state()).groups[0].note, { timeout: 8000 }).toBe('键盘写入');
});

test('touch devices open the memo on tap and close it on an outside tap', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState(), viewport: { width: 390, height: 844 }, hasTouch: true });
  await f.login();
  await openNote(f.page, 'seed');
  await f.page.keyboard.press('Escape');
  const icon = noteIcon(f.page, 'seed');
  await icon.tap();
  await expect(noteCard(f.page)).toBeVisible();
  await expect(noteInput(f.page)).toBeFocused();
  // Without hover, the card must not vanish while the pointer rests elsewhere.
  await f.page.locator('.page-heading h1').tap();
  await expect(noteCard(f.page)).toBeHidden();
  await icon.tap();
  await expect(noteCard(f.page)).toBeVisible();
  await icon.tap();
  await expect(noteCard(f.page)).toBeHidden();
});

test('the memo card stays readable in both themes and fits a phone screen', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState(), viewport: { width: 390, height: 844 } });
  await f.login();
  for (const theme of ['dark', 'light']) {
    if (theme === 'light') await f.page.locator('#theme-toggle').click();
    await openNote(f.page, 'seed');
    await noteInput(f.page).fill('这是一段用来检查对比度和换行的便签内容'.repeat(4));
    await noteInput(f.page).blur();
    await noteIcon(f.page, 'seed').hover();
    await expect(noteCard(f.page)).toBeVisible();
    const box = await noteCard(f.page).boundingBox();
    assert.ok(box.x >= 0 && box.x + box.width <= 390, `${theme} card must stay inside the viewport: ${JSON.stringify(box)}`);
    assert.ok(box.y >= 0 && box.y + box.height <= 844, `${theme} card must stay inside the viewport vertically`);
    assert.ok(await textContrast(noteInput(f.page)) >= 4.5, `${theme} memo text must reach 4.5 contrast`);
    assert.ok(await textContrast(f.page.locator('.group-note-status')) >= 4.5, `${theme} memo status must reach 4.5 contrast`);
    await f.page.keyboard.press('Escape');
    assert.ok(await f.page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${theme}: no horizontal overflow`);
  }
});

test('an older backend disables the memo action instead of writing', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  let writes = 0;
  await f.page.route('**/api/state', async route => {
    const response = await route.fetch(), body = await response.json();
    delete body.capabilities.groupNotes;
    await route.fulfill({ json: body });
  });
  await f.page.route('**/note', async route => { writes++; await route.continue(); });
  await f.login();
  await groupMenu(f.page, 'seed').click();
  const open = f.page.locator('.model-group-header .action-menu-items').getByRole('button', { name: '打开便签', exact: true });
  await expect(open).toBeDisabled();
  await open.evaluate(el => el.click());
  assert.equal(writes, 0);
});
