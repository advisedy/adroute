import test from 'node:test';
import assert from 'node:assert/strict';
import { expect } from '@playwright/test';
import { goBrowserFixture, goSavedModel, openRowActions } from './go-fixture.js';

test('copy creates numbered independent models in the same group', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t);
  const original = await goSavedModel(f, { name: 'coding', model: 'vendor/model', key: 'original-key', isDefault: true });
  await f.login();
  const row = f.page.locator(`.model-row[data-model-id="${original.id}"]`);
  for (const name of ['coding-copy', 'coding-copy-2']) {
    await openRowActions(row);
    await row.getByRole('button', { name: '复制', exact: true }).click();
    await expect(f.page.getByRole('heading', { name, exact: true })).toBeVisible();
  }
  const state = await f.state();
  assert.equal(state.defaultModelId, original.id);
  for (const name of ['coding-copy', 'coding-copy-2']) {
    const copy = state.models.find(model => model.name === name);
    assert.equal(copy.groupId, original.groupId);
    assert.equal(copy.apiKey, 'original-key');
    assert.equal(copy.model, 'vendor/model');
    assert.equal(copy.baseUrl, original.baseUrl);
    assert.equal(copy.open, true);
    assert.notEqual(copy.id, original.id);
  }
  await f.page.setViewportSize({ width: 390, height: 844 });
  await openRowActions(row);
  await expect(row.getByRole('button', { name: '复制', exact: true })).toBeVisible();
  assert.ok(await f.page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
});

test('desktop sidebar stays in the viewport as the model list scrolls', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { viewport: { width: 1280, height: 600 } });
  for (let i = 0; i < 9; i++) await goSavedModel(f, { name: `model-${i}` });
  await f.login();
  const sidebar = f.page.locator('.sidebar');
  const before = await sidebar.boundingBox();
  await f.page.evaluate(() => window.scrollTo(0, document.body.scrollHeight));
  await expect.poll(() => f.page.evaluate(() => window.scrollY)).toBeGreaterThan(100);
  const after = await sidebar.boundingBox();
  assert.ok(Math.abs(after.y - before.y) < 1);
  const logout = await f.page.locator('#logout-button').boundingBox();
  assert.ok(logout.y >= 0 && logout.y + logout.height <= 600);
});

test('copy reports an overlong name without creating an entry', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t);
  const original = await goSavedModel(f, { name: 'x'.repeat(100), disabled: true });
  await f.login();
  const entry = f.page.locator(`.model-row[data-model-id="${original.id}"]`);
  await openRowActions(entry);
  await entry.getByRole('button', { name: '复制', exact: true }).click();
  await expect(f.page.locator('#global-error')).toContainText('超过 100 个字符');
  assert.equal((await f.state()).models.length, 1);
});
