const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

global.renderSessionAttentionCount = () => false;
global.scheduleCompletionRead = () => false;
global.updateConsoleLocation = () => {};

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');

function applicationSlice(start, end) {
  const startIndex = application.indexOf(start);
  const endIndex = application.indexOf(end, startIndex);
  assert.notEqual(startIndex, -1, `${start} not found`);
  assert.notEqual(endIndex, -1, `${end} not found`);
  return application.slice(startIndex, endIndex);
}

vm.runInThisContext(applicationSlice('function errorMessage', 'function requireElements'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function resourceLocation', 'function updateConsoleLocation'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('const allResourceKind', 'async function loadResources'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('async function loadResources', 'async function refreshConsole'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('async function refreshConsole', 'async function openResourceDetail'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function resourceDetailTabs', 'const pendingCompletionReads'), {filename: 'app.js'});

vm.runInThisContext(applicationSlice('function addOption', 'function credentialTypeLabel'), {filename: 'app.js'});

vm.runInThisContext(applicationSlice('function renderSessions(', 'function createSessionListItem('), {filename: 'app.js'});

function testViewsWithoutNamespace() {
  const element = () => ({children: [], textContent: '', replaceChildren(...items) { this.children = items; }, append(...items) { this.children.push(...items); }});
  global.document = {createElement: element};
  global.state = {namespace: '', sessions: [], resourceGroups: []};
  global.elements = {summaryGrid: element(), recentResources: element(), list: element()};
  global.syncSessionActionsMenu = () => {};
  try {
    assert.equal(createResourceTable([]).textContent, 'Select a namespace to view resources.');
    renderSessions();
    assert.equal(elements.list.children[0].textContent, 'Select a namespace to view Sessions.');
  } finally {
    delete global.document;
    delete global.syncSessionActionsMenu;
    delete global.state;
    delete global.elements;
  }
}

