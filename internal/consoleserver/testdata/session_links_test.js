const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
function slice(start, end) {
  const from = source.indexOf(start);
  const to = source.indexOf(end, from);
  assert.ok(from >= 0 && to > from, start);
  return source.slice(from, to);
}

function element() {
  return {
    append() {}, replaceChildren() {}, setAttribute() {}, removeAttribute() {}, close() {},
    classList: {remove() {}},
  };
}

function harness(url = 'https://console.example/', sessions = []) {
  const entries = [new URL(url)];
  let position = 0;
  const toasts = [];
  const requests = [];
  const c = {
    URL, URLSearchParams,
    window: {
      location: entries[0],
      history: {
        pushState(_state, _title, value) {
          entries.splice(++position, entries.length, new URL(value));
          c.window.location = entries[position];
        },
        replaceState(_state, _title, value) {
          entries[position] = c.window.location = new URL(value);
        },
      },
    },
    state: {namespace: 'team-a', namespaces: ['team-a', 'team-b'], namespaceGeneration: 0, sessionListGeneration: 0, consoleView: 'overview', selected: null, sessions, resourceGroups: [], resourceDetailGeneration: 0, resourceDetailLogGeneration: 0},
    elements: Object.fromEntries([
      'overviewView', 'sessionsView', 'resourcesView', 'resourceDetailPage', 'adminView', 'sessionSidebar',
      'backToResources', 'resourceDetailTitle', 'resourceDetailSubtitle', 'resourceDetailTabs',
      'resourceDetailLogsTab', 'resourceDetailManifestTab', 'resourceDetailLogsPanel', 'resourceDetailManifestPanel',
      'refreshResourceLogs', 'resourceDetailLogs', 'resourceDetailYAML', 'taskSpawnerLifecycle',
      'taskSpawnerLifecycleButton', 'taskSpawnerLifecycleStatus', 'taskSpawnerLifecycleError',
      'taskSpawnerRunForm', 'taskSpawnerRunValues', 'taskSpawnerRunError', 'taskSpawnerRunButton',
      'overviewButton', 'sessionsButton', 'resourcesButton', 'adminButton', 'messages',
      'pending', 'changesList', 'sidebar', 'welcome', 'subsessionDialog',
    ].map(key => [key, element()])),
    document: {createElement: element},
    consoleReady: Promise.resolve(),
    async loadNamespaceOptions(namespace) { return namespace; },
    async switchNamespace(namespace, options) { c.state.namespace = namespace; c.selectSession(null, false, options); },
    async loadSessions() { return c.state.sessions; },
    resourceCollections: () => c.state.resourceGroups.flatMap(group => group.resources),
    api: async path => { requests.push(path); return {kind: 'Task', yaml: 'kind: Task'}; },
    apiText: async path => { requests.push(path); return 'task logs'; },
    async syncPushSubscription() {},
    sessionKey: session => `${session.namespace}/${session.name}`,
    showToast: message => toasts.push(message),
    errorMessage: error => error.message,
    cachedSessionView: () => ({messages: {hasChildNodes: () => false}, historyLoaded: false}),
  };
  for (const name of [
    'savePromptDraft', 'closeSessionSectionEditor', 'closeSocket', 'saveCurrentSessionView',
    'setActiveView', 'restorePromptDraft', 'renderPendingAttachments', 'renderSessions',
    'renderHeader', 'resetCurrentSessionView', 'activateSessionView', 'scheduleBottomAnchor',
    'resizeComposer', 'updateCurrentRequest', 'updateJumpToLatest', 'setSidebarOpen',
    'scheduleCompletionRead', 'syncCompletionReads', 'pruneBrowserNotifications',
    'notifySessions', 'renderSectionOptions', 'discardSessionView',
  ]) c[name] = () => {};
  vm.createContext(c);
  vm.runInContext(slice('let consoleLocationReady', 'const allResourceKind'), c);
  vm.runInContext(slice('function setConsoleView', 'function resourceAge'), c);
  vm.runInContext(slice('function setResourceDetailView', 'const pendingCompletionReads'), c);
  vm.runInContext(slice('function selectSession', 'async function resumeIdleSession'), c);
  return {
    c, toasts, entries, requests,
    async move(offset) {
      position += offset;
      c.window.location = entries[position];
      await c.applyConsoleLocation();
    },
  };
}

