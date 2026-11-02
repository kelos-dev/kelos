const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
function slice(start, end) {
  const from = source.indexOf(start);
  const to = source.indexOf(end, from);
  assert.notEqual(from, -1);
  assert.notEqual(to, -1);
  return source.slice(from, to);
}

const redirects = [];
global.state = {};
global.window = {location: {search: '', replace: target => redirects.push(target)}};
vm.runInThisContext(slice('async function api(', 'let toastTimer'), {filename: 'app.js'});
vm.runInThisContext(slice('async function loadIdentity(', 'async function loadConfig('), {filename: 'app.js'});

(async () => {
  global.fetch = async () => ({status: 401});
  await assert.rejects(api('/api/config'), {message: 'Authentication required', status: 401});
  assert.deepEqual(redirects, ['/login']);
  redirects.length = 0;
  window.location.search = '?namespace=team-a&session=worker';
  await assert.rejects(api('/api/config'), {message: 'Authentication required', status: 401});
  assert.deepEqual(redirects, ['/login?namespace=team-a&session=worker']);
  window.location.search = '';
  redirects.length = 0;
  global.fetch = async () => ({status: 403, ok: false, json: async () => ({error: 'access denied'})});
  await assert.rejects(api('/api/sessions'), {message: 'access denied', status: 403});
  assert.deepEqual(redirects, []);

  const identity = {};
  global.requiredElement = selector => {
    assert.equal(selector, '#console-identity');
    return identity;
  };
  for (const username of ['oidc:alice', 'oidc:bob', undefined]) {
    let requests = 0;
    global.fetch = async path => {
      assert.equal(path, '/api/config');
      requests++;
      return {status: 200, ok: true, json: async () => ({defaultNamespace: 'team-a', username})};
    };
    await loadIdentity();
    assert.equal(requests, 1);
    assert.equal(identity.hidden, !username);
    assert.equal(identity.textContent, username ? `Signed in as ${username}` : '');
  }

  let logout;
  global.requiredElement = () => ({addEventListener: (_event, listener) => { logout = listener; }});
  global.closeBrowserNotifications = () => {};
  global.removePushSubscription = async () => {};
  vm.runInThisContext(slice("requiredElement('#logout').addEventListener", 'function setSidebarOpen'), {filename: 'app.js'});
  global.fetch = async (_path, options) => {
    assert.equal(options.method, 'POST');
    return {status: 200, ok: true, json: async () => ({logoutURL: '/oauth2/sign_out?rd=/oauth2/sign_in'})};
  };
  await logout();
  assert.deepEqual(redirects, ['/oauth2/sign_out?rd=/oauth2/sign_in']);
  redirects.length = 0;
  global.fetch = async () => ({status: 200, ok: true, json: async () => ({authenticated: false})});
  await logout();
  assert.deepEqual(redirects, ['/login']);

  const loginSource = fs.readFileSync(path.join(__dirname, '../web/login.js'), 'utf8');
  for (const search of ['', '?namespace=team-a&session=worker&uid=instance']) {
    let submit;
    const loginRedirects = [];
    const nodes = {
      '#login-form': {addEventListener(_event, listener) { submit = listener; }},
      '#token': {value: 'secret-token'},
      '#login-error': {},
      '#reveal': {addEventListener() {}},
      '#login-form button[type="submit"]': {},
    };
    vm.runInNewContext(loginSource, {
      document: {querySelector: selector => nodes[selector]},
      window: {location: {search, replace: value => loginRedirects.push(value)}},
      fetch: async (url, options) => {
        assert.equal(url, '/api/login');
        assert.equal(options.method, 'POST');
        assert.deepEqual(JSON.parse(options.body), {token: 'secret-token'});
        return {ok: true};
      },
    });
    await submit({preventDefault() {}});
    assert.deepEqual(loginRedirects, [`/${search}`]);
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
