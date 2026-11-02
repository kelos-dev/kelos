const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const app = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');
function extract(start, end) {
  const from = app.indexOf(start), to = app.indexOf(end, from);
  assert.ok(from > 0 && to > from);
  vm.runInThisContext(app.slice(from, to));
}
extract('function memberName(', 'function adminResourcePath(');
extract('async function loadNamespaceOptions(', 'function defaultSessionYAML(');
extract('async function openDialog(', 'elements.newSessionButton.addEventListener(');

function element() {
  return {
    hidden: false, disabled: false, value: '', textContent: '', children: [], open: false,
    listeners: new Map(), attributes: new Map(),
    get options() { return this.children; },
    append(...children) { this.children.push(...children); },
    replaceChildren(...children) { this.children = children; },
    addEventListener(name, listener) { this.listeners.set(name, listener); },
    setAttribute(name, value) { this.attributes.set(name, value); },
    getAttribute(name) { return this.attributes.get(name); }, focus() {},
    showModal() { this.open = true; }, close() { this.open = false; },
  };
}
const member = {id: 'Ym9i', username: 'oidc:bob', role: 'admin', version: '3', sources: [{binding: 'binding', role: 'admin', managed: true}], canChange: true, canRemove: true};
const inventory = {enabled: true, usernamePrefix: 'oidc:', currentUser: 'oidc:alice', roles: [{name: 'user', label: 'User', description: 'Use Sessions', canAssign: true}, {name: 'admin', label: 'Admin', description: 'Manage members', canAssign: true}], members: [member], groups: [{name: 'oidc:developers', role: 'user', binding: 'developers'}]};
function reset() {
  global.state = {namespace: 'team-a', namespaces: [], namespaceGeneration: 0, namespaceRefresh: null, defaultNamespace: 'team-a', memberGeneration: 0, memberInventory: null, memberSaving: false, memberEditing: null};
  global.elements = Object.fromEntries(['memberStatus', 'memberList', 'memberGroups', 'memberError', 'addMember', 'memberDialog', 'memberDialogTitle', 'memberDialogDescription', 'memberSubject', 'memberRole', 'memberHelp', 'memberDialogError', 'memberSave', 'activeNamespace', 'namespace', 'namespaceForm', 'namespaceStatus', 'newSessionButton', 'welcomeNew', 'dialog', 'dialogError'].map(key => [key, element()]));
  elements.namespaceLabels = [element()];
  global.document = {createElement: element};
  global.addOption = (select, value, label) => { const option = element(); option.value = value; option.textContent = label; select.append(option); };
  global.errorMessage = error => error.message;
  global.showToast = () => {};
  global.renderOverview = global.renderResources = global.renderSessions = () => {};
  global.setConsoleView = global.setCreationMode = () => {};
  global.loadOptions = async () => {};
  global.adminRefreshes = 0;
  global.loadAdminResources = async () => { adminRefreshes++; };
  global.loadAdmin = async () => { await Promise.all([loadMembers(), loadAdminResources()]); };
  global.switchNamespace = async namespace => { state.namespace = namespace; state.namespaceGeneration++; };
  global.window = {location: {search: ''}, confirm: () => true, setTimeout: () => {}, localStorage: new class {
    values = new Map();
    getItem(key) { return this.values.get(key) || null; }
    setItem(key, value) { this.values.set(key, value); }
    removeItem(key) { this.values.delete(key); }
  }};
}
async function testMembers() {
  reset();
  global.api = async url => {
    assert.equal(url, '/api/admin/members?namespace=team-a');
    return {...inventory, members: [member, {...member, username: 'oidc:external', sources: [{binding: 'outside', role: 'admin', managed: false}], canChange: false, canRemove: false}]};
  };
  await loadMembers();
  assert.equal(elements.addMember.hidden, false);
  assert.equal(elements.memberList.children[0].children[0].children[0].textContent, 'bob');
  assert.deepEqual(elements.memberList.children[0].children[1].children.map(child => child.textContent), ['Admin', 'Change role', 'Remove member']);
  assert.equal(elements.memberList.children[1].children[1].children.length, 1);
  assert.equal(elements.memberList.children[1].children[0].children[1].textContent, 'External access: outside (Admin)');
  assert.equal(elements.memberGroups.children[0].textContent, 'Access through groups');
  global.api = async () => ({enabled: false});
  await loadMembers();
  assert.equal(elements.addMember.hidden, true);
  assert.equal(elements.memberList.children.length, 0);
  assert.match(elements.memberStatus.textContent, /require OIDC/);
  global.api = async () => { throw new Error('access denied'); };
  await loadMembers();
  assert.equal(elements.memberStatus.textContent, 'Unable to load members: access denied');
}
async function testAddAndChange() {
  reset(); state.memberInventory = inventory;
  openMemberDialog();
  assert.equal(elements.memberDialog.open, true);
  assert.equal(elements.memberRole.value, 'user');
  assert.equal(elements.memberDialogDescription.textContent, 'Membership in team-a');
  elements.memberSubject.value = ' bob ';
  const requests = [];
  global.api = async (url, options) => { requests.push({url, options}); return inventory; };
  await saveMember();
  assert.deepEqual(requests[0], {url: '/api/admin/members?namespace=team-a', options: {method: 'POST', body: JSON.stringify({subject: 'bob', role: 'user'})}});
  assert.equal(elements.memberDialog.open, false);
  assert.equal(adminRefreshes, 1);
  openMemberDialog(member);
  assert.equal(elements.memberSubject.disabled, true);
  elements.memberRole.value = 'user';
  await saveMember();
  assert.deepEqual(requests[2], {url: '/api/admin/members/team-a/Ym9i?version=3', options: {method: 'PUT', body: JSON.stringify({role: 'user'})}});
  assert.equal(adminRefreshes, 2);
  openMemberDialog();
  elements.memberSubject.value = 'bob';
  global.api = async () => { throw new Error('member already exists'); };
  await saveMember();
  assert.equal(elements.memberDialog.open, true);
  assert.equal(elements.memberSubject.value, 'bob');
  assert.equal(elements.memberDialogError.textContent, 'member already exists');
  assert.equal(elements.memberSave.disabled, false);
}
async function testRemoveAndSelfDemotion() {
  reset(); state.memberInventory = inventory;
  const requests = [];
  global.api = async (url, options) => { requests.push({url, options}); return inventory; };
  window.confirm = () => false;
  await removeMember(member, 'team-a', element());
  assert.equal(requests.length, 0);
  window.confirm = message => { assert.match(message, /Remove bob from team-a/); return true; };
  await removeMember(member, 'team-a', element());
  assert.deepEqual(requests[0], {url: '/api/admin/members/team-a/Ym9i?version=3', options: {method: 'DELETE'}});
  openMemberDialog({...member, username: 'oidc:alice'});
  elements.memberRole.value = 'user';
  window.confirm = message => { assert.match(message, /Change your role to User in team-a/); return false; };
  await saveMember();
  assert.equal(requests.length, 2);
}
async function testStaleNamespace() {
  reset();
  let resolve;
  global.api = () => new Promise(done => { resolve = done; });
  const request = loadMembers();
  state.namespace = 'team-b'; resolve(inventory); await request;
  assert.equal(elements.memberList.children.length, 0);
  assert.equal(state.memberInventory, null);
  state.namespace = 'team-a'; state.memberInventory = inventory;
  openMemberDialog(); elements.memberSubject.value = 'bob';
  let calls = 0;
  global.api = () => { calls++; return new Promise(done => { resolve = done; }); };
  const save = saveMember();
  assert.equal(elements.memberSubject.disabled, true);
  assert.equal(elements.memberRole.disabled, true);
  state.namespace = 'team-b'; state.memberEditing = null;
  elements.memberList.replaceChildren('team-b members');
  resolve({}); await save;
  assert.equal(state.memberSaving, false);
  assert.equal(elements.memberDialog.open, true);
  assert.equal(calls, 1);
  assert.equal(adminRefreshes, 0);
  assert.deepEqual(elements.memberList.children, ['team-b members']);
}
async function testNamespacePicker() {
  reset();
  global.loadIdentity = async () => ({defaultNamespace: 'team-a'});
  const requests = [];
  let available = ['team-a', 'team-b'];
  global.api = async url => {
    requests.push(url);
    const parsed = new URL(url, 'https://console.example');
    assert.equal(parsed.pathname, '/api/namespaces');
    const namespace = parsed.searchParams.get('namespace');
    return {namespaces: available.filter(name => !namespace || name === namespace)};
  };
  window.localStorage.setItem('kelos-console-namespace', 'team-b');
  assert.equal(await loadConfig(), true);
  assert.equal(state.namespace, 'team-b');
  assert.equal(elements.activeNamespace.value, 'team-b');
  assert.deepEqual(elements.activeNamespace.children.map(option => option.value), ['team-b']);
  assert.deepEqual(requests, ['/api/namespaces?namespace=team-b']);
  await refreshNamespaces();
  assert.equal(state.namespace, 'team-b');
  assert.deepEqual(elements.activeNamespace.children.map(option => option.value), ['team-a', 'team-b']);
  requests.length = 0;
  window.location.search = '?namespace=team-a&session=worker';
  window.localStorage.setItem('kelos-console-namespace', 'team-b');
  assert.equal(await loadConfig(), true);
  assert.equal(state.namespace, 'team-a', 'A link must override the remembered namespace');
  assert.deepEqual(requests, ['/api/namespaces?namespace=team-a']);
  window.location.search = '';
  requests.length = 0;
  window.localStorage.setItem('kelos-console-namespace', 'inaccessible');
  assert.equal(await loadConfig(), false);
  assert.equal(state.namespace, 'team-a');
  assert.deepEqual(requests, ['/api/namespaces?namespace=inaccessible', '/api/namespaces']);
  available = ['team-c'];
  assert.equal(await loadConfig(), false);
  assert.equal(state.namespace, 'team-c');
  available = [];
  assert.equal(await loadConfig(), false);
  assert.equal(state.namespace, '');
  assert.equal(elements.activeNamespace.disabled, false);
  assert.equal(elements.newSessionButton.disabled, true);
  assert.match(elements.namespaceStatus.textContent, /Ask an administrator/);
  assert.equal(window.localStorage.getItem('kelos-console-namespace'), null);
}