const first = {namespace: 'team-a', name: 'first', uid: 'first-uid', displayName: 'Friendly name', phase: 'Pending'};
const second = {...first, name: 'second', uid: 'second-uid'};

async function testSelectionReloadAndHistory() {
  const h = harness('https://console.example/?other=keep#anchor', [first, second]);
  const c = h.c;
  await c.applyConsoleLocation();
  assert.equal(h.entries.length, 1, 'Initial namespace normalization must replace history');
  c.setConsoleView('sessions');
  assert.equal(c.state.selected, first);
  assert.equal(c.window.location.pathname, '/namespaces/team-a/sessions/first');
  assert.equal(c.window.location.search, '?other=keep');
  assert.equal(c.window.location.hash, '#anchor');
  assert.equal(h.entries.length, 2);
  c.selectSession(second);
  assert.equal(c.window.location.pathname, '/namespaces/team-a/sessions/second');
  assert.equal(h.entries.length, 3);
  c.selectSession(second);
  assert.equal(h.entries.length, 3, 'Selecting the same session must not add history');
  await h.move(-1);
  assert.equal(c.state.selected, first);
  assert.equal(h.entries.length, 3, 'Back must not push a history entry');
  await h.move(1);
  assert.equal(c.state.selected, second);
  const reloaded = harness(c.window.location.href, [first, second]);
  await reloaded.c.applyConsoleLocation();
  assert.equal(reloaded.c.state.selected, second);
  assert.equal(reloaded.c.state.consoleView, 'sessions');
  c.setConsoleView('overview');
  assert.equal(c.window.location.searchParams.has('session'), false);
  await h.move(-1);
  assert.equal(c.state.selected, second);
}

async function testCrossNamespaceAndOptionalUID() {
  const target = {...first, namespace: 'team-b'};
  const h = harness('https://console.example/?namespace=team-b&session=first', [target]);
  const c = h.c;
  c.loadNamespaceOptions = async (namespace, {onlyPreferred = false} = {}) => {
    c.state.namespaces = onlyPreferred ? [namespace] : ['team-a', 'team-b'];
    return namespace;
  };
  await c.applyConsoleLocation();
  assert.deepEqual(c.state.namespaces, ['team-a', 'team-b'], 'Cross-namespace links must keep all namespace choices');
  assert.equal(c.state.namespace, 'team-b');
  assert.equal(c.state.selected, target);
  assert.equal(c.window.location.pathname, '/namespaces/team-b/sessions/first');
  assert.equal(h.entries.length, 1, 'Namespace switching must not add intermediate history');
  assert.equal(h.toasts.length, 0);
  c.window.location = new URL('https://console.example/?namespace=team-b&session=first&uid=first-uid');
  await c.applyConsoleLocation();
  assert.equal(c.state.selected, target);
  assert.equal(c.window.location.searchParams.has('uid'), false, 'Successful links use namespace and name');
}

async function testUnavailableTargets() {
  for (const [query, message] of [
    ['namespace=team-a&session=missing', 'Session team-a/missing is no longer available'],
    ['namespace=team-a&session=first&uid=deleted-uid', 'Session team-a/first is no longer available'],
    ['session=first', 'A Session link requires a namespace'],
  ]) {
    const h = harness(`https://console.example/?${query}`, [first]);
    await h.c.applyConsoleLocation();
    assert.equal(h.c.state.selected, null, 'Invalid targets must not open another session');
    assert.equal(h.c.state.consoleView, 'sessions');
    assert.equal(h.c.window.location.search, `?${query}`, 'Keep failed links available for retry');
    assert.deepEqual(h.toasts, [message]);
    assert.equal(h.entries.length, 1);
  }
  const h = harness('https://console.example/?namespace=forbidden&session=first', [first]);
  h.c.loadNamespaceOptions = async () => '';
  await h.c.applyConsoleLocation();
  assert.equal(h.c.state.namespace, 'team-a');
  assert.equal(h.c.state.selected, null);
  assert.deepEqual(h.toasts, ['Namespace forbidden is not accessible']);
  const failed = harness('https://console.example/?namespace=team-a&session=first', [first]);
  failed.c.loadSessions = async () => { throw new Error('Unable to load sessions'); };
  await failed.c.applyConsoleLocation();
  assert.equal(failed.c.state.selected, null, 'Failed requests must not open a stale session');
  assert.deepEqual(failed.toasts, ['Unable to load sessions']);
}

