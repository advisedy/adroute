import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdir, readFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { expect } from '@playwright/test';
import { goBrowserFixture, goSavedModel } from './go-fixture.js';

const row = (page, id) => page.locator(`.model-row[data-model-id="${id}"]`);
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };

async function refresh(page) {
  const response = page.waitForResponse(r => r.url().endsWith('/api/state'));
  // A native modal makes the toolbar inert; returning to the tab still invokes
  // the real visibility refresh while an edit dialog is open.
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
  await response;
}

test('illegal names are rejected without trimming or writes and conflicts identify the name field', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  await goSavedModel(f);
  await f.login();
  let modelWrites = 0, groupWrites = 0;
  page.on('request', r => { if (r.method() === 'POST' && r.url().endsWith('/api/models')) modelWrites++; if (r.method() === 'POST' && r.url().endsWith('/api/groups')) groupWrites++; });
  await page.locator('#add-model').click();
  await page.locator('#model-group').selectOption((await f.state()).groups[0].id);
  await page.locator('#model-base-url').fill('https://synthetic.invalid/v1');
  await page.locator('#model-api-key').fill('synthetic');
  await page.locator('#model-real-id').fill('real-model');
  await page.locator('#model-name').fill(' white ');
  await page.locator('#model-save').click();
  await expect(page.locator('#model-error-name')).toBeVisible();
  await expect(page.locator('#model-name')).toHaveValue(' white ');
  assert.equal(modelWrites, 0, 'invalid name must not be silently fixed and sent');
  await page.locator('#model-new-group').fill(' bad-group ');
  await page.locator('#model-create-group').click();
  await expect(page.locator('#model-error-newgroup')).toBeVisible();
  assert.equal(groupWrites, 0);
  await page.locator('#model-name').fill('coding');
  await page.locator('#model-save').click();
  await expect(page.locator('#model-error-name')).toContainText('同名');
  assert.equal(modelWrites, 1);
  assert.equal((await f.state()).models.length, 1);
});

test('a pending save remains single-flight after state refresh and programmatic resubmit', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  const model = await goSavedModel(f);
  await f.login();
  const gate = deferred(), seen = deferred(); let writes = 0;
  t.after(() => gate.resolve());
  await page.route(`**/api/models/${model.id}`, async route => {
    if (route.request().method() !== 'PUT') return route.continue();
    writes++; seen.resolve(); await gate.promise; await route.continue().catch(() => {});
  });
  await row(page, model.id).getByRole('button', { name: '编辑', exact: true }).click();
  await page.locator('#model-real-id').fill('new-upstream');
  await page.locator('#model-save').click(); await seen.promise;
  await refresh(page);
  await expect(page.locator('#model-save')).toBeDisabled();
  await page.locator('#model-form').evaluate(form => form.requestSubmit());
  assert.equal(writes, 1);
  gate.resolve();
  await expect(page.locator('#model-dialog')).toBeHidden();
  assert.equal((await f.state()).models[0].model, 'new-upstream');
  assert.equal(writes, 1);
});

test('aggregate edits can be saved again after reopening and followed by a new aggregate', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  const model = await goSavedModel(f);
  const created = await f.api('/api/aggregates', 'POST', { name: 'fallback', members: [model.id], maxRounds: 5 });
  assert.equal(created.status, 201);
  const aggregate = (await created.json()).aggregates[0];
  const edit = () => page.locator(`[data-aggregate-id="${aggregate.id}"]`).getByRole('button', { name: '编辑', exact: true }).click();
  await f.login();
  await page.locator('nav [data-tab="aggregates"]').click();
  for (const rounds of ['3', '7']) {
    await edit();
    await page.locator('#aggregate-rounds').fill(rounds);
    await expect(page.locator('#aggregate-save')).toBeEnabled();
    await page.locator('#aggregate-save').click();
    await expect(page.locator('#aggregate-dialog')).toBeHidden();
    assert.equal((await f.state()).aggregates[0].maxRounds, Number(rounds));
  }
  await page.locator('#add-aggregate').click();
  await page.locator('#aggregate-name').fill('another-fallback');
  await page.locator('#aggregate-members select').nth(1).selectOption(model.id);
  await expect(page.locator('#aggregate-save')).toBeEnabled();
  await page.locator('#aggregate-save').click();
  await expect(page.locator('#aggregate-dialog')).toBeHidden();
  assert.equal((await f.state()).aggregates.length, 2);
});