function startConsole() {
  const from = app.indexOf('const configReady = loadConfig();');
  const to = app.indexOf('window.setInterval(', from);
  assert.ok(from > 0 && to > from);
  const loaded = [];
  vm.runInNewContext(app.slice(from, to), {
    loadConfig, refreshNamespaces, errorMessage, showToast,
    URLSearchParams, window: {addEventListener() {}}, applyConsoleLocation() {},
    loadOptions: async () => { loaded.push('options'); },
    loadSessions: async () => { loaded.push('sessions'); },
    loadResources: async () => { loaded.push('resources'); },
    setConsoleView: view => { loaded.push(view); },
  });
  return loaded;
}

async function testBackgroundNamespaceDiscovery() {
  for (const outcome of ['success', 'failure', 'revoked', 'default']) {
    reset();
    state.namespace = '';
    const preferred = outcome === 'default' ? 'team-a' : 'team-b';
    if (outcome !== 'default') window.localStorage.setItem('kelos-console-namespace', preferred);
    global.loadIdentity = async () => ({defaultNamespace: 'team-a'});
    const requests = [];
    let resolve, reject;
    global.api = async url => {
      requests.push(url);
      if (url === `/api/namespaces?namespace=${preferred}`) return {namespaces: [preferred]};
      assert.equal(url, '/api/namespaces');
      return new Promise((a, b) => { resolve = a; reject = b; });
    };
    const loaded = startConsole();
    await new Promise(setImmediate);
    assert.deepEqual(loaded, ['options', 'sessions', 'resources', 'overview']);
    assert.deepEqual(requests, [`/api/namespaces?namespace=${preferred}`, '/api/namespaces']);
    assert.equal(state.namespace, preferred);
    assert.deepEqual(state.namespaces, [preferred]);
    assert.equal(elements.activeNamespace.disabled, false);
    assert.equal(elements.newSessionButton.disabled, false);
    assert.equal(elements.activeNamespace.getAttribute('aria-busy'), 'true');
    assert.equal(elements.namespaceLabels[0].textContent, preferred);
    if (outcome === 'failure') reject(new Error('authorization service unavailable'));
    else resolve({namespaces: outcome === 'revoked' ? ['team-a'] : ['team-a', 'team-b']});
    await new Promise(setImmediate);
    assert.equal(state.namespace, outcome === 'revoked' ? 'team-a' : preferred);
    assert.equal(elements.activeNamespace.getAttribute('aria-busy'), 'false');
    if (outcome === 'failure') {
      assert.deepEqual(state.namespaces, [preferred]);
      assert.equal(elements.namespaceStatus.textContent, 'Unable to load namespaces: authorization service unavailable');
      assert.equal(elements.namespaceStatus.hidden, false);
    } else {
      assert.deepEqual(state.namespaces, outcome === 'revoked' ? ['team-a'] : ['team-a', 'team-b']);
    }
  }
}

