import test from 'node:test';
import assert from 'node:assert/strict';
import { expect } from '@playwright/test';
import { createServer } from 'node:http';
import { goBrowserFixture, goSavedModel, openRowActions } from './go-fixture.js';

const modelRow = (page, name) => page.locator('.model-row').filter({ hasText: name });

test('built Go binary: auth, add model, discovery, default, setup, compatibility, mobile and logout privacy', { timeout: 120000 }, async t => {
  const seen = [];
  const upstream = createServer(async (req, res) => {
    seen.push({ path: req.url, auth: req.headers.authorization });
    if (req.url === '/v1/models') {
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({ data: [{ id: 'vendor/model' }, { id: 'simple-model' }] }));
      return;
    }
    res.statusCode = 404; res.end('{}');
  });
  await new Promise(resolve => upstream.listen(0, '127.0.0.1', resolve));
  t.after(() => upstream.close());
  const baseUrl = `http://127.0.0.1:${upstream.address().port}/v1`;
  const f = await goBrowserFixture(t); const { page } = f;

  await page.goto(f.url); await expect(page.locator('#auth-screen')).toBeVisible();
  await page.locator('#admin-token').fill('invalid-token'); await page.locator('#auth-submit').click();
  await expect(page.locator('#auth-error')).toBeVisible();
  await f.login();
  assert.equal(new URL(page.url()).hash, '');
  await expect(page.locator('#client-api-key')).toBeHidden();
  await page.locator('nav [data-tab="setup"]').click();
  await expect(page.locator('#client-base-url')).toHaveValue(/\/v1$/);
  await expect(page.locator('#client-api-key')).toHaveAttribute('type', 'password');
  await page.locator('#toggle-client-api-key').click();
  await expect(page.locator('#client-api-key')).toHaveAttribute('type', 'text');
  const proxyToken = await page.locator('#client-api-key').inputValue();
  assert.equal(proxyToken.length, 64);
  await page.locator('#toggle-client-api-key').click();
  await expect(page.locator('#client-api-key')).toHaveAttribute('type', 'password');

  await page.locator('nav [data-tab="models"]').click();
  // Add a model through the single form, creating its group inline.
  await page.locator('#add-model').click();
  await expect(page.locator('#model-dialog')).toBeVisible();
  await page.locator('#model-new-group').fill('work');
  await page.locator('#model-create-group').click();
  await expect(page.locator('#model-group')).toHaveValue(/.+/);
  await page.locator('#model-base-url').fill(baseUrl);
  await page.locator('#model-api-key').fill('unsaved-draft-key');
  await page.locator('#model-discover').click();
  await expect(page.locator('#model-discover-results')).toContainText('vendor/model');
  assert.ok(seen.some(item => item.path === '/v1/models' && item.auth === 'Bearer unsaved-draft-key'), 'discovery uses the unsaved form credential');
  await page.locator('#model-discover-results').getByRole('button', { name: 'simple-model', exact: true }).click();
  await expect(page.locator('#model-real-id')).toHaveValue('simple-model');
  await expect(page.locator('#model-name')).toHaveValue('simple-model');
  await expect(page.locator('#model-route-preview')).toHaveText('adroute/work/simple-model');
  assert.equal((await f.state()).models.length, 0, 'discovery must not save anything');
  await page.locator('#model-save').click();
  await expect(page.locator('#model-dialog')).toBeHidden();
  let state = await f.state();
  assert.equal(state.models.length, 1);
  const created = state.models[0];
  assert.equal(created.routeId, 'adroute/work/simple-model');
  assert.equal(created.model, 'simple-model');
  assert.equal(created.apiKey, 'unsaved-draft-key');
  assert.equal(created.open, true);
  await expect(page.locator('#model-list')).toContainText('work / simple-model');
  await expect(page.locator('#model-list')).toContainText('adroute/work/simple-model');

  // A real model id containing "/" stays untouched for upstream, but the
  // defaulted model name is invalid and must be fixed by hand.
  await page.locator('#add-model').click();
  await page.locator('#model-group').selectOption(state.groups[0].id);
  await page.locator('#model-base-url').fill(baseUrl);
  await page.locator('#model-api-key').fill('second-key');
  await page.locator('#model-real-id').fill('vendor/model');
  await expect(page.locator('#model-name')).toHaveValue('vendor/model');
  await expect(page.locator('#model-error-name')).toBeHidden();
  await page.locator('#model-save').click();
  await expect(page.locator('#model-error-name')).toBeVisible();
  assert.equal((await f.state()).models.length, 1, 'invalid defaulted name must block saving');
  await page.locator('#model-name').fill('coding');
  await page.locator('#model-real-id').fill('vendor/other');
  await expect(page.locator('#model-name')).toHaveValue('coding', 'a user edit is never overwritten');
  await page.locator('#model-save').click();
  await expect(page.locator('#model-dialog')).toBeHidden();
  state = await f.state();
  assert.equal(state.models.length, 2);
  const coding = state.models.find(item => item.name === 'coding');
  assert.equal(coding.model, 'vendor/other');

  // Duplicate names in one group are rejected by the backend.
  await page.locator('#add-model').click();
  await page.locator('#model-group').selectOption(state.groups[0].id);
  await page.locator('#model-base-url').fill(baseUrl);
  await page.locator('#model-api-key').fill('third-key');
  await page.locator('#model-real-id').fill('whatever');
  await page.locator('#model-name').fill('coding');
  await page.locator('#model-save').click();
  await expect(page.locator('#model-error')).toContainText('同名');
  await page.locator('#model-dialog [data-close-dialog]').first().click();

  // Edit the connection only: the public id must stay stable.
  await modelRow(page, 'coding').getByRole('button', { name: '编辑', exact: true }).click();
  await page.locator('#model-real-id').fill('vendor/rotated');
  await page.locator('#model-save').click();
  await expect(page.locator('#model-dialog')).toBeHidden();
  state = await f.state();
  assert.equal(state.models.find(item => item.name === 'coding').routeId, 'adroute/work/coding');
  assert.equal(state.models.find(item => item.name === 'coding').model, 'vendor/rotated');

  // Default model select.
  await page.locator('#default-model').selectOption(created.id);
  await expect(page.locator('#default-status')).toContainText('默认模型已更新');
  assert.equal((await f.state()).defaultModelId, created.id);

  // Setup panel uses the real proxy token, never the admin token or upstream key.
  await page.locator('nav [data-tab="setup"]').click();
  await expect(page.locator('#setup-content')).toBeVisible();
  const setup = await f.api('/api/setup').then(r => r.json());
  const config = await page.locator('#codex-config').textContent();
  assert.ok(config.includes(setup.proxyToken));
  assert.ok(!config.includes(f.token));
  assert.ok(!config.includes('unsaved-draft-key'));

  // Compatibility panel loads and targets a single model.
  await page.locator('nav [data-tab="compatibility"]').click();
  await expect(page.locator('#adapter-directory')).not.toHaveText('');
  await page.locator('#compat-model').selectOption(created.id);
  await expect(page.locator('#compat-binding-help')).toContainText('work / simple-model');

  // Mobile layout must not overflow and must keep all three row buttons.
  await page.locator('nav [data-tab="models"]').click();
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile viewport has no horizontal overflow');
  const row = modelRow(page, 'coding');
  await expect(row.getByRole('button', { name: '编辑', exact: true })).toBeVisible();
  await expect(row.getByRole('button', { name: '测速', exact: true })).toBeVisible();
  await openRowActions(row);
  await expect(row.getByRole('button', { name: '删除', exact: true })).toBeVisible();

  await page.locator('#logout-button').click();
  await expect(page.locator('#auth-screen')).toBeVisible();
  assert.ok(!(await page.evaluate(() => JSON.stringify({ ...sessionStorage, ...localStorage }))).includes(f.token));
  await expect(page.locator('#codex-config')).toHaveText('');
  await expect(page.locator('#model-list')).toHaveText('');
  await expect(page.locator('#client-api-key')).toHaveValue('');
});

