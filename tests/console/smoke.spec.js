// Browser smoke test of the OpenS3 web console (Playwright Test).
//
// Every test signs in through the real login form and drives the UI with
// role/label/text selectors. Any uncaught page error or console.error
// fails the test: those are the bugs no Go test can see (an undefined
// helper in a view, a missing import, a form posting stale field names).
'use strict';

const fs = require('fs');
const { test, expect } = require('@playwright/test');

const ROOT_USER = process.env.OPENS3_ROOT_USER || 'opens3admin';
const ROOT_PASSWORD = process.env.OPENS3_ROOT_PASSWORD || 'opens3admin';
const uniq = () => Date.now().toString(36) + Math.random().toString(36).slice(2, 6);

// watch(page, allow) collects page errors and console errors from now on.
// Browsers report every failed HTTP response as a console error ("Failed
// to load resource: ... 401"). The app's probe of /console/api/me on a
// fresh load (before login) is the one expected failure; tests that
// deliberately fail a request add their own {url, text} allowance.
function watch(page, allow = []) {
  const errors = [];
  const allowed = [{ url: /\/console\/api\/me$/, text: /401/ }, ...allow];
  page.on('pageerror', (e) => errors.push('pageerror: ' + (e.stack || e.message)));
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return;
    const url = (msg.location() && msg.location().url) || '';
    const text = msg.text();
    if (allowed.some((a) => a.url.test(url) && a.text.test(text))) return;
    errors.push('console.error: ' + text + (url ? ' (' + url + ')' : ''));
  });
  return errors;
}

