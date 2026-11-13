import test from 'node:test';
import assert from 'node:assert/strict';
import { expect } from '@playwright/test';
import { goBrowserFixture, goSavedModel, seedState, openRowActions } from './go-fixture.js';

const row = (page,id) => page.locator(`.model-row[data-model-id="${id}"]`);
const view = (page,name) => page.locator('#models-panel').getByRole('button',{ name,exact:true });
const more = async entry => entry.locator('summary').filter({hasText:'更多'}).click();

async function addModel(page, groupId, name) {
 await page.locator('#add-model').click();await page.locator('#model-group').selectOption(groupId);
 await page.locator('#model-base-url').fill('https://new.invalid/v1');await page.locator('#model-api-key').fill('new-key');
 await page.locator('#model-real-id').fill('new-upstream');await page.locator('#model-name').fill(name);
 await page.locator('#model-save').click();await expect(page.locator('#model-dialog')).toBeHidden();
}

for (const archived of [false,true]) test(`new model is immediately discoverable despite favorites, search, filters and collapse (archived group: ${archived})`,{timeout:60000},async t=>{
 const initial=seedState();initial.models[0].favorite=true;initial.groups[0].archived=archived;
 const f=await goBrowserFixture(t,{initialState:initial});await f.login();
 if(archived) await view(f.page,'已归档').click();
 await f.page.getByRole('button',{name:'收起 seed',exact:true}).click();
 await f.page.locator('#model-search').fill('no-matches');await f.page.locator('#model-exposure-filter').selectOption('closed');
 await addModel(f.page,'seed-group','just-created');
 await expect(f.page.getByRole('heading',{name:'just-created',exact:true})).toBeVisible();
 await expect(view(f.page,archived?'已归档':'全部')).toHaveAttribute('aria-pressed','true');
 assert.equal((await f.state()).groups[0].archived,archived);
});

test('new aggregate is visible even when created from a filtered favorites view',{timeout:60000},async t=>{
 const f=await goBrowserFixture(t,{initialState:seedState()});await f.login();await f.page.locator('[data-tab="aggregates"]').click();
 await f.page.locator('#aggregates-panel').getByRole('button',{name:'常用',exact:true}).click();await f.page.locator('#aggregate-search').fill('not-new');
 await f.page.locator('#aggregate-exposure-filter').selectOption('closed');await f.page.locator('#add-aggregate').click();
 await f.page.locator('#aggregate-name').fill('new-chain');await f.page.locator('.aggregate-member-row select').last().selectOption('seed-model');
 await f.page.locator('#aggregate-save').click();await expect(f.page.locator('#aggregate-dialog')).toBeHidden();
 await expect(f.page.getByRole('heading',{name:'new-chain',exact:true})).toBeVisible();
});

test('favorites span groups, survive reload and archive without changing exposure or default', {timeout:90000}, async t => {
 const f=await goBrowserFixture(t,{initialState:seedState({isDefault:true})});const other=await goSavedModel(f,{group:'backup',name:'spare'});await f.login();
 await row(f.page,'seed-model').getByRole('button',{name:'收藏',exact:true}).click();
 await row(f.page,other.id).getByRole('button',{name:'收藏',exact:true}).click();
 await view(f.page,'常用').click();await expect(f.page.locator('.model-row[data-model-id]')).toHaveCount(2);
 await more(row(f.page,other.id));await row(f.page,other.id).getByRole('button',{name:'归档',exact:true}).click();
 await expect(row(f.page,other.id)).toHaveCount(0);await view(f.page,'已归档').click();await expect(row(f.page,other.id)).toBeVisible();
 await row(f.page,other.id).getByRole('button',{name:'恢复',exact:true}).click();await expect(row(f.page,other.id)).toHaveCount(0);
 await f.page.reload();await expect(view(f.page,'常用')).toHaveAttribute('aria-pressed','true');await expect(f.page.locator('.model-row[data-model-id]')).toHaveCount(2);
 const state=await f.state();assert.equal(state.defaultModelId,'seed-model');assert.equal(state.models[0].exposureDisabled,false);assert.equal(state.models.find(m=>m.id===other.id).favorite,true);
});