test('built Go binary: stale private prompt response is ignored after logout', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t); const { page } = f;
  let release; const gate = new Promise(resolve => { release = resolve; });
  let markSeen; const seen = new Promise(resolve => { markSeen = resolve; });
  let markSettled; const settled = new Promise(resolve => { markSettled = resolve; });
  await page.route('**/api/compatibility/prompt', async route => { markSeen(); await gate; await route.fulfill({ json: { prompt: 'LATE_PRIVATE_CONTEXT', directory: '/tmp/private-late', diagnosisAvailable: false } }).catch(() => {}); markSettled(); });
  await f.login();
  await page.locator('nav [data-tab="compatibility"]').click();
  await page.locator('#compat-copy-prompt').click(); await seen;
  await page.locator('#logout-button').click(); release();
  await settled; await page.waitForLoadState('networkidle');
  await expect(page.locator('#compat-prompt-text')).toHaveValue('');
  await expect(page.locator('body')).not.toContainText('LATE_PRIVATE_CONTEXT');
});

test('built Go binary: renaming changes the public id and deleting the default clears it', { timeout: 90000 }, async t => {
  const f = await goBrowserFixture(t); const { page } = f;
  const model = await goSavedModel(f, { group: 'pixel', name: 'daily', model: 'swe-2', isDefault: true });
  await f.login();
  await expect(page.locator('#default-model')).toHaveValue(model.id);
  await modelRow(page, 'daily').getByRole('button', { name: '编辑', exact: true }).click();
  await page.locator('#model-name').fill('renamed');
  page.once('dialog', dialog => dialog.dismiss());
  await page.locator('#model-save').click();
  await expect(page.locator('#model-dialog')).toBeVisible();
  let state = await f.state();
  assert.equal(state.models[0].name, 'daily', 'cancelling the rename confirmation changes nothing');
  page.once('dialog', dialog => dialog.accept());
  await page.locator('#model-save').click();
  await expect(page.locator('#model-dialog')).toBeHidden();
  state = await f.state();
  assert.equal(state.models[0].routeId, 'adroute/pixel/renamed');
  assert.equal(state.models[0].id, model.id, 'renaming keeps the internal entry id');

  await page.locator('#logout-button').click();
  await expect(page.locator('#auth-screen')).toBeVisible();
  await f.login();
  await openRowActions(modelRow(page, 'renamed'));
  await modelRow(page, 'renamed').getByRole('button', { name: '删除', exact: true }).click();
  await expect(page.locator('#delete-dialog')).toContainText('pixel / renamed');
  await expect(page.locator('#delete-dialog')).toContainText('adroute/pixel/renamed');
  await expect(page.locator('#delete-default-warning')).toBeVisible();
  await page.locator('#delete-dialog [data-close-dialog]').first().click();
  assert.equal((await f.state()).models.length, 1, 'cancelling the delete keeps the model');
  await openRowActions(modelRow(page, 'renamed'));
  await modelRow(page, 'renamed').getByRole('button', { name: '删除', exact: true }).click();
  await page.locator('#delete-submit').click();
  await expect(page.locator('#delete-dialog')).toBeHidden();
  state = await f.state();
  assert.equal(state.models.length, 0);
  assert.equal(state.defaultModelId, '', 'deleting the default clears it');
});

