import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { execFile, execFileSync, spawn } from 'node:child_process';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { mkdtempSync, mkdirSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { goBrowserFixture, goSavedModel } from '../browser/go-fixture.js';

const codex = process.env.ADROUTE_TEST_CODEX_BIN || 'codex';
let codexAvailable = false;
const codexTestRoot = join(new URL('..', import.meta.url).pathname, '.go-codex-test-homes');
mkdirSync(codexTestRoot, { recursive: true, mode: 0o700 });
const probeHome = mkdtempSync(join(codexTestRoot, 'version-probe-'));
try {
  const versionEnv = { PATH: process.env.PATH, HOME: probeHome, CODEX_HOME: probeHome, XDG_CONFIG_HOME: probeHome, LANG: 'C.UTF-8', TERM: 'dumb' };
  codexAvailable = execFileSync(codex, ['--version'], { encoding: 'utf8', timeout: 5000, env: versionEnv }).trim() === 'codex-cli 0.156.0';
} catch {} finally { rmSync(probeHome, { recursive: true, force: true }); }

function event(res, type, data) { const v = { type, ...data }; res.write(`event: ${type}\ndata: ${JSON.stringify(v)}\n\n`); }
function execute(executable, args, options) {
  return new Promise(resolve => {
    const child = execFile(executable, args, { timeout: 30000, maxBuffer: 4 * 1024 * 1024, ...options }, (error, stdout, stderr) => resolve({ error, stdout, stderr }));
    child.stdin.end();
  });
}

function codexToolReply(res, sequence, name, args, repair = '') {
  const item = { id: `fc_${sequence}`, type: 'function_call', call_id: 'call_synthetic', name, arguments: JSON.stringify(args), status: 'completed' };
  res.writeHead(200, { 'content-type': 'text/event-stream' });
  event(res, 'response.created', { response: { id: `r${sequence}`, object: 'response', status: 'in_progress', output: [] } });
  event(res, 'response.output_item.added', { output_index: 0, item: { ...item, status: 'in_progress', arguments: '' } });
  if (repair) {
    const ghost = { ...item, id: 'fc_ghost', call_id: 'call_ghost', name: '' };
    event(res, 'response.output_item.done', { output_index: 0, item: { ...item, arguments: '{}' } });
    event(res, 'response.output_item.added', { output_index: 1, item: { ...ghost, arguments: '' } });
    event(res, 'response.function_call_arguments.delta', { item_id: ghost.id, output_index: 1, delta: item.arguments });
    event(res, 'response.function_call_arguments.done', { item_id: ghost.id, output_index: 1, arguments: item.arguments });
    event(res, 'response.output_item.done', { output_index: 1, item: ghost });
    event(res, 'response.completed', { response: { id: `r${sequence}`, object: 'response', status: 'completed', output: [
      { ...item, arguments: '{}', ...(repair === 'v2' ? { id: 'fc_final' } : {}) },
      { ...ghost, ...(repair === 'v2' ? { id: 'fc_ghost_final' } : {}) },
    ] } });
  } else {
    event(res, 'response.function_call_arguments.delta', { item_id: item.id, output_index: 0, delta: item.arguments });
    event(res, 'response.function_call_arguments.done', { item_id: item.id, output_index: 0, arguments: item.arguments });
    event(res, 'response.output_item.done', { output_index: 0, item });
    event(res, 'response.completed', { response: { id: `r${sequence}`, object: 'response', status: 'completed', output: [item], usage: { input_tokens: 10, output_tokens: 10, total_tokens: 20 } } });
  }
  res.end();
}

async function goBinaryCodexConversation(t, { authMode = 'env', repair = '' } = {}) {
  const calls = [];
  const upstream = createServer(async (req, res) => {
    let raw = ''; for await (const chunk of req) raw += chunk;
    const body = JSON.parse(raw); calls.push(body);
    assert.equal(req.url, '/v1/responses'); assert.equal(req.headers.authorization, 'Bearer synthetic-upstream-key');
    assert.ok(calls.length <= 2, 'synthetic Codex conversation ends after the tool result');
    if (calls.length === 1) {
      const tool = body.tools.find(x => x.type === 'function' && ['exec_command', 'shell', 'shell_command'].includes(x.name));
      assert.ok(tool, 'Codex offered a synthetic shell tool');
      const args = tool.name === 'exec_command' ? { cmd: 'printf go-binary-roundtrip', max_output_tokens: 100 } : tool.name === 'shell' ? { command: ['sh', '-c', 'printf go-binary-roundtrip'], timeout_ms: 1000 } : { command: 'printf go-binary-roundtrip', timeout_ms: 1000 };
      codexToolReply(res, 1, tool.name, args, repair);
    }
    else {
      const item = { id: 'msg_2', type: 'message', role: 'assistant', status: 'completed', content: [{ type: 'output_text', text: 'GO_BINARY_CODEX_OK', annotations: [] }] };
      res.writeHead(200, { 'content-type': 'text/event-stream' });
      for (const [type, data] of [
        ['response.created', { response: { id: 'r2', object: 'response', status: 'in_progress', output: [] } }],
        ['response.output_item.added', { output_index: 0, item: { ...item, status: 'in_progress', content: [] } }],
        ['response.content_part.added', { item_id: item.id, output_index: 0, content_index: 0, part: { type: 'output_text', text: '', annotations: [] } }],
        ['response.output_text.delta', { item_id: item.id, output_index: 0, content_index: 0, delta: 'GO_BINARY_CODEX_OK' }],
        ['response.output_text.done', { item_id: item.id, output_index: 0, content_index: 0, text: 'GO_BINARY_CODEX_OK' }],
        ['response.content_part.done', { item_id: item.id, output_index: 0, content_index: 0, part: item.content[0] }],
        ['response.output_item.done', { output_index: 0, item }],
        ['response.completed', { response: { id: 'r2', object: 'response', status: 'completed', output: [item], usage: { input_tokens: 10, output_tokens: 5, total_tokens: 15 } } }],
      ]) event(res, type, data);
      res.end();
    }
  });
  await new Promise(resolve => upstream.listen(0, '127.0.0.1', resolve)); t.after(() => upstream.close());
  const f = await goBrowserFixture(t);
  const entry = await goSavedModel(f, { group: 'roundtrip', name: 'coding', baseUrl: `http://127.0.0.1:${upstream.address().port}/v1`, key: 'synthetic-upstream-key', model: 'synthetic-actual-model', isDefault: true });
  if (repair) {
    const filename = repair === 'v2' ? 'pixel-swe-v2.adroute-adapter.json' : 'pixel-swe.adroute-adapter.json';
    const pack = JSON.parse(await readFile(new URL(`../examples/${filename}`, import.meta.url), 'utf8'));
    assert.equal((await f.api('/api/compatibility/packages', 'POST', pack)).status, 201);
    assert.equal((await f.api(`/api/compatibility/selection/${entry.id}`, 'PUT', { profile: `${pack.id}@${pack.version}` })).status, 200);
  }
  const setup = await f.api('/api/setup').then(r => r.json());
  const repo = new URL('..', import.meta.url).pathname, codexRoot = join(repo, '.go-codex-test-homes'); await mkdir(codexRoot, { recursive: true, mode: 0o700 });
  const codexHome = await mkdtemp(join(codexRoot, 'home-')); t.after(() => rm(codexHome, { recursive: true, force: true }));
  const env = { PATH: process.env.PATH, HOME: f.temp, CODEX_HOME: codexHome, XDG_CONFIG_HOME: f.temp, LANG: 'C.UTF-8', TERM: 'dumb' };
  const catalogPath = setup.codexConfig.match(/^model_catalog_json = "(.*)"$/m)?.[1]; assert.ok(catalogPath, 'Go setup preview identifies the generated catalog');
  const providerConfig = `name="adroute",base_url="${f.url}/v1",wire_api="responses",requires_openai_auth=false,supports_websockets=false`;
  let configArgs;
  if (authMode === 'config') {
    await writeFile(join(codexHome, 'config.toml'), `model_provider = "adroute"\nmodel = "adroute/default"\nmodel_catalog_json = ${JSON.stringify(catalogPath)}\nweb_search = "disabled"\ncheck_for_update_on_startup = false\n[features]\nshell_snapshot = false\n[model_providers.adroute]\nname = "adroute"\nbase_url = "${f.url}/v1"\nwire_api = "responses"\nexperimental_bearer_token = "${setup.proxyToken}"\nrequires_openai_auth = false\nsupports_websockets = false\n`, { mode: 0o600, flag: 'wx' });
    configArgs = [];
  } else {
    env.ADROUTE_PROXY_TOKEN = setup.proxyToken;
    configArgs = ['--ignore-user-config', '-m', 'adroute/default', '-c', 'model_provider="adroute"', '-c', `model_providers.adroute={${providerConfig},env_key="ADROUTE_PROXY_TOKEN"}`, '-c', `model_catalog_json=${JSON.stringify(catalogPath)}`, '-c', 'web_search="disabled"', '-c', 'check_for_update_on_startup=false', '-c', 'features.shell_snapshot=false'];
  }
  const exeArgs = ['exec', '--ignore-rules', '--ephemeral', '--skip-git-repo-check', '--sandbox', 'read-only', '--color', 'never', '--json', '-C', f.temp, ...configArgs, 'Run only the harmless printf command requested in the tool call, then finish.'];
  const result = await execute(codex, exeArgs, { env, cwd: f.temp, timeout: 60000 });
  assert.equal(result.error, null, `${result.stdout}\n${result.stderr}`);
  assert.equal(calls.length, 2, `${result.stdout}\n${result.stderr}`);
  assert.ok(result.stdout.includes('GO_BINARY_CODEX_OK'), result.stdout);
  const output = calls[1].input.find(x => x.type === 'function_call_output' && x.call_id === 'call_synthetic');
  assert.ok(output && JSON.stringify(output).includes('go-binary-roundtrip'), JSON.stringify(calls[1]));
  for (const secret of [setup.proxyToken, 'synthetic-upstream-key']) assert.ok(!JSON.stringify(calls).includes(secret));
}

test('official Codex 0.156.0 against built Go binary completes a synthetic tool roundtrip with environment bearer auth', { skip: !codexAvailable && 'optional official Codex 0.156.0 executable is unavailable', timeout: 90000 }, async t => {
  const calls = [];
  const upstream = createServer(async (req, res) => {
    let raw = ''; for await (const chunk of req) raw += chunk;
    assert.equal(req.url, '/v1/responses'); assert.equal(req.headers.authorization, 'Bearer synthetic-upstream-key');
    const body = JSON.parse(raw); calls.push(body);
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    if (calls.length === 1) {
      const tool = body.tools.find(x => x.type === 'function' && ['exec_command', 'shell', 'shell_command'].includes(x.name));
      assert.ok(tool, 'Codex offers the expected harmless local shell tool');
      const args = tool.name === 'exec_command' ? { cmd: 'printf go-binary-roundtrip', max_output_tokens: 100 } : tool.name === 'shell' ? { command: ['sh', '-c', 'printf go-binary-roundtrip'], timeout_ms: 1000 } : { command: 'printf go-binary-roundtrip', timeout_ms: 1000 };
      const item = { id: 'fc_synthetic', type: 'function_call', call_id: 'call_synthetic', name: tool.name, arguments: JSON.stringify(args), status: 'completed' };
      event(res, 'response.created', { response: { id: 'r1', status: 'in_progress', output: [] } });
      event(res, 'response.output_item.added', { output_index: 0, item: { ...item, status: 'in_progress', arguments: '' } });
      event(res, 'response.function_call_arguments.delta', { item_id: item.id, output_index: 0, delta: item.arguments });
      event(res, 'response.function_call_arguments.done', { item_id: item.id, output_index: 0, arguments: item.arguments });
      event(res, 'response.output_item.done', { output_index: 0, item });
      event(res, 'response.completed', { response: { id: 'r1', status: 'completed', output: [item] } });
    } else {
      const item = { id: 'm2', type: 'message', role: 'assistant', status: 'completed', content: [{ type: 'output_text', text: 'GO_BINARY_CODEX_OK', annotations: [] }] };
      event(res, 'response.created', { response: { id: 'r2', status: 'in_progress', output: [] } });
      event(res, 'response.output_item.added', { output_index: 0, item: { ...item, status: 'in_progress', content: [] } });
      event(res, 'response.output_text.delta', { item_id: item.id, output_index: 0, content_index: 0, delta: 'GO_BINARY_CODEX_OK' });
      event(res, 'response.output_item.done', { output_index: 0, item });
      event(res, 'response.completed', { response: { id: 'r2', status: 'completed', output: [item] } });
    }
    res.end();
  });
  await new Promise(r => upstream.listen(0, '127.0.0.1', r)); t.after(() => upstream.close());
  const f = await goBrowserFixture(t);
  await goSavedModel(f, { group: 'roundtrip', name: 'coding', baseUrl: `http://127.0.0.1:${upstream.address().port}/v1`, key: 'synthetic-upstream-key', model: 'synthetic-actual-model', isDefault: true });
  const codexRoot = join(new URL('..', import.meta.url).pathname, '.go-codex-test-homes'); await mkdir(codexRoot, { recursive: true, mode: 0o700 });
  const codexHome = await mkdtemp(join(codexRoot, 'home-')); t.after(() => rm(codexHome, { recursive: true, force: true }));
  const env = { PATH: process.env.PATH, HOME: f.temp, CODEX_HOME: codexHome, XDG_CONFIG_HOME: f.temp, LANG: 'C.UTF-8', TERM: 'dumb', ADROUTE_PROXY_TOKEN: (await f.api('/api/setup').then(r => r.json())).proxyToken };
  const args = ['exec', '--ignore-rules', '--ephemeral', '--skip-git-repo-check', '--sandbox', 'read-only', '--color', 'never', '--json', '-C', f.temp,
    '--ignore-user-config', '-m', 'adroute/default', '-c', 'model_provider="adroute"', '-c', `model_providers.adroute={name="adroute",base_url="${f.url}/v1",wire_api="responses",env_key="ADROUTE_PROXY_TOKEN",requires_openai_auth=false,supports_websockets=false}`, '-c', `model_catalog_json="${join(f.home, 'codex', 'models.json')}"`, '-c', 'web_search="disabled"', '-c', 'check_for_update_on_startup=false', '-c', 'features.shell_snapshot=false', 'Run only printf go-binary-roundtrip and then finish.'];
  const result = await execute(codex, args, { cwd: f.temp, env, timeout: 60000 });
  assert.equal(result.error, null, `${result.stdout}\n${result.stderr}`);
  assert.equal(calls.length, 2, `${result.stdout}\n${result.stderr}`);
  assert.ok(result.stdout.includes('GO_BINARY_CODEX_OK'), result.stdout);
  const output = calls[1].input.find(x => x.type === 'function_call_output' && x.call_id === 'call_synthetic');
  assert.ok(output && JSON.stringify(output).includes('go-binary-roundtrip'), JSON.stringify(calls[1]));
  assert.ok(calls.every(x => x.model === 'synthetic-actual-model'));
  assert.ok(!JSON.stringify(calls).includes(env.ADROUTE_PROXY_TOKEN));
});

for (const authMode of ['config', 'env']) {
  test(`official Codex 0.156.0 Go-binary roundtrip with ${authMode === 'config' ? 'config bearer auth' : 'environment bearer auth'}`, { skip: !codexAvailable && 'optional official Codex 0.156.0 executable is unavailable', timeout: 90000 }, async t => {
    await goBinaryCodexConversation(t, { authMode });
  });
}
for (const repair of ['v1', 'v2']) {
  test(`official Codex 0.156.0 Go-binary roundtrip preserves original tool call ID through ${repair} repair`, { skip: !codexAvailable && 'optional official Codex 0.156.0 executable is unavailable', timeout: 90000 }, async t => {
    await goBinaryCodexConversation(t, { authMode: 'config', repair });
  });
}

test('official Codex native /model picker on built Go binary switches entries that share a real model id', { skip: !codexAvailable && 'optional official Codex 0.156.0 executable is unavailable', timeout: 90000 }, async t => {
  const calls = [];
  const makeUpstream = label => {
    const server = createServer(async (req, res) => {
      let raw = ''; for await (const chunk of req) raw += chunk;
      const body = JSON.parse(raw); calls.push({ label, auth: req.headers.authorization, body });
      const text = `MOCK_${label}_OK`, item = { id: `m${calls.length}`, type: 'message', role: 'assistant', status: 'completed', content: [{ type: 'output_text', text, annotations: [] }] };
      res.writeHead(200, { 'content-type': 'text/event-stream' });
      for (const [type, data] of [
        ['response.created', { response: { id: `r${calls.length}`, status: 'in_progress', output: [] } }],
        ['response.output_item.added', { output_index: 0, item: { ...item, status: 'in_progress', content: [] } }],
        ['response.content_part.added', { item_id: item.id, output_index: 0, content_index: 0, part: { type: 'output_text', text: '', annotations: [] } }],
        ['response.output_text.delta', { item_id: item.id, output_index: 0, content_index: 0, delta: text }],
        ['response.output_text.done', { item_id: item.id, output_index: 0, content_index: 0, text }],
        ['response.content_part.done', { item_id: item.id, output_index: 0, content_index: 0, part: item.content[0] }],
        ['response.output_item.done', { output_index: 0, item }],
        ['response.completed', { response: { id: `r${calls.length}`, status: 'completed', output: [item] } }],
      ]) event(res, type, data);
      res.end();
    });
    return new Promise(resolve => server.listen(0, '127.0.0.1', () => resolve({ server, url: `http://127.0.0.1:${server.address().port}/v1` })));
  };
  const [a, b] = await Promise.all([makeUpstream('A'), makeUpstream('B')]); t.after(() => { a.server.close(); b.server.close(); });
  const f = await goBrowserFixture(t);
  await goSavedModel(f, { group: 'alpha', name: 'entry', baseUrl: a.url, key: 'synthetic-alpha-key', model: 'duplicate-model', isDefault: true });
  await goSavedModel(f, { group: 'beta', name: 'entry', baseUrl: b.url, key: 'synthetic-beta-key', model: 'duplicate-model' });
  const repo = new URL('..', import.meta.url).pathname, root = join(repo, '.go-codex-test-homes'); await mkdir(root, { recursive: true, mode: 0o700 });
  const work = await mkdtemp(join(root, 'picker-work-')), home = join(work, 'codex-home'); await mkdir(home, { mode: 0o700 });
  t.after(() => rm(work, { recursive: true, force: true }));
  const setup = await f.api('/api/setup').then(r => r.json()), catalog = setup.codexConfig.match(/^model_catalog_json = "(.*)"$/m)?.[1];
  await writeFile(join(home, 'config.toml'), `model_provider="adroute"\nmodel="adroute/default"\nmodel_catalog_json=${JSON.stringify(catalog)}\nweb_search = "disabled"\ncheck_for_update_on_startup = false\n[features]\nshell_snapshot = false\n[model_providers.adroute]\nname="adroute"\nbase_url="${f.url}/v1"\nwire_api="responses"\nexperimental_bearer_token="${setup.proxyToken}"\nrequires_openai_auth=false\nsupports_websockets=false\n[projects.${JSON.stringify(work)}]\ntrust_level="trusted"\n`, { mode: 0o600, flag: 'wx' });
  const env = { PATH: process.env.PATH, HOME: work, CODEX_HOME: home, XDG_CONFIG_HOME: work, LANG: 'C.UTF-8', TERM: 'xterm-256color' };
  const child = spawn('python3', ['-u', fileURLToPath(new URL('./terminal-bridge.py', import.meta.url)), codex, '--no-daemon', '--no-alt-screen', '--sandbox', 'read-only', '--ask-for-approval', 'never', '-C', work], { env, cwd: work, stdio: ['pipe', 'pipe', 'pipe'] });
  let output = '', errors = ''; child.stdout.on('data', d => output += d.toString()); child.stderr.on('data', d => errors += d.toString());
  const exited = new Promise(resolve => child.once('exit', resolve)); t.after(async () => { if (child.exitCode === null) child.kill('SIGTERM'); await Promise.race([exited, delay(3000)]); });
  const plain = () => output.replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '').replace(/\x1b\][^\x07]*(?:\x07)/g, '');
  const waitFor = async (fn, msg) => { for (let i=0;i<250;i++) { if(fn()) return; if(child.exitCode!==null) break; await delay(40); } assert.fail(`${msg}\n${plain().slice(-8000)}\n${errors}`); };
  const send = async value => { child.stdin.write(value); await delay(150); };
  await waitFor(() => plain().includes('alpha / entry'), 'Codex loads Go-generated model catalog');
  await send('/model'); await send('\r'); await waitFor(() => plain().includes('beta / entry'), 'Native model picker includes both duplicate routes');
  assert.equal(calls.length, 0, 'Opening picker does not infer');
  await send('\x1b[B'); await send('\x1b[B'); await send('\r');
  await waitFor(() => plain().includes('Select Reasoning Level for beta / entry'), 'Picker selection chooses Beta entry');
  await send('\r'); await send('Synthetic transport probe B'); await send('\r');
  await waitFor(() => plain().includes('MOCK_B_OK'), 'Selecting Beta routes the turn to synthetic Beta');
  const turns = calls.filter(x => JSON.stringify(x.body.input).includes('Synthetic transport probe B'));
  assert.ok(turns.length >= 1); assert.ok(turns.every(x => x.label === 'B' && x.auth === 'Bearer synthetic-beta-key' && x.body.model === 'duplicate-model'));
  assert.ok(!JSON.stringify(calls).includes(setup.proxyToken));
});
