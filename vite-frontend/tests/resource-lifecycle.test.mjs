import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = file => readFile(new URL(`../src/${file}`, import.meta.url), 'utf8');
const compile = text => ts.transpileModule(text, {
  compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.ES2020 },
}).outputText;
const load = async file => import(`data:text/javascript;base64,${Buffer.from(compile(await source(file))).toString('base64')}`);
const { CaptchaRequests, manageCaptcha } = await load('utils/captcha-lifecycle.ts');
const { runForwardImport, importResultPage, IMPORT_PAGE_SIZE } = await load('utils/forward-import.ts');
const networkJs = compile((await source('api/network.ts'))
  .replace(/^import .*;\s*$/gm, '')
  .replaceAll('import.meta.env.VITE_API_BASE', 'undefined')
  .replaceAll('export ', '')
  .replace('default Network;', 'globalThis.Network = Network;'));
const library = await source('utils/tac.min.js');
const flush = () => new Promise(resolve => setImmediate(resolve));

test('GET and POST settle all 401 branches and preserve the response contract', async () => {
  for (const method of ['get', 'post']) {
    for (const pathname of ['/', '/node']) {
      for (const kind of ['success', 'application-401', 'http-401', 'network-error']) {
        const removed = [];
        const request = () => kind === 'success'
          ? Promise.resolve({ data: { code: 0, msg: 'ok', data: 'ok' } })
          : kind === 'application-401'
            ? Promise.resolve({ data: { code: 401, msg: '未登录或token已过期', data: null } })
            : Promise.reject(kind === 'http-401' ? { response: { status: 401 } } : new Error('offline'));
        const sandbox = {
          axios: { defaults: {}, get: request, post: request },
          window: { localStorage: { getItem: () => null, removeItem: key => removed.push(key) }, location: { pathname, href: pathname } },
          isWebViewFunc: () => false, getPanelAddresses() {}, console: { error() {} },
        };
        vm.createContext(sandbox);
        vm.runInContext(networkJs, sandbox);
        let finalized = false;
        let response;
        sandbox.Network[method]('/mock').then(value => { response = value; }).finally(() => { finalized = true; });
        await flush();
        assert.equal(finalized, true, `${method} ${pathname} ${kind}`);
        assert.equal(response.code, kind.includes('401') ? 401 : kind === 'success' ? 0 : -1);
        assert.equal(typeof response.msg, 'string');
        if (kind.includes('401')) {
          assert.deepEqual(removed, ['token', 'role_id', 'name']);
          assert.equal(sandbox.window.location.href, '/');
        }
      }
    }
  }
});

class MockXHR {
  static DONE = 4;
  static requests = [];
  timeout = 0;
  active = false;
  aborted = false;
  headers = {};
  constructor() { MockXHR.requests.push(this); }
  open(method, url) { this.method = method; this.url = url; }
  setRequestHeader(key, value) { this.headers[key] = value; }
  send(data) { this.data = data; this.active = true; }
  abort() { this.aborted = true; this.active = false; this.onabort?.(); }
  respond(data, status = 200) {
    this.active = false; this.status = status; this.readyState = 4;
    this.responseText = typeof data === 'string' ? data : JSON.stringify(data);
    this.onreadystatechange?.();
  }
  expire() { this.active = false; this.ontimeout?.(); }
}

function fixture() {
  MockXHR.requests = [];
  globalThis.XMLHttpRequest = MockXHR;
  const sandbox = {
    window: {}, self: {}, XMLHttpRequest: MockXHR, setTimeout, clearTimeout,
    document: {
      nodeType: 9, currentScript: { src: 'http://localhost/tac.min.js' },
      querySelector: () => ({}), querySelectorAll: () => [], getElementsByTagName: () => [],
    },
  };
  globalThis.window = sandbox.window;
  vm.createContext(sandbox);
  vm.runInContext(library, sandbox);
  const config = new sandbox.window.CaptchaConfig({ bindEl: '#test', requestCaptchaDataUrl: '/generate', validCaptchaUrl: '/verify' });
  const tac = Object.create(sandbox.window.TAC.prototype);
  tac.config = config;
  tac.showLoading = () => {};
  const errors = [];
  manageCaptcha(tac, error => errors.push(error));
  return { tac, errors, window: sandbox.window };
}

test('50 CAPTCHA refreshes retain one request, with timeout, and closing cancels it', async () => {
  const { tac, errors } = fixture();
  for (let i = 0; i < 50; i++) tac.reloadCaptcha();
  assert.equal(MockXHR.requests.filter(xhr => xhr.active).length, 1);
  assert.equal(MockXHR.requests.filter(xhr => xhr.aborted).length, 49);
  assert.ok(MockXHR.requests.every(xhr => xhr.timeout === 15000));
  tac.destroyWindow();
  tac.destroyWindow();
  await flush();
  assert.equal(MockXHR.requests.filter(xhr => xhr.active).length, 0);
  assert.equal(MockXHR.requests.filter(xhr => xhr.aborted).length, 50);
  assert.deepEqual(errors, []);
});

test('the real TAC init template cleanup does not dispose its new request owner', async () => {
  const { tac, errors } = fixture();
  const element = {
    append() { return this; }, find() { return this; }, click() { return this; },
    css() { return this; }, remove() {}, querySelectorAll() { return []; },
  };
  tac.config.domBindEl = element;
  tac.style = {};
  tac.init();
  assert.equal(tac.destroyed, false);
  assert.equal(MockXHR.requests.filter(xhr => xhr.active).length, 1);
  tac.destroyWindow();
  await flush();
  assert.equal(MockXHR.requests.filter(xhr => xhr.active).length, 0);
  assert.deepEqual(errors, []);
});

