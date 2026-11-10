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
vm.runInThisContext(applicationSlice('async function refreshConsole', 'async function openResourceDetail'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function setResourceDetailView', 'const pendingCompletionReads'), {filename: 'app.js'});

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
    resourceDetailPage: {hidden: true},
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
    ...Object.fromEntries([
      'overviewView', 'sessionsView', 'resourcesView', 'adminView', 'sessionSidebar',
      'overviewButton', 'sessionsButton', 'resourcesButton', 'adminButton',
    ].map(key => [key, {setAttribute() {}, removeAttribute() {}}])),
    backToResources: {setAttribute() {}},
    resourceDetailPage: {hidden: true},
    taskSpawnerRunForm: {hidden: true},
    taskSpawnerRunValues: {value: ''},
    taskSpawnerRunError: {textContent: ''},
    taskSpawnerRunButton: {disabled: false, textContent: ''},
    taskSpawnerLifecycle: {hidden: true},
    taskSpawnerLifecycleButton: {disabled: false, textContent: ''},
    taskSpawnerLifecycleStatus: {textContent: ''},
    taskSpawnerLifecycleError: {textContent: ''},
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

async function testTaskSpawnerRun() {
  global.state = {resourceDetailGeneration: 0, resourceDetailLogGeneration: 0};
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
  assert.equal(elements.resourceDetailTabs.hidden, true);
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
  assert.equal(elements.resourceDetailLogsPanel.hidden, false);
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
  global.state = {resourceDetailGeneration: 0, resourceDetailLogGeneration: 0};
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

testViewsWithoutNamespace();
testReturningToSessionsRefreshesCurrentRequest();
testResourceInventorySupportsAllTypesAndSearch();
testResourceTableLinks();
testResourceLocations();
testResourceRelationshipHelpersResolveExistingAndMissingResources();
testResourceViewTabsSupportKeyboardNavigation();
testResourceDetailsIgnoreStaleResponses()
  .then(() => testTaskSpawnerRun())
  .then(() => testTaskSpawnerRunIgnoresStaleResponses())
  .then(() => testTaskSpawnerSuspension())
  .then(() => testTaskSpawnerSuspensionIgnoresStaleResponses())
  .then(() => testTaskSpawnerSuspensionWaitsForManifest())
  .then(() => testRefreshReportsOptionFailures())
  .then(() => testResourceDetailTabsSupportKeyboardNavigation())
  .then(() => process.stdout.write('Console resources tests passed\n'));