test('group archives inherit without overwriting individual archives, search includes archives explicitly', {timeout:90000},async t=>{
 const f=await goBrowserFixture(t,{initialState:seedState()});const other=await goSavedModel(f,{group:'seed',name:'spare',model:'vendor/special'});await f.login();
 await more(row(f.page,other.id));await row(f.page,other.id).getByRole('button',{name:'归档',exact:true}).click();
 const group=f.page.locator('.model-group');await group.getByRole('button',{name:'归档分组',exact:true}).click();await expect(f.page.locator('.model-row[data-model-id]')).toHaveCount(0);
 await f.page.locator('#model-search').fill('vendor/special');await expect(row(f.page,other.id)).toHaveCount(0);
 await f.page.locator('#model-include-archived').check();await expect(row(f.page,other.id)).toBeVisible();
 await f.page.locator('#model-search').fill('seed');await expect(f.page.locator('.model-row[data-model-id]')).toHaveCount(2);
 await f.page.locator('#model-include-archived').uncheck();await f.page.locator('#model-search').fill('');await view(f.page,'已归档').click();
 await f.page.locator('.model-group').getByRole('button',{name:'恢复分组',exact:true}).click();
 await expect(row(f.page,other.id)).toBeVisible();await expect(row(f.page,'seed-model')).toHaveCount(0);
 await view(f.page,'全部').click();await expect(row(f.page,'seed-model')).toBeVisible();await expect(row(f.page,other.id)).toHaveCount(0);
 assert.equal((await f.state()).models.find(m=>m.id===other.id).archived,true);
});

test('collapsed groups persist across reload and searches temporarily reveal matches', {timeout:90000},async t=>{
 const f=await goBrowserFixture(t,{initialState:seedState()});await f.login();
 await f.page.getByRole('button',{name:'收起 seed',exact:true}).click();await f.page.reload();await expect(row(f.page,'seed-model')).toBeHidden();
 await f.page.locator('#model-search').fill('seeded');await expect(row(f.page,'seed-model')).toBeVisible();await f.page.locator('#model-search').fill('');await expect(row(f.page,'seed-model')).toBeHidden();
});

test('workbench exposes route IDs, hides connection details and secondary actions, moves credentials to setup', {timeout:90000},async t=>{
 const f=await goBrowserFixture(t,{initialState:seedState()});await f.login();const entry=row(f.page,'seed-model');
 await expect(entry.getByText('adroute/seed/seeded',{exact:true})).toBeVisible();await expect(entry.getByText('https://seed.invalid/v1',{exact:true})).toBeHidden();
 await expect(entry.getByRole('button',{name:'删除',exact:true})).toBeHidden();await entry.getByRole('button',{name:'详情',exact:true}).click();await expect(entry.getByText('https://seed.invalid/v1',{exact:true})).toBeVisible();
 await more(entry);await expect(entry.getByRole('button',{name:'删除',exact:true})).toBeVisible();await f.page.keyboard.press('Escape');await expect(entry.getByRole('button',{name:'删除',exact:true})).toBeHidden();
 const menu=f.page.locator('.workbench-heading .action-menu');await menu.locator('summary').click();await f.page.locator('#manage-groups').click();await f.page.locator('#groups-dialog [data-close-dialog]').first().click();await expect(menu).not.toHaveAttribute('open');
 await expect(f.page.locator('#client-api-key')).toBeHidden();await f.page.locator('nav [data-tab="setup"]').click();await expect(f.page.locator('#client-api-key')).toBeVisible();
});

test('copying a search match reveals the saved copy even when its new name no longer matches',{timeout:60000},async t=>{
 const f=await goBrowserFixture(t,{initialState:seedState({name:'seeded-copy'})});await f.login();
 await f.page.locator('#model-search').fill('seeded-copy seeded-model');await more(row(f.page,'seed-model'));
 await row(f.page,'seed-model').getByRole('button',{name:'复制',exact:true}).click();
 await expect(f.page.getByRole('heading',{name:'seeded-copy-copy',exact:true})).toBeVisible();
 assert.equal((await f.state()).models.length,2);
});

test('import clears obsolete filters and reveals archived content without changing metadata',{timeout:60000},async t=>{
 const initial=seedState();initial.models[0].favorite=true;const f=await goBrowserFixture(t,{initialState:initial});await f.login();
 await f.page.getByRole('button',{name:'收起 seed',exact:true}).click();await f.page.locator('#model-search').fill('not-imported');
 await f.page.locator('#model-exposure-filter').selectOption('closed');
 const imported=seedState();imported.models[0].name='imported';imported.groups[0].archived=true;
 imported.aggregates=[{id:'imported-chain',name:'imported-chain',members:['seed-model'],maxRounds:5,archived:true}];
 const backup={format:'adroute-config',version:1,config:imported};
 await f.page.locator('#import-config-file').setInputFiles({name:'backup.json',mimeType:'application/json',buffer:Buffer.from(JSON.stringify(backup))});
 await f.page.locator('#import-confirm').click();await expect(f.page.locator('#import-dialog')).toBeHidden();
 await expect(f.page.getByRole('heading',{name:'imported',exact:true})).toBeVisible();
 await f.page.locator('[data-tab="aggregates"]').click();await expect(f.page.getByRole('heading',{name:'imported-chain',exact:true})).toBeVisible();
 const state=await f.state();assert.equal(state.groups[0].archived,true);assert.equal(state.models[0].archived,false);assert.equal(state.aggregates[0].archived,true);
});