test('late CAPTCHA generation completion never accesses removed DOM', async () => {
  const { tac, errors } = fixture();
  let domCalls = 0;
  tac.closeLoading = () => { domCalls++; };
  tac.reloadCaptcha();
  // Resolve the XHR first, then unmount before Promise continuations run.
  MockXHR.requests.at(-1).respond({ captcha: { type: 'SLIDER' } });
  tac.destroyWindow();
  await flush();
  assert.equal(domCalls, 0);
  assert.deepEqual(errors, []);
});

test('an old failed request cannot close its replacement CAPTCHA', async () => {
  const { tac, errors } = fixture();
  tac.reloadCaptcha();
  MockXHR.requests.at(-1).respond({}, 500);
  tac.reloadCaptcha();
  await flush();
  assert.equal(tac.destroyed, false);
  assert.equal(MockXHR.requests.filter(xhr => xhr.active).length, 1);
  assert.deepEqual(errors, []);
  tac.destroyWindow();
  await flush();
});

test('CAPTCHA timeout closes the owner and reports one recoverable error', async () => {
  const { tac, errors } = fixture();
  tac.reloadCaptcha();
  MockXHR.requests.at(-1).expire();
  await flush();
  assert.equal(tac.destroyed, true);
  assert.equal(errors.length, 1);
  assert.match(errors[0].message, /超时/);
  assert.equal(MockXHR.requests.filter(xhr => xhr.active).length, 0);
});

test('malformed CAPTCHA JSON settles instead of leaving the caller pending', async () => {
  fixture();
  const requests = new CaptchaRequests();
  const promise = requests.send({ url: '/generate' });
  MockXHR.requests.at(-1).respond('{bad json');
  await assert.rejects(promise, /响应格式错误/);
  assert.equal(MockXHR.requests.at(-1).onreadystatechange, null);
});

test('verification callbacks are suppressed after refresh and render listeners are destroyed', async () => {
  const { tac, window, errors } = fixture();
  let success = 0;
  let destroyed = 0;
  let delayedTip;
  const captcha = {
    destroy: () => { destroyed++; }, el: { css() {} },
    showTips: (_message, _status, callback) => { delayedTip = callback; },
  };
  tac.config.validSuccess = () => { success++; };
  window.currentCaptcha = captcha;
  tac.openCaptcha();
  const pending = tac.config.validCaptcha('id', { trackList: [] }, captcha);
  MockXHR.requests.at(-1).respond({ code: 200, data: { validToken: 'test-token' } });
  await pending;
  tac.reloadCaptcha();
  delayedTip();
  assert.equal(success, 0);
  assert.equal(destroyed, 1);
  assert.equal(window.currentCaptcha, undefined);
  tac.destroyWindow();
  await flush();
  assert.deepEqual(errors, []);
});

test('successful verification preserves the callback and request payload', async () => {
  const { tac } = fixture();
  let response;
  tac.config.validSuccess = value => { response = value; };
  const captcha = { showTips: (_message, _status, callback) => callback() };
  const pending = tac.config.validCaptcha('captcha-id', { trackList: [1] }, captcha);
  assert.deepEqual(JSON.parse(MockXHR.requests.at(-1).data), { id: 'captcha-id', data: { trackList: [1] } });
  MockXHR.requests.at(-1).respond({ code: 200, data: { validToken: 'test-token' } });
  await pending;
  assert.equal(response.data.validToken, 'test-token');
  tac.destroyWindow();
});

test('10,000 invalid imports yield in batches and preserve paginated results without copying prefixes', async () => {
  let yields = 0;
  const snapshots = [];
  const completed = await runForwardImport({
    text: Array.from({ length: 10000 }, (_, i) => `invalid-${i}`).join('\n'), tunnelId: 1,
    createForward: () => { throw new Error('Invalid input must not call the API'); },
    onProgress: progress => snapshots.push(progress), shouldStop: () => false,
    yieldToBrowser: async () => { yields++; },
  });
  assert.equal(completed, true);
  assert.equal(yields, 200);
  assert.ok(snapshots.length <= 205);
  assert.ok(snapshots.every(snapshot => snapshot.results === snapshots[0].results));
  const final = snapshots.at(-1);
  assert.equal(final.total, 10000);
  assert.equal(final.success, 0);
  assert.equal(importResultPage(final, 1).length, IMPORT_PAGE_SIZE);
  assert.equal(importResultPage(final, 1)[0].line, 'invalid-9999');
  assert.equal(importResultPage(final, 200).at(-1).line, 'invalid-0');
  assert.equal(importResultPage(snapshots[0], 1)[0].line, 'invalid-0');
});

test('bulk import runs serially, reports errors, and stops before the next request on unmount', async () => {
  const calls = [];
  let stopped = false;
  let final;
  const completed = await runForwardImport({
    text: 'host:80|first|123\nhost:81|second|456\nhost:82|third|789', tunnelId: 3,
    createForward: async data => { calls.push(data); stopped = true; return { code: 0 }; },
    onProgress: progress => { final = progress; }, shouldStop: () => stopped,
  });
  assert.equal(completed, false);
  assert.equal(calls.length, 1);
  assert.equal(final, undefined);

  await runForwardImport({
    text: 'host:80|good|123\nhost:81|bad|456\nhost:82|invalid|123abc', tunnelId: 3,
    createForward: async data => data.name === 'good' ? { code: 0 } : { code: -1, msg: 'rejected' },
    onProgress: progress => { final = progress; }, shouldStop: () => false,
  });
  assert.equal(final.total, 3);
  assert.equal(final.success, 1);
  assert.equal(final.results[1].message, 'rejected');
  assert.match(final.results[2].message, /入口端口格式错误/);
});

test.after(() => { delete globalThis.XMLHttpRequest; delete globalThis.window; });