test('built Go binary: one click starts a quick test, sends one request and cannot be duplicated', { timeout: 120000 }, async t => {
  const requests = [];
  let release; const gate = new Promise(resolve => { release = resolve; });
  let markStarted; const started = new Promise(resolve => { markStarted = resolve; });
  const upstream = createServer(async (req, res) => {
    for await (const _ of req) { /* drain */ }
    requests.push({ path: req.url, auth: req.headers.authorization });
    markStarted();
    res.setHeader('content-type', 'text/event-stream');
    res.write('event: response.output_text.delta\ndata: {"type":"response.output_text.delta","delta":"OK"}\n\n');
    await gate;
    res.write('event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}],"usage":{"output_tokens":20}}}\n\n');
    res.end();
  });
  await new Promise(resolve => upstream.listen(0, '127.0.0.1', resolve));
  t.after(() => { release(); upstream.close(); });
  const f = await goBrowserFixture(t); const { page } = f;
  const model = await goSavedModel(f, { baseUrl: `http://127.0.0.1:${upstream.address().port}/v1`, model: 'vendor/model', key: 'upstream-secret' });
  await f.login();

  const row = modelRow(page, 'coding');
  await row.getByRole('button', { name: '测速', exact: true }).click();
  await expect(row).toContainText('测速中');
  await started;
  await expect(page.locator('dialog[open]')).toHaveCount(0);
  await expect(row.getByRole('button', { name: '编辑', exact: true })).toBeDisabled();
  await openRowActions(row);
  await expect(row.getByRole('button', { name: '删除', exact: true })).toBeDisabled();
  await expect(row.getByRole('button', { name: '测速中' })).toBeDisabled();
  // Re-render through a refresh keeps the same single in-flight attempt.
  await page.locator('#refresh-button').click();
  await expect(modelRow(page, 'coding')).toContainText('测速中');
  await modelRow(page, 'coding').getByRole('button', { name: '测速中' }).evaluate(element => element.click());
  assert.equal(requests.length, 1, 'one click sends exactly one request');
  release();
  await expect.poll(() => requests.length).toBe(1);
  await expect(modelRow(page, 'coding')).toContainText('可用');
  await expect(modelRow(page, 'coding')).toContainText('速度不可统计');
  assert.equal(requests.filter(item => item.path === '/v1/responses').length, 1, 'no automatic retry');
  assert.equal(requests[0].auth, 'Bearer upstream-secret');
  await expect(modelRow(page, 'coding').getByRole('button', { name: '编辑', exact: true })).toBeEnabled();
});

