import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const browser = await chromium.launch({
  headless: true,
  ...(process.env.BROWSER_EXECUTABLE ? { executablePath: process.env.BROWSER_EXECUTABLE } : {}),
});
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const page = await context.newPage();
page.setDefaultTimeout(10000);
const errors = [];
const posts = [];
const modern = {
  id: 3, name: 'SZ-new', serverIp: 'proxy.tkmo.xin', port: '5001-6000,61001',
  interfaceName: 'ens19', tcpListenAddr: '0.0.0.0', udpListenAddr: '172.16.115.226',
  http: 0, tls: 0, socks: 0, status: 0,
};
let nodes = [modern];
page.on('pageerror', error => errors.push(error.message));
await context.addInitScript(() => {
  const payload = btoa(JSON.stringify({ sub: '1', role_id: 0, user: 'test-admin', exp: Math.floor(Date.now() / 1000) + 3600 }));
  localStorage.setItem('token', `e30.${payload}.test`);
  localStorage.setItem('role_id', '0');
  localStorage.setItem('name', 'test-admin');
});
await page.route('**/api/v1/**', async route => {
  const url = route.request().url();
  if (url.endsWith('/node/update') || url.endsWith('/node/create')) {
    posts.push(route.request().postDataJSON());
  }
  await route.fulfill({ json: { code: 0, msg: 'ok', data: url.endsWith('/node/list') ? nodes : [] } });
});
const openEdit = async () => {
  await page.getByRole('button', { name: '编辑', exact: true }).click();
  await page.getByRole('dialog').waitFor();
  await page.waitForTimeout(700);
};
const save = async () => {
  await page.getByRole('dialog').getByRole('button', { name: '确定', exact: true }).click();
  await page.getByRole('dialog').waitFor({ state: 'hidden' });
};
try {
  await page.goto(`${process.env.TEST_BASE_URL || 'http://127.0.0.1:3067'}/node`);
  await openEdit();
  assert.equal(await page.getByRole('textbox', { name: '端口', exact: true }).inputValue(), modern.port);
  await page.getByRole('textbox', { name: '端口', exact: true }).fill('7000-6000');
  await page.getByRole('dialog').getByRole('button', { name: '确定', exact: true }).click();
  assert.equal(posts.length, 0);
  assert.equal(await page.getByText('请输入1-65535内的端口或端口段，用英文逗号分隔').isVisible(), true);
  await page.getByRole('textbox', { name: '端口', exact: true }).fill(modern.port);
  await save();
  const { status, ...expected } = modern;
  assert.deepEqual(posts.at(-1), expected);
  await openEdit();
  if (process.env.SCREENSHOT_DIR) {
    await page.screenshot({ path: `${process.env.SCREENSHOT_DIR}/flux-node-edit-desktop.png`, fullPage: true });
  }
  await page.setViewportSize({ width: 390, height: 844 });
  // Responsive layout remounts NodePage, so reopen the dialog after resizing.
  await openEdit();
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  if (process.env.SCREENSHOT_DIR) {
    await page.screenshot({ path: `${process.env.SCREENSHOT_DIR}/flux-node-edit-mobile.png`, fullPage: true });
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  const legacy = { id: 1, name: 'legacy-entry', serverIp: 'example.com', ip: '1.2.3.4,example.com', portSta: 5001, portEnd: 20169, http: 1, tls: 0, socks: 0, status: 0 };
  nodes = [legacy];
  await page.reload();
  await openEdit();
  assert.equal(await page.getByRole('spinbutton', { name: '起始端口', exact: true }).inputValue(), '5001');
  assert.equal(await page.getByRole('spinbutton', { name: '结束端口', exact: true }).inputValue(), '20169');
  assert.equal(await page.getByRole('switch', { name: '屏蔽HTTP', exact: true }).isDisabled(), true);
  await save();
  const { status: legacyStatus, ...legacyExpected } = legacy;
  assert.deepEqual(posts.at(-1), legacyExpected);
  nodes = [{ id: 5, name: null, serverIp: null, portSta: null, portEnd: null, status: 0 }];
  await page.reload();
  await openEdit();
  assert.equal(await page.getByRole('spinbutton', { name: '起始端口', exact: true }).inputValue(), '1000');
  nodes = [modern];
  await page.reload();
  await page.getByRole('button', { name: '新增', exact: true }).click();
  await page.getByRole('dialog').waitFor();
  await page.getByRole('textbox', { name: '节点名称', exact: true }).fill('new-entry');
  await page.getByRole('textbox', { name: '服务器IP', exact: true }).fill('example.com');
  await save();
  assert.equal(posts.at(-1).port, '1000-65535');
  assert.equal('id' in posts.at(-1), false);
  assert.deepEqual(errors, []);
  console.log('PASS: modern/legacy edit and save, invalid ports, null fields, creation, desktop/mobile, no page errors');
} finally {
  await browser.close();
}
