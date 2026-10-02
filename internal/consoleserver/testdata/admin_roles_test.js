const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const app = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');
const start = app.indexOf('async function loadAdminRoles(');
const end = app.indexOf('function adminResourcePath(', start);
assert.ok(start > 0 && end > start);
vm.runInThisContext(app.slice(start, end));

function element() {
  return {
    hidden: false, disabled: false, value: '', textContent: '', children: [],
    append(...children) { this.children.push(...children); },
    replaceChildren(...children) { this.children = children; },
    addEventListener() {}, setAttribute() {},
  };
}

const assignment = {username: 'oidc:bob', role: 'admin', binding: 'binding', resourceVersion: '3', managed: true, canRemove: true};
const inventory = {enabled: true, usernamePrefix: 'oidc:', currentUser: 'oidc:alice', roles: [{name: 'user', label: 'User', description: 'Use Sessions', canAssign: true}, {name: 'admin', label: 'Admin', description: 'Manage users', canAssign: false}], assignments: [assignment]};

function reset() {
  global.state = {namespace: 'team-a', adminRoleGeneration: 0, adminRoleInventory: null, adminRoleSaving: false};
  global.elements = Object.fromEntries(['adminRoleStatus', 'adminRoleForm', 'adminRoleSubject', 'adminRoleChoice', 'adminRoleHelp', 'adminRoleError', 'adminRoleList', 'adminRoleAssign'].map(key => [key, element()]));
  global.document = {createElement: element};
  global.errorMessage = error => error.message;
  global.showToast = () => {};
}

async function testInventory() {
  reset();
  global.api = async url => {
    assert.equal(url, '/api/admin/roles?namespace=team-a');
    return {...inventory, assignments: [assignment, {...assignment, username: 'oidc:external', managed: false, canRemove: false}]};
  };
  await loadAdminRoles();
  assert.equal(elements.adminRoleForm.hidden, false);
  assert.deepEqual(elements.adminRoleChoice.children.map(option => option.value), ['user']);
  assert.equal(elements.adminRoleList.children[0].children[0].children[0].textContent, 'oidc:bob');
  assert.equal(elements.adminRoleList.children[0].children[1].children[1].textContent, 'Remove role');
  assert.equal(elements.adminRoleList.children[1].children[1].children.length, 1);
  assert.equal(elements.adminRoleList.children[1].children[0].children[1].textContent, 'Managed outside Console');
  assert.match(elements.adminRoleHelp.textContent, /prefix “oidc:”/);

  global.api = async () => ({enabled: false});
  await loadAdminRoles();
  assert.equal(elements.adminRoleForm.hidden, true);
  assert.equal(elements.adminRoleList.children.length, 0);
  assert.match(elements.adminRoleStatus.textContent, /require OIDC/);

  global.api = async () => { throw new Error('access denied'); };
  await loadAdminRoles();
  assert.equal(elements.adminRoleForm.hidden, true);
  assert.equal(elements.adminRoleStatus.textContent, 'Unable to load user roles: access denied');
}

async function testAssign() {
  reset();
  state.adminRoleInventory = inventory;
  elements.adminRoleSubject.value = ' bob ';
  elements.adminRoleChoice.value = 'user';
  const requests = [];
  global.api = async (url, options) => {
    requests.push({url, options});
    return options ? assignment : inventory;
  };
  await assignAdminRole();
  assert.deepEqual(requests[0], {url: '/api/admin/roles?namespace=team-a', options: {method: 'POST', body: JSON.stringify({subject: 'bob', role: 'user'})}});
  assert.equal(elements.adminRoleSubject.value, '');
  assert.equal(elements.adminRoleAssign.disabled, false);

  elements.adminRoleSubject.value = 'bob';
  global.api = async () => { throw new Error('Role assignment already exists'); };
  await assignAdminRole();
  assert.equal(elements.adminRoleSubject.value, 'bob');
  assert.equal(elements.adminRoleError.textContent, 'Role assignment already exists');
  assert.equal(elements.adminRoleError.hidden, false);
}

async function testRemove() {
  reset();
  state.adminRoleInventory = inventory;
  const requests = [];
  global.api = async (url, options) => { requests.push({url, options}); return inventory; };
  global.window = {confirm: () => false};
  await removeAdminRole(assignment, 'team-a', element());
  assert.equal(requests.length, 0);
  window.confirm = message => { assert.match(message, /Admin role from oidc:bob in team-a/); return true; };
  await removeAdminRole(assignment, 'team-a', element());
  assert.deepEqual(requests[0], {url: '/api/admin/roles/team-a/binding?resourceVersion=3', options: {method: 'DELETE'}});
}

async function testNamespaceChange() {
  reset();
  let resolve;
  global.api = () => new Promise(done => { resolve = done; });
  const request = loadAdminRoles();
  state.namespace = 'team-b';
  resolve(inventory);
  await request;
  assert.equal(elements.adminRoleList.children.length, 0);
  assert.equal(elements.adminRoleForm.hidden, true);
  assert.equal(state.adminRoleInventory, null);
}

(async () => {
  await testInventory();
  await testAssign();
  await testRemove();
  await testNamespaceChange();
  process.stdout.write('Admin role tests passed\n');
})().catch(error => { console.error(error); process.exitCode = 1; });