test('built Go binary: cancelling a running quick test aborts the upstream request', { timeout: 90000 }, async t => {
  let disconnected; const closed = new Promise(resolve => { disconnected = resolve; });
  let markStarted; const started = new Promise(resolve => { markStarted = resolve; });
  const upstream = createServer((req, res) => {
    req.resume();
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    res.write('event: response.created\ndata: {"type":"response.created","response":{"status":"in_progress"}}\n\n');
    markStarted();
    res.on('close', () => disconnected());
  });
  await new Promise(resolve => upstream.listen(0, '127.0.0.1', resolve));
  t.after(() => upstream.close());
  const f = await goBrowserFixture(t); const { page } = f;
  await goSavedModel(f, { baseUrl: `http://127.0.0.1:${upstream.address().port}/v1` });
  await f.login();
  await modelRow(page, 'coding').getByRole('button', { name: '测速', exact: true }).click();
  await started;
  await expect(modelRow(page, 'coding')).toContainText('测速中');
  await modelRow(page, 'coding').getByRole('button', { name: '取消', exact: true }).click();
  await closed;
  await expect(modelRow(page, 'coding')).toContainText('测速已取消');
  await expect(modelRow(page, 'coding').getByRole('button', { name: '编辑', exact: true })).toBeEnabled();
});

test('built Go binary: late quick-test responses remain private after logout and a new login', { timeout: 90000 }, async t => {
  const f = await goBrowserFixture(t); const { page } = f;
  let release; const gate = new Promise(resolve => { release = resolve; });
  let markSeen; const seen = new Promise(resolve => { markSeen = resolve; });
  let markSettled; const settled = new Promise(resolve => { markSettled = resolve; });
  await page.route('**/quick-test', async route => {
    markSeen(); await gate;
    await route.fulfill({ json: { outcome: 'ok', status: 200, durationMs: 900, firstTextMs: 0, textReturned: true, usageReported: true, outputTokens: 30, reasoningTokens: 0, textTokens: 30, textDurationMs: 900, textTokensPerSecond: 33.3, tokensPerSecond: 33.3, text: 'late-private-test-output', truncated: false, message: '可用。' } }).catch(() => {});
    markSettled();
  });
  await goSavedModel(f);
  await f.login();
  await modelRow(page, 'coding').getByRole('button', { name: '测速', exact: true }).click();
  await seen;
  await page.locator('#logout-button').click();
  await expect(page.locator('#auth-screen')).toBeVisible();
  release();
  await settled; await page.waitForLoadState('networkidle');
  await expect(page.locator('body')).not.toContainText('tokens/s');
  await expect(page.locator('body')).not.toContainText('late-private-test-output');
  await f.login();
  await expect(page.locator('#model-list')).toContainText('work / coding');
  await expect(page.locator('body')).not.toContainText('tokens/s');
  await expect(page.locator('body')).not.toContainText('late-private-test-output');
});