async function testInaccessibleStartupNamespace() {
  reset();
  state.namespace = '';
  window.localStorage.setItem('kelos-console-namespace', 'deleted');
  global.loadIdentity = async () => ({defaultNamespace: 'team-a'});
  const requests = [];
  let resolve;
  global.api = async url => {
    requests.push(url);
    if (url === '/api/namespaces?namespace=deleted') return {namespaces: []};
    assert.equal(url, '/api/namespaces');
    return new Promise(done => { resolve = done; });
  };
  const loaded = startConsole();
  await new Promise(setImmediate);
  assert.deepEqual(loaded, []);
  assert.equal(state.namespace, '');
  assert.equal(elements.newSessionButton.disabled, true);
  resolve({namespaces: ['team-a', 'team-b']});
  await new Promise(setImmediate);
  assert.deepEqual(loaded, ['options', 'sessions', 'resources', 'overview']);
  assert.equal(state.namespace, 'team-a');
  assert.deepEqual(requests, ['/api/namespaces?namespace=deleted', '/api/namespaces']);
}
async function testStartupNamespaceFailure() {
  reset();
  state.namespace = '';
  window.localStorage.setItem('kelos-console-namespace', 'team-c');
  global.loadIdentity = async () => ({defaultNamespace: 'team-a'});
  global.api = async () => { throw new Error('authorization service unavailable'); };
  global.configReady = loadConfig();
  await configReady;
  assert.equal(state.namespace, '');
  assert.equal(elements.namespaceStatus.hidden, false);
  assert.match(elements.namespaceStatus.textContent, /authorization service unavailable/);
  assert.equal(elements.newSessionButton.disabled, true);
  assert.equal(elements.welcomeNew.disabled, true);
  assert.equal(elements.activeNamespace.disabled, false);
  assert.equal(elements.activeNamespace.children[0].textContent, 'Open to load namespaces');
  assert.equal(window.localStorage.getItem('kelos-console-namespace'), null);
  global.api = async () => ({namespaces: ['team-a']});
  bindNamespacePicker();
  elements.activeNamespace.listeners.get('pointerdown')({});
  await new Promise(setImmediate);
  assert.equal(state.namespace, 'team-a');
  assert.equal(elements.newSessionButton.disabled, false);
  assert.equal(elements.welcomeNew.disabled, false);
  await openDialog();
  assert.equal(elements.dialog.open, true);
}