test('aggregate default action shows failures on the active page and allows retry',{timeout:60000},async t=>{
 const f=await goBrowserFixture(t,{initialState:seedState()});await f.api('/api/aggregates','POST',{name:'chain',members:['seed-model']});await f.login();await f.page.locator('[data-tab="aggregates"]').click();
 await f.page.route('**/api/default',route=>route.fulfill({status:500,json:{error:{message:'synthetic-default-failure'}}}));
 const card=f.page.locator('.aggregate-card');await more(card);await card.getByRole('button',{name:'设为默认',exact:true}).click();
 await expect(f.page.locator('#global-error')).toBeVisible();await expect(f.page.locator('#global-error')).toContainText('synthetic-default-failure');
 assert.equal((await f.state()).defaultModelId,'');await f.page.unroute('**/api/default');await openRowActions(card);await card.getByRole('button',{name:'设为默认',exact:true}).click();
 await expect(card).toContainText('网页默认');await expect(f.page.locator('#toast')).toBeVisible();await expect(f.page.locator('#global-error')).toBeHidden();
});

test('model count reflects the selected group but global search spans all groups',{timeout:60000},async t=>{
 const f=await goBrowserFixture(t,{initialState:seedState()});await goSavedModel(f,{group:'other',name:'second'});await f.login();
 await f.page.getByRole('combobox',{name:'按模型组筛选'}).selectOption('seed-group');await expect(f.page.locator('.model-row[data-model-id]')).toHaveCount(1);await expect(f.page.locator('#model-count')).toHaveText('1 / 2');
 await f.page.locator('#model-search').fill('adroute/');await expect(f.page.locator('.model-row[data-model-id]')).toHaveCount(2);await expect(f.page.locator('#model-count')).toHaveText('2 / 2');
});

test('organization errors reread committed metadata and late responses stay private after logout',{timeout:60000},async t=>{
 const f=await goBrowserFixture(t,{initialState:seedState()});await f.login();
 await f.page.route('**/organization',async route=>{await route.fetch();await route.abort('failed');});
 await row(f.page,'seed-model').getByRole('button',{name:'收藏',exact:true}).click();
 await expect(row(f.page,'seed-model').getByRole('button',{name:'取消收藏',exact:true})).toBeEnabled();
 await expect(f.page.locator('#global-error')).toBeVisible();assert.equal((await f.state()).models[0].favorite,true);
 await f.page.unroute('**/organization');
 let release,started;const gate=new Promise(r=>release=r),seen=new Promise(r=>started=r);t.after(()=>release());
 await f.page.route('**/organization',async route=>{const response=await route.fetch();started();await gate;await route.fulfill({response}).catch(()=>{});});
 await row(f.page,'seed-model').getByRole('button',{name:'取消收藏',exact:true}).click();await seen;
 await f.page.locator('#logout-button').click();release();await expect(f.page.locator('#auth-screen')).toBeVisible();
 await expect(f.page.locator('.model-row')).toHaveCount(0);await expect(f.page.locator('#toast')).toBeHidden();
 await f.login();await expect(row(f.page,'seed-model').getByRole('button',{name:'收藏',exact:true})).toBeEnabled();
});

test('missing organization capability disables metadata writes without blocking existing model editing',{timeout:60000},async t=>{
 const f=await goBrowserFixture(t,{initialState:seedState()});let writes=0;
 await f.page.route('**/api/state',async route=>{const response=await route.fetch(),body=await response.json();delete body.capabilities.organization;await route.fulfill({json:body});});
 await f.page.route('**/organization',async route=>{writes++;await route.continue();});await f.login();
 const favorite=row(f.page,'seed-model').getByRole('button',{name:'收藏',exact:true});await expect(favorite).toBeDisabled();
 await favorite.evaluate(el=>el.click());assert.equal(writes,0);await expect(row(f.page,'seed-model').getByRole('button',{name:'编辑',exact:true})).toBeEnabled();
 await expect(f.page.getByRole('button',{name:'归档分组',exact:true})).toBeDisabled();
});