function testReturningToSessionsRefreshesCurrentRequest() {
  let updates = 0;
  let resizes = 0;
  const navigationButton = () => ({setAttribute() {}, removeAttribute() {}});
  global.state = {consoleView: 'overview', selected: {}, sessions: []};
  global.elements = {
    overviewView: {hidden: false},
    sessionsView: {hidden: true},
    resourcesView: {hidden: true},
    resourceDetailPanel: {hidden: true},
    adminView: {hidden: true},
    sessionSidebar: {hidden: true},
    overviewButton: navigationButton(),
    sessionsButton: navigationButton(),
    resourcesButton: navigationButton(),
    adminButton: navigationButton(),
  };
  global.updateCurrentRequest = () => { updates++; };
  global.resizeComposer = () => {
    assert.equal(elements.sessionsView.hidden, false);
    resizes++;
  };
  global.updateJumpToLatest = () => {};
  global.setSidebarOpen = () => {};

  setConsoleView('sessions');

  assert.equal(elements.sessionsView.hidden, false);
  assert.equal(updates, 1);
  assert.equal(resizes, 1);
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

function testResourceTableLinks() {
  const element = tag => ({
    tag, dataset: {}, children: [], listeners: {},
    append(...children) { this.children.push(...children); },
    setAttribute() {},
    addEventListener(event, listener) { this.listeners[event] = listener; },
  });
  global.document = {createElement: element};
  global.state = {namespace: 'team-a'};
  const collection = {resource: 'tasks', kind: 'Task', label: 'Tasks'};
  const item = {namespace: 'team-a', name: 'build'};
  const table = createResourceTable([{collection, item}]);
  const link = table.children[1].children[0].children[0].children[0];
  assert.equal(link.tag, 'a');
  assert.equal(link.href, '/namespaces/team-a/resources/tasks/build');
  assert.equal(link.textContent, 'build');
  let prevented = false;
  link.listeners.click({button: 0, ctrlKey: true, preventDefault() { prevented = true; }});
  assert.equal(prevented, false, 'Modified clicks must keep native link behavior');
  const openDetail = global.openResourceDetail;
  let opened;
  global.openResourceDetail = (collection, item) => { opened = {collection, item}; };
  try {
    link.listeners.click({button: 0, preventDefault() { prevented = true; }});
    assert.equal(prevented, true);
    assert.deepEqual(opened, {collection, item});
  } finally {
    global.openResourceDetail = openDetail;
    delete global.document;
  }
}

function testResourceLocations() {
  assert.deepEqual(resourceLocation('/namespaces/team-a/resources'), {namespace: 'team-a', resource: undefined, name: undefined});
  assert.deepEqual(resourceLocation('/namespaces/team-a/resources/tasks/build'), {namespace: 'team-a', resource: 'tasks', name: 'build'});
  assert.deepEqual(resourceLocation('/namespaces/team-a/resources/tasks/build/'), {namespace: 'team-a', resource: 'tasks', name: 'build'});
  assert.equal(resourceLocation('/namespaces/team-a/resources/tasks'), null);
  assert.equal(resourceLocation('/namespaces/team-a/resources/tasks/build/extra'), null);
  assert.deepEqual(resourceLocation('/namespaces/team-a/resources/sessions/worker'), {namespace: 'team-a', resource: 'sessions', name: 'worker'});
  assert.equal(resourceLocation('/namespaces/team-a/sessions/worker'), null);
  assert.deepEqual(sessionLocation('/namespaces/team-a/sessions/worker'), {namespace: 'team-a', name: 'worker'});
  assert.deepEqual(sessionLocation('/namespaces/team-a/sessions/worker/'), {namespace: 'team-a', name: 'worker'});
  assert.deepEqual(sessionLocation('/namespaces/team-a/sessions'), {namespace: 'team-a', name: undefined});
  assert.equal(sessionLocation('/namespaces/team-a/resources/sessions/worker'), null);
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
    key: 'ArrowRight',
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

function resourceElement(tagName = 'div') {
  return {
    tagName, children: [], dataset: {}, attributes: {}, handlers: {}, value: '', textContent: '', hidden: false,
    append(...items) { this.children.push(...items); },
    replaceChildren(...items) { this.children = items; },
    setAttribute(name, value) { this.attributes[name] = value; },
    removeAttribute(name) { delete this.attributes[name]; },
    addEventListener(name, handler) { this.handlers[name] = handler; },
    focus() { document.activeElement = this; },
    click() { return this.handlers.click?.({button: 0, preventDefault() {}}); },
    contains(element) { return this === element || this.querySelectorAll('*').includes(element); },
    querySelectorAll(selector) {
      const descendants = this.children.flatMap(child => [child, ...child.querySelectorAll('*')]);
      if (selector === '*') return descendants;
      if (selector === 'a[data-resource-key]') return descendants.filter(child => child.tagName === 'a' && child.dataset.resourceKey);
      if (selector === 'th button') return descendants.filter(child => child.tagName === 'th').flatMap(child => child.children);
      return descendants.filter(child => child.tagName === selector);
    },
  };
}

function setupResourceInspector() {
  global.document = {createElement: resourceElement};
  global.formatSessionRecency = () => '5m ago';
  global.state = {
    consoleView: 'resources', namespace: 'default', namespaceGeneration: 0, resourceListGeneration: 0, resourceGroups: [], resourceRelationships: [],
    resourceDetail: null, resourceDetailGeneration: 0, resourceDetailLogGeneration: 0,
  };
  global.elements = Object.fromEntries([
    'overviewView', 'sessionsView', 'resourcesView', 'adminView', 'sessionSidebar',
    'overviewButton', 'sessionsButton', 'resourcesButton', 'adminButton', 'backToResources',
    'resourceKind', 'resourceTypeList', 'resourceListTitle', 'resourceListCount', 'resourceListSummary',
    'resourceSearch', 'resourceStatus', 'resourceSort', 'resourceList',
    'resourceInventoryPanel', 'resourceDiagramPanel', 'resourceInventoryTab', 'resourceRelationshipFocus',
    'resourceDetailTitle', 'resourceDetailSubtitle', 'resourceDetailTabs', 'resourceDetailSummaryTab',
    'resourceDetailRelationshipsTab', 'resourceDetailLogsTab', 'resourceDetailManifestTab',
    'resourceDetailSummaryPanel', 'resourceDetailRelationshipsPanel', 'resourceDetailLogsPanel',
    'resourceDetailManifestPanel', 'resourceDetailSummary', 'resourceDetailMessage', 'resourceDetailRelationships',
    'refreshResourceLogs', 'resourceDetailLogs', 'resourceDetailYAML', 'resourceDetailPanel',
    'taskSpawnerRunForm', 'taskSpawnerRunValues', 'taskSpawnerRunError', 'taskSpawnerRunButton',
    'taskSpawnerLifecycle', 'taskSpawnerLifecycleButton', 'taskSpawnerLifecycleStatus', 'taskSpawnerLifecycleError',
  ].map(name => [name, resourceElement()]));
  elements.resourceDetailTabs.append(elements.resourceDetailSummaryTab, elements.resourceDetailRelationshipsTab, elements.resourceDetailLogsTab, elements.resourceDetailManifestTab);
  elements.resourceDetailPanel.append(elements.resourceDetailTitle, elements.resourceDetailTabs, elements.resourceDetailRelationships, elements.refreshResourceLogs);
  elements.resourceSort.value = 'created-desc';
  elements.resourceDiagramPanel.hidden = true;
}

function testResourceInventoryFilteringSortingAndColumns() {
  setupResourceInspector();
  const tasks = {resource: 'tasks', kind: 'Task', label: 'Tasks', items: [
    {namespace: 'default', name: 'zeta', phase: 'Running', createdAt: '2026-11-01T00:00:00Z'},
    {namespace: 'default', name: 'alpha', phase: 'Failed', createdAt: '2026-11-02T00:00:00Z'},
  ]};
  state.resourceGroups = [{name: 'Workloads', resources: [tasks,
    {resource: 'workspaces', kind: 'Workspace', label: 'Workspaces', items: [{namespace: 'default', name: 'repo'}]},
  ]}];
  elements.resourceKind.value = allResourceKind;
  renderResources();
  let table = elements.resourceList.children[0];
  assert.deepEqual(table.querySelectorAll('th').map(cell => cell.textContent || cell.children[0].textContent), ['Name', 'Kind', 'Status', 'Created ↓']);
  assert.equal(table.querySelectorAll('a')[0].textContent, 'alpha');
  elements.resourceStatus.value = 'Running';
  elements.resourceSearch.value = 'zeta';
  renderResources();
  assert.equal(elements.resourceList.querySelectorAll('tbody')[0].children.length, 1);
  assert.equal(elements.resourceList.querySelectorAll('a')[0].textContent, 'zeta');
  assert.equal(elements.resourceListSummary.textContent, '1 of 3 resources match your filters.');
  elements.resourceSearch.value = 'absent';
  renderResources();
  assert.equal(elements.resourceList.children[0].textContent, 'No resources match your filters. Try another search or status.');
  elements.resourceStatus.value = '';
  elements.resourceSearch.value = '';
  selectResourceKind('tasks');
  assert.equal(elements.resourceList.querySelectorAll('th').length, 3);
  elements.resourceList.querySelectorAll('th button')[0].click();
  assert.equal(elements.resourceSort.value, 'name-asc');
  assert.equal(elements.resourceList.querySelectorAll('th')[0].attributes['aria-sort'], 'ascending');
  assert.equal(elements.resourceList.querySelectorAll('tbody')[0].children[0].children[0].children[0].textContent, 'alpha');
  elements.resourceList.querySelectorAll('th button')[0].click();
  assert.equal(elements.resourceSort.value, 'name-desc');
  assert.equal(elements.resourceList.querySelectorAll('tbody')[0].children[0].children[0].children[0].textContent, 'zeta');
  elements.resourceList.querySelectorAll('th button')[1].click();
  assert.equal(elements.resourceSort.value, 'status-asc');
  assert.equal(elements.resourceList.querySelectorAll('th')[1].attributes['aria-sort'], 'ascending');
  assert.equal(elements.resourceList.querySelectorAll('tbody')[0].children[0].children[0].children[0].textContent, 'alpha');
  elements.resourceList.querySelectorAll('th button')[1].click();
  assert.equal(elements.resourceSort.value, 'status-desc');
  assert.equal(elements.resourceList.querySelectorAll('tbody')[0].children[0].children[0].children[0].textContent, 'zeta');
  elements.resourceList.querySelectorAll('th button')[2].click();
  assert.equal(elements.resourceSort.value, 'created-asc');
  assert.equal(elements.resourceList.querySelectorAll('th')[2].attributes['aria-sort'], 'ascending');
  assert.equal(elements.resourceList.querySelectorAll('tbody')[0].children[0].children[0].children[0].textContent, 'zeta');
  elements.resourceList.querySelectorAll('th button')[2].click();
  assert.equal(elements.resourceSort.value, 'created-desc');
  assert.equal(elements.resourceList.querySelectorAll('tbody')[0].children[0].children[0].children[0].textContent, 'alpha');
  assert.deepEqual(sortResourceEntries(resourceEntries([tasks], 'tasks'), 'created-asc').map(entry => entry.item.name), ['zeta', 'alpha']);
  assert.deepEqual(sortResourceEntries(resourceEntries([tasks], 'tasks'), 'status-desc').map(entry => entry.item.name), ['zeta', 'alpha']);
  elements.resourceStatus.value = 'Failed';
  selectResourceKind('workspaces');
  assert.equal(elements.resourceStatus.value, '');
}

async function testResourceDetailsIgnoreStaleResponses() {
  setupResourceInspector();
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
  assert.equal(elements.resourceDetailSummaryPanel.hidden, false);
  assert.equal(elements.resourceDetailLogsTab.hidden, false);
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

async function testTaskSpawnerRun() {
  setupResourceInspector();
  let postRequests = 0;
  let refreshed = 0;
  let toast = '';
  let creation = deferred();
  global.loadResources = async () => { refreshed++; };
  global.showToast = message => { toast = message; };
  global.apiText = async () => 'No logs yet.';
  global.api = (requestPath, options) => {
    if (!options) return Promise.resolve({yaml: 'kind: TaskSpawner'});
    postRequests++;
    assert.equal(requestPath, '/api/resources/taskspawners/team-a/daily/run');
    assert.equal(options.method, 'POST');
    assert.deepEqual(JSON.parse(options.body), {values: 'Title: Fix login'});
    return creation.promise;
  };

  await openResourceDetail({resource: 'taskspawners', kind: 'TaskSpawner'}, {namespace: 'team-a', name: 'daily'});
  assert.equal(elements.taskSpawnerRunForm.hidden, false);
  assert.equal(elements.taskSpawnerRunButton.disabled, false);
  assert.equal(elements.resourceDetailLogsTab.hidden, true);
  elements.taskSpawnerRunValues.value = 'Title: Fix login';

  let prevented = false;
  const event = {preventDefault() { prevented = true; }};
  const failedRun = runTaskSpawner(event);
  await runTaskSpawner(event);
  assert.equal(prevented, true);
  assert.equal(postRequests, 1);
  assert.equal(elements.taskSpawnerRunButton.disabled, true);
  assert.equal(elements.taskSpawnerRunButton.textContent, 'Creating task…');
  creation.reject(new Error('Task creation denied'));
  await failedRun;
  assert.equal(elements.taskSpawnerRunButton.disabled, false);
  assert.equal(elements.taskSpawnerRunError.textContent, 'Task creation denied');
  assert.equal(elements.taskSpawnerRunValues.value, 'Title: Fix login');

  creation = deferred();
  const successfulRun = runTaskSpawner(event);
  assert.equal(elements.taskSpawnerRunError.textContent, '');
  creation.resolve({namespace: 'team-a', name: 'daily-manual-abcde'});
  await successfulRun;
  assert.equal(postRequests, 2);
  assert.equal(refreshed, 1);
  assert.equal(toast, 'Created Task daily-manual-abcde');
  assert.equal(elements.resourceDetailTitle.textContent, 'Task daily-manual-abcde');
  assert.equal(elements.taskSpawnerRunForm.hidden, true);
  assert.equal(elements.resourceDetailSummaryPanel.hidden, false);
  assert.equal(elements.resourceDetailLogsTab.hidden, false);
}

async function testTaskSpawnerRunIgnoresStaleResponses() {
  const creation = deferred();
  global.api = (_path, options) => options ? creation.promise : Promise.resolve({yaml: 'kind: Workspace'});
  await openResourceDetail({resource: 'taskspawners', kind: 'TaskSpawner'}, {namespace: 'team-a', name: 'daily'});
  const run = runTaskSpawner({preventDefault() {}});
  await openResourceDetail({resource: 'workspaces', kind: 'Workspace'}, {namespace: 'team-a', name: 'repository'});
  creation.resolve({namespace: 'team-a', name: 'daily-manual-abcde'});
  await run;
  assert.equal(elements.resourceDetailTitle.textContent, 'Workspace repository');
  assert.equal(elements.taskSpawnerRunForm.hidden, true);
  assert.equal(elements.taskSpawnerRunError.textContent, '');
}

async function testTaskSpawnerSuspension() {
  setupResourceInspector();
  let mutation = deferred();
  let postRequests = 0;
  let refreshed = 0;
  const toasts = [];
  global.loadResources = async () => { refreshed++; };
  global.showToast = message => { toasts.push(message); };
  global.api = (requestPath, options) => {
    if (!options) return Promise.resolve({yaml: 'suspend: false', suspended: false});
    postRequests++;
    assert.equal(options.method, 'POST');
    assert.equal(requestPath, `/api/resources/taskspawners/team-a/daily/${postRequests === 3 ? 'resume' : 'suspend'}`);
    return mutation.promise;
  };
  await openResourceDetail({resource: 'taskspawners', kind: 'TaskSpawner'}, {namespace: 'team-a', name: 'daily', phase: 'Suspended'});
  assert.equal(elements.taskSpawnerLifecycle.hidden, false);
  assert.equal(elements.taskSpawnerLifecycleButton.textContent, 'Suspend');
  assert.equal(elements.taskSpawnerLifecycleButton.disabled, false);
  elements.taskSpawnerRunValues.value = 'Title: Operator request';

  const failed = toggleTaskSpawnerSuspension();
  await toggleTaskSpawnerSuspension();
  assert.equal(postRequests, 1);
  assert.equal(elements.taskSpawnerLifecycleButton.disabled, true);
  assert.equal(elements.taskSpawnerLifecycleButton.textContent, 'Suspending…');
  mutation.reject(new Error('Permission denied'));
  await failed;
  assert.equal(elements.taskSpawnerLifecycleError.textContent, 'Permission denied');
  assert.equal(elements.taskSpawnerLifecycleButton.disabled, false);
  assert.equal(elements.taskSpawnerLifecycleButton.textContent, 'Suspend');

  mutation = deferred();
  const suspended = toggleTaskSpawnerSuspension();
  assert.equal(elements.taskSpawnerLifecycleError.textContent, '');
  mutation.resolve({yaml: 'suspend: true', suspended: true});
  await suspended;
  assert.equal(elements.resourceDetailYAML.textContent, 'suspend: true');
  assert.equal(elements.taskSpawnerLifecycleButton.textContent, 'Resume');
  assert.equal(elements.taskSpawnerLifecycleStatus.textContent, 'Automatic task creation is suspended.');
  assert.equal(elements.taskSpawnerRunValues.value, 'Title: Operator request');
  assert.equal(elements.taskSpawnerRunButton.disabled, false);

  mutation = deferred();
  const resumed = toggleTaskSpawnerSuspension();
  assert.equal(elements.taskSpawnerLifecycleButton.textContent, 'Resuming…');
  mutation.resolve({yaml: 'suspend: false', suspended: false});
  await resumed;
  assert.equal(elements.resourceDetailYAML.textContent, 'suspend: false');
  assert.equal(elements.taskSpawnerLifecycleButton.textContent, 'Suspend');
  assert.equal(elements.taskSpawnerLifecycleStatus.textContent, 'Automatic task creation is enabled.');
  assert.equal(refreshed, 2);
  assert.deepEqual(toasts, ['TaskSpawner daily suspend requested', 'TaskSpawner daily resume requested']);
}

async function testTaskSpawnerSuspensionIgnoresStaleResponses() {
  for (const fail of [false, true]) {
    const mutation = deferred();
    global.api = (requestPath, options) => options ? mutation.promise : Promise.resolve(requestPath.includes('/workspaces/')
      ? {yaml: 'kind: Workspace'} : {yaml: 'suspend: true', suspended: true});
    await openResourceDetail({resource: 'taskspawners', kind: 'TaskSpawner'}, {namespace: 'team-a', name: 'daily', phase: 'Running'});
    assert.equal(elements.taskSpawnerLifecycleButton.textContent, 'Resume');
    const pending = toggleTaskSpawnerSuspension();
    await openResourceDetail({resource: 'workspaces', kind: 'Workspace'}, {namespace: 'team-a', name: 'repository'});
    if (fail) mutation.reject(new Error('Request failed'));
    else mutation.resolve({yaml: 'suspend: false', suspended: false});
    await pending;
    assert.equal(elements.taskSpawnerLifecycle.hidden, true);
    assert.equal(elements.taskSpawnerLifecycleError.textContent, '');
    assert.equal(elements.taskSpawnerLifecycleButton.disabled, true);
    assert.equal(elements.resourceDetailTitle.textContent, 'Workspace repository');
    assert.equal(elements.resourceDetailYAML.textContent, 'kind: Workspace');
  }
}

async function testTaskSpawnerSuspensionWaitsForManifest() {
  const manifest = deferred();
  let requests = 0;
  global.api = () => { requests++; return manifest.promise; };
  const opening = openResourceDetail({resource: 'taskspawners', kind: 'TaskSpawner'}, {namespace: 'team-a', name: 'daily'});
  assert.equal(elements.taskSpawnerLifecycleButton.disabled, true);
  await toggleTaskSpawnerSuspension();
  assert.equal(requests, 1);
  manifest.reject(new Error('Cannot read TaskSpawner'));
  await opening;
  assert.equal(elements.taskSpawnerLifecycleButton.disabled, true);
  assert.equal(elements.resourceDetailYAML.textContent, 'Cannot read TaskSpawner');
  assert.equal(elements.taskSpawnerLifecycleStatus.textContent, 'Suspension state unavailable.');
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
  const logsTab = {hidden: false};
  const manifestTab = {
    click() { clicked = true; },
    focus() { focused = true; },
  };
  global.elements = {
    resourceDetailSummaryTab: {hidden: false},
    resourceDetailRelationshipsTab: {hidden: false},
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
  clicked = false;
  logsTab.hidden = true;
  handleResourceDetailTabKeydown({target: elements.resourceDetailRelationshipsTab, key: 'ArrowRight', preventDefault() {}});
  assert.equal(clicked, true);
}

async function testResourceInspectorSummaryRelationshipsAndClose() {
  setupResourceInspector();
  const task = {namespace: 'default', name: 'fix-login', phase: 'Failed', message: 'Command exited with status 1'};
  const workspace = {namespace: 'default', name: 'repository'};
  const tasks = {resource: 'tasks', kind: 'Task', label: 'Tasks', items: [task]};
  state.resourceGroups = [{name: 'Resources', resources: [tasks,
    {resource: 'workspaces', kind: 'Workspace', label: 'Workspaces', items: [workspace]},
  ]}];
  state.resourceRelationships = [{source: {resource: 'tasks', name: task.name}, target: {resource: 'workspaces', name: workspace.name}, relationship: 'uses'}];
  const pending = deferred();
  global.api = request => request.includes('/tasks/') ? pending.promise : Promise.resolve({yaml: 'kind: Workspace'});
  global.apiText = async () => 'Task logs';
  const opening = openResourceDetail(tasks, task);
  assert.equal(elements.resourceDetailPanel.hidden, false);
  assert.equal(elements.resourceDetailMessage.textContent, task.message);
  assert.deepEqual(elements.resourceDetailSummary.children.map(child => child.textContent), [
    'Kind', 'Task', 'Namespace', 'default', 'Name', 'fix-login', 'Status', 'Failed', 'Created', '—',
  ]);
  assert.equal(elements.resourceList.querySelectorAll('tbody')[0].children[0].dataset.selected, 'true');
  setResourceDetailView('relationships');
  assert.equal(elements.resourceDetailRelationshipsPanel.hidden, false);
  const related = elements.resourceDetailRelationships.querySelectorAll('button')[0];
  assert.equal(related.textContent, 'Workspace/repository · uses');
  await related.click();
  assert.equal(elements.resourceDetailTitle.textContent, 'Workspace repository');
  assert.equal(elements.resourceDetailLogsTab.hidden, true);
  assert.equal(elements.resourceDetailSummaryPanel.hidden, false);
  closeResourceDetail();
  assert.equal(elements.resourceDetailPanel.hidden, true);
  assert.equal(state.resourceDetail, null);
  assert.equal(state.resourceDetailTask, null);
  assert.equal(document.activeElement.dataset.resourceKey, 'workspaces/repository');
  pending.resolve({yaml: 'stale manifest'});
  await opening;
  assert.equal(elements.resourceDetailYAML.textContent, 'kind: Workspace');
}

async function testResourceInspectorRefresh() {
  setupResourceInspector();
  const task = {namespace: 'default', name: 'fix-login', phase: 'Running'};
  const tasks = {resource: 'tasks', kind: 'Task', label: 'Tasks', items: [task]};
  const workspace = {resource: 'workspaces', kind: 'Workspace', label: 'Workspaces', items: [{namespace: 'default', name: 'repository'}]};
  let inventory = {groups: [{name: 'Workloads', resources: [tasks, workspace]}]};
  const relationships = [{source: {resource: 'tasks', name: task.name}, target: {resource: 'workspaces', name: 'repository'}, relationship: 'uses'}];
  state.resourceRelationships = relationships;
  state.resourceGroups = inventory.groups;
  const manifest = deferred();
  const logs = deferred();
  global.api = request => request.startsWith('/api/resources?') ? Promise.resolve(inventory) : manifest.promise;
  global.apiText = () => logs.promise;
  const renderKinds = global.renderResourceKindOptions;
  const renderSummary = global.renderOverview;
  const toast = global.showToast;
  const messages = [];
  global.showToast = message => messages.push(message);
  global.renderResourceKindOptions = () => {};
  global.renderOverview = () => {};
  try {
    const opening = openResourceDetail(tasks, task);
    setResourceDetailView('relationships');
    const relatedButton = elements.resourceDetailRelationships.querySelectorAll('button')[0];
    relatedButton.focus();
    elements.resourceStatus.value = 'Running';
    inventory = {groups: [{name: 'Workloads', resources: [{...tasks, items: [{...task, phase: 'Failed', message: 'Command failed'}]}, workspace]}], relationships};
    await loadResources();
    assert.equal(elements.resourceDetailPanel.hidden, false);
    assert.equal(state.resourceDetail.item.phase, 'Failed');
    assert.equal(elements.resourceDetailMessage.textContent, 'Command failed');
    assert.equal(elements.resourceStatus.value, 'Running');
    assert.equal(elements.resourceList.children[0].textContent, 'No resources match your filters. Try another search or status.');
    assert.notEqual(document.activeElement, relatedButton);
    assert.equal(document.activeElement.dataset.relationshipKey, relatedButton.dataset.relationshipKey);
    assert.equal(elements.resourceDetailRelationships.contains(document.activeElement), true);
    const detailGeneration = state.resourceDetailGeneration;
    const logGeneration = state.resourceDetailLogGeneration;
    inventory = {groups: [{name: 'Workloads', resources: [{...tasks, items: []}]}]};
    await loadResources();
    assert.equal(elements.resourceDetailPanel.hidden, true);
    assert.equal(state.resourceDetail, null);
    assert.equal(state.resourceDetailTask, null);
    assert.equal(state.resourceDetailSpawner, null);
    assert.equal(state.resourceDetailGeneration, detailGeneration + 1);
    assert.equal(state.resourceDetailLogGeneration, logGeneration + 1);
    assert.equal(document.activeElement, elements.resourceInventoryTab);
    assert.deepEqual(messages, ['Task fix-login no longer exists']);
    manifest.resolve({yaml: 'stale manifest'});
    logs.resolve('stale logs');
    await opening;
    assert.equal(elements.resourceDetailYAML.textContent, 'Loading manifest…');
    assert.equal(elements.resourceDetailLogs.textContent, 'Loading logs…');
    state.resourceGroups = [{name: 'Workloads', resources: [tasks, workspace]}];
    await openResourceDetail(tasks, task);
    elements.resourceSearch.focus();
    await loadResources();
    assert.equal(elements.resourceDetailPanel.hidden, true);
    assert.equal(document.activeElement, elements.resourceSearch);
  } finally {
    global.renderResourceKindOptions = renderKinds;
    global.renderOverview = renderSummary;
    global.showToast = toast;
  }
}

testViewsWithoutNamespace();
testReturningToSessionsRefreshesCurrentRequest();
testResourceInventorySupportsAllTypesAndSearch();
testResourceTableLinks();
testResourceLocations();
testResourceInventoryFilteringSortingAndColumns();
testResourceRelationshipHelpersResolveExistingAndMissingResources();
testResourceViewTabsSupportKeyboardNavigation();
testResourceDetailsIgnoreStaleResponses()
  .then(() => testResourceInspectorRefresh())
  .then(() => testTaskSpawnerRun())
  .then(() => testTaskSpawnerRunIgnoresStaleResponses())
  .then(() => testTaskSpawnerSuspension())
  .then(() => testTaskSpawnerSuspensionIgnoresStaleResponses())
  .then(() => testTaskSpawnerSuspensionWaitsForManifest())
  .then(() => testRefreshReportsOptionFailures())
  .then(() => testResourceInspectorSummaryRelationshipsAndClose())
  .then(() => testResourceDetailTabsSupportKeyboardNavigation())
  .then(() => process.stdout.write('Console resources tests passed\n'));