function bindNamespacePicker() {
  extract('elements.namespaceForm.addEventListener(', 'elements.sessionSource.addEventListener(');
}

async function testNamespacePickerInteractions() {
  for (const [name, event] of [
    ['focus', {}], ['pointerdown', {pointerType: 'mouse'}], ['pointerdown', {pointerType: 'touch'}],
    ['keydown', {key: ' '}], ['keydown', {key: 'Enter'}], ['keydown', {key: 'F4'}],
    ['keydown', {key: 'ArrowDown', altKey: true}], ['keydown', {key: 'ArrowUp', altKey: true}],
  ]) {
    reset();
    bindNamespacePicker();
    state.namespaces = ['team-a'];
    let calls = 0;
    global.api = async url => {
      assert.equal(url, '/api/namespaces');
      calls++;
      return {namespaces: ['team-a', 'team-b']};
    };
    elements.activeNamespace.listeners.get(name)(event);
    await new Promise(setImmediate);
    assert.equal(calls, 1);
    assert.deepEqual(state.namespaces, ['team-a', 'team-b']);
    assert.equal(state.namespace, 'team-a');
    assert.equal(elements.activeNamespace.value, 'team-a');
    assert.equal(elements.namespaceStatus.textContent, '');
    const options = [...elements.activeNamespace.options];
    elements.activeNamespace.listeners.get(name)(event);
    await new Promise(setImmediate);
    assert.equal(calls, 2);
    assert.equal(elements.activeNamespace.value, 'team-a');
    assert.equal(elements.activeNamespace.options.length, options.length);
    for (const [index, option] of options.entries()) assert.equal(elements.activeNamespace.options[index], option);
    elements.activeNamespace.listeners.get('keydown')({key: 'Escape'});
    elements.activeNamespace.listeners.get('keydown')({key: 'ArrowDown'});
    await new Promise(setImmediate);
    assert.equal(calls, 2);
  }
}