async function testNavigationDuringPendingRequest() {
  for (const outcome of ['resolve', 'reject']) {
    const h = harness('https://console.example/?namespace=team-a&session=first', [first, second]);
    let finish;
    h.c.loadSessions = () => new Promise((resolve, reject) => {
      finish = () => outcome === 'resolve' ? resolve(h.c.state.sessions) : reject(new Error('Disconnected'));
    });
    const pending = h.c.applyConsoleLocation();
    await new Promise(setImmediate);
    h.c.window.location = new URL('https://console.example/?namespace=team-a&session=second');
    h.c.loadSessions = async () => h.c.state.sessions;
    await h.c.applyConsoleLocation();
    finish();
    await pending;
    assert.equal(h.c.state.selected, second, 'A delayed request must not overwrite a later navigation');
    assert.equal(h.c.window.location.pathname, '/namespaces/team-a/sessions/second');
    assert.equal(h.toasts.length, 0, 'A superseded request must not show an error');
  }

  const h = harness('https://console.example/?namespace=team-a&session=first', [first, second]);
  await h.c.applyConsoleLocation();
  let finish;
  h.c.loadSessions = () => new Promise(resolve => { finish = resolve; });
  h.c.window.location = new URL('https://console.example/?namespace=team-a&session=first');
  const delayed = h.c.applyConsoleLocation();
  await new Promise(setImmediate);
  h.c.selectSession(second);
  finish(h.c.state.sessions);
  await delayed;
  assert.equal(h.c.state.selected, second, 'A user selection must supersede pending URL navigation');
  assert.equal(h.c.window.location.pathname, '/namespaces/team-a/sessions/second');
}

function useSessionLoader(h) {
  vm.runInContext(slice('async function loadSessions', 'async function loadIdentity'), h.c);
}

async function testBackgroundRefreshReplacesHistory() {
  for (const sessions of [[], [{...first, uid: 'replacement'}]]) {
    const h = harness('https://console.example/?namespace=team-a&session=first', [first]);
    await h.c.applyConsoleLocation();
    useSessionLoader(h);
    h.c.api = async () => sessions;
    await h.c.loadSessions({quiet: true});
    assert.equal(h.entries.length, 1, 'A background update must not push browser history');
    assert.equal(h.c.state.selected, sessions[0] || null);
    assert.equal(h.c.window.location.pathname, sessions.length ? '/namespaces/team-a/sessions/first' : '/namespaces/team-a/sessions');
    assert.equal(h.toasts.length, 0);
  }
}

async function testDiscardedListIsRetried() {
  for (const outcome of ['resolve', 'reject']) {
    const h = harness('https://console.example/?namespace=team-a&session=second', [first]);
    useSessionLoader(h);
    const requests = [];
    h.c.api = () => new Promise((resolve, reject) => { requests.push({resolve, reject}); });
    const navigation = h.c.applyConsoleLocation();
    await new Promise(setImmediate);
    const poll = h.c.loadSessions({quiet: true});
    assert.equal(requests.length, 2);
    if (outcome === 'resolve') requests[0].resolve([first, second]);
    else requests[0].reject(new Error('Discarded request failed'));
    await new Promise(setImmediate);
    assert.equal(requests.length, 3, 'Discarded results must trigger a fresh lookup');
    assert.equal(h.c.state.selected, null, 'A discarded response must not select from a stale list');
    assert.equal(h.toasts.length, 0, 'A discarded response must not report a missing session');
    requests[1].resolve([first]);
    await poll;
    requests[2].resolve([first, second]);
    await navigation;
    assert.equal(h.c.state.selected, second);
    assert.equal(h.c.window.location.pathname, '/namespaces/team-a/sessions/second');
    assert.equal(h.entries.length, 1);
    assert.equal(h.toasts.length, 0);
  }
}

