'use strict';

window.createCompatibilityController = ({ api, getState, applyState, showTab }) => {
  const $ = id => document.getElementById(id);
  let revision = 0, targetVersion = 0, data = null, preview = null, timer, loading = false, requestedModel = null, bindingSignature = '';
  const pending = new Map();
  let renderedTargetSignature = '';
  const text = (id, value) => { $(id).textContent = value; };
  const ready = () => getState()?.capabilities?.models === true && getState()?.capabilities?.compatibility === true && getState()?.capabilities?.adapterDirectory === true;
  const option = (value, label) => { const node = document.createElement('option'); node.value = value; node.textContent = label; return node; };
  const message = value => { text('compat-error', value); $('compat-error').hidden = !value; };
  const target = () => (getState()?.models || []).find(model => model.id === $('compat-model').value);
  const label = model => `${model.groupName} / ${model.name} · ${model.model}`;
  const targetSignature = model => model ? JSON.stringify([model.id, model.groupId, model.groupName, model.name, model.model, model.baseUrl, model.apiKey, model.profile]) : '';
  function syncControls() {
    const bindingBusy = pending.has(`binding:${target()?.id}`);
    for (const id of ['compat-copy-prompt', 'adapter-refresh', 'compat-save', 'compat-disable', 'compat-structure-test', 'diagnosis-arm', 'diagnosis-stop', 'diagnosis-view', 'diagnosis-replay']) {
      $(id).disabled = !ready() || pending.has(id) || (bindingBusy && (id === 'compat-save' || id === 'compat-disable'));
    }
  }
  async function work(control, run, matchesTarget = () => true, key = control.id || control.dataset.operationKey || control) {
    if (pending.has(key)) return;
    const started = revision, operation = {};
    pending.set(key, operation);
    const current = () => started === revision && matchesTarget();
    control.disabled = true; message(''); syncControls();
    try { await run(current); }
    catch (error) { if (current() && error.constructor.name !== 'StaleRequest') message(error.message); }
    finally {
      if (pending.get(key) === operation) pending.delete(key);
      if (started === revision) { control.disabled = !ready(); syncControls(); }
    }
  }
  function invalidatePrompt() {
    targetVersion++;
    $('compat-prompt-text').value = '';
    text('compat-prompt-status', '');
    const model = target();
    text('compat-prompt-target', model ? `复制目标：${label(model)}（${model.routeId}）。请确认它是出问题的模型。` : '未指定目标：可先复制通用 Prompt；选择下方模型后再复制会更准确。');
  }
  function fillProfiles(id, selected) {
    const select = $(id);
    select.replaceChildren(option('none', '原样透传 · 关闭修复'), ...(data?.packages || []).filter(pack => pack.canApply).map(pack => option(pack.ref, `${pack.name} · ${pack.ref}`)));
    if (![...select.options].some(o => o.value === selected)) {
      const unavailable = option(selected, `${selected} · 源文件异常或版本缺失，请处理`); unavailable.disabled = true; select.append(unavailable);
    }
    select.value = selected;
  }
  function renderProfile(selected = null) {
    const model = target();
    fillProfiles('compat-profile', selected ?? model?.profile ?? 'none');
    renderedTargetSignature = targetSignature(model);
    text('compat-binding-help', model ? `当前模型：${label(model)}。只修改此条目，不影响组内其他模型，也不会切换客户端模型或网页默认路由。` : '请选择一个已保存的模型条目。没有组默认、继承或按真实模型 ID 匹配。');
  }
  function renderBindings() {
    const selected = requestedModel || $('compat-model').value;
    const profileDraft = $('compat-profile').value, previousTarget = renderedTargetSignature, explicitOpen = Boolean(requestedModel);
    const models = getState()?.models || [];
    $('compat-model').replaceChildren(option('', models.length ? '请选择模型条目' : '还没有保存模型'), ...models.map(model => option(model.id, `${label(model)}${model.disabled ? '（已关闭）' : ''}`)));
    if ([...$('compat-model').options].some(o => o.value === selected)) $('compat-model').value = selected;
    requestedModel = null;
    const preserveDraft = !explicitOpen && previousTarget === targetSignature(target());
    renderProfile(preserveDraft ? profileDraft : null);
    if (!preserveDraft) { invalidatePrompt(); text('compat-structure-result', ''); }
    syncControls();
  }
  function paragraph(value, className = 'field-help') { const p = document.createElement('p'); p.className = className; p.textContent = value; return p; }
  function removeButton(payload, name, bound) {
    const control = document.createElement('button'); control.type = 'button'; control.className = 'button subtle small-button'; control.textContent = bound ? '先解除绑定' : '移除'; control.disabled = bound || !ready();
    control.dataset.operationKey = `remove:${payload.ref || payload.file}`;
    control.disabled = control.disabled || pending.has(control.dataset.operationKey);
    control.addEventListener('click', () => work(control, async current => {
      if (!confirm(`移除 ${name} 的登记及对应候选文件？此操作不可撤销，已绑定版本不能移除。`)) return;
      const result = await api('/api/compatibility/remove', { method: 'POST', body: payload });
      if (current()) { data = result; render(); }
    }));
    return control;
  }
  function renderPackages() {
    const container = $('adapter-packages'); container.replaceChildren();
    const known = new Set();
    for (const pack of data?.packages || []) {
      known.add(pack.file);
      const card = document.createElement('article'); card.className = 'compat-package panel'; card.dataset.ref = pack.ref;
      const heading = document.createElement('h3'); heading.textContent = `${pack.name} · ${pack.ref}`;
      const labels = { verified: '文件格式及本地样例通过', unscanned: '已验证登记快照；目录待刷新', missing: '源文件缺失，保留原验证快照；现有绑定不变', invalid: '源文件无效，保留原验证快照', conflict: '同版本文件内容冲突，未覆盖原验证快照' };
      const bound = pack.bindings.length > 0;
      const usage = bound ? pack.bindings.map(item => `${item.groupName} / ${item.modelName} / ${item.model}`).join('；') : '未绑定模型';
      const issue = data.files.find(file => file.file === pack.file && file.status !== 'verified')?.message;
      card.append(heading, paragraph(pack.description, 'muted'), paragraph(`文件：${pack.file}`), paragraph(`${labels[pack.sourceStatus] || pack.sourceStatus} · 真实上游未验证`), paragraph(`绑定：${usage}`));
      if (issue) card.append(paragraph(issue, 'compat-file-error'));
      const actions = document.createElement('div'); actions.className = 'provider-actions'; actions.append(removeButton({ ref: pack.ref }, pack.ref, bound)); card.append(actions); container.append(card);
    }
    for (const file of (data?.files || []).filter(file => file.status !== 'verified' && !known.has(file.file))) {
      const card = document.createElement('article'); card.className = 'compat-package panel'; card.dataset.file = file.file;
      const heading = document.createElement('h3'); heading.textContent = file.file;
      card.append(heading, paragraph(`不可适配：${file.message}`, 'compat-file-error'), removeButton({ file: file.file }, file.file, false)); container.append(card);
    }
    if (!container.children.length) container.append(paragraph('尚无修复包。让 AI 将文件放到上方目录，再点击“刷新修复包”。'));
    text('adapter-count', `${data?.packages.length || 0} 个登记版本`);
  }
  function render() {
    text('adapter-directory', data.directory);
    renderPackages();
    const diagnosis = data.diagnosis || { status: 'off' };
    const statuses = { off: '未开启', waiting: '等待下一次匹配请求', capturing: '正在采集', ready: '采集完成', expired: '已超时停止' };
    text('diagnosis-status', `${statuses[diagnosis.status] || diagnosis.status}${diagnosis.model ? ` · ${diagnosis.model}` : ''}${diagnosis.truncated ? ' · 已截断，不能视为完整样本' : ''}`);
    const signature = JSON.stringify([(getState()?.models || []).map(targetSignature), data.packages.map(pack => [pack.ref, pack.canApply])]);
    if (signature !== bindingSignature) { bindingSignature = signature; renderBindings(); }
    if (data.directoryError) message(data.directoryError);
    clearTimeout(timer);
    if (['waiting', 'capturing'].includes(diagnosis.status)) timer = setTimeout(load, 2000);
  }
  async function load() {
    if (!ready()) { message('后台不支持兼容修复与 Prompt，请重启 adroute 后刷新。'); return; }
    if (loading) return;
    loading = true; const started = revision;
    try { const result = await api('/api/compatibility'); if (started === revision) { data = result; render(); } }
    catch (error) { if (started === revision && error.constructor.name !== 'StaleRequest') message(error.message); }
    finally { if (started === revision) loading = false; }
  }
  $('adapter-refresh').addEventListener('click', () => work($('adapter-refresh'), async current => {
    const result = await api('/api/compatibility/refresh', { method: 'POST', body: {} });
    if (current()) { data = result; render(); text('adapter-refresh-result', result.directoryStatus === 'error' ? '未完成目录刷新，已保留原验证快照和绑定；请先处理上方错误。' : `刷新完成：${result.packages.filter(pack => pack.canApply).length} 个可选版本，${result.files.filter(file => file.status !== 'verified').length} 个异常文件。刷新不会自动绑定模型。`); }
  }));
  $('compat-copy-prompt').addEventListener('click', () => work($('compat-copy-prompt'), async current => {
    const version = targetVersion;
    const model = target();
    const includeDiagnosis = Boolean(preview?.status === 'ready' && preview.raw && preview.modelId === model?.id && $('diagnosis-export-consent').checked);
    const diagnosisId = includeDiagnosis ? preview.captureId : undefined;
    const result = await api('/api/compatibility/prompt', { method: 'POST', body: { modelId: model?.id || '', includeDiagnosis, diagnosisId } });
    if (!current() || version !== targetVersion || (includeDiagnosis && (!$('diagnosis-export-consent').checked || preview?.captureId !== diagnosisId))) return;
    $('compat-prompt-text').value = result.prompt;
    text('adapter-directory', result.directory);
    try {
      await navigator.clipboard.writeText(result.prompt);
      if (current() && version === targetVersion) text('compat-prompt-status', `Prompt 已复制。${result.diagnosisAvailable ? '已附此目标的授权诊断路径。' : '未附诊断；如需共享，请先在高级诊断预览并确认。'}请发给能访问本机文件的 AI，并补充报错。`);
    } catch {
      if (current() && version === targetVersion) { $('compat-prompt-view').open = true; text('compat-prompt-status', '自动复制失败。请选择下方完整 Prompt，手动复制后交给本地 AI。'); $('compat-prompt-text').focus(); $('compat-prompt-text').select(); }
    }
  }));
  $('diagnosis-export-consent').addEventListener('change', () => { invalidatePrompt(); text('compat-prompt-status', '诊断共享确认已变更，请按当前选择重新复制 Prompt。'); });
  $('compat-model').addEventListener('change', () => { renderProfile(); invalidatePrompt(); text('compat-save-result', ''); text('compat-structure-result', ''); syncControls(); });
  function saveBinding(disable = false) {
    const model = target();
    const version = targetVersion, signature = targetSignature(model);
    const ref = disable ? 'none' : $('compat-profile').value;
    const control = $(disable ? 'compat-disable' : 'compat-save');
    return work(control, async current => {
      if (!model) throw new Error('请选择已保存的模型条目。');
      if (ref !== 'none' && !data?.packages.some(pack => pack.ref === ref && pack.canApply)) throw new Error('请选择已校验的可用版本，或先处理源文件异常。');
      const result = await api(`/api/compatibility/selection/${model.id}`, { method: 'PUT', body: { profile: ref } });
      const stillCurrent = current();
      applyState(result);
      if (stillCurrent && target()?.id === model.id) {
        text('compat-save-result', `绑定已保存：${label(model)}，只影响此条目的后续请求；未切换客户端模型或默认路由。`);
        await load();
      }
    }, () => version === targetVersion && signature === targetSignature(target()), model ? `binding:${model.id}` : control.id);
  }
  $('compat-binding-form').addEventListener('submit', event => { event.preventDefault(); saveBinding(); });
  $('compat-disable').addEventListener('click', () => saveBinding(true));
  $('compat-structure-test').addEventListener('click', () => {
    const model = target();
    const version = targetVersion, signature = targetSignature(model);
    work($('compat-structure-test'), async current => {
      if (!model) throw new Error('请先选择模型条目。');
      if (!confirm('工具结构测试会发送一次真实请求，只检查探针调用结构，不执行工具。继续？')) return;
      const result = await api(`/api/models/${model.id}/tool-structure-test`, { method: 'POST' });
      if (current()) text('compat-structure-result', `结构测试通过 · HTTP ${result.status} · ${result.durationMs} ms；未执行工具，也不等于真实工具往返已验证。`);
    }, () => version === targetVersion && signature === targetSignature(target()));
  });
  $('diagnosis-arm').addEventListener('click', () => work($('diagnosis-arm'), async current => {
    const model = target();
    if (!model) throw new Error('请先在上方选择模型条目。');
    const raw = $('diagnosis-raw').checked;
    if (raw && !$('diagnosis-consent').checked) throw new Error('保留事件内容需要确认敏感信息风险。');
    await api('/api/compatibility/diagnosis', { method: 'POST', body: { modelId: model.id, raw, consent: $('diagnosis-consent').checked } });
    if (current()) { invalidatePrompt(); preview = null; $('diagnosis-export-consent').checked = false; $('diagnosis-download').disabled = true; text('diagnosis-preview', ''); text('diagnosis-replay-result', ''); await load(); }
  }));
  $('diagnosis-stop').addEventListener('click', () => work($('diagnosis-stop'), async current => {
    await api('/api/compatibility/diagnosis', { method: 'DELETE' });
    if (current()) { invalidatePrompt(); preview = null; text('diagnosis-preview', ''); text('diagnosis-replay-result', ''); $('diagnosis-export-consent').checked = false; $('diagnosis-download').disabled = true; await load(); }
  }));
  $('diagnosis-view').addEventListener('click', () => work($('diagnosis-view'), async current => {
    const result = await api('/api/compatibility/diagnosis');
    if (current()) { invalidatePrompt(); preview = result; $('diagnosis-export-consent').checked = false; text('diagnosis-preview', JSON.stringify(result, null, 2)); if (data) { data.diagnosis = result; render(); } $('diagnosis-download').disabled = result.status !== 'ready' && result.status !== 'expired'; }
  }));
  $('diagnosis-download').addEventListener('click', () => {
    if (!preview || !ready()) return;
    if (!$('diagnosis-export-consent').checked) { message('请先审查预览内容，并确认可以导出。'); return; }
    const url = URL.createObjectURL(new Blob([JSON.stringify(preview, null, 2) + '\n'], { type: 'application/json' }));
    const link = document.createElement('a'); link.href = url; link.download = 'adroute-diagnosis.json'; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  });
  $('diagnosis-replay').addEventListener('click', () => work($('diagnosis-replay'), async current => {
    if (!preview?.frames?.length || preview.truncated) throw new Error('需要预览一份未截断、含事件内容的诊断。');
    const ref = $('compat-profile').value;
    if (ref === 'none') throw new Error('请先选择一个具体修复包，无需适配即可回放。');
    const result = await api('/api/compatibility/replay', { method: 'POST', body: { ref, events: preview.frames } });
    if (current()) text('diagnosis-replay-result', JSON.stringify(result, null, 2));
  }));
  return {
    load,
    update() {
      syncControls();
      if (data) render();
    },
    open(modelId) { requestedModel = modelId; showTab('compatibility'); if (data) renderBindings(); },
    reset() {
      revision++; targetVersion++; clearTimeout(timer); loading = false; data = preview = requestedModel = null; bindingSignature = ''; renderedTargetSignature = ''; pending.clear();
      if ($('compat-prompt-text')) $('compat-prompt-text').value = '';
      for (const id of ['diagnosis-raw', 'diagnosis-consent', 'diagnosis-export-consent']) $(id).checked = false;
      for (const id of ['compat-prompt-view', 'adapter-manager', 'diagnosis-details']) $(id).open = false;
      for (const id of ['adapter-directory', 'adapter-refresh-result', 'adapter-count', 'compat-prompt-target', 'compat-prompt-status', 'adapter-packages', 'compat-model', 'compat-profile', 'compat-binding-help', 'diagnosis-preview', 'diagnosis-replay-result', 'compat-save-result', 'compat-structure-result']) { const element = $(id); if (element) element.replaceChildren(); }
      text('diagnosis-status', '未开启'); $('diagnosis-download').disabled = true; message('');
    },
  };
};