test('search crosses favorites and group selection then restores both when cleared',{timeout:60000},async t=>{
 const initial=seedState();initial.models[0].favorite=true;const f=await goBrowserFixture(t,{initialState:initial});
 const other=await goSavedModel(f,{group:'other',name:'target'});await f.login();
 await f.page.getByRole('combobox',{name:'按模型组筛选'}).selectOption('seed-group');await expect(row(f.page,other.id)).toHaveCount(0);
 await f.page.locator('#model-search').fill('target');await expect(row(f.page,other.id)).toBeVisible();
 await f.page.locator('#model-search').fill('');await expect(row(f.page,other.id)).toHaveCount(0);await expect(row(f.page,'seed-model')).toBeVisible();
 await expect(view(f.page,'常用')).toHaveAttribute('aria-pressed','true');await expect(f.page.getByRole('combobox',{name:'按模型组筛选'})).toHaveValue('seed-group');
});

test('model rows stay compact, the details toggle sits in the action row and its panel spans the row',{timeout:60000},async t=>{
 const f=await goBrowserFixture(t,{initialState:seedState()});await goSavedModel(f,{group:'seed',name:'second'});await f.login();
 const entry=row(f.page,'seed-model');
 const geo=await entry.evaluate(el=>{
  const r=el.getBoundingClientRect();
  const box=s=>{const n=el.querySelector(s),b=n.getBoundingClientRect();return {t:b.top-r.top,b:b.bottom-r.top,l:b.left-r.left,r:b.right-r.left};};
  const toggle=el.querySelector('.details-toggle'),tb=toggle.getBoundingClientRect();
  return {h:r.height,main:box('.model-main'),controls:box('.model-controls'),toggle:{inActions:toggle.closest('.model-actions')!==null,t:tb.top-r.top,b:tb.bottom-r.top,l:tb.left-r.left},hidden:el.querySelector('.entry-details').hidden};
 });
 assert.ok(geo.h<80,`row height ${geo.h} must stay compact`);
 assert.ok(geo.main.r<=geo.controls.l,'model text and actions must not overlap');
 assert.ok(geo.toggle.inActions,'details toggle belongs to the action row');
 assert.ok(geo.toggle.t>=0&&geo.toggle.b<=geo.h,'details toggle inside the row');
 assert.equal(geo.hidden,true);
 const toggle=entry.locator('.details-toggle'),target=await toggle.getAttribute('aria-controls');
 await toggle.click();await expect(f.page.locator(`#${target}`)).toBeVisible();
 assert.ok((await f.page.locator(`#${target}`).boundingBox()).width>(await entry.boundingBox()).width*0.95,'panel spans the row');
 await toggle.click();await expect(f.page.locator(`#${target}`)).toBeHidden();
 const rows=f.page.locator('.model-row'),a=await rows.nth(0).boundingBox(),b=await rows.nth(1).boundingBox();
 assert.ok(b.y-a.y<80,`row pitch ${b.y-a.y} must stay compact`);
 assert.equal(await f.page.locator('.model-group-header h2').first().evaluate(el=>getComputedStyle(el).fontSize),'14px');
});

test('phone workbench and menus fit both themes without exposing credentials',{timeout:60000},async t=>{
 const f=await goBrowserFixture(t,{viewport:{width:360,height:800}});const model=await goSavedModel(f,{group:'a-very-long-group-name',name:'a-long-model-name-with-a-long-route-identifier'});await f.login();
 for(const theme of ['dark','light']){
  if(theme==='light') await f.page.locator('#theme-toggle').click();
  const entry=row(f.page,model.id);await expect(entry.getByRole('button',{name:'测速',exact:true})).toBeVisible();
  await more(entry);const remove=entry.getByRole('button',{name:'删除',exact:true});await expect(remove).toBeVisible();
  const bounds=await remove.boundingBox();assert.ok(bounds.x>=0&&bounds.x+bounds.width<=360,`${theme} menu fits`);
  assert.ok(await f.page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`${theme} page must not overflow`);
  await expect(f.page.locator('#client-api-key')).toBeHidden();await f.page.keyboard.press('Escape');
 }
});

