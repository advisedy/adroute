import test from 'node:test';
import assert from 'node:assert/strict';
import { expect } from '@playwright/test';
import { goBrowserFixture, seedState } from './go-fixture.js';

test('action menus support keyboard navigation and restore focus when dismissed', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await f.login();
  const menu = f.page.locator('.workbench-heading .action-menu');
  const trigger = menu.locator('summary');
  await trigger.focus();
  await f.page.keyboard.press('ArrowDown');
  await expect(menu).toHaveAttribute('open');
  await expect(f.page.locator('#manage-groups')).toBeFocused();
  await f.page.keyboard.press('ArrowDown');
  await expect(f.page.locator('#import-config')).toBeFocused();
  await f.page.keyboard.press('End');
  await expect(f.page.locator('#export-config')).toBeFocused();
  await f.page.keyboard.press('Home');
  await expect(f.page.locator('#manage-groups')).toBeFocused();
  await f.page.keyboard.press('Escape');
  await expect(menu).not.toHaveAttribute('open');
  await expect(trigger).toBeFocused();
  await f.page.keyboard.press('ArrowUp');
  await expect(f.page.locator('#export-config')).toBeFocused();
});

test('status notifications can be dismissed without obscuring the model workbench', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await f.context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await f.login();
  await f.page.locator('.model-row .copy-route').click();
  const toast = f.page.locator('#toast');
  await expect(toast).toBeVisible();
  await expect(toast).toContainText('已复制');
  await toast.getByRole('button', { name: '关闭提示' }).click();
  await expect(toast).toBeHidden();
  await expect(f.page.locator('#model-search')).toBeVisible();
  assert.equal(await f.page.locator('.model-row[data-model-id]').count(), 1);
});

test('row menus skip unavailable actions and selecting a default still saves in both themes', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await f.login();
  for (const theme of ['dark', 'light']) {
    if (theme === 'light') await f.page.locator('#theme-toggle').click();
    const menu = f.page.locator('.model-row .action-menu');
    await menu.locator('summary').focus();
    await f.page.keyboard.press('ArrowDown');
    await expect(menu.getByRole('button', { name: '设为默认', exact: true })).toBeFocused();
    await f.page.keyboard.press('ArrowDown');
    await expect(menu.getByRole('button', { name: '复制', exact: true })).toBeFocused();
    await f.page.keyboard.press('ArrowDown');
    await expect(menu.getByRole('button', { name: '修改详细参数', exact: true })).toBeFocused();
    await f.page.keyboard.press('ArrowDown');
    await expect(menu.getByRole('button', { name: '归档', exact: true })).toBeFocused();
    await f.page.keyboard.press('Escape');
    await f.page.locator('#default-model').selectOption('seed-model');
    await expect.poll(async () => (await f.state()).defaultModelId).toBe('seed-model');
    await f.page.reload();
    await expect(f.page.locator('#default-model')).toHaveValue('seed-model');
    await f.page.locator('#default-model').selectOption('');
    await expect.poll(async () => (await f.state()).defaultModelId).toBe('');
  }
});
