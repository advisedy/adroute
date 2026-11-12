import test from 'node:test';
import assert from 'node:assert/strict';
import { expect } from '@playwright/test';
import { goBrowserFixture, goSavedModel, seedState } from './go-fixture.js';

const row = (page, id) => page.locator(`.model-row[data-model-id="${id}"]`);
const toggleOf = (page, id) => row(page, id).locator('input[data-availability]');

test('closing the default model needs confirmation, clears the default atomically and leaves the catalog', { timeout: 90000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState({ isDefault: true }) });
  const { page } = f;
  const mutations = [];
  page.on('request', r => { if (r.url().includes('/availability') && r.method() === 'PUT') mutations.push(r.postData()); });
  await f.login();

  await expect(row(page, 'seed-model')).toContainText('seed / seeded');
  await expect(row(page, 'seed-model')).toContainText('adroute/seed/seeded');
  await expect(row(page, 'seed-model')).toContainText('网页默认');
  await expect(toggleOf(page, 'seed-model')).toBeChecked();

  page.once('dialog', dialog => dialog.dismiss());
  await toggleOf(page, 'seed-model').click();
  await expect(toggleOf(page, 'seed-model')).toBeChecked();
  assert.equal(mutations.length, 0, 'cancelling must not send any mutation');
  let state = await f.state();
  assert.equal(state.defaultModelId, 'seed-model');
  assert.notEqual(state.models[0].disabled, true);

  page.once('dialog', dialog => dialog.accept());
  await toggleOf(page, 'seed-model').click();
  await expect.poll(async () => (await f.state()).models[0].disabled).toBe(true);
  await expect.poll(() => mutations.length).toBe(1);
  assert.deepEqual(JSON.parse(mutations[0]), { enabled: false });
  state = await f.state();
  assert.equal(state.defaultModelId, '', 'closing the default clears it in the same transaction');
  await expect(row(page, 'seed-model')).toContainText('已关闭');
  await expect(page.locator('#default-model')).toHaveValue('');

  const proxyToken = (await f.api('/api/setup').then(r => r.json())).proxyToken;
  const listed = await fetch(`${f.url}/v1/models`, { headers: { authorization: `Bearer ${proxyToken}` } }).then(r => r.json());
  assert.equal(listed.data.some(item => item.id === 'adroute/seed/seeded'), false, 'closed model leaves the public list');
  assert.equal(listed.data.some(item => item.id === 'adroute/default'), false, 'no default route without an open default');

  // Re-opening keeps the entry but never restores the default.
  await toggleOf(page, 'seed-model').click();
  await expect.poll(async () => (await f.state()).models[0].disabled).toBe(false);
  state = await f.state();
  assert.equal(state.defaultModelId, '');
  assert.equal(state.models[0].id, 'seed-model');
});

test('a stale edit draft cannot reopen a model the server closed meanwhile', { timeout: 90000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  const { page } = f;
  await f.login();
  await row(page, 'seed-model').getByRole('button', { name: '编辑', exact: true }).click();
  await expect(page.locator('#model-dialog')).toBeVisible();
  await expect(page.locator('#model-api-key')).toHaveValue('');
  // Server-side close while the form is open.
  const closed = await f.api('/api/models/seed-model/availability', 'PUT', { enabled: false });
  assert.equal(closed.status, 200);
  // Connection-only edit: no public-id change, therefore no rename confirmation.
  await page.locator('#model-real-id').fill('seeded-model-2');
  await page.locator('#model-save').click();
  await expect(page.locator('#model-dialog')).toBeHidden();
  const state = await f.state();
  assert.equal(state.models[0].model, 'seeded-model-2');
  assert.equal(state.models[0].name, 'seeded');
  assert.equal(state.models[0].disabled, true, 'ordinary save must not reopen a closed entry');
});

test('availability stays single-flight across re-render and refresh', { timeout: 90000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  const { page } = f;
  let release; const gate = new Promise(resolve => { release = resolve; });
  let markStarted; const started = new Promise(resolve => { markStarted = resolve; });
  let calls = 0;
  page.on('request', r => { if (r.url().includes('/availability') && r.method() === 'PUT') calls++; });
  await page.route('**/availability', async route => { markStarted(); await gate; await route.continue(); });
  await f.login();
  await toggleOf(page, 'seed-model').click();
  await started;
  await expect(toggleOf(page, 'seed-model')).toBeDisabled();
  await page.locator('#refresh-button').click();
  await expect(toggleOf(page, 'seed-model')).toBeDisabled();
  await toggleOf(page, 'seed-model').evaluate(element => element.click());
  assert.equal(calls, 1, 'a re-rendered control must not start a second request');
  release();
  await expect.poll(async () => (await f.state()).models[0].disabled).toBe(true);
  await expect(toggleOf(page, 'seed-model')).toBeEnabled();
  await expect(toggleOf(page, 'seed-model')).not.toBeChecked();
  assert.equal(calls, 1);
});

test('availability failure rolls back and a late response after logout restores nothing', { timeout: 90000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  const { page } = f;
  await page.route('**/availability', route => route.fulfill({ status: 500, json: { error: { message: 'synthetic failure' } } }));
  await f.login();
  await toggleOf(page, 'seed-model').click();
  await expect(page.locator('#toast')).toContainText('开放状态未更改');
  await expect(toggleOf(page, 'seed-model')).toBeChecked();
  await expect(toggleOf(page, 'seed-model')).toBeEnabled();
  assert.notEqual((await f.state()).models[0].disabled, true);

  let release; const gate = new Promise(resolve => { release = resolve; });
  let markStarted; const started = new Promise(resolve => { markStarted = resolve; });
  await page.unroute('**/availability');
  let markSettled; const settled = new Promise(resolve => { markSettled = resolve; });
  await page.route('**/availability', async route => { markStarted(); await gate; await route.fulfill({ json: seedState() }).catch(() => {}); markSettled(); });
  await toggleOf(page, 'seed-model').click();
  await started;
  await page.locator('#logout-button').click();
  await expect(page.locator('#auth-screen')).toBeVisible();
  release();
  await settled; await page.waitForLoadState('networkidle');
  await expect(page.locator('#model-list')).toHaveText('');
  await expect(page.locator('body')).not.toContainText('adroute/seed/seeded');
});

test('availability controls are disabled when the backend lacks the capability', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t, { initialState: seedState() });
  await f.page.route('**/api/state', async route => {
    const response = await route.fetch(); const state = await response.json();
    delete state.capabilities.modelAvailability;
    await route.fulfill({ json: state });
  });
  await f.login();
  await expect(f.page.locator('#global-error')).toContainText('不支持模型开放状态');
  await expect(toggleOf(f.page, 'seed-model')).toBeDisabled();
});

test('model availability is independent per entry, including same real model id', { timeout: 90000 }, async t => {
  const f = await goBrowserFixture(t);
  const first = await goSavedModel(f, { name: 'one', model: 'shared-real', key: 'key-one' });
  const second = await goSavedModel(f, { name: 'two', model: 'shared-real', key: 'key-two' });
  await f.login();
  await expect(row(f.page, first.id)).toContainText('adroute/work/one');
  await expect(row(f.page, second.id)).toContainText('adroute/work/two');
  await toggleOf(f.page, first.id).click();
  await expect.poll(async () => (await f.state()).models.find(item => item.id === first.id).disabled).toBe(true);
  const state = await f.state();
  assert.notEqual(state.models.find(item => item.id === second.id).disabled, true, 'the other entry stays open');
  await expect(toggleOf(f.page, second.id)).toBeChecked();
});