// login signs in through the form. The header shows the session's name:
// the root account is always "root (root)", whatever OPENS3_ROOT_USER is.
async function login(page, user, password, shownAs = user) {
  await page.goto('/console/');
  await expect(page.getByRole('button', { name: 'Log in' })).toBeVisible();
  await page.getByLabel('User name').fill(user);
  await page.getByLabel('Password', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Log in' }).click();
  await expect(page.locator('#app')).toBeVisible();
  await expect(page.locator('#topbar-user')).toHaveText(shownAs);
}
const loginRoot = (page) => login(page, ROOT_USER, ROOT_PASSWORD, 'root (root)');

async function logout(page) {
  await page.getByRole('button', { name: 'Log out' }).click();
  await expect(page.getByRole('button', { name: 'Log in' })).toBeVisible();
  await expect(page.locator('#app')).toBeHidden();
}

const toast = (page, text) => expect(page.locator('#toasts .toast', { hasText: text })).toBeVisible();
const dialog = (page, name, exact = true) => page.getByRole('dialog', { name, exact });
const card = (page, name) => page.locator('#main .card', { has: page.getByRole('heading', { name }) });

// createBucket uses the Create bucket dialog and ends in the object browser.
async function createBucket(page, name) {
  await page.goto('/console/#/buckets');
  await page.getByRole('button', { name: 'Create bucket' }).click();
  const d = dialog(page, 'Create bucket');
  await d.getByLabel('Name').fill(name);
  await d.getByRole('button', { name: 'Create' }).click();
  await toast(page, 'Bucket ' + name + ' created');
  await expect(page).toHaveURL(new RegExp('#/b/' + name + '/$'));
  await expect(page.locator('.breadcrumb .cur')).toHaveText(name);
}

async function deleteBucketFromList(page, name) {
  await page.goto('/console/#/buckets');
  const row = page.getByRole('row', { name });
  await row.getByRole('button', { name: 'Delete' }).click();
  await dialog(page, 'Delete bucket').getByRole('button', { name: 'Delete' }).click();
  await toast(page, 'Bucket deleted');
  await expect(row).toHaveCount(0);
}

test('every route renders, settings and density/theme apply', async ({ page }) => {
  const errors = watch(page);
  await loginRoot(page);
  // Plain http from localhost is fine; the banner is for remote addresses.
  await expect(page.locator('#http-banner')).toBeHidden();
  await expect(page.getByRole('heading', { level: 1, name: 'Buckets' })).toBeVisible();

  const bucket = 'smoke-routes-' + uniq();
  await createBucket(page, bucket);
  await expect(page.locator('#main .empty')).toContainText('This folder is empty');
  await page.getByRole('link', { name: 'Bucket settings' }).click();
  await expect(page).toHaveURL(new RegExp('#/buckets/' + bucket + '/settings$'));
  await expect(page.getByRole('heading', { level: 1, name: bucket })).toBeVisible();
  for (const name of ['Overview', 'Versioning', 'Default encryption', 'Tags', 'Delete bucket']) {
    await expect(card(page, name)).toBeVisible();
  }
  await expect(card(page, 'Overview')).toContainText('Default encryption');

  for (const tab of ['users', 'keys', 'groups', 'policies']) {
    await page.goto('/console/#/identity/' + tab);
    await expect(page.getByRole('heading', { level: 1, name: 'Identity' })).toBeVisible();
    await expect(page.locator('#main .tabs a.active')).toHaveAttribute('href', '#/identity/' + tab);
    await expect(page.locator('#main .table-wrap')).toBeVisible();
    await expect(page.locator('#main .toolbar .btn-primary')).toBeVisible();
    await expect(page.getByRole('link', { name: 'Identity' })).toHaveClass(/active/);
    if (tab === 'users') await expect(page.getByRole('row', { name: 'root' })).toBeVisible();
  }

  await page.goto('/console/#/kms');
  await expect(page.getByRole('heading', { level: 1, name: 'Encryption keys' })).toBeVisible();
  await expect(page.locator('#main .table-wrap')).toBeVisible();

  await page.goto('/console/#/status');
  await expect(page.getByRole('heading', { level: 1, name: 'Status' })).toBeVisible();
  await expect(page.locator('#main .stat')).toHaveCount(6);
  await expect(card(page, 'Disk')).toBeVisible();
  await expect(card(page, 'Endpoints')).toContainText('/console/');

  // Settings dialog through the gear: compact + dark, applied to <html>.
  const html = page.locator('html');
  await expect(html).toHaveAttribute('data-density', 'comfortable');
  await expect(html).not.toHaveAttribute('data-theme', /.+/);
  await page.getByRole('button', { name: 'Settings' }).click();
  let d = dialog(page, 'Settings');
  await d.locator('#setting-density').selectOption('compact');
  await d.locator('#setting-theme').selectOption('dark');
  await d.getByRole('button', { name: 'Apply' }).click();
  await expect(d).toHaveCount(0);
  await expect(html).toHaveAttribute('data-density', 'compact');
  await expect(html).toHaveAttribute('data-theme', 'dark');
  // And back through the "," shortcut and Reset to defaults.
  await page.keyboard.press(',');
  d = dialog(page, 'Settings');
  await expect(d).toBeVisible();
  await expect(d.locator('#setting-theme')).toHaveValue('dark');
  await d.getByRole('button', { name: 'Reset to defaults' }).click();
  await d.getByRole('button', { name: 'Apply' }).click();
  await expect(html).toHaveAttribute('data-density', 'comfortable');
  await expect(html).not.toHaveAttribute('data-theme', /.+/);

  await deleteBucketFromList(page, bucket);
  expect(errors).toEqual([]);
});

test('bucket and object lifecycle in the browser', async ({ page }) => {
  const errors = watch(page);
  await loginRoot(page);
  const bucket = 'smoke-objects-' + uniq();
  await createBucket(page, bucket);

  // Upload through the dialog (page.setInputFiles on its file input).
  const content = 'hello from the console smoke test ' + uniq() + '\n';
  await page.getByRole('button', { name: 'Upload' }).click();
  const up = dialog(page, 'Upload to ' + bucket + '/');
  await up.locator('input[type=file]').setInputFiles({ name: 'hello.txt', mimeType: 'text/plain', buffer: Buffer.from(content) });
  await up.getByRole('button', { name: 'Upload' }).click();
  await toast(page, 'Uploaded 1 file');
  const row = page.getByRole('row', { name: 'hello.txt' });
  await expect(row).toBeVisible();
  await expect(row.locator('.ficon svg')).toBeVisible();
  await expect(row).toContainText(content.length + ' B');

  // Details panel and download; the body must round-trip.
  await row.getByRole('link', { name: 'hello.txt' }).click();
  const panel = page.locator('.panel');
  await expect(panel.getByRole('heading', { name: 'hello.txt' })).toBeVisible();
  await expect(panel).toContainText('(' + content.length + ' bytes)');
  await expect(panel).toContainText('text/plain');
  await panel.getByRole('button', { name: 'Copy key' }).click();
  await toast(page, 'Copied');
  const [download] = await Promise.all([page.waitForEvent('download'), panel.getByRole('link', { name: 'Download' }).click()]);
  expect(download.suggestedFilename()).toBe('hello.txt');
  expect(fs.readFileSync(await download.path(), 'utf8')).toBe(content);

  // Folder: created through the dialog, which navigates into it.
  await page.getByRole('button', { name: 'New folder' }).click();
  const nf = dialog(page, 'New folder');
  await nf.getByLabel('Folder name').fill('docs');
  await nf.getByRole('button', { name: 'Create' }).click();
  await expect(page).toHaveURL(new RegExp('#/b/' + bucket + '/docs/$'));
  await expect(page.locator('.breadcrumb .cur')).toHaveText('docs');
  await expect(page.getByRole('row', { name: '(folder marker)' })).toBeVisible();
  await page.locator('.breadcrumb').getByRole('link', { name: bucket }).click();
  const folderRow = page.getByRole('row', { name: 'docs' });
  await expect(folderRow.locator('.ficon-folder svg')).toBeVisible();

  // Delete the object, then the folder (dry run + confirmation), then the bucket.
  await row.getByRole('button', { name: 'Delete' }).click();
  await dialog(page, 'Delete').getByRole('button', { name: 'Delete' }).click();
  await toast(page, 'Deleted');
  await expect(row).toHaveCount(0);
  await folderRow.getByRole('button', { name: 'Delete' }).click();
  const df = dialog(page, 'Delete folder');
  await expect(df).toContainText('"docs/" and the 1 object inside');
  await df.getByRole('button', { name: 'Delete' }).click();
  await toast(page, 'Deleted 1 object');
  await expect(folderRow).toHaveCount(0);
  await expect(page.locator('#main .empty')).toContainText('This folder is empty');
  await deleteBucketFromList(page, bucket);
  expect(errors).toEqual([]);
});

test('bucket settings: encryption, versioning, tags and policy', async ({ page }) => {
  const errors = watch(page);
  await loginRoot(page);
  const bucket = 'smoke-settings-' + uniq();
  await createBucket(page, bucket);
  await page.getByRole('link', { name: 'Bucket settings' }).click();
  await expect(page.getByRole('heading', { level: 1, name: bucket })).toBeVisible();

  const enc = card(page, 'Default encryption');
  await enc.locator('select').selectOption('AES256');
  await enc.getByRole('button', { name: 'Apply' }).click();
  await toast(page, 'Default encryption set');

  const ver = card(page, 'Versioning');
  await ver.locator('select').selectOption('Enabled');
  await ver.getByRole('button', { name: 'Apply' }).click();
  await toast(page, 'Versioning enabled');

  const tags = card(page, 'Tags');
  await tags.getByLabel('Tag key').fill('env');
  await tags.getByLabel('Tag value').fill('smoke');
  await tags.getByRole('button', { name: 'Add' }).click();
  await expect(tags.locator('.chip')).toHaveText(/env=smoke/);
  await tags.getByRole('button', { name: 'Save tags' }).click();
  await toast(page, 'Tags saved');

  // Everything survives a reload (fresh GET of the bucket).
  await page.reload();
  await expect(card(page, 'Overview')).toContainText('AES256');
  await expect(card(page, 'Versioning').locator('select')).toHaveValue('Enabled');
  await expect(card(page, 'Tags').locator('.chip')).toHaveText(/env=smoke/);
  await expect(card(page, 'Default encryption').locator('select')).toHaveValue('AES256');

  const pol = card(page, /^Bucket policy/);
  const doc = { Version: '2012-10-17', Statement: [{ Effect: 'Allow', Principal: '*', Action: ['s3:GetObject'], Resource: ['arn:aws:s3:::' + bucket + '/*'] }] };
  await pol.locator('textarea').fill(JSON.stringify(doc));
  await expect(pol.locator('.hint.ok')).toHaveText('Valid JSON');
  await pol.getByRole('button', { name: 'Save policy' }).click();
  await toast(page, 'Policy saved');
  await expect(pol.locator('.badge.danger')).toHaveText('Public');
  await page.reload();
  await expect(card(page, /^Bucket policy/).locator('textarea')).toHaveValue(/s3:GetObject/);
  await card(page, /^Bucket policy/).getByRole('button', { name: 'Remove', exact: true }).click();
  await dialog(page, 'Remove policy').getByRole('button', { name: 'Remove' }).click();
  await toast(page, 'Policy removed');
  await expect(card(page, /^Bucket policy/).locator('textarea')).toHaveValue('');
  await expect(card(page, /^Bucket policy/).locator('.badge.danger')).toHaveCount(0);

  await card(page, 'Delete bucket').getByRole('button', { name: 'Delete bucket' }).click();
  await dialog(page, 'Delete bucket').getByRole('button', { name: 'Delete' }).click();
  await toast(page, 'Bucket deleted');
  await expect(page).toHaveURL(/#\/buckets$/);
  expect(errors).toEqual([]);
});

test('identity: create a user and key, sign in as the user', async ({ page }) => {
  const errors = watch(page);
  await loginRoot(page);
  const user = 'smoke-' + uniq();
  const password = 'smoke-password-' + uniq();

  await page.goto('/console/#/identity/users');
  await page.getByRole('button', { name: 'Create user' }).click();
  const cu = dialog(page, 'Create user');
  await cu.getByLabel('User name').fill(user);
  await cu.getByLabel('Console password').fill(password);
  await cu.getByRole('button', { name: 'Create' }).click();
  await toast(page, 'User created');
  const keyDialog = dialog(page, 'Access key for ' + user);
  const codes = keyDialog.locator('dd code');
  await expect(codes).toHaveCount(2);
  const ak1 = await codes.nth(0).textContent();
  const sk1 = await codes.nth(1).textContent();
  expect(ak1).toMatch(/^[A-Z0-9]{20}$/);
  expect(sk1).toHaveLength(40);
  await keyDialog.getByRole('button', { name: 'Done' }).click();
  await expect(keyDialog).toHaveCount(0);
  const userRow = page.getByRole('row', { name: user });
  await expect(userRow).toContainText('password set');
  await expect(userRow).toContainText('enabled');

  await page.goto('/console/#/identity/keys');
  await page.getByRole('button', { name: 'Create access key' }).click();
  const ck = dialog(page, 'Create access key');
  // A <label> wrapping a <select> has the option texts in its text, so match by role.
  await ck.getByRole('combobox', { name: 'User', exact: true }).selectOption(user);
  await ck.getByLabel('Description').fill('smoke test key');
  await ck.getByRole('button', { name: 'Create' }).click();
  const created = dialog(page, 'Access key created');
  const codes2 = created.locator('dd code');
  await expect(codes2).toHaveCount(2);
  const ak2 = await codes2.nth(0).textContent();
  expect(ak2).toMatch(/^[A-Z0-9]{20}$/);
  expect(await codes2.nth(1).textContent()).toHaveLength(40);
  expect(ak2).not.toBe(ak1);
  await created.getByRole('button', { name: 'Done' }).click();
  await expect(page.getByRole('row', { name: ak2 })).toContainText('smoke test key');

  // As the new user: no admin navigation, admin routes bounce to buckets.
  await logout(page);
  await login(page, user, password);
  await expect(page.getByRole('link', { name: 'Identity' })).toBeHidden();
  await expect(page.getByRole('link', { name: 'Encryption keys' })).toBeHidden();
  await expect(page.getByRole('link', { name: 'My access keys' })).toBeVisible();
  for (const hash of ['#/identity/users', '#/identity/policies', '#/kms']) {
    await page.goto('/console/' + hash);
    await expect(page).toHaveURL(/#\/buckets$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Buckets' })).toBeVisible();
  }
  await page.getByRole('link', { name: 'My access keys' }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'My access keys' })).toBeVisible();
  await expect(page.getByRole('row', { name: ak1 })).toBeVisible();
  await expect(page.getByRole('row', { name: ak2 })).toBeVisible();
  await page.goto('/console/#/status');
  await expect(page.getByRole('heading', { level: 1, name: 'Status' })).toBeVisible();
  await expect(page.locator('#main .stat')).toHaveCount(3);

  // Change password dialog opens and cancels cleanly.
  await page.getByRole('button', { name: 'Change password' }).click();
  const cp = dialog(page, 'Change console password');
  await expect(cp.getByLabel('Current password')).toBeVisible();
  await cp.getByRole('button', { name: 'Cancel' }).click();
  await expect(cp).toHaveCount(0);
  await logout(page);
  expect(errors).toEqual([]);
});

test('login refuses a wrong password and an access key pair', async ({ page }) => {
  const errors = watch(page, [{ url: /\/console\/api\/login$/, text: /40[01]/ }]);
  await page.goto('/console/');
  await page.getByLabel('User name').fill(ROOT_USER);
  await page.getByLabel('Password', { exact: true }).fill('not-the-password-' + uniq());
  await page.getByRole('button', { name: 'Log in' }).click();
  const alert = page.locator('#toasts .toast.error');
  await expect(alert).toContainText('invalid user name or password');
  await alert.getByRole('button', { name: 'Dismiss' }).click();
  await expect(alert).toHaveCount(0);
  await expect(page.locator('#app')).toBeHidden();

  // A generated key pair (20-char ID, 40-char secret) is for the S3 API only.
  await page.getByLabel('User name').fill('SMOKE' + 'ABCDEFGHIJ0123456789'.slice(0, 15));
  await page.getByLabel('Password', { exact: true }).fill('s'.repeat(40));
  await page.getByRole('button', { name: 'Log in' }).click();
  await expect(alert).toContainText('S3 API');
  await expect(page.locator('#app')).toBeHidden();
  expect(errors).toEqual([]);
});