async function testResourcePagesAndHistory() {
  const h = harness('https://console.example/namespaces/team-b/resources/tasks/build?other=keep');
  await h.c.applyConsoleLocation();
  assert.equal(h.c.state.namespace, 'team-b');
  assert.equal(h.c.state.consoleView, 'resource');
  assert.equal(h.c.elements.resourceDetailPage.hidden, false);
  assert.equal(h.c.elements.resourcesView.hidden, true);
  assert.equal(h.c.elements.resourceDetailTitle.textContent, 'Task build');
  assert.equal(h.c.elements.resourceDetailYAML.textContent, 'kind: Task');
  assert.equal(h.c.elements.resourceDetailLogs.textContent, 'task logs');
  assert.deepEqual(h.requests, ['/api/resources/tasks/team-b/build', '/api/resources/tasks/team-b/build/logs']);
  assert.equal(h.entries.length, 1);
  assert.equal(h.c.window.location.pathname, '/namespaces/team-b/resources/tasks/build');
  assert.equal(h.c.window.location.search, '?other=keep');

  await h.c.openResourceDetail({resource: 'tasks', kind: 'Task'}, {namespace: 'team-b', name: 'test'});
  assert.equal(h.c.window.location.pathname, '/namespaces/team-b/resources/tasks/test');
  assert.deepEqual(h.requests.slice(-2), ['/api/resources/tasks/team-b/test', '/api/resources/tasks/team-b/test/logs']);
  assert.equal(h.entries.length, 2);
  await h.move(-1);
  assert.equal(h.c.elements.resourceDetailTitle.textContent, 'Task build');
  await h.move(1);
  assert.equal(h.c.elements.resourceDetailTitle.textContent, 'Task test');
  h.c.setConsoleView('resources');
  assert.equal(h.c.window.location.pathname, '/namespaces/team-b/resources');
  assert.equal(h.c.state.resourceDetail, null);
  assert.equal(h.c.state.resourceDetailTask, null);
  assert.equal(h.c.elements.resourceDetailPage.hidden, true);
  await h.move(-1);
  assert.equal(h.c.elements.resourceDetailTitle.textContent, 'Task test');
  await h.move(1);
  assert.equal(h.c.state.consoleView, 'resources');
  const reload = harness(h.c.window.location.href);
  await reload.c.applyConsoleLocation();
  assert.equal(reload.c.state.consoleView, 'resources');
  assert.equal(reload.c.state.namespace, 'team-b');
  assert.deepEqual(h.toasts, []);
}

async function testResourceErrorsAndNavigation() {
  const h = harness('https://console.example/namespaces/team-a/resources/workspaces/missing');
  h.c.api = async () => { throw new Error('Workspace missing not found'); };
  await h.c.applyConsoleLocation();
  assert.equal(h.c.state.consoleView, 'resource');
  assert.equal(h.c.elements.resourceDetailYAML.textContent, 'Workspace missing not found');
  assert.equal(h.c.window.location.pathname, '/namespaces/team-a/resources/workspaces/missing');

  let finish;
  h.c.api = () => new Promise(resolve => { finish = resolve; });
  const pending = h.c.openResourceDetail({resource: 'workspaces', kind: 'Workspace'}, {namespace: 'team-a', name: 'repository'});
  h.c.setConsoleView('resources');
  finish({kind: 'Workspace', yaml: 'stale manifest'});
  await pending;
  assert.equal(h.c.elements.resourceDetailYAML.textContent, 'Loading manifest…');
  assert.equal(h.c.state.consoleView, 'resources');

  const denied = harness('https://console.example/namespaces/forbidden/resources/tasks/build');
  denied.c.loadNamespaceOptions = async () => '';
  await denied.c.applyConsoleLocation();
  assert.equal(denied.c.state.resourceDetail, undefined);
  assert.equal(denied.c.state.namespace, 'team-a');
  assert.equal(denied.c.window.location.pathname, '/namespaces/forbidden/resources/tasks/build', 'Keep the requested destination available for retry');
  assert.deepEqual(denied.toasts, ['Namespace forbidden is not accessible']);

  const notification = harness('https://console.example/namespaces/team-a/resources/tasks/build', [first]);
  await notification.c.applyConsoleLocation();
  await notification.c.openNotificationSession(first);
  assert.equal(notification.c.state.consoleView, 'sessions');
  assert.equal(notification.c.window.location.pathname, '/namespaces/team-a/sessions/first');
  assert.equal(notification.c.window.location.searchParams.has('session'), false);
}