test('aggregate saves stay single-flight across refresh and reopen, and failures allow retry', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  const model = await goSavedModel(f);
  for (const name of ['fallback', 'taken']) {
    assert.equal((await f.api('/api/aggregates', 'POST', { name, members: [model.id], maxRounds: 5 })).status, 201);
  }
  const aggregate = (await f.state()).aggregates.find(item => item.name === 'fallback');
  const edit = () => page.locator(`[data-aggregate-id="${aggregate.id}"]`).getByRole('button', { name: '编辑', exact: true }).click();
  const gate = deferred(), seen = deferred(); let writes = 0;
  t.after(() => gate.resolve());
  await page.route(`**/api/aggregates/${aggregate.id}`, async route => {
    if (route.request().method() !== 'PUT') return route.continue();
    writes++; seen.resolve(); await gate.promise; await route.continue().catch(() => {});
  });
  await f.login();
  await page.locator('nav [data-tab="aggregates"]').click();
  await edit();
  await page.locator('#aggregate-name').fill('taken');
  await page.locator('#aggregate-save').click(); await seen.promise;
  await refresh(page);
  await expect(page.locator('#aggregate-save')).toBeDisabled();
  await page.locator('#aggregate-dialog [data-close-dialog]').first().click();
  await edit();
  await expect(page.locator('#aggregate-save')).toBeDisabled();
  await page.locator('#aggregate-name').fill('taken');
  await page.locator('#aggregate-form').evaluate(form => form.requestSubmit());
  // Let the original dialog's request finish, then retry from the newer draft.
  gate.resolve();
  await expect(page.locator('#aggregate-save')).toBeEnabled();
  assert.equal(writes, 1);
  await expect(page.locator('#aggregate-dialog')).toBeVisible();
  await expect(page.locator('#aggregate-name')).toHaveValue('taken');
  await page.locator('#aggregate-save').click();
  await expect(page.locator('#aggregate-error')).toBeVisible();
  await expect(page.locator('#aggregate-save')).toBeEnabled();
  await page.locator('#aggregate-name').fill('fallback-updated');
  await page.locator('#aggregate-save').click();
  await expect(page.locator('#aggregate-dialog')).toBeHidden();
  assert.equal((await f.state()).aggregates.find(item => item.id === aggregate.id).name, 'fallback-updated');
  assert.equal(writes, 3);
});

test('a late inline group response updates global groups but never a newer model draft', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  const model = await goSavedModel(f);
  await f.login();
  const gate = deferred(), seen = deferred(); t.after(() => gate.resolve());
  await page.route('**/api/groups', async route => {
    const response = await route.fetch(); seen.resolve(); await gate.promise; await route.fulfill({ response }).catch(() => {});
  });
  await page.locator('#add-model').click();
  await page.locator('#model-new-group').fill('late-group');
  await page.locator('#model-create-group').click(); await seen.promise;
  await page.locator('#model-dialog [data-close-dialog]').first().click();
  await page.locator('#add-model').click();
  await page.locator('#model-group').selectOption(model.groupId);
  await page.locator('#model-name').fill('new-draft');
  gate.resolve();
  await expect(page.locator('#group-filter')).toContainText('late-group');
  await expect(page.locator('#model-group')).toHaveValue(model.groupId);
  await expect(page.locator('#model-name')).toHaveValue('new-draft');
});

test('default selection stays single-flight across refresh and later intentional selection still works', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  const a = await goSavedModel(f, { name: 'alpha' }), b = await goSavedModel(f, { name: 'beta' });
  await f.login();
  const gate = deferred(), seen = deferred(); let writes = 0; t.after(() => gate.resolve());
  await page.route('**/api/default', async route => { writes++; seen.resolve(); await gate.promise; await route.continue().catch(() => {}); });
  await page.locator('#default-model').selectOption(a.id); await seen.promise;
  await refresh(page);
  await expect(page.locator('#default-model')).toBeDisabled();
  await page.locator('#default-model').evaluate((select, id) => { select.value = id; select.dispatchEvent(new Event('change')); }, b.id);
  assert.equal(writes, 1);
  gate.resolve();
  await expect(page.locator('#default-model')).toBeEnabled();
  await expect(page.locator('#default-model')).toHaveValue(a.id);
  await page.locator('#default-model').selectOption(b.id);
  await expect.poll(async () => (await f.state()).defaultModelId).toBe(b.id);
  assert.equal(writes, 2);
});