async function testNamespacePickerCoalescesRequests() {
  for (const fail of [false, true]) {
    reset();
    bindNamespacePicker();
    let calls = 0, resolve, reject;
    global.api = url => {
      assert.equal(url, '/api/namespaces');
      calls++;
      return new Promise((a, b) => { resolve = a; reject = b; });
    };
    const background = refreshNamespaces();
    elements.activeNamespace.listeners.get('pointerdown')({});
    elements.activeNamespace.listeners.get('focus')({});
    elements.activeNamespace.listeners.get('keydown')({key: ' '});
    assert.equal(calls, 1);
    assert.equal(elements.activeNamespace.disabled, false);
    if (fail) {
      reject(new Error('temporarily unavailable'));
      await assert.rejects(background, /temporarily unavailable/);
    } else {
      resolve({namespaces: ['team-a']});
      await background;
    }
    elements.activeNamespace.listeners.get('pointerdown')({});
    assert.equal(calls, 2);
    resolve({namespaces: ['team-a', 'team-b']});
    await new Promise(setImmediate);
    assert.deepEqual(state.namespaces, ['team-a', 'team-b']);
    assert.equal(state.namespace, 'team-a');
  }
}

async function testNamespacePickerAfterAccessGranted() {
  reset();
  state.namespace = '';
  elements.activeNamespace.disabled = true;
  global.loadIdentity = async () => ({defaultNamespace: 'team-a'});
  global.api = async () => ({namespaces: []});
  await loadConfig();
  assert.equal(elements.activeNamespace.disabled, false);
  assert.equal(elements.newSessionButton.disabled, true);
  const placeholder = elements.activeNamespace.options[0];
  assert.equal(placeholder.textContent, 'No accessible namespaces');
  bindNamespacePicker();
  elements.activeNamespace.listeners.get('focus')({});
  await new Promise(setImmediate);
  assert.equal(elements.activeNamespace.options.length, 1);
  assert.equal(elements.activeNamespace.options[0], placeholder);
  global.api = async () => ({namespaces: ['team-a']});
  elements.activeNamespace.listeners.get('focus')({});
  await new Promise(setImmediate);
  assert.equal(state.namespace, 'team-a');
  assert.equal(elements.newSessionButton.disabled, false);
  assert.equal(elements.namespaceStatus.hidden, true);
}
async function testRoleChangeConflict() {
  reset(); state.memberInventory = inventory;
  openMemberDialog(member); elements.memberRole.value = 'user';
  const refreshed = {...inventory, members: [{...member, version: '4'}]};
  const requests = [];
  global.api = async (url, options) => {
    requests.push({url, options});
    if (options) throw new Error('membership changed');
    return refreshed;
  };
  await saveMember();
  assert.deepEqual(requests.map(request => request.url), ['/api/admin/members/team-a/Ym9i?version=3', '/api/admin/members?namespace=team-a']);
  assert.equal(adminRefreshes, 1);
  assert.equal(state.memberInventory.members[0].version, '4');
  assert.match(elements.memberDialogError.textContent, /membership changed.*Close this dialog/);
  assert.equal(elements.memberDialogError.hidden, false);
  assert.equal(elements.memberSave.disabled, true);
  await saveMember();
  assert.equal(requests.length, 2);
  openMemberDialog(state.memberInventory.members[0]); elements.memberRole.value = 'user';
  global.api = async (url, options) => { requests.push({url, options}); return refreshed; };
  await saveMember();
  assert.equal(requests[2].url, '/api/admin/members/team-a/Ym9i?version=4');
}
async function testRemovalAlertRefresh() {
  reset(); state.memberInventory = inventory;
  global.api = async (_url, options) => {
    if (options) throw new Error('membership changed');
    return inventory;
  };
  await removeMember(member, 'team-a', element());
  assert.equal(elements.memberError.hidden, false);
  assert.equal(elements.memberError.textContent, 'membership changed');
  await loadAdmin();
  assert.equal(elements.memberError.hidden, true);
}
async function testStaleNamespaceDiscovery() {
  for (const fail of [false, true]) {
    reset();
    state.namespaces = ['team-a', 'team-b'];
    elements.activeNamespace.value = 'team-a';
    elements.activeNamespace.replaceChildren('original options');
    elements.namespaceStatus.textContent = 'current status';
    elements.namespaceStatus.hidden = true;
    let resolve, reject, switches = 0;
    global.api = () => new Promise((a, b) => { resolve = a; reject = b; });
    global.switchNamespace = async () => { switches++; };
    const refresh = refreshNamespaces();
    state.namespace = 'team-b'; state.namespaceGeneration++;
    elements.activeNamespace.value = 'team-b';
    if (fail) reject(new Error('stale failure'));
    else resolve({namespaces: ['team-a']});
    await refresh;
    assert.equal(switches, 0);
    assert.equal(state.namespace, 'team-b');
    assert.equal(elements.activeNamespace.value, 'team-b');
    assert.deepEqual(elements.activeNamespace.children, ['original options']);
    assert.deepEqual(state.namespaces, ['team-a', 'team-b']);
    assert.equal(elements.namespaceStatus.textContent, 'current status');
    assert.equal(elements.namespaceStatus.hidden, true);
  }
}
(async () => {
  await testMembers(); await testAddAndChange(); await testRemoveAndSelfDemotion(); await testStaleNamespace(); await testNamespacePicker(); await testBackgroundNamespaceDiscovery(); await testInaccessibleStartupNamespace(); await testStartupNamespaceFailure(); await testNamespacePickerInteractions(); await testNamespacePickerCoalescesRequests(); await testNamespacePickerAfterAccessGranted(); await testRoleChangeConflict(); await testRemovalAlertRefresh(); await testStaleNamespaceDiscovery();
  process.stdout.write('Member and namespace tests passed\n');
})().catch(error => { console.error(error); process.exitCode = 1; });
