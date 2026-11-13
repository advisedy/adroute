import test from 'node:test';
import { expect } from '@playwright/test';
import { goBrowserFixture, goSavedGroup, goSavedModel, openRowActions } from './go-fixture.js';

test('home shows each group with its own models and keeps row actions on mobile', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t);
  await goSavedModel(f, { group: 'work', name: 'alpha', model: 'vendor/alpha' });
  await goSavedModel(f, { group: 'work', name: 'beta', model: 'vendor/beta' });
  await goSavedModel(f, { group: 'personal', name: 'gamma', model: 'vendor/gamma', disabled: true });
  await goSavedGroup(f, 'empty');
  await f.login();

  const cards = f.page.locator('.model-group');
  await expect(cards).toHaveCount(3);
  const work = cards.filter({ has: f.page.getByRole('heading', { name: 'work', exact: true }) });
  const personal = cards.filter({ has: f.page.getByRole('heading', { name: 'personal', exact: true }) });
  const empty = cards.filter({ has: f.page.getByRole('heading', { name: 'empty', exact: true }) });
  await expect(work.locator('.model-row')).toHaveCount(2);
  await expect(work).toContainText('vendor/alpha');
  await expect(personal.locator('.model-row')).toHaveCount(1);
  await expect(personal).not.toContainText('alpha');
  await expect(empty).toContainText('暂无模型');

  await f.page.setViewportSize({ width: 390, height: 844 });
  const row = work.locator('.model-row').filter({ hasText: 'alpha' });
  for (const name of ['测速', '编辑']) await expect(row.getByRole('button', { name, exact: true })).toBeVisible();
  await openRowActions(row);
  await expect(row.getByRole('button', { name: '删除', exact: true })).toBeVisible();
  await expect(row.getByRole('checkbox')).toBeVisible();
  expect(await f.page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('group cards expand independently and stay collapsed after state refresh', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t);
  await goSavedModel(f, { group: 'work', name: 'alpha' });
  await goSavedModel(f, { group: 'personal', name: 'beta' });
  await goSavedGroup(f, 'empty');
  await f.login();

  const group = name => f.page.locator('.model-group').filter({ has: f.page.getByRole('heading', { name, exact: true }) });
  const work = group('work'), personal = group('personal'), empty = group('empty');
  const toggle = work.getByRole('button', { name: '收起 work' });
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await toggle.click();
  await expect(work.locator('.model-row')).toBeHidden();
  await expect(work).toContainText('1 个模型');
  await expect(personal.locator('.model-row')).toBeVisible();
  await expect(work.getByRole('button', { name: '展开 work' })).toHaveAttribute('aria-expanded', 'false');
  const previousCard = await work.elementHandle();
  await f.page.locator('#refresh-button').click();
  await f.page.waitForFunction(card => !card.isConnected, previousCard);
  await expect(work.locator('.model-row')).toBeHidden();
  await work.getByRole('button', { name: '展开 work' }).focus();
  await f.page.keyboard.press('Enter');
  await expect(work.locator('.model-row')).toBeVisible();
  await empty.getByRole('button', { name: '收起 empty' }).click();
  await expect(empty.getByText('暂无模型')).toBeHidden();
  await empty.getByRole('button', { name: '展开 empty' }).click();
  await expect(empty.getByText('暂无模型')).toBeVisible();
});
