import test from 'node:test';
import assert from 'node:assert/strict';
import { expect } from '@playwright/test';
import { goBrowserFixture, goSavedModel } from './go-fixture.js';

test('quick-test shows only speed; hover, keyboard and touch reveal measured details and actual output safely', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  const model = await goSavedModel(f);
  const output = Array.from({ length: 120 }, (_, i) => i + 1).join(' ');
  await page.route('**/quick-test', route => route.fulfill({ json: {
    outcome: 'ok', status: 200, durationMs: 3435, textReturned: true, usageReported: true,
    outputTokens: 557, firstTextMs: 1675, reasoningTokens: 0, textTokens: 557,
    textDurationMs: 1600, textTokensPerSecond: 348.1, tokensPerSecond: 162.1,
    text: `${output} <img src=x onerror="window.previewInjected=true">`, truncated: false, message: '可用。',
  } }));
  await f.login();
  const row = page.locator(`.model-row[data-model-id="${model.id}"]`);
  await row.getByRole('button', { name: '测速', exact: true }).click();
  const region = row.locator('.model-test');
  await expect(region.locator('.test-status')).toHaveText('348.1 tokens/s');
  await expect(region).not.toContainText('162.1 tokens/s');
  const info = region.getByRole('button', { name: '查看测速详情' });
  const tooltip = region.getByRole('tooltip', { includeHidden: true });
  await expect(tooltip).toBeHidden();
  await info.hover();
  await expect(tooltip).toBeVisible();
  for (const value of ['速度 348.1 tokens/s', '首字 1675 ms', '总耗时 3435 ms', '输出 557 tokens', `返回：${output}`, '不含首字等待']) await expect(tooltip).toContainText(value);
  await expect(tooltip.locator('img')).toHaveCount(0);
  assert.equal(await page.evaluate(() => window.previewInjected), undefined);
  await page.locator('#model-count').hover();
  await expect(tooltip).toBeHidden();

  await info.focus();
  await expect(tooltip).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(tooltip).toBeHidden();

  await page.setViewportSize({ width: 390, height: 844 });
  await info.click();
  await expect(tooltip).toBeVisible();
  const bounds = await tooltip.boundingBox();
  assert.ok(bounds.x >= 0 && bounds.x + bounds.width <= 390, 'details must stay inside the phone viewport');
  await page.locator('#model-count').click();
  await expect(tooltip).toBeHidden();
  await info.click();
  await expect(tooltip).toBeVisible();
  await page.locator('#logout-button').click();
  await expect(page.locator('[role="tooltip"]')).toHaveCount(0);
  await expect(page.locator('body')).not.toContainText(output);
});

test('compact quick-test states retain the reason and usage in details instead of inventing a speed', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  const model = await goSavedModel(f);
  let result = { outcome: 'ok', durationMs: 2000, firstTextMs: 800, textReturned: true, usageReported: true, outputTokens: 100, tokensPerSecond: 50, speedUnavailableReason: 'reasoning-usage-unavailable', text: '1 2 3', message: '可用。' };
  await page.route('**/quick-test', route => route.fulfill({ json: result }));
  await f.login();
  const row = page.locator(`.model-row[data-model-id="${model.id}"]`);
  const run = async () => {
    await row.getByRole('button', { name: '测速', exact: true }).click();
    await expect(row.getByRole('button', { name: '测速', exact: true })).toBeEnabled();
    await row.getByRole('button', { name: '查看测速详情' }).hover();
  };
  await run();
  await expect(row.locator('.test-status')).toHaveText('可用 · 速度不可统计');
  await expect(row.getByRole('tooltip')).toContainText('上游未区分正文与推理用量');
  await expect(row.getByRole('tooltip')).toContainText('返回：1 2 3');
  await expect(row).not.toContainText('tokens/s');

  result = { ...result, outcome: 'truncated', truncated: true, reasoningTokens: 0, textTokens: 100, textTokensPerSecond: 500, speedUnavailableReason: undefined };
  await run();
  await expect(row.locator('.test-status')).toHaveText('500.0 tokens/s');
  await expect(row.getByRole('tooltip')).toContainText('达到本次测试输出上限');

  result = { ...result, outcome: 'no-usage', usageReported: false, outputTokens: undefined, reasoningTokens: undefined, textTokensPerSecond: undefined, textTokens: undefined, speedUnavailableReason: 'usage-unavailable', firstTextMs: 0 };
  await run();
  await expect(row.getByRole('tooltip')).toContainText('首字 0 ms');
  await expect(row.getByRole('tooltip')).toContainText('上游未返回可信用量');

  result = { outcome: 'no-text', durationMs: 2163, textReturned: false, usageReported: true, outputTokens: 192, reasoningTokens: 192, truncated: true, incompleteReason: 'max_output_tokens', message: '本次输出额度全部用于推理，尚未返回正文。' };
  await run();
  await expect(row.locator('.test-status')).toHaveText('未返回正文');
  await expect(row.getByRole('tooltip')).toContainText('输出额度全部用于推理');
  await expect(row.getByRole('tooltip')).toContainText('输出 192 tokens');
  await expect(row.getByRole('tooltip')).toContainText('推理 192 tokens');
  await expect(row.getByRole('tooltip')).not.toContainText('tokens/s');
});

