import test from 'node:test';
import assert from 'node:assert/strict';
import { expect } from '@playwright/test';
import { goBrowserFixture, goSavedGroup, goSavedModel } from './go-fixture.js';

const row = (page, id) => page.locator(`.model-row[data-model-id="${id}"]`);

test('model form text fields use the same appearance in both themes', { timeout: 90000 }, async t => {
  const f = await goBrowserFixture(t);
  await f.login();
  await f.page.locator('#add-model').click();
  for (const theme of ['dark', 'light']) {
    if (theme === 'light') await f.page.locator('#theme-toggle').evaluate(button => button.click());
    const styles = await f.page.evaluate(() => Object.fromEntries(
      ['model-base-url', 'model-new-group', 'model-real-id', 'model-name'].map(id => {
        const el = document.getElementById(id), s = getComputedStyle(el);
        return [id, { height: s.height, background: s.backgroundColor, border: s.border, radius: s.borderRadius, font: s.fontSize, padding: s.padding }];
      })
    ));
    for (const id of ['model-new-group', 'model-real-id', 'model-name']) {
      assert.deepEqual(styles[id], styles['model-base-url'], `${id} differs from other inputs in ${theme} mode`);
    }
    const nameWidth = await f.page.locator('#model-name').evaluate(el => el.getBoundingClientRect().width);
    const baseWidth = await f.page.locator('#model-base-url').evaluate(el => el.getBoundingClientRect().width);
    assert.equal(nameWidth, baseWidth, 'model name should span the form like the Base URL');
  }
});

test('model name appears before the website in the model form', { timeout: 90000 }, async t => {
  const f = await goBrowserFixture(t);
  await f.login();
  await f.page.locator('#add-model').click();
  const name = await f.page.locator('#model-name').boundingBox();
  const website = await f.page.locator('#model-website').boundingBox();
  assert.ok(name.y < website.y, 'website should appear below the model name');
});

test('theme toggle switches to a persisted light appearance and back', { timeout: 90000 }, async t => {
  const f = await goBrowserFixture(t);
  const { page } = f;
  await f.login();

  const toggle = page.locator('#theme-toggle');
  const root = page.locator('html');
  await expect(toggle).toHaveText('浅色模式');
  assert.equal(await root.getAttribute('data-theme'), null, 'dark is the default');
  const darkBackground = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);

  await toggle.click();
  await expect(toggle).toHaveText('深色模式');
  await expect(toggle).toHaveAttribute('aria-pressed', 'true');
  assert.equal(await root.getAttribute('data-theme'), 'light');
  const lightBackground = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  assert.notEqual(lightBackground, darkBackground, 'background must change with the theme');
  assert.equal(lightBackground, 'rgb(246, 246, 248)');

  // Text stays readable: foreground and background must not collapse together.
  const contrast = await page.evaluate(() => {
    const body = getComputedStyle(document.body);
    return { text: body.color, bg: body.backgroundColor, panel: getComputedStyle(document.querySelector('.panel')).backgroundColor };
  });
  assert.equal(contrast.text, 'rgb(23, 23, 28)');
  assert.equal(contrast.panel, 'rgb(255, 255, 255)');

  // The choice survives a reload and is applied before first paint.
  await page.reload();
  await expect(page.locator('#app-shell')).toBeVisible();
  assert.equal(await root.getAttribute('data-theme'), 'light');
  await expect(page.locator('#theme-toggle')).toHaveText('深色模式');

  await page.locator('#theme-toggle').click();
  await expect(page.locator('#theme-toggle')).toHaveText('浅色模式');
  assert.equal(await root.getAttribute('data-theme'), null);
  assert.equal(await page.evaluate(() => getComputedStyle(document.body).backgroundColor), darkBackground);

  // Light mode also applies to the login screen.
  await page.locator('#theme-toggle').click();
  await page.locator('#logout-button').click();
  await expect(page.locator('#auth-screen')).toBeVisible();
  assert.equal(await root.getAttribute('data-theme'), 'light');
});

test('website field saves, and the row opens it only when present', { timeout: 90000 }, async t => {
  const f = await goBrowserFixture(t);
  const { page } = f;
  const group = await goSavedGroup(f, 'web');
  const model = await goSavedModel(f, { group: group.name, name: 'site-model' });
  await f.login();

  // No website saved yet: the button exists but is disabled.
  const openSite = row(page, model.id).getByRole('button', { name: '打开网站' });
  await expect(openSite).toBeVisible();
  await expect(openSite).toBeDisabled();

  // Adding a website through the edit dialog enables it.
  await row(page, model.id).getByRole('button', { name: '编辑' }).click();
  await expect(page.locator('#model-dialog')).toBeVisible();
  await expect(page.locator('#model-website')).toHaveValue('');
  await page.locator('#model-website').fill('https://example.com/models');
  await page.locator('#model-save').click();
  await expect(page.locator('#model-dialog')).toBeHidden();

  await expect.poll(async () => (await f.state()).models.find(m => m.id === model.id)?.website).toBe('https://example.com/models');
  await expect(openSite).toBeEnabled();
  await expect(openSite).toHaveAttribute('title', 'https://example.com/models');

  // Clicking opens a new tab at that URL.
  const opened = await page.evaluate(() => {
    const calls = [];
    const original = window.open;
    window.open = (url, target) => { calls.push([url, target]); return null; };
    document.querySelector('.model-row [data-model-action="open-website"]').click();
    window.open = original;
    return calls;
  });
  assert.deepEqual(opened, [['https://example.com/models', '_blank']]);

  // An invalid value is rejected inline before any request.
  await row(page, model.id).getByRole('button', { name: '编辑' }).click();
  await page.locator('#model-website').fill('ftp://example.com/files');
  await page.locator('#model-save').click();
  await expect(page.locator('#model-error-website')).toBeVisible();
  await expect(page.locator('#model-dialog')).toBeVisible();
  await expect.poll(async () => (await f.state()).models.find(m => m.id === model.id)?.website).toBe('https://example.com/models');

  // A browser-native invalid URL (no scheme) is blocked before submit too.
  await page.locator('#model-website').fill('example.com/no-scheme');
  await page.locator('#model-save').click();
  await expect(page.locator('#model-dialog')).toBeVisible();
  await expect.poll(async () => (await f.state()).models.find(m => m.id === model.id)?.website).toBe('https://example.com/models');

  // Clearing it removes the website and disables the button again.
  await page.locator('#model-website').fill('');
  await page.locator('#model-save').click();
  await expect(page.locator('#model-dialog')).toBeHidden();
  await expect.poll(async () => (await f.state()).models.find(m => m.id === model.id)?.website ?? '').toBe('');
  await expect(openSite).toBeDisabled();
});
