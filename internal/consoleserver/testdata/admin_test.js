const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');
const start = application.indexOf('function adminResourcePath(');
const end = application.indexOf('async function loadResources(', start);
assert.ok(start > 0 && end > start);
vm.runInThisContext(application.slice(start, end), {filename: 'app.js'});

function element() {
  return {
    children: [], hidden: false, value: '', disabled: false, textContent: '',
    append(...children) { this.children.push(...children); },
    replaceChildren(...children) { this.children = children; },
    setAttribute() {}, addEventListener() {}, focus() {}, setSelectionRange() {},
    showModal() { this.open = true; }, close() { this.open = false; },
  };
}

function reset() {
  global.state = {namespace: 'team-a', adminGeneration: 0, adminEditorGeneration: 0, adminSaving: false, adminEditing: null};
  global.elements = Object.fromEntries(['adminStatus', 'adminCollections', 'adminEditor', 'adminEditorTitle', 'adminEditorDescription', 'adminEditorError', 'adminYAML', 'adminSave'].map(key => [key, element()]));
  global.document = {createElement: element};
  global.resourceDescriptions = {workspaces: 'Repository sources and workspace setup.'};
  global.resourceStatus = item => item.phase || 'Configured';
  global.errorMessage = error => error.message;
  global.showToast = () => {};
  global.loadResources = async () => {};
  global.loadOptions = async () => {};
}

function deferred() {
  let resolve, reject;
  const promise = new Promise((a, b) => { resolve = a; reject = b; });
  return {promise, resolve, reject};
}

const workspace = {resource: 'workspaces', kind: 'Workspace', label: 'Workspaces', canCreate: true, items: []};

async function testInventory() {
  reset();
  global.api = async url => {
    assert.equal(url, '/api/admin?namespace=team-a');
    return [{...workspace, items: [
      {name: 'editable', canGet: true, canUpdate: true, canDelete: true},
      {name: 'read-only', canGet: true, canUpdate: false, canDelete: false},
    ]}];
  };
  await loadAdmin();
  assert.equal(elements.adminStatus.hidden, true);
  const card = elements.adminCollections.children[0];
  assert.equal(card.children[0].children[1].textContent, 'Create');
  assert.deepEqual(card.children[1].children[1].children.map(child => child.textContent), ['Edit YAML', 'Delete']);
  assert.deepEqual(card.children[2].children[1].children.map(child => child.textContent), ['View YAML']);

  global.api = async () => [];
  await loadAdmin();
  assert.equal(elements.adminCollections.children.length, 0);
  assert.equal(elements.adminStatus.hidden, false);
  assert.match(elements.adminStatus.textContent, /do not have permission/);

  global.api = async () => { throw new Error('access service unavailable'); };
  await loadAdmin();
  assert.equal(elements.adminStatus.textContent, 'Unable to load configuration: access service unavailable');
}

async function testStaleInventory() {
  reset();
  const old = deferred();
  global.api = () => old.promise;
  const first = loadAdmin();
  state.namespace = 'team-b';
  global.api = async () => [];
  await loadAdmin();
  old.resolve([workspace]);
  await first;
  assert.equal(elements.adminCollections.children.length, 0);
  assert.match(elements.adminStatus.textContent, /do not have permission/);
}

async function testEditorAndSave() {
  reset();
  await openAdminEditor(workspace);
  assert.equal(elements.adminEditor.open, true);
  assert.match(elements.adminYAML.value, /namespace: "team-a"/);
  assert.equal(elements.adminSave.disabled, false);
  const yaml = elements.adminYAML.value;
  const calls = [];
  global.api = async (url, options) => {
    calls.push({url, options});
    return options ? {name: 'my-workspace'} : [];
  };
  await saveAdminResource();
  assert.deepEqual(calls[0], {url: '/api/admin/workspaces/team-a', options: {method: 'POST', headers: {'Content-Type': 'application/yaml'}, body: yaml}});
  assert.equal(elements.adminEditor.open, false);

  global.api = async () => ({yaml: 'editable manifest'});
  await openAdminEditor(workspace, {name: 'repo', canUpdate: true});
  elements.adminYAML.value = 'updated manifest';
  global.api = async (url, options) => {
    assert.equal(url, '/api/admin/workspaces/team-a/repo');
    assert.equal(options.method, 'PUT');
    assert.equal(options.body, 'updated manifest');
    throw new Error('Resource version conflict');
  };
  await saveAdminResource();
  assert.equal(elements.adminEditor.open, true);
  assert.equal(elements.adminEditorError.textContent, 'Resource version conflict');
  assert.equal(elements.adminYAML.value, 'updated manifest');
  assert.equal(elements.adminSave.disabled, false);

  global.api = async () => ({yaml: 'read-only manifest'});
  await openAdminEditor(workspace, {name: 'repo', canUpdate: false});
  assert.equal(elements.adminYAML.readOnly, true);
  assert.equal(elements.adminSave.hidden, true);
  assert.equal(state.adminEditing, null);
}

async function testStaleEditor() {
  reset();
  const old = deferred();
  global.api = () => old.promise;
  const first = openAdminEditor(workspace, {name: 'old', canUpdate: true});
  await openAdminEditor(workspace);
  old.resolve({yaml: 'stale manifest'});
  await first;
  assert.notEqual(elements.adminYAML.value, 'stale manifest');
  assert.equal(state.adminEditing.name, undefined);
}

async function testDelete() {
  reset();
  let calls = 0;
  global.window = {confirm: () => false};
  global.api = async () => { calls++; return []; };
  await deleteAdminResource(workspace, {name: 'repo'}, element());
  assert.equal(calls, 0);
  window.confirm = message => {
    assert.match(message, /Workspace "repo" in team-a/);
    return true;
  };
  const requests = [];
  global.api = async (url, options) => { requests.push({url, options}); return []; };
  const button = element();
  await deleteAdminResource(workspace, {name: 'repo'}, button);
  assert.deepEqual(requests[0], {url: '/api/admin/workspaces/team-a/repo', options: {method: 'DELETE'}});
  assert.equal(button.disabled, false);
}

(async () => {
  await testInventory();
  await testStaleInventory();
  await testEditorAndSave();
  await testStaleEditor();
  await testDelete();
  process.stdout.write('Admin tests passed\n');
})().catch(error => { console.error(error); process.exitCode = 1; });
