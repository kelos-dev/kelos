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

class SearchParamsWithoutSize extends URLSearchParams {
  get size() { return undefined; }
}

const redirects = [];
global.state = {};
global.window = {location: {pathname: '/', search: '', replace: target => redirects.push(target)}};
vm.runInThisContext(slice('async function api(', 'let toastTimer'), {filename: 'app.js'});
vm.runInThisContext(slice('async function uploadAttachment(', 'async function submitComposer('), {filename: 'app.js'});
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
  window.location.pathname = '/namespaces/team-a/resources/tasks/build';
  redirects.length = 0;
  await assert.rejects(api('/api/config'), {message: 'Authentication required', status: 401});
  assert.deepEqual(redirects, ['/login?next=%2Fnamespaces%2Fteam-a%2Fresources%2Ftasks%2Fbuild']);
  redirects.length = 0;
  await assert.rejects(apiText('/api/resources/tasks/team-a/build/logs'), {message: 'Authentication required'});
  assert.deepEqual(redirects, ['/login?next=%2Fnamespaces%2Fteam-a%2Fresources%2Ftasks%2Fbuild']);
  window.location.pathname = '/namespaces/team-a/sessions/worker';
  redirects.length = 0;
  const uploadSession = {namespace: 'team-a', name: 'worker'};
  const uploadFile = {name: 'notes.txt'};
  global.FormData = class {
    fields = [];
    append(...field) { this.fields.push(field); }
  };
  const attachment = {id: 'attachment-id', name: 'notes.txt'};
  for (const status of [200, 401]) {
    global.fetch = async (url, options) => {
      assert.equal(url, '/api/sessions/team-a/worker/attachments');
      assert.equal(options.method, 'POST');
      assert.deepEqual(options.body.fields, [['file', uploadFile, 'notes.txt']]);
      return {status, ok: status === 200, json: async () => attachment};
    };
    if (status === 200) {
      assert.deepEqual(await uploadAttachment(uploadSession, uploadFile), attachment);
      assert.deepEqual(redirects, []);
    } else {
      await assert.rejects(uploadAttachment(uploadSession, uploadFile), {message: 'Authentication required'});
      assert.deepEqual(redirects, ['/login?next=%2Fnamespaces%2Fteam-a%2Fsessions%2Fworker']);
    }
  }
  window.location.pathname = '/';
  redirects.length = 0;
  global.fetch = async () => ({status: 403, ok: false, json: async () => ({error: 'access denied'})});
  await assert.rejects(api('/api/sessions'), {message: 'access denied', status: 403});
  assert.deepEqual(redirects, []);

  const limitedRedirects = [];
  const context = {
    URLSearchParams: SearchParamsWithoutSize,
    fetch: async () => ({status: 401}),
    window: {location: {pathname: '/namespaces/team-a/sessions/worker', search: '?uid=instance', replace: value => limitedRedirects.push(value)}},
  };
  vm.createContext(context);
  vm.runInContext(slice('async function api(', 'let toastTimer'), context);
  await assert.rejects(context.api('/api/config'), {message: 'Authentication required'});
  assert.deepEqual(limitedRedirects, ['/login?uid=instance&next=%2Fnamespaces%2Fteam-a%2Fsessions%2Fworker']);
  context.window.location.pathname = '/';
  context.window.location.search = '?namespace=team-a&session=worker&uid=instance';
  await assert.rejects(context.api('/api/config'), {message: 'Authentication required'});
  assert.equal(limitedRedirects[1], '/login?namespace=team-a&session=worker&uid=instance');

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
  for (const SearchParams of [URLSearchParams, SearchParamsWithoutSize]) {
    for (const [search, destination] of [
      ['', '/'],
      ['?namespace=team-a&session=worker&uid=instance', '/?namespace=team-a&session=worker&uid=instance'],
      ['?next=%2Fnamespaces%2Fteam-a%2Fresources%2Ftasks%2Fbuild', '/namespaces/team-a/resources/tasks/build'],
      ['?next=%2Fnamespaces%2Fteam-a%2Fresources', '/namespaces/team-a/resources'],
      ['?next=%2Fnamespaces%2Fteam-a%2Fsessions%2Fworker', '/namespaces/team-a/sessions/worker'],
      ['?next=%2Fnamespaces%2Fteam-a%2Fsessions%2Fworker&uid=instance', '/namespaces/team-a/sessions/worker?uid=instance'],
      ['?next=%2Fnamespaces%2Fteam-a%2Fresources%2Fsessions%2Fworker', '/namespaces/team-a/resources/sessions/worker'],
      ['?next=https%3A%2F%2Fexample.com', '/'],
      ['?next=%2F%2Fexample.com', '/'],
      ['?next=%2Fnamespaces%2Fteam-a%2Fresources%2Ftasks%2Fbuild%5Cexample.com', '/'],
    ]) {
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
        URLSearchParams: SearchParams,
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
      assert.deepEqual(loginRedirects, [destination]);
    }
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