test('built Go binary: diagnostics require consent, capture the stream, replay locally and gate prompt sharing', { timeout: 120000 }, async t => {
  const upstream = createServer(async (req, res) => {
    for await (const _ of req) { /* drain */ }
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    for (const event of [
      { type: 'response.output_item.added', output_index: 0, item: { type: 'function_call', id: 'fc_real', call_id: 'call_real', name: 'exec_command', arguments: '' } },
      { type: 'response.output_item.done', output_index: 0, item: { type: 'function_call', id: 'fc_real', call_id: 'call_real', name: 'exec_command', arguments: '{"cmd":"printf synthetic"}' } },
      { type: 'response.completed', response: { status: 'completed', output: [{ type: 'function_call', id: 'fc_real', call_id: 'call_real', name: 'exec_command', arguments: '{"cmd":"printf synthetic"}' }] } },
    ]) res.write(`event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`);
    res.end();
  });
  await new Promise(resolve => upstream.listen(0, '127.0.0.1', resolve));
  t.after(() => upstream.close());
  const f = await goBrowserFixture(t); const { page } = f;
  const model = await goSavedModel(f, { baseUrl: `http://127.0.0.1:${upstream.address().port}/v1` });
  await f.login();
  const noConsent = await f.api('/api/compatibility/diagnosis', 'POST', { modelId: model.id, raw: true, consent: false }); assert.equal(noConsent.status, 400);
  const armed = await f.api('/api/compatibility/diagnosis', 'POST', { modelId: model.id, raw: true, consent: true }); assert.equal(armed.status, 200);
  const proxyToken = (await f.api('/api/setup').then(r => r.json())).proxyToken;
  const proxy = await fetch(`${f.url}/v1/responses`, { method: 'POST', headers: { authorization: `Bearer ${proxyToken}`, 'content-type': 'application/json' }, body: JSON.stringify({ model: 'adroute/work/coding', input: 'SYNTHETIC_PRIVATE_PROMPT', stream: true }) });
  assert.equal(proxy.status, 200); await proxy.text();
  const diagnosis = await f.api('/api/compatibility/diagnosis').then(r => r.json());
  assert.equal(diagnosis.status, 'ready');
  assert.ok(diagnosis.raw && diagnosis.events.length >= 1 && diagnosis.frames.length >= 1, JSON.stringify(diagnosis));
  assert.ok(!JSON.stringify(diagnosis).includes('SYNTHETIC_PRIVATE_PROMPT'), 'request prompt is never captured with response diagnostics');
  const pack = JSON.parse(await (await f.api('/api/compatibility/template')).text());
  await f.api('/api/compatibility/packages', 'POST', pack);
  const replay = await f.api('/api/compatibility/replay', 'POST', { ref: `${pack.id}@${pack.version}`, events: diagnosis.frames });
  assert.equal(replay.status, 200); assert.equal((await replay.json()).liveVerified, false);

  await page.locator('nav [data-tab="compatibility"]').click();
  await page.locator('#compat-model').selectOption(model.id);
  await page.locator('#diagnosis-details > summary').click();
  await page.locator('#diagnosis-view').click();
  await page.locator('#diagnosis-export-consent').check();
  const download = page.waitForEvent('download'); await page.locator('#diagnosis-download').click(); await download;
  await page.evaluate(() => Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async value => { window.copiedPrompt = value; } } }));
  await page.locator('#compat-copy-prompt').click();
  await expect(page.locator('#compat-prompt-status')).toContainText('已复制');
  const sharedPrompt = await page.evaluate(() => window.copiedPrompt);
  assert.ok(sharedPrompt.includes(`capture-${diagnosis.captureId}.json`));
  assert.ok(!sharedPrompt.includes('SYNTHETIC_PRIVATE_PROMPT'));
  await page.locator('#logout-button').click();
  await expect(page.locator('#diagnosis-preview')).toHaveText('');
});