test('aggregate organization is persistent and mutation controls stay single-flight after refresh', {timeout:90000},async t=>{
 const f=await goBrowserFixture(t,{initialState:seedState()});await f.api('/api/aggregates','POST',{name:'chain',members:['seed-model']});await f.login();await f.page.locator('[data-tab="aggregates"]').click();const card=f.page.locator('.aggregate-card');
 let release,started;const gate=new Promise(r=>release=r),seen=new Promise(r=>started=r);let calls=0;
 await f.page.route('**/organization',async route=>{calls++;started();await gate;await route.continue();});
 await card.getByRole('button',{name:'收藏',exact:true}).click();await seen;await f.page.locator('#refresh-button').click();await expect(card.getByRole('button',{name:'收藏',exact:true})).toBeDisabled();await card.getByRole('button',{name:'收藏',exact:true}).evaluate(el=>el.click());assert.equal(calls,1);release();
 await expect(card.getByRole('button',{name:'取消收藏',exact:true})).toBeEnabled();await more(card);await card.getByRole('button',{name:'归档',exact:true}).click();await expect(card).toHaveCount(0);
 await f.page.locator('#aggregates-panel').getByRole('button',{name:'已归档',exact:true}).click();await expect(card).toBeVisible();await card.getByRole('button',{name:'恢复',exact:true}).click();await expect(card).toHaveCount(0);
});

test('the parameters dialog publishes a context window and leaves the credential alone',{timeout:90000},async t=>{
 const f=await goBrowserFixture(t,{initialState:seedState()});const {page}=f;await f.login();
 await more(row(page,'seed-model'));await row(page,'seed-model').getByRole('button',{name:'修改详细参数',exact:true}).click();
 await expect(page.locator('#params-dialog')).toBeVisible();
 await expect(page.locator('#params-model-name')).toHaveText('seed / seeded');
 await expect(page.locator('#params-context-window')).toHaveValue('');
 await page.locator('#params-context-window').fill('200000');
 await page.locator('#params-save').click();
 await expect(page.locator('#params-dialog')).toBeHidden();
 let state=await f.state();
 assert.equal(state.models[0].contextWindow,200000);
 assert.equal(state.models[0].apiKey,'seed-key','the narrow write must not touch the credential');
 assert.equal(state.models[0].baseUrl,'https://seed.invalid/v1');
 await row(page,'seed-model').getByRole('button',{name:'详情',exact:true}).click();
 await expect(page.locator('#entry-details-seed-model')).toContainText('200,000');

 // Clearing publishes nothing, so the client keeps its own fallback.
 await more(row(page,'seed-model'));await row(page,'seed-model').getByRole('button',{name:'修改详细参数',exact:true}).click();
 await expect(page.locator('#params-context-window')).toHaveValue('200000');
 await page.locator('#params-context-window').fill('');
 await page.locator('#params-save').click();
 await expect(page.locator('#params-dialog')).toBeHidden();
 assert.equal((await f.state()).models[0].contextWindow,0);

 // An out-of-range value is refused in the form, before any request.
 await more(row(page,'seed-model'));await row(page,'seed-model').getByRole('button',{name:'修改详细参数',exact:true}).click();
 await page.locator('#params-context-window').fill('10');
 await page.locator('#params-save').click();
 await expect(page.locator('#params-error-contextwindow')).toBeVisible();
 await expect(page.locator('#params-dialog')).toBeVisible();
 assert.equal((await f.state()).models[0].contextWindow,0);
});

test('editing a model shows its saved credential and submits it only when changed',{timeout:90000},async t=>{
 const f=await goBrowserFixture(t,{initialState:seedState()});const {page}=f;await f.login();
 const bodies=[];page.on('request',r=>{if(r.method()==='PUT'&&/\/api\/models\/seed-model$/.test(r.url()))bodies.push(JSON.parse(r.postData()));});
 await row(page,'seed-model').getByRole('button',{name:'编辑',exact:true}).click();
 await expect(page.locator('#model-api-key')).toHaveAttribute('type','text');
 await expect(page.locator('#model-api-key')).toHaveValue('seed-key');
 await page.locator('#model-real-id').fill('seeded-model-2');
 await page.locator('#model-save').click();
 await expect(page.locator('#model-dialog')).toBeHidden();
 assert.equal(bodies.length,1);
 assert.ok(!('apiKey' in bodies[0]),'an untouched credential must not be submitted');
 assert.equal((await f.state()).models[0].apiKey,'seed-key');

 await row(page,'seed-model').getByRole('button',{name:'编辑',exact:true}).click();
 await page.locator('#model-api-key').fill('rotated-key');
 await page.locator('#model-save').click();
 await expect(page.locator('#model-dialog')).toBeHidden();
 assert.equal(bodies[1].apiKey,'rotated-key');
 assert.equal((await f.state()).models[0].apiKey,'rotated-key');
 await expect(page.locator('#model-list')).not.toContainText('rotated-key');
});