test('key-only server edits invalidate a completed quick-test result after refresh', { timeout: 60000 }, async t => {
  const up = createServer(async (req, res) => {
    for await (const chunk of req) { /* drain */ }
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ object: 'response', status: 'completed', output: [{ type: 'message', content: [{ type: 'output_text', text: 'OK' }] }], usage: { output_tokens: 8 } }));
  });
  await new Promise(resolve => up.listen(0, '127.0.0.1', resolve)); t.after(() => up.close());
  const f = await goBrowserFixture(t), { page } = f;
  const model = await goSavedModel(f, { baseUrl: `http://127.0.0.1:${up.address().port}/v1` });
  await f.login();
  await row(page, model.id).getByRole('button', { name: '测速', exact: true }).click();
  await expect(row(page, model.id)).toContainText('可用');
  const updated = await f.api(`/api/models/${model.id}`, 'PUT', { groupId: model.groupId, name: model.name, model: model.model, baseUrl: model.baseUrl, apiKey: 'new-synthetic-key' });
  assert.equal(updated.status, 200);
  await refresh(page);
  await expect(row(page, model.id).locator('.model-test')).toHaveText('');
});

test('a completed tool probe is never presented as evidence for a newly selected target', { timeout: 60000 }, async t => {
  const gate = deferred(), started = deferred(); t.after(() => gate.resolve());
  const up = createServer(async (req, res) => {
    for await (const chunk of req) { /* drain */ }
    res.writeHead(200, { 'content-type': 'text/event-stream' }); res.flushHeaders(); started.resolve(); await gate.promise;
    const call = { type: 'function_call', call_id: 'probe', name: 'adroute_probe', arguments: '{"marker":"adroute-test"}' };
    res.write(`data: ${JSON.stringify({ type: 'response.output_item.done', item: call })}\n\n`);
    res.end(`data: ${JSON.stringify({ type: 'response.completed', response: { status: 'completed', output: [call] } })}\n\n`);
  });
  await new Promise(resolve => up.listen(0, '127.0.0.1', resolve)); t.after(() => up.close());
  const f = await goBrowserFixture(t), { page } = f;
  const a = await goSavedModel(f, { name: 'alpha', baseUrl: `http://127.0.0.1:${up.address().port}/v1` }), b = await goSavedModel(f, { name: 'beta' });
  await f.login();
  await page.locator('nav [data-tab="compatibility"]').click();
  await expect(page.locator('#adapter-directory')).not.toHaveText('');
  await page.locator('#compat-model').selectOption(a.id);
  page.once('dialog', dialog => dialog.accept());
  await page.locator('#compat-structure-test').click(); await started.promise;
  await page.locator('#compat-model').selectOption(b.id);
  gate.resolve();
  await expect(page.locator('#compat-structure-test')).toBeEnabled();
  await expect(page.locator('#compat-structure-result')).toHaveText('');
});

test('failed client catalog publication is shown and can be retried without resaving the model', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  const catalog = join(f.home, 'codex', 'models.json');
  await f.login();
  await rm(catalog); await mkdir(catalog, { mode: 0o700 });
  const model = await goSavedModel(f);
  await refresh(page);
  await expect(page.locator('#catalog-notice')).toContainText('配置已保存');
  await page.locator('#catalog-retry').click();
  await expect(page.locator('#catalog-status')).toContainText('失败');
  assert.equal((await f.state()).models.length, 1);
  const gate = deferred(), seen = deferred(), settled = deferred(); t.after(() => gate.resolve());
  await page.route('**/api/state', async route => {
    const old = await route.fetch(); seen.resolve(); await gate.promise;
    await route.fulfill({ response: old }).catch(() => {}); settled.resolve();
  });
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange'))); await seen.promise;
  await rm(catalog, { recursive: true });
  await page.locator('#catalog-retry').click();
  await expect(page.locator('#catalog-notice')).toBeHidden();
  gate.resolve(); await settled.promise; await page.waitForLoadState('networkidle');
  await expect(page.locator('#catalog-notice')).toBeHidden();
  const contents = JSON.parse(await readFile(catalog, 'utf8'));
  assert.ok(contents.models.some(item => item.slug === 'adroute/work/coding'));
  assert.equal((await f.state()).models[0].id, model.id);
});

