'use strict';

(() => {
  const $ = id => document.getElementById(id);
  const SESSION = 'adroute.adminToken';
  const MODEL_NAME_RE = /^[A-Za-z0-9._-]+$/;
  const GROUP_NAME_RE = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

  let token = '', epoch = 0, state = null, tab = 'models', setup = null;
  let editingModel = null, filterGroup = '', deleting = null, nameTouched = false, formVersion = 0, discoverVersion = 0;
  let loadingState = false, syncing = false, toastTimer;
  let groupsVersion = 0, deleteVersion = 0, defaultIntent = '';
  let stateUpdateVersion = 0;
  let importDraft = null;
  let discoverCatalog = [];
  let editingAggregate = null, aggregateVersion = 0, aggregateMemberRows = [];
  const pendingTests = new Map();
  const testResults = new Map();
  const collapsedGroups = new Set();
  const pendingAvailability = new Map();
  const pendingWrites = new Map();
  const groupDrafts = new Map();
  const controllers = new Set();
  let activeTestDetail = null, testDetailCloseTimer;

  const BACKEND_UPDATE = '后台仍在运行旧版本，请先执行 adroute stop && adroute start，再刷新网页；已暂停配置修改。';
  const titles = {
    models: ['模型管理', '添加模型，保存它们自己的上游地址与密钥，通过统一的本地地址调用。'],
    aggregates: ['聚合', '把多个模型串成故障转移链；某个成员不可用时自动换下一个。'],
    requests: ['请求记录', '查看最近的路由结果，不保存对话正文。'],
    compatibility: ['兼容修复', '每个已保存模型条目独立绑定修复包，不继承组或其他模型设置。'],
    setup: ['客户端接入', 'Codex 配置接入与恢复，以及 OpenCode 显式同步。'],
  };
  const compatibility = window.createCompatibilityController({ api, getState: () => state, applyState, showTab });

  class StaleRequest extends Error {}

  function text(id, value) { $(id).textContent = value ?? ''; }
  function notice(id, value = '') { text(id, value); $(id).hidden = !value; }
  function node(tag, value, className) {
    const result = document.createElement(tag);
    if (value !== undefined) result.textContent = value;
    if (className) result.className = className;
    return result;
  }
  function button(label, run, className = 'button secondary small-button') {
    const result = node('button', label, className); result.type = 'button';
    result.addEventListener('click', run); return result;
  }
  function backendReady() { return state?.capabilities?.models === true; }
  function availabilityReady() { return backendReady() && state?.capabilities?.modelAvailability === true; }
  function quickTestReady() { return backendReady() && state?.capabilities?.quickTest === true; }
  function aggregatesReady() { return backendReady() && state?.capabilities?.aggregates === true; }
  function remember(value) { try { if (value) sessionStorage.setItem(SESSION, value); else sessionStorage.removeItem(SESSION); } catch { /* tabs may block storage */ } }
  function toast(value) { clearTimeout(toastTimer); notice('toast', value); toastTimer = setTimeout(() => { $('toast').hidden = true; }, 4500); }
  // This identity stays private in memory; it is never rendered or persisted.
  function modelSignature(model) { return JSON.stringify([model.groupId, model.groupName, model.name, model.model, model.baseUrl, model.apiKey, model.profile]); }
  function modelWriteKey() { return editingModel ? `model:${editingModel.id}` : `new-model:${formVersion}`; }
  function aggregateWriteKey() { return `aggregate:${editingAggregate ? editingAggregate.id : 'new:' + aggregateVersion}`; }
  function groupName(id) { return state?.groups.find(group => group.id === id)?.name || ''; }
  function closeDialog(id) {
    if ($(id).open) $(id).close();
    if (id === 'model-dialog') { editingModel = null; nameTouched = false; formVersion++; $('model-form').reset(); resetDiscovery(); clearFieldErrors(); notice('model-error'); }
    if (id === 'aggregate-dialog') { editingAggregate = null; aggregateVersion++; $('aggregate-form').reset(); notice('aggregate-error'); for (const name of ['name', 'members', 'rounds']) notice(`aggregate-error-${name}`); }
    if (id === 'delete-dialog') { deleting = null; deleteVersion++; }
    if (id === 'groups-dialog') { groupsVersion++; groupDrafts.clear(); }
    if (id === 'import-dialog') { importDraft = null; $('import-config-file').value = ''; notice('import-error'); }
  }
  function clearFieldErrors() {
    for (const id of ['model-error-group', 'model-error-newgroup', 'model-error-baseurl', 'model-error-apikey', 'model-error-realid', 'model-error-name', 'model-error-website']) notice(id);
  }
  function fieldError(name, value) { notice(`model-error-${name}`, value); }
  function aggregateFieldError(name, value) { notice(`aggregate-error-${name}`, value); }
  function clearSession(value = '') {
    closeTestDetail();
    epoch++;
    compatibility.reset();
    for (const controller of controllers) controller.abort();
    controllers.clear();
    for (const operation of pendingTests.values()) operation.controller.abort();
    pendingTests.clear(); testResults.clear(); collapsedGroups.clear(); pendingAvailability.clear(); pendingWrites.clear(); groupDrafts.clear();
    token = ''; remember(''); state = setup = editingModel = deleting = editingAggregate = null;
    importDraft = null; $('import-config-file').value = '';
    filterGroup = ''; tab = 'models'; loadingState = syncing = false; formVersion++; discoverVersion++; groupsVersion++; deleteVersion++; defaultIntent = '';
    for (const dialog of document.querySelectorAll('dialog')) if (dialog.open) dialog.close();
    for (const id of ['auth-form', 'model-form', 'group-create-form', 'aggregate-form']) $(id).reset();
    for (const id of ['codex-config', 'setup-url', 'model-list', 'aggregate-list', 'aggregate-members', 'all-requests', 'group-list', 'model-discover-results', 'delete-model-name', 'delete-model-route', 'request-detail-body', 'group-filter', 'default-model', 'request-meta-time', 'request-meta-group', 'request-meta-route', 'request-meta-model', 'request-meta-effort', 'request-meta-status', 'request-meta-duration']) { const element = $(id); if (element) element.replaceChildren(); }
    for (const id of ['client-base-url', 'client-api-key', 'admin-token']) $(id).value = '';
    $('client-api-key').type = 'password'; text('toggle-client-api-key', '显示');
    notice('opencode-sync-status'); notice('global-error'); notice('default-status'); notice('auth-error', value);
    $('catalog-notice').hidden = true; notice('catalog-status'); text('catalog-message', '');
    $('setup-content').hidden = $('app-shell').hidden = true; $('auth-screen').hidden = false; $('toast').hidden = true;
  }

  async function api(path, { method = 'GET', body, timeoutMs = 45000, signal } = {}) {
    // Reads stay available so the page can detect an older backend; every
    // mutation is blocked until the new capabilities are present.
    if (!backendReady() && method !== 'GET') throw new Error(BACKEND_UPDATE);
    const started = epoch, controller = new AbortController();
    controllers.add(controller);
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    const onAbort = () => controller.abort();
    if (signal) {
      if (signal.aborted) controller.abort();
      else signal.addEventListener('abort', onAbort);
    }
    try {
      const response = await fetch(path, {
        method, credentials: 'omit',
        headers: { authorization: `Bearer ${token}`, ...(body !== undefined ? { 'content-type': 'application/json' } : {}) },
        ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
        signal: controller.signal,
      });
      const result = await response.json();
      if (started !== epoch) throw new StaleRequest();
      if (response.status === 401) { clearSession('管理令牌无效或已失效，请重新登录。'); throw new StaleRequest(); }
      if (!response.ok) {
        const error = new Error(result.error?.message || `请求失败（HTTP ${response.status}）。`);
        error.code = result.error?.code;
        throw error;
      }
      return result;
    } catch (error) {
      if (started !== epoch || error instanceof StaleRequest) throw new StaleRequest();
      if (signal?.aborted) throw new Error('已取消。');
      if (error.name === 'AbortError') throw new Error('请求已中断或超时，请检查服务后重试。');
      if (error instanceof TypeError) throw new Error('无法连接本地服务，请确认 adroute 正在运行。');
      throw error;
    } finally {
      clearTimeout(timer);
      if (signal) signal.removeEventListener('abort', onAbort);
      controllers.delete(controller);
    }
  }

  function syncPendingControls() {
    const ready = backendReady();
    for (const id of ['add-model', 'manage-groups']) $(id).disabled = !ready;
    for (const id of ['import-config', 'export-config']) $(id).disabled = !ready || state?.capabilities?.configTransfer !== true || pendingWrites.has(id);
    $('import-confirm').disabled = !ready || pendingWrites.has('import-confirm');
    $('model-save').disabled = !ready || pendingWrites.has(modelWriteKey());
    $('aggregate-save').disabled = !ready || pendingWrites.has(aggregateWriteKey());
    $('model-create-group').disabled = !ready || pendingWrites.has(`inline-group:${formVersion}`);
    $('delete-submit').disabled = !ready || pendingWrites.has(`delete:${deleting}`);
    $('group-create-submit').disabled = !ready || pendingWrites.has('group-create-submit');
    $('default-model').disabled = !ready || pendingWrites.has('default-model');
    $('catalog-retry').disabled = !ready || pendingWrites.has('catalog-retry');
    $('opencode-sync').disabled = !ready || syncing || pendingWrites.has('opencode-sync');
    for (const row of $('group-list').querySelectorAll('[data-group-id]')) {
      const group = state?.groups.find(item => item.id === row.dataset.groupId);
      const busy = !ready || pendingWrites.has(`group:${row.dataset.groupId}`);
      row.querySelector('input').disabled = busy;
      for (const button of row.querySelectorAll('button')) button.disabled = busy || (button.dataset.groupDelete === 'true' && group?.modelCount > 0);
    }
    for (const row of $('model-list').querySelectorAll('[data-model-id]')) {
      const id = row.dataset.modelId, busy = pendingWrites.has(`model:${id}`) || pendingWrites.has(`delete:${id}`) || pendingWrites.has(`copy:${id}`);
      for (const button of row.querySelectorAll('[data-model-action]')) {
        const action = button.dataset.modelAction;
        button.disabled = !ready || busy || pendingTests.has(id) || pendingAvailability.has(id) || (action === 'test' && !quickTestReady()) || (action === 'copy' && state?.capabilities?.modelCopy !== true);
        // "打开网站" stays inert until that entry has a saved website.
        if (action === 'open-website') button.disabled = button.disabled || !state?.models.find(item => item.id === id)?.website;
      }
      const toggle = row.querySelector('[data-availability]');
      toggle.disabled = !availabilityReady() || busy || pendingAvailability.has(id);
    }
    $('add-aggregate').disabled = !ready;
    for (const card of $('aggregate-list').querySelectorAll('[data-aggregate-id]')) {
      const id = card.dataset.aggregateId;
      for (const button of card.querySelectorAll('button')) {
        if (button.dataset.aggregateAvailability) continue;
        button.disabled = !ready;
      }
      const toggle = card.querySelector('[data-aggregate-availability]');
      if (toggle) toggle.disabled = !ready;
    }
  }

  async function action(control, errorId, work, current = () => true, key = control.id) {
    if (pendingWrites.has(key)) return;
    const started = epoch, operation = {};
    pendingWrites.set(key, operation); control.disabled = true; notice(errorId); syncPendingControls();
    try { await work(); }
    catch (error) {
      if (!(error instanceof StaleRequest) && started === epoch && current()) {
        notice(errorId, error.message);
        if (errorId === 'model-error') {
          let field = { MODEL_NAME_CONFLICT: 'name', MODEL_NAME_INVALID: 'name', GROUP_NOT_FOUND: 'group', MODEL_UPSTREAM_ID_INVALID: 'realid', INVALID_BASE_URL: 'baseurl', UPSTREAM_KEY_INVALID: 'apikey' }[error.code];
          if (error.code === 'INVALID_REQUEST' && error.message.includes('网站')) field = 'website';
          if (field) fieldError(field, error.message);
        }
      }
    } finally {
      if (pendingWrites.get(key) === operation) pendingWrites.delete(key);
      if (started === epoch) syncPendingControls();
    }
  }

  function applyState(value) {
    if (Number.isSafeInteger(state?.revision) && (!Number.isSafeInteger(value.revision) || value.revision < state.revision)) return;
    stateUpdateVersion++;
    state = value;
    for (const id of collapsedGroups) if (!state.groups.some(group => group.id === id)) collapsedGroups.delete(id);
    $('initial-loading').hidden = $('state-unavailable').hidden = true;
    notice('global-error', backendReady() ? (state.capabilities.modelAvailability === true ? '' : '当前后台不支持模型开放状态，请先重启 adroute。') : BACKEND_UPDATE);
    text('connection-status', backendReady() ? '● 已连接 · 本机服务' : '● 已连接 · 后台待重启');
    text('app-version', `AdRoute ${value.version}`);
    // Drop stale quick-test results whose upstream target changed elsewhere.
    for (const [id, record] of [...testResults]) {
      const model = state.models.find(item => item.id === id);
      if (!model || (record.signature && record.signature !== modelSignature(model))) testResults.delete(id);
    }
    renderAccess(); renderModels(); renderRequests();
    if ($('aggregate-dialog').open && editingAggregate) {
      const latest = state.aggregates?.find(item => item.id === editingAggregate.id);
      if (!latest) closeDialog('aggregate-dialog');
    }
    const publication = state.catalogPublication;
    $('catalog-notice').hidden = !publication?.pending;
    text('catalog-message', publication?.message || '');
    if (!publication?.pending) notice('catalog-status');
    if ($('model-dialog').open) renderGroupOptions($('model-group').value);
    if ($('groups-dialog').open) renderGroups();
    compatibility.update();
    showTab(tab, false);
    syncPendingControls();
  }

  async function refresh() {
    if (!token || loadingState) return;
    loadingState = true; const started = epoch, version = stateUpdateVersion;
    try {
      const value = await api('/api/state');
      // Publication retries change metadata without editing the config revision.
      // An older read must not restore warnings cleared by a newer operation.
      if (version === stateUpdateVersion || (Number.isSafeInteger(value.revision) && value.revision > state?.revision)) applyState(value);
    }
    catch (error) {
      if (error instanceof StaleRequest || started !== epoch) return;
      notice('global-error', error.message); text('connection-status', '连接中断');
      $('initial-loading').hidden = true; $('state-unavailable').hidden = Boolean(state);
    } finally { if (started === epoch) loadingState = false; }
  }

  function renderAccess() {
    if (!setup) return;
    $('client-base-url').value = setup.proxyUrl;
    $('client-api-key').value = setup.proxyToken;
    text('setup-url', setup.proxyUrl);
    text('codex-config', setup.codexConfig);
  }

  async function loadSetup() {
    $('setup-loading').hidden = false; $('setup-retry').hidden = true; notice('setup-error');
    const started = epoch;
    try {
      setup = await api('/api/setup');
      renderAccess(); $('setup-content').hidden = false;
    } catch (error) {
      if (!(error instanceof StaleRequest) && started === epoch) { notice('setup-error', error.message); $('setup-retry').hidden = false; }
    } finally { if (started === epoch) $('setup-loading').hidden = true; }
  }

  // ---- model groups -----------------------------------------------------

  function renderGroupFilter() {
    const container = $('group-filter'); container.replaceChildren();
    const all = button(`全部模型 ${state.models.length}`, () => { filterGroup = ''; renderModels(); }, 'chip');
    all.setAttribute('aria-pressed', String(filterGroup === ''));
    if (filterGroup === '') all.classList.add('selected');
    container.append(all);
    for (const group of state.groups) {
      const count = state.models.filter(model => model.groupId === group.id).length;
      const chip = button(`${group.name} ${count}`, () => { filterGroup = group.id; renderModels(); }, 'chip');
      chip.setAttribute('aria-pressed', String(filterGroup === group.id));
      if (filterGroup === group.id) chip.classList.add('selected');
      container.append(chip);
    }
    if (filterGroup && !state.groups.some(group => group.id === filterGroup)) filterGroup = '';
  }

  function renderGroups() {
    const list = $('group-list'); list.replaceChildren();
    for (const group of state.groups) {
      const row = node('div', undefined, 'group-row');
      row.dataset.groupId = group.id;
      const input = document.createElement('input');
      input.type = 'text'; input.value = groupDrafts.get(group.id) ?? group.name; input.maxLength = 48; input.autocomplete = 'off'; input.spellcheck = false;
      input.addEventListener('input', () => groupDrafts.set(group.id, input.value));
      input.className = 'group-name-input'; input.setAttribute('aria-label', `组名 ${group.name}`);
      const save = button('改名', async () => {
        const next = input.value;
        if (next === group.name) return;
        if (!GROUP_NAME_RE.test(next) || next.length > 48) { notice('groups-error', '组名不能包含中文、空格或其他非法字符，请手动修改。'); return; }
        if (pendingWrites.has(`group:${group.id}`)) return;
        if (!confirm(`改组名会让该组模型的对外 ID 从 adroute/${group.name}/… 变成 adroute/${next}/…，已保存的客户端选择会失效。继续？`)) return;
        const version = groupsVersion, current = () => version === groupsVersion && $('groups-dialog').open;
        await action(save, 'groups-error', async () => {
          applyState(await api(`/api/groups/${group.id}`, { method: 'PUT', body: { name: next } }));
          if (current()) {
            if (groupDrafts.get(group.id) === next) groupDrafts.delete(group.id);
            toast('组名已更新，请让客户端重新获取目录。');
          }
        }, current, `group:${group.id}`);
      }, 'button subtle small-button');
      const remove = button('删除', async () => {
        if (pendingWrites.has(`group:${group.id}`)) return;
        if (group.modelCount > 0) { notice('groups-error', '组内仍有模型，请先移动或删除这些模型。'); return; }
        if (!confirm(`删除空组 ${group.name}？`)) return;
        const version = groupsVersion, current = () => version === groupsVersion && $('groups-dialog').open;
        await action(remove, 'groups-error', async () => {
          applyState(await api(`/api/groups/${group.id}`, { method: 'DELETE' }));
          if (current()) toast('空组已删除。');
        }, current, `group:${group.id}`);
      }, 'button subtle small-button danger');
      remove.dataset.groupDelete = 'true'; remove.disabled = group.modelCount > 0;
      row.append(input, save, remove, node('span', `${group.modelCount} 个模型`, 'field-help'));
      list.append(row);
    }
    if (!state.groups.length) list.append(node('p', '还没有模型组。先在上方新建一个组，再添加模型。', 'field-help'));
    syncPendingControls();
  }

  // ---- model list -------------------------------------------------------

  function renderModels() {
    closeTestDetail();
    renderGroupFilter();
    const list = $('model-list'); list.replaceChildren();
    text('model-count', state.models.length);
    const groups = state.groups.filter(group => !filterGroup || group.id === filterGroup);
    if (!groups.length) {
      const empty = node('div', undefined, 'empty-panel');
      empty.append(node('h2', '还没有添加模型'), node('p', '点击“添加模型”，填写上游 Base URL、上游 API Key 和真实模型 ID；组只用于分类。', 'muted'),
        button('添加模型', () => openModelForm(), 'button primary'));
      list.append(empty);
    }
    for (const group of groups) {
      const card = node('section', undefined, 'model-group');
      const header = node('div', undefined, 'model-group-header');
      const icon = node('span', '▦', 'model-group-icon'); icon.setAttribute('aria-hidden', 'true');
      const models = state.models.filter(model => model.groupId === group.id);
      const content = node('div', undefined, 'model-group-content');
      content.id = `model-group-content-${group.id}`;
      content.hidden = collapsedGroups.has(group.id);
      const toggle = button('⌄', () => {
        content.hidden = !content.hidden;
        if (content.hidden) collapsedGroups.add(group.id); else collapsedGroups.delete(group.id);
        toggle.setAttribute('aria-expanded', String(!content.hidden));
        toggle.setAttribute('aria-label', `${content.hidden ? '展开' : '收起'} ${group.name}`);
      }, 'icon-button model-group-toggle');
      toggle.setAttribute('aria-expanded', String(!content.hidden));
      toggle.setAttribute('aria-label', `${content.hidden ? '展开' : '收起'} ${group.name}`);
      toggle.setAttribute('aria-controls', content.id);
      header.append(icon, node('h2', group.name), node('span', `${models.length} 个模型`, 'model-group-count'), toggle);
      card.append(header, content);
      for (const model of models) content.append(modelRow(model));
      if (!models.length) content.append(node('p', '暂无模型', 'model-group-empty'));
      list.append(card);
    }
    renderAggregates();
    renderDefaultSelect();
    syncPendingControls();
  }

  function badge(label, className = 'badge') { return node('span', label, className); }

  function modelRow(model) {
    const row = node('article', undefined, 'model-row');
    row.dataset.modelId = model.id;
    const main = node('div', undefined, 'model-main');
    const title = node('div', undefined, 'model-title');
    title.append(node('span', `${model.groupName} / ${model.name}`, 'sr-only'), node('h3', model.name));
    if (model.disabled) title.append(badge('已关闭', 'badge muted-badge'));
    if (state.defaultModelId === model.id) title.append(badge('网页默认'));
    main.append(title);

    const meta = node('dl', undefined, 'model-meta');
    const pairs = [
      ['对外模型 ID', model.routeId, true],
      ['上游真实模型 ID', model.model, true],
      ['上游地址', model.baseUrl, false],
    ];
    for (const [label, value, mono] of pairs) {
      const wrap = node('div', undefined, label === '上游真实模型 ID' ? 'model-real-id' : 'sr-only');
      wrap.append(node('dt', label), node('dd', value, mono ? 'mono' : ''));
      meta.append(wrap);
    }
    main.append(meta);

    const testRegion = node('div', undefined, 'model-test');
    testRegion.dataset.testRegion = model.id;
    main.append(testRegion);
    renderTestRegion(model, testRegion);

    const controls = node('div', undefined, 'model-controls');
    const toggleLabel = node('label', undefined, 'availability-control');
    const toggle = document.createElement('input');
    toggle.type = 'checkbox'; toggle.checked = !model.disabled; toggle.dataset.availability = model.id;
    toggle.setAttribute('aria-label', `对外开放 ${model.groupName} / ${model.name}`);
    const toggleText = node('span', model.disabled ? '已关闭' : '已开放');
    const busy = pendingAvailability.has(model.id);
    toggle.disabled = !availabilityReady() || busy;
    if (busy) toggleText.textContent = '正在保存…';
    toggle.addEventListener('change', () => toggleAvailability(model, toggle.checked, toggle, toggleText));
    toggleLabel.append(toggle, toggleText);

    const actions = node('div', undefined, 'model-actions');
    const testing = pendingTests.has(model.id);
    const edit = button('编辑', () => openModelForm(model), 'button subtle small-button');
    const test = button(testing ? '测速中' : '测速', () => startQuickTest(model.id), 'button subtle small-button');
    const copy = button('复制', () => copyModel(model, copy), 'button subtle small-button');
    const remove = button('删除', () => openDelete(model), 'button subtle small-button danger');
    const website = button('打开网站', () => {
      const url = state.models.find(item => item.id === model.id)?.website || model.website;
      if (url) window.open(url, '_blank', 'noopener');
    }, 'button subtle small-button');
    website.dataset.modelAction = 'open-website';
    website.disabled = !model.website;
    website.title = model.website ? model.website : '尚未填写网站地址；编辑模型可添加。';
    edit.dataset.modelAction = 'edit'; test.dataset.modelAction = 'test'; copy.dataset.modelAction = 'copy'; remove.dataset.modelAction = 'delete';
    for (const control of [edit, test, copy, remove]) {
      control.disabled = testing || !backendReady();
      if (control !== test) control.disabled = control.disabled || pendingAvailability.has(model.id);
    }
    actions.append(test, edit, copy, website, remove);
    controls.append(toggleLabel, actions);
    row.append(main, controls);
    return row;
  }

  async function copyModel(model, control) {
    if (state?.capabilities?.modelCopy !== true) return;
    await action(control, 'global-error', async () => {
      applyState(await api(`/api/models/${model.id}/copy`, { method: 'POST' }));
      toast('模型已复制到同一组。');
    }, () => true, `copy:${model.id}`);
  }

  function renderTestRegion(model, container) {
    container.replaceChildren();
    const operation = pendingTests.get(model.id);
    if (operation) {
      const line = node('div', undefined, 'test-line pending');
      line.append(node('span', '测速中，等待上游返回…', 'test-status'));
      line.append(button('取消', () => { operation.controller.abort(); }, 'button subtle small-button'));
      container.append(line);
      return;
    }
    const record = testResults.get(model.id);
    if (!record) return;
    const line = node('div', undefined, 'test-line');
    line.dataset.tone = record.tone || 'success';
    line.append(node('span', record.label, 'test-status'));
    const wrap = node('span', undefined, 'test-info-wrap');
    const info = button('i', () => {
      if (activeTestDetail?.root === wrap && activeTestDetail.pinned) closeTestDetail();
      else show(true);
    }, 'test-info');
    const tooltip = node('span', record.detail || record.label, 'test-tooltip');
    tooltip.id = `test-detail-${model.id}`;
    tooltip.setAttribute('role', 'tooltip');
    tooltip.tabIndex = 0;
    tooltip.hidden = true;
    info.setAttribute('aria-label', '查看测速详情');
    info.setAttribute('aria-describedby', tooltip.id);
    info.setAttribute('aria-expanded', 'false');
    const show = (pinned = false) => {
      clearTimeout(testDetailCloseTimer);
      if (activeTestDetail?.root !== wrap) closeTestDetail();
      const keepPinned = pinned || (activeTestDetail?.root === wrap && activeTestDetail.pinned);
      activeTestDetail = { root: wrap, tooltip, info, pinned: keepPinned };
      tooltip.hidden = false;
      info.setAttribute('aria-expanded', 'true');
      const anchor = info.getBoundingClientRect();
      const box = tooltip.getBoundingClientRect();
      tooltip.style.left = `${Math.max(12, Math.min(anchor.left, innerWidth - box.width - 12))}px`;
      const below = anchor.bottom + 8;
      tooltip.style.top = `${below + box.height <= innerHeight - 12 ? below : Math.max(12, anchor.top - box.height - 8)}px`;
    };
    wrap.addEventListener('pointerenter', event => { if (event.pointerType === 'mouse') show(); });
    wrap.addEventListener('pointerleave', () => {
      if (activeTestDetail?.root === wrap && !activeTestDetail.pinned && !wrap.contains(document.activeElement)) {
        testDetailCloseTimer = setTimeout(() => {
          if (activeTestDetail?.root === wrap && !activeTestDetail.pinned && !wrap.contains(document.activeElement)) closeTestDetail();
        }, 160);
      }
    });
    wrap.addEventListener('focusin', () => show());
    wrap.addEventListener('focusout', event => {
      if (activeTestDetail?.root === wrap && !wrap.contains(event.relatedTarget)) closeTestDetail();
    });
    wrap.append(info, tooltip);
    line.append(wrap);
    container.append(line);
  }

  function closeTestDetail() {
    clearTimeout(testDetailCloseTimer);
    if (!activeTestDetail) return;
    const current = activeTestDetail;
    // Hiding a focused preview dispatches focusout synchronously.
    activeTestDetail = null;
    current.info.setAttribute('aria-expanded', 'false');
    current.tooltip.hidden = true;
  }

  function formatTestResult(result) {
    const duration = `${result.durationMs} ms`;
    const tokens = result.outputTokens;
    const rate = result.textTokensPerSecond;
    const hasRate = Number.isFinite(rate) && rate > 0;
    const estimated = result.textTokensSource === 'cl100k_base-estimate';
    const unavailable = {
      'usage-unavailable': '上游未返回可信用量',
      'reasoning-usage-unavailable': '上游未区分正文与推理用量',
      'no-stream-timing': '未收到足够的流式文本分片',
      'sample-too-short': '输出样本过短或分片过于集中',
      'non-text-output': '用量还包含工具或其他非文本输出',
    }[result.speedUnavailableReason] || '缺少正文用量或流式计时';
    const speed = hasRate ? `${estimated ? '约 ' : ''}${rate.toFixed(1)} tokens/s` : '速度不可统计';
    const firstText = Number.isFinite(result.firstTextMs) && result.firstTextMs >= 0
      ? `首字 ${result.firstTextMs} ms` : '首字延迟不可统计（未收到流式文本）';
    const details = [hasRate ? `速度 ${speed}` : `${speed}（${unavailable}）`, firstText, `总耗时 ${duration}`,
      Number.isInteger(tokens) && tokens > 0 ? `输出 ${tokens} tokens` : estimated ? `正文约 ${result.textTokens} tokens（估算）` : '输出用量未报告'];
    details.push(`返回：${result.text || '（无正文）'}${result.textPreviewTruncated ? '…（预览已截断）' : ''}`);
    const notes = [];
    if (estimated) notes.push(`正文约 ${result.textTokens} tokens，cl100k_base 本地分词估算，不是上游计费 token 数，可能与该模型的实际分词不同`);
    if (Number.isInteger(result.reasoningTokens) && result.reasoningTokens > 0) notes.push(`推理 ${result.reasoningTokens} tokens`);
    if (hasRate) notes.push('不含首字等待与推理 tokens，流式采样参考');
    if (result.outcome === 'truncated') notes.push(result.truncated ? '达到本次测试输出上限' : '上游提前结束');
    if (result.outcome === 'no-text') notes.push(result.message || '上游未返回正文');
    const detail = details.join(' · ') + (notes.length ? `\n${notes.join(' · ')}` : '');
    return {
      label: hasRate ? speed : result.outcome === 'no-text' ? '未返回正文' : `可用 · ${speed}`,
      detail,
      tone: ['truncated', 'no-text'].includes(result.outcome) ? 'warning' : 'success',
    };
  }

  function renderDefaultSelect() {
    const select = $('default-model');
    select.replaceChildren();
    const none = document.createElement('option');
    none.value = ''; none.textContent = '未设置默认';
    select.append(none);
    for (const model of state.models.filter(item => !item.disabled)) {
      const option = document.createElement('option');
      option.value = model.id; option.textContent = `${model.groupName} / ${model.name}`;
      select.append(option);
    }
    for (const aggregate of (state.aggregates || []).filter(item => !item.disabled && item.openMemberCount > 0)) {
      const option = document.createElement('option');
      option.value = aggregate.id; option.textContent = `聚合 / ${aggregate.name}`;
      select.append(option);
    }
    if (state.defaultModelId && !state.models.some(model => model.id === state.defaultModelId) && !(state.aggregates || []).some(aggregate => aggregate.id === state.defaultModelId)) {
      const stale = document.createElement('option');
      stale.value = ''; stale.textContent = '默认已清空';
      select.append(stale);
    }
    select.value = pendingWrites.has('default-model') ? defaultIntent : (state.defaultModelId || '');
    select.disabled = !backendReady() || pendingWrites.has('default-model');
  }

  async function toggleAvailability(model, enabled, control, label) {
    if (pendingAvailability.has(model.id)) return;
    const closingDefault = !enabled && state.defaultModelId === model.id;
    if (closingDefault && !confirm('关闭当前网页默认模型会清空默认，不会自动切换其他模型；重新开放也不会恢复默认。继续？')) {
      control.checked = true; label.textContent = '已开放'; return;
    }
    const operation = {};
    pendingAvailability.set(model.id, operation);
    control.disabled = true; label.textContent = '正在保存…';
    try {
      applyState(await api(`/api/models/${model.id}/availability`, { method: 'PUT', body: { enabled } }));
    } catch (error) {
      if (!(error instanceof StaleRequest)) toast(`开放状态未更改：${error.message}`);
    } finally {
      if (pendingAvailability.get(model.id) === operation) { pendingAvailability.delete(model.id); renderModels(); }
    }
  }

  async function changeDefault(value) {
    if (pendingWrites.has('default-model')) { renderDefaultSelect(); return; }
    defaultIntent = value;
    notice('default-status', '正在保存…');
    await action($('default-model'), 'default-status', async () => {
      applyState(await api('/api/default', { method: 'POST', body: { modelId: value || null } }));
      notice('default-status', value ? '默认模型已更新；其他开放模型不受影响。' : '默认模型已清空。');
    });
    if (state) renderDefaultSelect();
  }

  // ---- model form -------------------------------------------------------

  function renderGroupOptions(selected) {
    const select = $('model-group');
    select.replaceChildren();
    const placeholder = document.createElement('option');
    placeholder.value = ''; placeholder.textContent = state.groups.length ? '请选择模型组' : '还没有模型组，请先创建';
    select.append(placeholder);
    for (const group of state.groups) {
      const option = document.createElement('option');
      option.value = group.id; option.textContent = group.name;
      select.append(option);
    }
    select.value = selected || '';
  }

  function updateRoutePreview() {
    const group = state?.groups.find(item => item.id === $('model-group').value)?.name || '组名';
    const name = $('model-name').value || '模型名称';
    text('model-route-preview', `adroute/${group}/${name}`);
  }

  function resetDiscovery() {
    discoverVersion++;
    $('model-discover').disabled = false;
    $('model-discover-region').hidden = true;
    $('model-discover-search').hidden = true;
    $('model-discover-search').value = '';
    $('model-discover-results').replaceChildren();
    notice('model-discover-status');
  }

  function openModelForm(model = null) {
    editingModel = model; nameTouched = Boolean(model);
    formVersion++;
    $('model-form').reset(); clearFieldErrors(); notice('model-error'); resetDiscovery();
    $('model-dialog-title').textContent = model ? '编辑模型' : '添加模型';
    $('model-save').textContent = model ? '保存修改' : '保存模型';
    renderGroupOptions(model?.groupId || filterGroup || '');
    $('model-base-url').value = model?.baseUrl || '';
    $('model-api-key').value = '';
    $('model-api-key').type = 'password'; text('model-api-key-toggle', '显示');
    $('model-api-key').setAttribute('aria-pressed', 'false');
    $('model-api-key').placeholder = model ? '留空保留已保存的上游 API Key' : '输入上游 API Key';
    $('model-api-key-required').hidden = Boolean(model);
    $('model-api-key-help').textContent = model ? '已保存的密钥不会回显；留空表示保留，输入新值才会替换。' : '保存后默认遮蔽；它只是这个模型的上游凭据。';
    $('model-real-id').value = model?.model || '';
    $('model-website').value = model?.website || '';
    $('model-name').value = model?.name || '';
    $('model-open').checked = model ? !model.disabled : true;
    $('model-open-row').hidden = Boolean(model);
    updateRoutePreview();
    $('model-dialog').showModal();
    syncPendingControls();
  }

  function showDiscoveredModels() {
    const query = $('model-discover-search').value.trim().toLowerCase();
    const results = $('model-discover-results');
    results.replaceChildren();
    for (const item of discoverCatalog.filter(entry => entry.id.toLowerCase().includes(query))) {
      results.append(button(item.id, () => {
        $('model-real-id').value = item.id;
        if (!nameTouched) { $('model-name').value = item.id; updateRoutePreview(); validateNameField(); }
        notice('model-error-realid');
      }, 'model-option'));
    }
    if (!results.children.length) results.append(node('p', '没有匹配的模型，仍可手动填写真实模型 ID。', 'field-help'));
  }

  async function discoverModels() {
    const version = discoverVersion, draft = formVersion, started = epoch;
    const baseUrl = $('model-base-url').value.trim();
    const apiKey = $('model-api-key').value.trim();
    if (!baseUrl) { fieldError('baseurl', '请先填写上游 Base URL。'); return; }
    if (!apiKey && !editingModel) { fieldError('apikey', '获取模型需要填写上游 API Key。'); return; }
    // With an untouched credential field on an existing model, the request uses
    // the saved credential for that entry; a typed value always wins.
    const useSaved = Boolean(editingModel) && !apiKey;
    const path = useSaved ? `/api/models/${editingModel.id}/discover` : '/api/models/discover';
    const body = useSaved ? { baseUrl } : { baseUrl, apiKey };
    $('model-discover').disabled = true;
    $('model-discover-region').hidden = false;
    text('model-discover-status', '正在使用表单里当前的地址和密钥获取模型…');
    try {
      const result = await api(path, { method: 'POST', body });
      if (version !== discoverVersion || draft !== formVersion || started !== epoch) return;
      discoverCatalog = result.models || [];
      $('model-discover-search').hidden = false;
      text('model-discover-status', `找到 ${discoverCatalog.length} 个模型；选择一个只会填入真实模型 ID，不会自动保存。`);
      showDiscoveredModels();
    } catch (error) {
      if (!(error instanceof StaleRequest) && started === epoch && version === discoverVersion && draft === formVersion) {
        text('model-discover-status', `${error.message} 仍可手动填写真实模型 ID。`);
      }
    } finally {
      if (version === discoverVersion && draft === formVersion) $('model-discover').disabled = false;
    }
  }

  function validateNameField() {
    const value = $('model-name').value;
    if (!value) { fieldError('name', '请填写模型名称。'); return false; }
    if (value.length > 100 || !MODEL_NAME_RE.test(value)) {
      fieldError('name', '模型名称只能使用英文字母、数字、点、下划线和连字符，不能包含中文、空格或斜杠。');
      return false;
    }
    notice('model-error-name');
    return true;
  }

  function modelBody() {
    const body = {
      groupId: $('model-group').value,
      name: $('model-name').value,
      model: $('model-real-id').value,
      baseUrl: $('model-base-url').value.trim(),
      website: $('model-website').value.trim(),
    };
    const apiKey = $('model-api-key').value.trim();
    if (apiKey || !editingModel) body.apiKey = apiKey;
    if (!editingModel) body.disabled = !$('model-open').checked;
    return body;
  }

  async function createGroupInline() {
    const version = formVersion, current = () => version === formVersion && $('model-dialog').open;
    if (pendingWrites.has(`inline-group:${version}`)) return;
    const name = $('model-new-group').value;
    notice('model-error-newgroup');
    if (!GROUP_NAME_RE.test(name) || name.length > 48) {
      fieldError('newgroup', '组名只能使用小写英文字母、数字和单个连字符，不能以连字符开头或结尾。');
      return;
    }
    await action($('model-create-group'), 'model-error-newgroup', async () => {
      const value = await api('/api/groups', { method: 'POST', body: { name } });
      const groups = value.groups || [];
      applyState(value);
      if (!current()) return;
      const created = groups.find(group => group.name === name);
      renderGroupOptions(created?.id || '');
      $('model-new-group').value = '';
      updateRoutePreview();
    }, current, `inline-group:${version}`);
  }

  function openDelete(model) {
    deleteVersion++;
    deleting = model.id;
    text('delete-model-name', `${model.groupName} / ${model.name}`);
    text('delete-model-route', model.routeId);
    $('delete-default-warning').hidden = state.defaultModelId !== model.id;
    notice('delete-error'); $('delete-dialog').showModal(); syncPendingControls();
  }

  // ---- quick test -------------------------------------------------------

  async function startQuickTest(modelId) {
    if (!quickTestReady() || pendingTests.has(modelId)) return;
    const model = state.models.find(item => item.id === modelId);
    if (!model) return;
    const signature = modelSignature(model);
    const controller = new AbortController();
    const operation = { controller };
    pendingTests.set(modelId, operation);
    testResults.delete(modelId);
    renderModels();
    const timeoutMs = (state.capabilities.quickTestTimeoutMs || 18000) + 10000;
    try {
      const result = await api(`/api/models/${modelId}/quick-test`, { method: 'POST', timeoutMs, signal: controller.signal });
      if (pendingTests.get(modelId) !== operation) return;
      const latest = state.models.find(item => item.id === modelId);
      if (!latest || modelSignature(latest) !== signature) return;
      const formatted = formatTestResult(result);
      testResults.set(modelId, { ...formatted, at: Date.now(), signature });
    } catch (error) {
      if (pendingTests.get(modelId) !== operation) return;
      const latest = state?.models.find(item => item.id === modelId);
      if (!latest || modelSignature(latest) !== signature) return;
      if (controller.signal.aborted) {
        testResults.set(modelId, { label: '测速已取消', detail: '未改变任何模型配置', tone: 'neutral', at: Date.now(), signature });
      } else if (!(error instanceof StaleRequest)) {
        testResults.set(modelId, { label: '测速未通过', detail: error.message, tone: 'error', at: Date.now(), signature });
      }
    } finally {
      if (pendingTests.get(modelId) === operation) { pendingTests.delete(modelId); renderModels(); }
    }
  }

  // ---- aggregates ---------------------------------------------------------

  function modelById(id) { return state?.models.find(item => item.id === id); }

  function aggregateRoutePreview() {
    const name = $('aggregate-name').value.trim() || '名称';
    text('aggregate-route-preview', `adroute/agg/${name}`);
  }

  function renderAggregateMemberOptions(select, groupId, selectedId) {
    select.replaceChildren();
    const placeholder = document.createElement('option');
    placeholder.value = ''; placeholder.textContent = '选择模型';
    select.append(placeholder);
    const models = state.models.filter(item => item.groupId === groupId);
    for (const model of models) {
      const option = document.createElement('option');
      option.value = model.id;
      option.textContent = model.name + (model.disabled ? '（已关闭）' : '');
      select.append(option);
    }
    select.value = selectedId || '';
  }

  function addAggregateMemberRow(groupId = '', modelId = '') {
    const row = node('div', undefined, 'aggregate-member-row');
    const groupSelect = document.createElement('select');
    groupSelect.setAttribute('aria-label', '成员所属组');
    const placeholder = document.createElement('option');
    placeholder.value = ''; placeholder.textContent = '选择组';
    groupSelect.append(placeholder);
    for (const group of state.groups) {
      const option = document.createElement('option');
      option.value = group.id; option.textContent = group.name;
      groupSelect.append(option);
    }
    groupSelect.value = groupId || '';
    const modelSelect = document.createElement('select');
    modelSelect.setAttribute('aria-label', '成员模型');
    renderAggregateMemberOptions(modelSelect, groupId, modelId);
    groupSelect.addEventListener('change', () => {
      renderAggregateMemberOptions(modelSelect, groupSelect.value, '');
    });
    const order = node('div', undefined, 'member-order');
    const moveUp = button('↑', () => moveAggregateMemberRow(row, -1), 'icon-button');
    const moveDown = button('↓', () => moveAggregateMemberRow(row, 1), 'icon-button');
    const remove = button('×', () => { row.remove(); }, 'icon-button');
    moveUp.title = '上移'; moveDown.title = '下移'; remove.title = '移除';
    for (const control of [moveUp, moveDown, remove]) {
      control.setAttribute('aria-label', control.title);
      control.style.width = '26px'; control.style.height = '26px';
    }
    order.append(moveUp, moveDown, remove);
    row.append(groupSelect, modelSelect, order);
    row.dataset.row = 'member';
    $('aggregate-members').append(row);
  }

  function moveAggregateMemberRow(row, delta) {
    const sibling = delta < 0 ? row.previousElementSibling : row.nextElementSibling;
    if (!sibling) return;
    if (delta < 0) row.parentElement.insertBefore(row, sibling);
    else row.parentElement.insertBefore(sibling, row);
  }

  function aggregateMembers() {
    const ids = [];
    for (const row of $('aggregate-members').querySelectorAll('.aggregate-member-row')) {
      const model = row.querySelectorAll('select')[1].value;
      if (model) ids.push(model);
    }
    return ids;
  }

  function validateAggregateName() {
    const value = $('aggregate-name').value;
    if (!value) { aggregateFieldError('name', '请填写聚合名称。'); return false; }
    if (value.length > 100 || !MODEL_NAME_RE.test(value)) {
      aggregateFieldError('name', '聚合名称只能使用英文字母、数字、点、下划线和连字符，不能包含中文、空格或斜杠。');
      return false;
    }
    notice('aggregate-error-name');
    return true;
  }

  function openAggregateForm(aggregate = null) {
    editingAggregate = aggregate;
    aggregateVersion++;
    $('aggregate-form').reset();
    notice('aggregate-error');
    for (const name of ['name', 'members', 'rounds']) notice(`aggregate-error-${name}`);
    $('aggregate-dialog-title').textContent = aggregate ? '编辑聚合' : '新建聚合';
    $('aggregate-save').textContent = aggregate ? '保存修改' : '保存聚合';
    $('aggregate-name').value = aggregate?.name || '';
    $('aggregate-rounds').value = aggregate ? String(aggregate.maxRounds || 5) : '5';
    $('aggregate-open').checked = aggregate ? !aggregate.disabled : true;
    $('aggregate-open-row').hidden = Boolean(aggregate);
    $('aggregate-members').replaceChildren();
    if (aggregate) {
      for (const member of aggregate.members) {
        const model = member.modelId ? modelById(member.modelId) : null;
        addAggregateMemberRow(model?.groupId || '', member.modelId || '');
      }
    } else {
      addAggregateMemberRow(state.groups[0]?.id || '', '');
    }
    aggregateRoutePreview();
    $('aggregate-dialog').showModal();
    syncPendingControls();
  }

  function renderAggregates() {
    const list = $('aggregate-list');
    list.replaceChildren();
    const aggregates = state.aggregates || [];
    text('aggregate-count', aggregates.length);
    if (!aggregates.length) {
      const empty = node('div', undefined, 'empty-panel');
      empty.append(node('h2', '还没有聚合'), node('p', '聚合把多个模型串成一条故障转移链；某个成员连不上时会自动换下一个，客户端只看到一个模型 ID。', 'muted'),
        button('新建聚合', () => openAggregateForm(), 'button primary'));
      list.append(empty);
      return;
    }
    for (const aggregate of aggregates) {
      const card = node('article', undefined, 'aggregate-card');
      card.dataset.aggregateId = aggregate.id;
      const main = node('div', undefined, 'model-main');
      const title = node('div', undefined, 'aggregate-title');
      title.append(node('h3', aggregate.name));
      if (aggregate.disabled) title.append(badge('已关闭', 'badge muted-badge'));
      if (state.defaultModelId === aggregate.id) title.append(badge('网页默认'));
      title.append(badge(`最多 ${aggregate.maxRounds} 轮`, 'badge muted-badge'));
      main.append(title);
      const meta = node('p', `对外模型 ID：${aggregate.routeId}`, 'aggregate-meta mono');
      main.append(meta);
      const chips = node('div', undefined, 'aggregate-members-preview');
      aggregate.members.forEach((member, index) => {
        const chip = node('span', undefined, 'aggregate-member-chip' + (member.disabled || member.missing ? ' off' : ''));
        chip.append(node('span', `${index + 1}`, 'member-index'));
        const label = member.missing ? '已删除' : `${member.groupName} / ${member.name}`;
        chip.append(node('span', label + (member.disabled ? '（已关闭）' : member.missing ? '（缺失）' : '')));
        chips.append(chip);
      });
      main.append(chips);
      const controls = node('div', undefined, 'model-controls');
      const toggleLabel = node('label', undefined, 'availability-control');
      const toggle = document.createElement('input');
      toggle.type = 'checkbox'; toggle.checked = !aggregate.disabled; toggle.dataset.aggregateAvailability = aggregate.id;
      toggle.setAttribute('aria-label', `对外开放聚合 ${aggregate.name}`);
      const toggleText = node('span', aggregate.disabled ? '已关闭' : '已开放');
      toggle.disabled = !aggregatesReady();
      toggle.addEventListener('change', () => toggleAggregateAvailability(aggregate, toggle.checked, toggle, toggleText));
      toggleLabel.append(toggle, toggleText);
      const actions = node('div', undefined, 'model-actions');
      const edit = button('编辑', () => openAggregateForm(aggregate), 'button subtle small-button');
      const remove = button('删除', () => deleteAggregate(aggregate), 'button subtle small-button danger');
      for (const control of [edit, remove]) control.disabled = !backendReady();
      actions.append(edit, remove);
      controls.append(toggleLabel, actions);
      const wrap = node('div', undefined, 'model-row');
      wrap.style.borderTop = '0';
      wrap.style.padding = '0';
      wrap.append(main, controls);
      card.append(wrap);
      list.append(card);
    }
  }

  async function toggleAggregateAvailability(aggregate, enabled, control, label) {
    control.disabled = true; label.textContent = '正在保存…';
    try {
      applyState(await api(`/api/aggregates/${aggregate.id}/availability`, { method: 'PUT', body: { enabled } }));
    } catch (error) {
      if (!(error instanceof StaleRequest)) toast(`开放状态未更改：${error.message}`);
      control.checked = !enabled;
      label.textContent = aggregate.disabled ? '已关闭' : '已开放';
      control.disabled = false;
    }
  }

  async function deleteAggregate(aggregate) {
    if (!confirm(`删除聚合 ${aggregate.name}？对外 ID adroute/agg/${aggregate.name} 会立即失效。`)) return;
    try {
      applyState(await api(`/api/aggregates/${aggregate.id}`, { method: 'DELETE' }));
      toast('聚合已删除。');
    } catch (error) {
      if (!(error instanceof StaleRequest)) toast(`删除失败：${error.message}`);
    }
  }

  // ---- requests ---------------------------------------------------------

  function renderRequests() {
    const requests = state.requests || [];
    const filter = $('request-filter').value;
    const visible = requests.filter(item => filter === 'all'
      || (filter === 'success' ? item.status >= 200 && item.status < 300
        : filter === 'error' ? item.status >= 400 : item.status < 200 || (item.status >= 300 && item.status < 400)));
    const container = $('all-requests'); container.replaceChildren();
    if (!visible.length) { container.append(node('div', '暂无请求记录。客户端发出请求后会在这里显示状态摘要。', 'empty-panel')); return; }
    const table = node('table'), head = node('tr'), thead = node('thead'), body = node('tbody');
    for (const label of ['时间', '模型组', '对外 ID', '档位', '状态', '耗时']) head.append(node('th', label));
    thead.append(head); table.append(thead);
    for (const item of visible) {
      const row = node('tr');
      row.append(node('td', new Date(item.time).toLocaleTimeString(), 'time-cell'),
        node('td', item.groupName || '已删除', 'provider-cell'),
        node('td', item.routeId || item.model, 'model-cell'),
        node('td', item.effort || '—'));
      const status = node('td', undefined, 'duration-cell');
      status.append(node('span', item.status, 'request-status'));
      if (item.status !== 200 && item.detail !== undefined) status.append(button('查看', () => openRequestDetail(item), 'text-button small-button request-detail-btn'));
      row.append(status, node('td', `${item.durationMs} ms`, 'duration-cell'));
      body.append(row);
    }
    table.append(body); container.append(table);
  }

  function openRequestDetail(item) {
    for (const [id, value] of Object.entries({
      time: new Date(item.time).toLocaleString(), group: item.groupName || '已删除',
      route: item.routeId || '—', model: item.model, effort: item.effort || '（未指定）',
      status: item.status, duration: `${item.durationMs} ms`,
    })) text(`request-meta-${id}`, value);
    text('request-detail-body', item.detail || ''); $('request-dialog').showModal();
  }

  function showTab(value, load = true) {
    tab = value;
    text('page-title', titles[tab][0]); text('breadcrumb-title', titles[tab][0]); text('page-description', titles[tab][1]);
    for (const item of Object.keys(titles)) $(`${item}-panel`).hidden = !state || item !== tab;
    for (const control of document.querySelectorAll('nav [data-tab]')) {
      const selected = control.dataset.tab === tab;
      control.classList.toggle('selected', selected);
      if (selected) control.setAttribute('aria-current', 'page'); else control.removeAttribute('aria-current');
    }
    if (tab === 'setup' && load && !setup) loadSetup();
    if (tab === 'compatibility' && load) compatibility.load();
  }

  async function copy(value) {
    if (!value) return;
    try { await navigator.clipboard.writeText(value); toast('已复制。'); }
    catch { toast('无法访问剪贴板，请手动选择并复制。'); }
  }

  // ---- appearance -------------------------------------------------------

  const THEME_KEY = 'adroute.theme';

  function currentTheme() {
    return document.documentElement.dataset.theme === 'light' ? 'light' : 'dark';
  }

  function applyTheme(theme) {
    const light = theme === 'light';
    if (light) document.documentElement.dataset.theme = 'light';
    else document.documentElement.removeAttribute('data-theme');
    for (const toggle of document.querySelectorAll('[data-theme-toggle]')) {
      toggle.textContent = light ? '深色模式' : '浅色模式';
      toggle.setAttribute('aria-pressed', String(light));
      toggle.title = light ? '切换到深色外观' : '切换到浅色外观';
    }
    $('theme-color-meta')?.setAttribute('content', light ? '#f6f6f8' : '#0a0a0a');
    try { localStorage.setItem(THEME_KEY, light ? 'light' : 'dark'); } catch { /* storage may be blocked */ }
  }

  for (const toggle of document.querySelectorAll('[data-theme-toggle]')) {
    toggle.addEventListener('click', () => applyTheme(currentTheme() === 'light' ? 'dark' : 'light'));
  }

  // ---- wiring -----------------------------------------------------------

  $('auth-form').addEventListener('submit', event => { event.preventDefault(); token = $('admin-token').value.trim(); login(); });
  async function login() {
    $('auth-submit').disabled = true;
    try {
      const value = await api('/api/state');
      remember(token); $('admin-token').value = '';
      $('auth-screen').hidden = true; $('app-shell').hidden = false;
      applyState(value); await loadSetup(); renderAccess();
    } catch (error) { if (!(error instanceof StaleRequest)) clearSession(error.message); }
    finally { $('auth-submit').disabled = false; }
  }

  $('logout-button').addEventListener('click', () => clearSession());
  applyTheme(currentTheme());
  $('refresh-button').addEventListener('click', refresh); $('retry-state').addEventListener('click', refresh);
  $('brand-home').addEventListener('click', event => { event.preventDefault(); showTab('models'); });
  for (const control of document.querySelectorAll('[data-tab]')) control.addEventListener('click', () => showTab(control.dataset.tab));
  for (const control of document.querySelectorAll('[data-close-dialog]')) control.addEventListener('click', () => closeDialog(control.dataset.closeDialog));
  for (const dialog of document.querySelectorAll('dialog')) dialog.addEventListener('cancel', event => { event.preventDefault(); closeDialog(dialog.id); });
  $('setup-back').addEventListener('click', () => showTab('models')); $('setup-retry').addEventListener('click', loadSetup);
  $('request-filter').addEventListener('change', () => { if (state) renderRequests(); });

  $('add-model').addEventListener('click', () => openModelForm());
  $('add-aggregate').addEventListener('click', () => openAggregateForm());
  $('aggregate-add-member').addEventListener('click', () => {
    if ($('aggregate-members').querySelectorAll('.aggregate-member-row').length >= 8) {
      aggregateFieldError('members', '聚合最多 8 个成员。');
      return;
    }
    notice('aggregate-error-members');
    addAggregateMemberRow(state.groups[0]?.id || '', '');
  });
  $('aggregate-name').addEventListener('input', () => { notice('aggregate-error-name'); aggregateRoutePreview(); });
  $('aggregate-form').addEventListener('submit', event => {
    event.preventDefault();
    if (!backendReady()) { notice('aggregate-error', BACKEND_UPDATE); return; }
    const context = editingAggregate;
    const version = aggregateVersion;
    const key = aggregateWriteKey();
    const current = () => version === aggregateVersion && $('aggregate-dialog').open;
    if (pendingWrites.has(key)) return;
    notice('aggregate-error');
    for (const name of ['name', 'members', 'rounds']) notice(`aggregate-error-${name}`);
    let ok = true;
    if (!validateAggregateName()) ok = false;
    const members = aggregateMembers();
    if (!members.length) { aggregateFieldError('members', '请至少选择一个成员模型。'); ok = false; }
    if (new Set(members).size !== members.length) { aggregateFieldError('members', '成员不能重复。'); ok = false; }
    if (members.length > 8) { aggregateFieldError('members', '聚合最多 8 个成员。'); ok = false; }
    const roundsRaw = $('aggregate-rounds').value.trim();
    const rounds = roundsRaw === '' ? 5 : Number(roundsRaw);
    if (!Number.isInteger(rounds) || rounds < 1 || rounds > 10) { aggregateFieldError('rounds', '重试轮数必须是 1~10 之间的整数。'); ok = false; }
    if (!ok) return;
    const body = { name: $('aggregate-name').value.trim(), members, maxRounds: rounds };
    if (!context) body.disabled = !$('aggregate-open').checked;
    action($('aggregate-save'), 'aggregate-error', async () => {
      const value = await api(context ? `/api/aggregates/${context.id}` : '/api/aggregates', { method: context ? 'PUT' : 'POST', body });
      applyState(value);
      if (!current()) return;
      closeDialog('aggregate-dialog');
      toast(context ? '聚合已保存。' : '聚合已保存，客户端可以开始使用。');
    }, current, key);
  });
  $('manage-groups').addEventListener('click', () => { groupsVersion++; groupDrafts.clear(); notice('groups-error'); $('group-create-form').reset(); renderGroups(); $('groups-dialog').showModal(); });
  $('export-config').addEventListener('click', () => {
    action($('export-config'), 'global-error', async () => {
      const backup = await api('/api/config/export');
      const url = URL.createObjectURL(new Blob([JSON.stringify(backup, null, 2) + '\n'], { type: 'application/json' }));
      const link = document.createElement('a'); link.href = url; link.download = 'adroute-config.json';
      document.body.append(link); link.click(); link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      toast('配置已导出；文件包含明文 API Key，请妥善保管。');
    });
  });
  $('import-config').addEventListener('click', () => $('import-config-file').click());
  $('import-config-file').addEventListener('change', async () => {
    const file = $('import-config-file').files[0];
    if (!file) return;
    const started = epoch, revision = state?.revision;
    try {
      if (file.size > 128 * 1024 * 1024) throw new Error('配置文件过大。');
      const backup = JSON.parse(await file.text());
      if (started !== epoch) return;
      importDraft = { backup, expectedRevision: revision };
      notice('global-error'); notice('import-error'); $('import-dialog').showModal();
    } catch (error) {
      if (started === epoch) { $('import-config-file').value = ''; notice('global-error', error instanceof SyntaxError ? '配置文件不是有效的 JSON。' : error.message); }
    }
  });
  $('import-confirm').addEventListener('click', () => {
    if (!importDraft) return;
    const draft = importDraft;
    action($('import-confirm'), 'import-error', async () => {
      const result = await api('/api/config/import', { method: 'POST', body: draft });
      applyState(result); closeDialog('import-dialog'); toast('配置导入成功。');
    }, () => $('import-dialog').open);
  });
  $('default-model').addEventListener('change', () => { if (state) changeDefault($('default-model').value); });

  $('model-real-id').addEventListener('input', () => {
    notice('model-error-realid');
    if (!nameTouched) { $('model-name').value = $('model-real-id').value; notice('model-error-name'); }
    updateRoutePreview();
  });
  $('model-name').addEventListener('input', () => {
    nameTouched = true; notice('model-error-name'); updateRoutePreview();
  });
  $('model-group').addEventListener('change', () => { notice('model-error-group'); updateRoutePreview(); });
  $('model-new-group').addEventListener('keydown', event => { if (event.key === 'Enter') { event.preventDefault(); createGroupInline(); } });
  $('model-create-group').addEventListener('click', createGroupInline);
  $('model-api-key-toggle').addEventListener('click', () => {
    const field = $('model-api-key');
    const reveal = field.type === 'password';
    field.type = reveal ? 'text' : 'password';
    text('model-api-key-toggle', reveal ? '隐藏' : '显示');
    $('model-api-key-toggle').setAttribute('aria-pressed', String(reveal));
  });
  $('model-discover').addEventListener('click', discoverModels);
  $('model-discover-search').addEventListener('input', showDiscoveredModels);
  $('model-api-key').addEventListener('input', () => { resetDiscovery(); notice('model-error-apikey'); });
  $('model-base-url').addEventListener('input', () => { resetDiscovery(); notice('model-error-baseurl'); });

  $('model-form').addEventListener('submit', event => {
    event.preventDefault();
    if (!backendReady()) { notice('model-error', BACKEND_UPDATE); return; }
    const context = editingModel;
    const version = formVersion;
    const key = modelWriteKey(), current = () => version === formVersion && $('model-dialog').open;
    if (pendingWrites.has(key)) return;
    clearFieldErrors(); notice('model-error');
    let ok = true;
    if (!$('model-group').value) { fieldError('group', '请选择模型组，或先创建一个组。'); ok = false; }
    if (!$('model-base-url').value.trim()) { fieldError('baseurl', '请填写上游 Base URL。'); ok = false; }
    if (!context && !$('model-api-key').value.trim()) { fieldError('apikey', '请填写这个模型的上游 API Key。'); ok = false; }
    if (!$('model-real-id').value.trim()) { fieldError('realid', '请填写上游真实模型 ID。'); ok = false; }
    if (!validateNameField()) ok = false;
    const website = $('model-website').value.trim();
    if (website && !/^https?:\/\/.+/i.test(website)) { fieldError('website', '网站地址无效；请填写 http:// 或 https:// 开头的完整网址，或留空。'); ok = false; }
    if (!ok) return;
    const body = modelBody();
    const renames = context && (context.name !== body.name || context.groupId !== body.groupId);
    if (renames && !confirm(`保存后这个模型的对外 ID 会变成 adroute/${groupName(body.groupId)}/${body.name}，旧 ID 立即失效，客户端需要重新选择。继续？`)) return;
    action($('model-save'), 'model-error', async () => {
      const value = await api(context ? `/api/models/${context.id}` : '/api/models', { method: context ? 'PUT' : 'POST', body });
      if (context) testResults.delete(context.id);
      applyState(value);
      if (!current()) return;
      closeDialog('model-dialog');
      toast(context ? '模型已保存；对外 ID 与连接配置已更新。' : '模型已保存，客户端可以开始使用。');
    }, current, key);
  });

  $('group-create-form').addEventListener('submit', event => {
    event.preventDefault();
    const name = $('group-new-name').value;
    notice('groups-error');
    if (!GROUP_NAME_RE.test(name) || name.length > 48) { notice('groups-error', '组名只能使用小写英文字母、数字和单个连字符，不能包含空格。'); return; }
    const version = groupsVersion, current = () => version === groupsVersion && $('groups-dialog').open;
    action($('group-create-submit'), 'groups-error', async () => {
      applyState(await api('/api/groups', { method: 'POST', body: { name } }));
      if (current()) $('group-create-form').reset();
    }, current);
  });

  $('delete-form').addEventListener('submit', event => {
    event.preventDefault();
    const target = deleting;
    if (!target) return;
    const version = deleteVersion, current = () => version === deleteVersion && deleting === target && $('delete-dialog').open;
    action($('delete-submit'), 'delete-error', async () => {
      const value = await api(`/api/models/${target}`, { method: 'DELETE' });
      if (current()) closeDialog('delete-dialog');
      pendingTests.get(target)?.controller.abort();
      testResults.delete(target); pendingTests.delete(target);
      if (filterGroup && !value.groups.some(group => group.id === filterGroup)) filterGroup = '';
      applyState(value);
      toast('模型已删除，对应对外 ID 会明确报错。');
    }, current, `delete:${target}`);
  });

  $('catalog-retry').addEventListener('click', () => {
    action($('catalog-retry'), 'catalog-status', async () => {
      applyState(await api('/api/catalog/retry', { method: 'POST' }));
    });
  });

  $('opencode-sync').addEventListener('click', () => {
    if (syncing) return;
    if (!confirm('将当前对外开放的模型同步到 OpenCode？已关闭模型不会同步。会备份并只替换现有 AdRoute 的模型列表，不修改其他设置。')) return;
    syncing = true; const started = epoch;
    action($('opencode-sync'), 'opencode-sync-status', async () => {
      notice('opencode-sync-status', '正在同步…');
      const result = await api('/api/opencode/sync', { method: 'POST', body: {} });
      notice('opencode-sync-status', `同步成功${Number.isInteger(result.modelCount) ? ` · ${result.modelCount} 个模型` : ''}${result.configPath ? ` · ${result.configPath}` : ''}${result.backupPath ? ` · 备份：${result.backupPath}` : ''}。请在 OpenCode 中刷新模型目录。`);
    }).finally(() => { if (started === epoch) { syncing = false; syncPendingControls(); } });
  });

  for (const [id, get] of Object.entries({
    'copy-client-base-url': () => setup?.proxyUrl,
    'copy-client-api-key': () => setup?.proxyToken,
    'copy-proxy-url': () => setup?.proxyUrl,
    'copy-config': () => setup?.codexConfig,
  })) $(id).addEventListener('click', () => copy(get()));
  $('toggle-client-api-key').addEventListener('click', () => {
    const field = $('client-api-key');
    const reveal = field.type === 'password';
    field.type = reveal ? 'text' : 'password';
    text('toggle-client-api-key', reveal ? '隐藏' : '显示');
    $('toggle-client-api-key').setAttribute('aria-pressed', String(reveal));
  });

  setInterval(() => { if (!document.hidden && token && state) refresh(); }, 45000);
  document.addEventListener('visibilitychange', () => { if (!document.hidden && token && state) refresh(); });
  document.addEventListener('pointerdown', event => { if (activeTestDetail && !activeTestDetail.root.contains(event.target)) closeTestDetail(); });
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && activeTestDetail) {
      activeTestDetail.info.focus({ preventScroll: true });
      closeTestDetail(); event.preventDefault();
    }
  });
  document.addEventListener('scroll', event => {
    if (activeTestDetail && !activeTestDetail.tooltip.contains(event.target)) closeTestDetail();
  }, true);
  window.addEventListener('resize', closeTestDetail);
  window.addEventListener('hashchange', () => {
    const incoming = new URLSearchParams(location.hash.slice(1)).get('token');
    if (!incoming) return;
    history.replaceState(null, '', location.pathname + location.search);
    clearSession(); token = incoming; login();
  });
  const supplied = new URLSearchParams(location.hash.slice(1)).get('token');
  if (location.hash) history.replaceState(null, '', location.pathname + location.search);
  try { token = supplied || sessionStorage.getItem(SESSION) || ''; } catch { token = supplied || ''; }
  if (token) login(); else clearSession();
})();