async function testSessionConversationPaths() {
  const target = {...first, namespace: 'team-b'};
  const other = {...second, namespace: 'team-b'};
  const h = harness('https://console.example/namespaces/team-b/sessions/first', [target, other]);
  const views = [];
  h.c.setActiveView = view => views.push(view);
  await h.c.applyConsoleLocation();
  assert.equal(h.c.state.namespace, 'team-b');
  assert.equal(h.c.state.selected, target);
  assert.equal(h.c.state.consoleView, 'sessions');
  assert.equal(h.c.elements.resourceDetailPage.hidden, true);
  assert.equal(views.at(-1), 'conversation');
  assert.deepEqual(h.requests, [], 'Conversation routes must not request resource manifests or logs');
  assert.equal(h.entries.length, 1);
  h.c.selectSession(other);
  assert.equal(h.c.window.location.pathname, '/namespaces/team-b/sessions/second');
  await h.move(-1);
  assert.equal(h.c.state.selected, target);
  await h.move(1);
  assert.equal(h.c.state.selected, other);
  const reload = harness(h.c.window.location.href, [target, other]);
  await reload.c.applyConsoleLocation();
  assert.equal(reload.c.state.selected, other);
  assert.equal(reload.c.state.consoleView, 'sessions');

  const list = harness('https://console.example/namespaces/team-b/sessions', [target]);
  await list.c.applyConsoleLocation();
  assert.equal(list.c.state.consoleView, 'sessions');
  assert.equal(list.c.state.selected, null);
  assert.equal(list.c.window.location.pathname, '/namespaces/team-b/sessions');

  for (const suffix of ['missing', 'first?uid=replaced']) {
    const failed = harness(`https://console.example/namespaces/team-b/sessions/${suffix}`, [target]);
    await failed.c.applyConsoleLocation();
    assert.equal(failed.c.state.selected, null);
    assert.equal(failed.c.state.consoleView, 'sessions');
    assert.equal(failed.c.window.location.href, `https://console.example/namespaces/team-b/sessions/${suffix}`);
    assert.match(failed.toasts[0], /is no longer available/);
  }
}

async function testSessionResourcePath() {
  const h = harness('https://console.example/namespaces/team-a/resources/sessions/first', [first]);
  h.c.api = async path => { h.requests.push(path); return {kind: 'Session', yaml: 'kind: Session'}; };
  h.c.loadSessions = async () => { throw new Error('Resource routes must not open conversations'); };
  await h.c.applyConsoleLocation();
  assert.equal(h.c.state.consoleView, 'resource');
  assert.equal(h.c.state.selected, null);
  assert.equal(h.c.elements.resourceDetailTitle.textContent, 'Session first');
  assert.equal(h.c.elements.resourceDetailYAML.textContent, 'kind: Session');
  assert.equal(h.c.elements.resourceDetailTabs.hidden, true);
  assert.equal(h.c.elements.resourceDetailLogsPanel.hidden, true);
  assert.deepEqual(h.requests, ['/api/resources/sessions/team-a/first']);
  assert.equal(h.c.window.location.pathname, '/namespaces/team-a/resources/sessions/first');
  assert.deepEqual(h.toasts, []);
}

(async () => {
  await testSelectionReloadAndHistory();
  await testCrossNamespaceAndOptionalUID();
  await testUnavailableTargets();
  await testNavigationDuringPendingRequest();
  await testBackgroundRefreshReplacesHistory();
  await testDiscardedListIsRetried();
  await testResourcePagesAndHistory();
  await testResourceErrorsAndNavigation();
  await testSessionConversationPaths();
  await testSessionResourcePath();
})().catch(error => { console.error(error); process.exitCode = 1; });