test('long speed details stay open across the hover gap, text selection and keyboard scrolling', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  const model = await goSavedModel(f);
  await page.route('**/quick-test', route => route.fulfill({ json: {
    outcome: 'ok', durationMs: 2000, firstTextMs: 800, textReturned: true, usageReported: true,
    outputTokens: 600, reasoningTokens: 0, textTokens: 600, textDurationMs: 1200, textTokensPerSecond: 500,
    text: Array.from({ length: 120 }, (_, i) => i + 1).join(' ').repeat(10), message: '可用。',
  } }));
  await f.login();
  const row = page.locator(`.model-row[data-model-id="${model.id}"]`);
  await row.getByRole('button', { name: '测速', exact: true }).click();
  const info = row.getByRole('button', { name: '查看测速详情' });
  await info.hover();
  const tooltip = row.getByRole('tooltip');
  const anchor = await info.boundingBox(), box = await tooltip.boundingBox();
  const below = box.y > anchor.y;
  await page.mouse.move(anchor.x + anchor.width / 2, below ? anchor.y + anchor.height + 4 : anchor.y - 4);
  await page.mouse.move(anchor.x + anchor.width / 2, below ? box.y + 15 : box.y + box.height - 15);
  // Crossing the gap must cancel deferred hover dismissal, not just delay it.
  await page.waitForTimeout(250);
  await expect(tooltip).toBeVisible();
  await tooltip.dblclick({ position: { x: 35, y: 30 } });
  await expect(tooltip).toBeVisible();
  assert.ok(await page.evaluate(() => window.getSelection().toString().length > 0), 'returned text must be selectable');

  await info.focus();
  await page.keyboard.press('Tab');
  await expect(tooltip).toBeFocused();
  await page.keyboard.press('PageDown');
  await expect.poll(() => tooltip.evaluate(element => element.scrollTop)).toBeGreaterThan(0);
  await expect(tooltip).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(row.getByRole('tooltip', { includeHidden: true })).toBeHidden();
});

test('missing upstream reasoning details can show an explicitly estimated visible-text speed', { timeout: 60000 }, async t => {
  const f = await goBrowserFixture(t), { page } = f;
  const model = await goSavedModel(f);
  await page.route('**/quick-test', route => route.fulfill({ json: {
    outcome: 'ok', durationMs: 11477, firstTextMs: 10698, textReturned: true, usageReported: true,
    outputTokens: 285, textTokens: 239, textTokensSource: 'cl100k_base-estimate', textDurationMs: 752,
    textTokensPerSecond: 317.8, text: '1 2 3', message: '可用。',
  } }));
  await f.login();
  const row = page.locator(`.model-row[data-model-id="${model.id}"]`);
  await row.getByRole('button', { name: '测速', exact: true }).click();
  await expect(row.locator('.test-status')).toHaveText('约 317.8 tokens/s');
  await row.getByRole('button', { name: '查看测速详情' }).hover();
  await expect(row.getByRole('tooltip')).toContainText('正文约 239 tokens');
  await expect(row.getByRole('tooltip')).toContainText('cl100k_base 本地分词估算');
  await expect(row.getByRole('tooltip')).toContainText('不是上游计费 token 数');
  await expect(row.getByRole('tooltip')).not.toContainText('推理 0 tokens');
});