test('missing quick-test capability prevents inference', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  const model = await goSavedModel(f);
  let attempts = 0;
  page.on('request', request => { if (request.url().endsWith('/quick-test')) attempts++; });
  await page.route('**/api/state', async route => { const response = await route.fetch(); const state = await response.json(); delete state.capabilities.quickTest; await route.fulfill({ json: state }); });
  await f.login();
  const testButton = row(page, model.id).getByRole('button', { name: '测速', exact: true });
  await expect(testButton).toBeDisabled();
  await testButton.evaluate(button => button.click());
  assert.equal(attempts, 0);
});

test('group rename drafts survive ordinary refresh and an old dialog rename response', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  const alpha = await goSavedModel(f, { group: 'alpha' });
  await goSavedModel(f, { group: 'beta' });
  await f.login();
  const input = name => page.getByRole('textbox', { name: `组名 ${name}`, exact: true });
  await page.locator('#manage-groups').click();
  await input('beta').fill('beta-draft');
  await refresh(page);
  await expect(input('beta')).toHaveValue('beta-draft');
  const gate = deferred(), seen = deferred(); t.after(() => gate.resolve());
  await page.route(`**/api/groups/${alpha.groupId}`, async route => {
    const response = await route.fetch(); seen.resolve(); await gate.promise; await route.fulfill({ response }).catch(() => {});
  });
  await input('alpha').fill('alpha-new');
  page.once('dialog', dialog => dialog.accept());
  await input('alpha').locator('..').getByRole('button', { name: '改名', exact: true }).click(); await seen.promise;
  await page.locator('#groups-dialog [data-close-dialog]').first().click();
  await page.locator('#manage-groups').click();
  await input('beta').fill('beta-new-draft');
  gate.resolve();
  await expect(page.locator('#group-filter')).toContainText('alpha-new');
  await expect(input('beta')).toHaveValue('beta-new-draft');
});

test('repair binding writes are single-flight per model and preserve another target draft', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  const a = await goSavedModel(f, { name: 'alpha' }), b = await goSavedModel(f, { name: 'beta' });
  const pack = await (await f.api('/api/compatibility/template')).json();
  assert.equal((await f.api('/api/compatibility/packages', 'POST', pack)).status, 201);
  const ref = `${pack.id}@${pack.version}`;
  await f.login();
  await page.locator('nav [data-tab="compatibility"]').click();
  await expect(page.locator('#adapter-directory')).not.toHaveText('');
  await page.locator('#compat-model').selectOption(a.id);
  await page.locator('#compat-profile').selectOption(ref);
  const gate = deferred(), seen = deferred(); let writesA = 0; t.after(() => gate.resolve());
  await page.route(`**/api/compatibility/selection/${a.id}`, async route => {
    writesA++; seen.resolve(); await gate.promise; await route.continue().catch(() => {});
  });
  await page.locator('#compat-save').click(); await seen.promise;
  await expect(page.locator('#compat-disable')).toBeDisabled();
  await page.locator('#compat-disable').evaluate(button => button.dispatchEvent(new Event('click')));
  await page.locator('#compat-model').selectOption(b.id);
  await expect(page.locator('#compat-save')).toBeEnabled();
  await page.locator('#compat-profile').selectOption(ref);
  await page.locator('#compat-save').click();
  await expect(page.locator('#compat-save')).toBeEnabled();
  await expect.poll(async () => (await f.state()).models.find(model => model.id === b.id).profile).toBe(ref);
  await page.locator('#compat-profile').selectOption('none');
  gate.resolve();
  await expect.poll(async () => (await f.state()).models.find(model => model.id === a.id).profile).toBe(ref);
  await expect(page.locator('#compat-profile')).toHaveValue('none');
  assert.equal(writesA, 1, 'a pending save must not also submit a conflicting disable');
  await page.locator('#compat-model').selectOption(a.id);
  await expect(page.locator('#compat-disable')).toBeEnabled();
  await page.locator('#compat-disable').click();
  await expect.poll(async () => (await f.state()).models.find(model => model.id === a.id).profile).toBe('none');
  assert.equal(writesA, 2, 'a later intentional disable remains supported');
});
