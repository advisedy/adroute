import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, chmod, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawn, execFileSync } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { chromium, expect } from '@playwright/test';

const root = resolve(new URL('..', import.meta.url).pathname);

export async function goBrowserFixture(t, { viewport = { width: 1440, height: 1000 }, initialState } = {}) {
  const temp = await mkdtemp(join(tmpdir(), 'adroute-go-browser-'));
  const bin = join(temp, 'adroute');
  execFileSync('go', ['build', '-o', bin, './cmd/adroute'], { cwd: root, stdio: 'pipe' });
  const home = join(temp, 'adroute-home');
  const codexHome = join(temp, 'codex-home');
  await mkdir(home, { mode: 0o700 }); await mkdir(codexHome, { mode: 0o700 });
  if (initialState !== undefined) {
    const config = join(home, 'config.json');
    await writeFile(config, `${JSON.stringify(initialState, null, 2)}\n`, { mode: 0o600 });
    await chmod(config, 0o600);
  }
  const fakeCodex = join(temp, 'codex');
  await writeFile(fakeCodex, '#!/bin/sh\necho "codex-cli 0.156.0"\nexit 0\n', { mode: 0o700 }); await chmod(fakeCodex, 0o700);
  const env = { ...process.env, HOME: temp, XDG_CONFIG_HOME: join(temp, 'xdg'), ADROUTE_HOME: home, CODEX_HOME: codexHome, ADROUTE_CODEX_BIN: fakeCodex, ADROUTE_PORT: '0' };
  const child = spawn(bin, ['start', '--foreground', '--port', '0'], { cwd: temp, env, stdio: ['ignore', 'pipe', 'pipe'] });
  let output = '', errors = '';
  child.stdout.on('data', b => output += b.toString()); child.stderr.on('data', b => errors += b.toString());
  let url, token;
  for (let i = 0; i < 150 && !url; i++) {
    const m = output.match(/管理页面：http:\/\/127\.0\.0\.1:(\d+)\/#token=([a-f0-9]+)/);
    if (m) { url = `http://127.0.0.1:${m[1]}`; token = m[2]; break; }
    if (child.exitCode !== null) break;
    await delay(40);
  }
  assert.ok(url, `Go binary did not become ready. stdout=${output} stderr=${errors}`);
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ viewport });
  const page = await context.newPage(); page.setDefaultTimeout(8000);
  const pageErrors = []; page.on('pageerror', e => pageErrors.push(e.message));
  await page.route('**/*', r => ['127.0.0.1', 'localhost'].includes(new URL(r.request().url()).hostname) ? r.continue() : r.abort());
  const api = async (path, method = 'GET', body) => fetch(`${url}${path}`, { method, headers: { authorization: `Bearer ${token}`, ...(body ? { 'content-type': 'application/json' } : {}) }, body: body ? JSON.stringify(body) : undefined });
  const state = async () => (await api('/api/state')).json();
  const login = async () => { await page.goto(`${url}/#token=${token}`); await expect(page.locator('#app-shell')).toBeVisible(); await expect(page.locator('#initial-loading')).toBeHidden(); };
  t.after(async () => {
    await browser.close();
    if (child.exitCode === null) { child.kill('SIGTERM'); await Promise.race([new Promise(r => child.once('exit', r)), delay(4000)]); }
    assert.deepEqual(pageErrors, [], 'No uncaught frontend errors');
    await rm(temp, { recursive: true, force: true });
  });
  return { temp, bin, home, codexHome, url, token, child, page, context, api, state, login };
}

export async function goSavedGroup(f, name) {
  const before = await f.state();
  if (before.groups.some(group => group.name === name)) return before.groups.find(group => group.name === name);
  const response = await f.api('/api/groups', 'POST', { name });
  const body = await response.text();
  assert.equal(response.status, 201, body);
  return JSON.parse(body).groups.find(group => group.name === name);
}

export async function goSavedModel(f, {
  group = 'work', name = 'coding', baseUrl = 'https://synthetic.invalid/v1',
  key = 'synthetic-key', model = 'synthetic-model', disabled = false, isDefault = false,
} = {}) {
  const savedGroup = await goSavedGroup(f, group);
  const response = await f.api('/api/models', 'POST', { groupId: savedGroup.id, name, model, baseUrl, apiKey: key, disabled });
  const body = await response.text();
  assert.equal(response.status, 201, body);
  const state = JSON.parse(body);
  const created = state.models.find(item => item.groupId === savedGroup.id && item.name === name);
  if (isDefault) {
    const set = await f.api('/api/default', 'POST', { modelId: created.id });
    assert.equal(set.status, 200, await set.text());
  }
  return created;
}

export function seedState({ group = 'seed', groupId = 'seed-group', name = 'seeded', model = 'seeded-model', baseUrl = 'https://seed.invalid/v1', apiKey = 'seed-key', disabled = false, isDefault = false } = {}) {
  return {
    version: 6,
    revision: 1,
    groups: [{ id: groupId, name: group }],
    models: [{ id: 'seed-model', groupId, name, model, baseUrl, apiKey, profile: 'none', ...(disabled ? { disabled: true } : {}) }],
    aggregates: [],
    ...(isDefault ? { defaultModelId: 'seed-model' } : {}),
  };
}
