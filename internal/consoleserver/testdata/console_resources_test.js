const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');

function applicationSlice(start, end) {
  const startIndex = application.indexOf(start);
  const endIndex = application.indexOf(end, startIndex);
  assert.notEqual(startIndex, -1, `${start} not found`);
  assert.notEqual(endIndex, -1, `${end} not found`);
  return application.slice(startIndex, endIndex);
}

vm.runInThisContext(applicationSlice('function errorMessage', 'function requireElements'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('const allResourceKind', 'async function loadResources'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('async function refreshConsole', 'async function openResourceDetail'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function setResourceDetailView', 'function sessionKey'), {filename: 'app.js'});

function testReturningToSessionsRefreshesCurrentRequest() {
  let updates = 0;
  const navigationButton = () => ({setAttribute() {}, removeAttribute() {}});
  global.state = {consoleView: 'overview', selected: {}, sessions: []};
  global.elements = {
    overviewView: {hidden: false},
    sessionsView: {hidden: true},
    resourcesView: {hidden: true},
    adminView: {hidden: true},
    sessionSidebar: {hidden: true},
    overviewButton: navigationButton(),
    sessionsButton: navigationButton(),
    resourcesButton: navigationButton(),
    adminButton: navigationButton(),
  };
  global.updateCurrentRequest = () => { updates++; };
  global.setSidebarOpen = () => {};

  setConsoleView('sessions');

  assert.equal(elements.sessionsView.hidden, false);
  assert.equal(updates, 1);
}

function testResourceInventorySupportsAllTypesAndSearch() {
  const collections = [
    {resource: 'tasks', kind: 'Task', label: 'Tasks', items: [
      {name: 'fix-console', phase: 'Running', message: 'Agent is working', createdAt: '2026-08-22T12:00:00Z'},
    ]},
    {resource: 'workspaces', kind: 'Workspace', label: 'Workspaces', items: [
      {name: 'kelos', createdAt: '2026-08-21T12:00:00Z'},
    ]},
  ];

  const allEntries = resourceEntries(collections, allResourceKind);
  assert.equal(allEntries.length, 2);
  assert.equal(allEntries[0].item.name, 'fix-console');
  assert.deepEqual(resourceEntries(collections, 'tasks'), [
    {collection: collections[0], item: collections[0].items[0]},
  ]);
  assert.deepEqual(filterResourceEntries(allEntries, 'WORKING'), [allEntries[0]]);
  assert.deepEqual(filterResourceEntries(allEntries, 'workspace'), [allEntries[1]]);
  assert.deepEqual(filterResourceEntries(allEntries, 'missing'), []);
}

function testResourceRelationshipHelpersResolveExistingAndMissingResources() {
  const task = {name: 'fix-console', namespace: 'default', phase: 'Running'};
  const collection = {resource: 'tasks', kind: 'Task', label: 'Tasks', items: [task]};
  global.state = {
    namespace: 'default',
    resourceGroups: [{name: 'Workloads', resources: [collection]}],
  };

  assert.equal(resourceReferenceKey({resource: 'tasks', name: 'fix-console'}), 'tasks/fix-console');
  assert.deepEqual(resourceEntryForReference({resource: 'tasks', kind: 'Task', name: 'fix-console'}), {collection: {...collection, group: 'Workloads'}, item: task});
  assert.deepEqual(resourceEntryForReference({resource: 'workspaces', kind: 'Workspace', name: 'missing'}), {
    collection: {resource: 'workspaces', kind: 'Workspace', label: 'Workspace'},
    item: {name: 'missing', namespace: 'default', phase: 'Missing', missing: true},
  });

  const creates = {
    source: {resource: 'taskspawners', name: 'issues'},
    target: {resource: 'tasks', name: 'fix-console'},
    relationship: 'creates',
  };
  const uses = {
    source: {resource: 'tasks', name: 'fix-console'},
    target: {resource: 'workspaces', name: 'repository'},
    relationship: 'uses',
  };
  assert.deepEqual(resourceRelationshipsForFocus([creates, uses], 'tasks/fix-console'), {
    incoming: [creates],
    outgoing: [uses],
  });
}

function testResourceViewTabsSupportKeyboardNavigation() {
  let focused = false;
  let prevented = false;
  const diagramTab = {
    setAttribute(name, value) { this[name] = value; },
    click() { setResourceView('diagram'); },
    focus() { focused = true; },
  };
  const inventoryTab = {
    setAttribute(name, value) { this[name] = value; },
    click() { setResourceView('inventory'); },
    focus() {},
  };
  global.elements = {
    resourceDiagramTab: diagramTab,
    resourceInventoryTab: inventoryTab,
    resourceDiagramPanel: {hidden: false},
    resourceInventoryPanel: {hidden: true},
  };

  setResourceView('inventory');
  assert.equal(elements.resourceDiagramPanel.hidden, true);
  assert.equal(elements.resourceInventoryPanel.hidden, false);
  assert.equal(inventoryTab['aria-selected'], 'true');

  handleResourceViewTabKeydown({
    target: inventoryTab,
    key: 'ArrowLeft',
    preventDefault() { prevented = true; },
  });
  assert.equal(elements.resourceDiagramPanel.hidden, false);
  assert.equal(focused, true);
  assert.equal(prevented, true);
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return {promise, resolve, reject};
}

async function testResourceDetailsIgnoreStaleResponses() {
  global.elements = {
    resourceDetailTitle: {textContent: ''},
    resourceDetailSubtitle: {textContent: ''},
    resourceDetailTabs: {hidden: false},
    resourceDetailLogsTab: {setAttribute() {}, tabIndex: 0},
    resourceDetailManifestTab: {setAttribute() {}, tabIndex: -1},
    resourceDetailLogsPanel: {hidden: true},
    resourceDetailManifestPanel: {hidden: false},
    refreshResourceLogs: {disabled: true},
    resourceDetailLogs: {textContent: ''},
    resourceDetailYAML: {textContent: ''},
    resourceDetailDialog: {showModal() {}},
  };
  global.state = {
    resourceDetailGeneration: 0,
    resourceDetailLogGeneration: 0,
    resourceDetailTask: null,
  };

  const manifestRequests = [];
  global.api = requestPath => {
    const request = deferred();
    manifestRequests.push({path: requestPath, ...request});
    return request.promise;
  };
  const logRequests = [];
  global.apiText = requestPath => {
    const request = deferred();
    logRequests.push({path: requestPath, ...request});
    return request.promise;
  };

  const first = openResourceDetail(
    {resource: 'tasks', kind: 'Task'},
    {namespace: 'default', name: 'first'},
  );
  const second = openResourceDetail(
    {resource: 'tasks', kind: 'Task'},
    {namespace: 'default', name: 'second'},
  );

  assert.equal(elements.resourceDetailTitle.textContent, 'Task second');
  assert.equal(elements.resourceDetailLogsPanel.hidden, false);
  assert.equal(logRequests[1].path, '/api/resources/tasks/default/second/logs');
  manifestRequests[1].resolve({yaml: 'name: second'});
  logRequests[1].resolve('second logs');
  await second;
  assert.equal(elements.resourceDetailYAML.textContent, 'name: second');
  assert.equal(elements.resourceDetailLogs.textContent, 'second logs');
  assert.equal(elements.refreshResourceLogs.disabled, false);

  manifestRequests[0].resolve({yaml: 'name: first'});
  logRequests[0].resolve('first logs');
  await first;
  assert.equal(elements.resourceDetailYAML.textContent, 'name: second');
  assert.equal(elements.resourceDetailLogs.textContent, 'second logs');
}

async function testRefreshReportsOptionFailures() {
  let message = '';
  global.loadSessions = async () => {};
  global.loadOptions = async () => { throw new Error('options unavailable'); };
  global.loadResources = async () => {};
  global.showToast = value => { message = value; };

  await refreshConsole();

  assert.equal(message, 'options unavailable');
}

function testResourceDetailTabsSupportKeyboardNavigation() {
  let clicked = false;
  let focused = false;
  let prevented = false;
  const logsTab = {};
  const manifestTab = {
    click() { clicked = true; },
    focus() { focused = true; },
  };
  global.elements = {
    resourceDetailLogsTab: logsTab,
    resourceDetailManifestTab: manifestTab,
  };

  handleResourceDetailTabKeydown({
    target: logsTab,
    key: 'ArrowRight',
    preventDefault() { prevented = true; },
  });

  assert.equal(clicked, true);
  assert.equal(focused, true);
  assert.equal(prevented, true);
}

testReturningToSessionsRefreshesCurrentRequest();
testResourceInventorySupportsAllTypesAndSearch();
testResourceRelationshipHelpersResolveExistingAndMissingResources();
testResourceViewTabsSupportKeyboardNavigation();
testResourceDetailsIgnoreStaleResponses()
  .then(() => testRefreshReportsOptionFailures())
  .then(() => testResourceDetailTabsSupportKeyboardNavigation())
  .then(() => process.stdout.write('Console resources tests passed\n'));