test('built Go binary: client switches are single-flight and surface failures', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  await page.route('**/api/clients', route => route.fulfill({ json: {
    proxyUrl: 'http://127.0.0.1:1/v1',
    clients: [
      { id: 'codex', name: 'Codex', enabled: false, available: true, stale: false, detail: '未接入。' },
      { id: 'opencode', name: 'OpenCode', enabled: false, available: true, stale: false, detail: '未接入。' },
    ],
  } }));
  let calls = 0, release; const gate = new Promise(resolve => { release = resolve; });
  let received; const hit = new Promise(resolve => { received = resolve; });
  await page.route('**/api/clients/opencode/enabled', async route => { calls++; received(); await gate; await route.fulfill({ json: {
    proxyUrl: 'http://127.0.0.1:1/v1',
    clients: [
      { id: 'codex', name: 'Codex', enabled: false, available: true, stale: false, detail: '未接入。' },
      { id: 'opencode', name: 'OpenCode', enabled: true, available: true, stale: false, detail: '已接入。' },
    ],
  } }); });
  await f.login();
  await page.locator('nav [data-tab="setup"]').click();
  await expect(page.locator('#client-list')).toContainText('Codex');
  await expect(page.locator('#client-list')).toContainText('OpenCode');
  const openCode = page.locator('.client-row[data-client-id="opencode"]');
  const toggle = openCode.locator('.client-switch');
  await expect(toggle).not.toBeChecked();
  await toggle.click(); await hit;
  await expect(toggle).toBeDisabled();
  release(); await expect(openCode.locator('.client-state')).toContainText('已接入');
  assert.equal(calls, 1);

  await page.unroute('**/api/clients/opencode/enabled');
  await page.route('**/api/clients/opencode/enabled', route => route.fulfill({ status: 400, json: { error: { message: 'Synthetic configuration missing' } } }));
  page.once('dialog', dialog => dialog.accept());
  await openCode.locator('.client-switch').click();
  await expect(page.locator('#setup-error')).toContainText('Synthetic configuration missing');
});

test('built Go binary: old capabilities disable writes and compatibility operations', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t); let writes = 0;
  await f.page.route('**/api/state', async route => {
    const response = await route.fetch(); const state = await response.json();
    delete state.capabilities.models; delete state.capabilities.adapterDirectory;
    await route.fulfill({ json: state });
  });
  f.page.on('request', request => { if (request.url().includes('/api/') && request.method() !== 'GET') writes++; });
  await f.login();
  await expect(f.page.locator('#global-error')).toContainText('旧版本');
  await expect(f.page.locator('#add-model')).toBeDisabled();
  await f.page.locator('nav [data-tab="compatibility"]').click();
  await expect(f.page.locator('#compat-save')).toBeDisabled();
  assert.equal(writes, 0);
});

test('built Go binary: late state refresh cannot overwrite a newer saved edit', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t); const { page } = f;
  const model = await goSavedModel(f, { name: 'race' });
  let release; const gate = new Promise(resolve => { release = resolve; });
  let markSeen; const seen = new Promise(resolve => { markSeen = resolve; });
  await f.login();
  let markSettled; const settled = new Promise(resolve => { markSettled = resolve; });
  await page.route('**/api/state', async route => { const old = await route.fetch(); markSeen(); await gate; await route.fulfill({ response: old }).catch(() => {}); markSettled(); });
  await page.locator('#refresh-button').click(); await seen;
  await modelRow(page, 'race').getByRole('button', { name: '编辑', exact: true }).click();
  await page.locator('#model-name').fill('race-renamed');
  page.once('dialog', dialog => dialog.accept());
  await page.locator('#model-save').click();
  await expect(modelRow(page, 'race-renamed')).toHaveCount(1);
  release();
  await settled; await page.waitForLoadState('networkidle');
  await expect(modelRow(page, 'race-renamed')).toHaveCount(1);
  await expect(page.locator('.model-row')).toHaveCount(1);
  assert.equal((await f.state()).models[0].name, 'race-renamed');
  assert.equal(model.id, (await f.state()).models[0].id);
});
