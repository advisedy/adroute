import test from 'node:test';
import assert from 'node:assert/strict';
import { expect } from '@playwright/test';
import { goBrowserFixture, seedState } from './go-fixture.js';
import { textContrast } from './color-contrast.js';

test('sidebar follows the theme and keeps navigation readable in every state', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await f.login();
  for (const theme of ['dark', 'light']) {
    if (theme === 'light') await f.page.locator('#theme-toggle').click();
    const brightness = await f.page.locator('.sidebar').evaluate(el => {
      const channels = getComputedStyle(el).backgroundColor.match(/[\d.]+/g).map(Number);
      return channels.slice(0, 3).reduce((sum, channel) => sum + channel, 0) / 3;
    });
    assert.ok(theme === 'light' ? brightness > 230 : brightness < 64, `${theme} sidebar must follow the active theme, got brightness ${brightness}`);
    for (const selector of ['.brand', '.brand small', '.nav-label', '.sidebar-note code', '#logout-button']) {
      assert.ok(await textContrast(f.page.locator(selector)) >= 4.5, `${theme} ${selector} must remain readable`);
    }
    for (const tab of ['aggregates', 'models']) {
      const item = f.page.locator(`nav [data-tab="${tab}"]`);
      await item.hover();
      assert.ok(await textContrast(item) >= 4.5, `${theme} hovered ${tab} label must remain readable`);
      await item.click();
      await expect(f.page.locator(`#${tab}-panel`)).toBeVisible();
      await f.page.mouse.move(1000, 100);
      assert.ok(await textContrast(item) >= 4.5, `${theme} selected ${tab} label must remain readable`);
      assert.ok(await textContrast(item.locator('svg')) >= 3, `${theme} selected ${tab} icon must remain distinguishable`);
    }
  }
});

test('supporting workbench text keeps accessible contrast in both themes', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await f.login();
  for (const theme of ['dark', 'light']) {
    if (theme === 'light') await f.page.locator('#theme-toggle').click();
    for (const selector of ['.nav-label', '.sidebar-note code', '.page-footer', '.workbench-help summary', '.route-identity code']) {
      const ratio = await textContrast(f.page.locator(selector));
      assert.ok(ratio >= 4.5, `${theme} ${selector}: contrast ${ratio.toFixed(2)} must reach 4.5`);
    }
  }
});

test('visible form validation and login errors remain readable in both themes', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  for (const theme of ['dark', 'light']) {
    await f.login();
    if (theme === 'light') await f.page.locator('#theme-toggle').click();
    await f.page.locator('#add-model').click();
    await f.page.locator('#model-discover').click();
    const fieldError = f.page.locator('#model-error-baseurl');
    await expect(fieldError).toBeVisible();
    assert.ok(await textContrast(fieldError) >= 4.5, `${theme} validation errors must reach 4.5 contrast`);
    await f.page.keyboard.press('Escape');
    await f.page.locator('#logout-button').click();
    await f.page.locator('#admin-token').fill('invalid-admin-token');
    await f.page.locator('#auth-submit').click();
    const authError = f.page.locator('#auth-error');
    await expect(authError).toBeVisible();
    assert.ok(await textContrast(authError) >= 4.5, `${theme} login errors must reach 4.5 contrast`);
  }
});

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
