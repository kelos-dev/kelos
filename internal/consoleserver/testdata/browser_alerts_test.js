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

vm.runInThisContext(applicationSlice('function errorMessage', 'function requireElements'));
vm.runInThisContext(applicationSlice('const browserAlertsStorageKey', 'const allResourceKind'));
vm.runInThisContext(applicationSlice('function sessionKey', 'function moveChildren'));
vm.runInThisContext(applicationSlice('function handleEvent', 'function renderUser'));
vm.runInThisContext(applicationSlice('async function loadSessions', 'async function loadConfig'));

let notifications;
let toasts;
let permissionRequests;
let rendered;
let focused;
let selected;
let view;
let storage;

class TestNotification {
  static permission = 'granted';
  static requestPermission = async () => {
    permissionRequests++;
    return TestNotification.permission = 'granted';
  };

  constructor(title, options) {
    this.title = title;
    this.options = options;
    this.listeners = new Map();
    this.closed = false;
    notifications.push(this);
  }

  addEventListener(type, listener) { this.listeners.set(type, listener); }
  close() { this.closed = true; this.listeners.get('close')?.(); }
  click() { this.listeners.get('click')?.(); }
}

function resetHarness() {
  closeBrowserNotifications();
  notifications = [];
  toasts = [];
  permissionRequests = 0;
  rendered = [];
  focused = false;
  selected = null;
  view = '';
  storage = new Map();
  TestNotification.permission = 'granted';
  TestNotification.requestPermission = async () => {
    permissionRequests++;
    return TestNotification.permission = 'granted';
  };
  global.Notification = TestNotification;
  global.window = {
    Notification: TestNotification,
    isSecureContext: true,
    focus() { focused = true; },
    localStorage: {
      getItem: key => storage.get(key) ?? null,
      setItem: (key, value) => storage.set(key, value),
    },
  };
  global.document = {visibilityState: 'hidden', hasFocus: () => false};
  global.elements = {
    browserAlerts: {setAttribute(name, value) { this[name] = value; }},
    browserAlertsStatus: {},
    messages: {hidden: false},
  };
  const session = {namespace: 'team', name: 'worker', displayName: 'Fix tests', uid: 'session-1'};
  global.state = {
    selected: session,
    sessions: [session],
    namespace: 'team',
    namespaceGeneration: 0,
    sessionListGeneration: 0,
    consoleView: 'sessions',
    replayingHistory: false,
    lastEventID: 0,
    browserAlertsEnabled: true,
    browserAlertsPending: false,
    browserAlertsUnavailable: '',
  };
  global.showToast = message => toasts.push(message);
  global.selectSession = session => { selected = session; };
  global.setConsoleView = value => { view = value; };
  global.endAssistantSegment = () => {};
  global.refreshSessionProgress = () => {};
  global.renderInputRequest = event => rendered.push(event);
  global.renderTurnEnd = event => rendered.push(event);
  global.renderError = event => rendered.push(event);
  global.finishHistoryReplay = () => { state.replayingHistory = false; };
  global.renderSectionOptions = () => {};
  global.renderHeader = () => {};
  global.renderSessions = () => {};
  global.discardSessionView = () => {};
}

async function testPreferenceAndPermission() {
  resetHarness();
  loadBrowserAlertPreference();
  assert.equal(state.browserAlertsEnabled, false, 'Browser permission alone must not opt in');
  assert.equal(elements.browserAlerts.textContent, 'Browser alerts: Off');
  assert.equal(permissionRequests, 0);

  TestNotification.permission = 'default';
  await toggleBrowserAlerts();
  assert.equal(permissionRequests, 1);
  assert.equal(state.browserAlertsEnabled, true);
  assert.equal(storage.get('kelos-console-browser-alerts'), 'true');
  assert.equal(elements.browserAlerts.textContent, 'Browser alerts: On');
  assert.equal(elements.browserAlerts['aria-pressed'], 'true');

  state.browserAlertsEnabled = false;
  loadBrowserAlertPreference();
  assert.equal(state.browserAlertsEnabled, true);
  assert.equal(permissionRequests, 1, 'Loading the preference must not request permission');

  handleEvent({id: 1, type: 'input.requested'});
  await toggleBrowserAlerts();
  assert.equal(state.browserAlertsEnabled, false);
  assert.equal(storage.get('kelos-console-browser-alerts'), 'false');
  assert.equal(notifications[0].closed, true);
  assert.equal(elements.browserAlerts['aria-pressed'], 'false');
}

async function testPermissionFailures() {
  for (const permission of ['default', 'denied']) {
    resetHarness();
    state.browserAlertsEnabled = false;
    TestNotification.permission = 'default';
    TestNotification.requestPermission = async () => TestNotification.permission = permission;
    await toggleBrowserAlerts();
    assert.equal(state.browserAlertsEnabled, false);
    assert.equal(state.browserAlertsPending, false);
    assert.equal(elements.browserAlerts.disabled, permission === 'denied');
    assert.equal(notifications.length, 0);
    assert.equal(toasts.length, 1);
  }

  resetHarness();
  TestNotification.permission = 'default';
  TestNotification.requestPermission = async () => { throw new Error('Permission unavailable'); };
  await toggleBrowserAlerts();
  assert.equal(state.browserAlertsEnabled, false);
  assert.equal(state.browserAlertsPending, false);
  assert.deepEqual(toasts, ['Could not enable browser alerts: Permission unavailable']);

  resetHarness();
  TestNotification.permission = 'default';
  let resolve;
  TestNotification.requestPermission = () => {
    permissionRequests++;
    return new Promise(done => { resolve = done; });
  };
  const pending = toggleBrowserAlerts();
  assert.equal(elements.browserAlerts.disabled, true);
  await toggleBrowserAlerts();
  assert.equal(permissionRequests, 1);
  TestNotification.permission = 'granted';
  resolve('granted');
  await pending;
  assert.equal(elements.browserAlerts.disabled, false);
}

function testLiveEventsAndClick() {
  resetHarness();
  const input = {id: 1, type: 'input.requested', questions: [{question: 'Private question'}]};
  handleEvent(input);
  assert.equal(notifications.length, 1);
  assert.equal(notifications[0].title, 'Input needed · Fix tests');
  assert.deepEqual(notifications[0].options, {
    body: 'team · Your Session is waiting for an answer.',
    tag: 'kelos-session-team/worker/session-1',
  });
  assert.deepEqual(rendered, [input]);
  handleEvent(input);
  assert.equal(notifications.length, 1, 'Duplicate events must not notify twice');

  handleEvent({id: 2, type: 'turn.completed', status: 'completed'});
  assert.equal(notifications[1].title, 'Work completed · Fix tests');
  assert.equal(notifications[0].closed, true);
  handleEvent({id: 3, type: 'error', text: 'Private error'});
  assert.equal(notifications.length, 2);
  handleEvent({id: 4, type: 'turn.completed', status: 'failed'});
  assert.equal(notifications[2].title, 'Work failed · Fix tests');
  assert.equal(notifications[2].options.body, 'team · Open the conversation to see the error.');

  const current = {...state.selected, displayName: 'Renamed'};
  state.sessions = [current];
  state.selected = {name: 'another-session', namespace: 'team', uid: 'another-uid'};
  notifications[2].click();
  assert.equal(focused, true);
  assert.equal(notifications[2].closed, true);
  assert.equal(selected, current);
  assert.equal(view, 'sessions');
}

function testHistoryAndIrrelevantEvents() {
  resetHarness();
  handleEvent({type: 'history.start'});
  handleEvent({id: 1, type: 'input.requested'});
  handleEvent({id: 2, type: 'turn.completed', status: 'failed'});
  handleEvent({id: 3, type: 'turn.completed', status: 'completed'});
  handleEvent({type: 'history.end'});
  handleEvent({id: 3, type: 'turn.completed', status: 'completed'});
  handleEvent({id: 4, type: 'turn.completed', status: 'interrupted'});
  handleEvent({id: 5, type: 'turn.completed', status: 'merged'});
  assert.equal(notifications.length, 0);
  handleEvent({id: 6, type: 'turn.completed', status: 'completed'});
  assert.equal(notifications.length, 1);

  state.historyPageReading = true;
  state.historyPageEvents = [];
  handleEvent({id: 7, type: 'input.requested'});
  assert.equal(notifications.length, 1, 'Buffered history must not produce alerts');
  assert.equal(state.historyPageEvents.length, 1);
}

function testVisibilityAndAvailability() {
  const cases = [
    {setup() { state.browserAlertsEnabled = false; }, expected: 0},
    {setup() { TestNotification.permission = 'denied'; }, expected: 0},
    {setup() { TestNotification.permission = 'default'; }, expected: 0},
    {setup() { state.selected = null; }, expected: 0},
    {setup() { window.isSecureContext = false; }, expected: 0},
    {setup() { delete window.Notification; delete global.Notification; }, expected: 0},
    {setup() { document.visibilityState = 'visible'; document.hasFocus = () => true; }, expected: 0},
    {setup() { document.visibilityState = 'visible'; }, expected: 1},
    {setup() { document.visibilityState = 'visible'; document.hasFocus = () => true; state.consoleView = 'resources'; }, expected: 1},
    {setup() { document.visibilityState = 'visible'; document.hasFocus = () => true; elements.messages.hidden = true; }, expected: 1},
  ];
  for (const {setup, expected} of cases) {
    resetHarness();
    setup();
    renderBrowserAlerts();
    handleEvent({id: 1, type: 'turn.completed', status: 'completed'});
    assert.equal(notifications.length, expected, setup.toString());
    assert.equal(rendered.length, 1, 'Conversation rendering must still run');
    assert.equal(permissionRequests, 0);
  }
}

function testStaleNotificationTargets() {
  for (const change of [
    () => { state.sessions = []; },
    () => { state.sessions = [{...state.selected, uid: 'replacement'}]; },
    () => { state.namespace = 'another-team'; },
  ]) {
    resetHarness();
    handleEvent({id: 1, type: 'input.requested'});
    change();
    notifications[0].click();
    assert.equal(selected, null);
    assert.equal(view, '');
    assert.equal(notifications[0].closed, true);
  }
}

async function testSessionRefreshClosesStaleNotifications() {
  for (const selectedSession of [true, false]) {
    for (const replaced of [true, false]) {
      resetHarness();
      const first = state.selected;
      const second = {...first, name: 'other', uid: 'session-2'};
      handleEvent({id: 1, type: 'input.requested'});
      state.selected = second;
      handleEvent({id: 2, type: 'input.requested'});
      state.sessions = [first, second];
      state.selected = selectedSession ? first : second;
      const sessions = [{...second, displayName: 'Renamed'}];
      if (replaced) sessions.push({...first, uid: 'replacement'});
      global.api = async () => sessions;

      await loadSessions();

      assert.equal(notifications[0].closed, true, 'Removed or replaced Session alerts must close');
      assert.equal(notifications[1].closed, false, 'Alerts for an existing Session must remain');
      assert.deepEqual(toasts, []);
      assert.equal(state.sessions, sessions);
      if (selectedSession) assert.equal(selected, replaced ? sessions[1] : null);

      global.api = async () => [];
      await loadSessions();
      assert.equal(notifications[1].closed, true);
    }
  }
}

async function testSessionRefreshKeepsAlertsOnFailedOrStaleResponses() {
  for (const response of ['failed', 'namespace-changed', 'superseded']) {
    resetHarness();
    handleEvent({id: 1, type: 'input.requested'});
    global.api = async () => {
      if (response === 'failed') throw new Error('List unavailable');
      if (response === 'namespace-changed') state.namespaceGeneration++;
      if (response === 'superseded') state.sessionListGeneration++;
      return [];
    };

    await loadSessions({quiet: true});

    assert.equal(notifications[0].closed, false, response);
    assert.equal(state.sessions.length, 1);
    assert.deepEqual(toasts, []);
  }
}

async function testUnavailableStorageAndNotification() {
  resetHarness();
  window.localStorage.getItem = () => { throw new Error('Storage blocked'); };
  window.localStorage.setItem = () => { throw new Error('Storage blocked'); };
  loadBrowserAlertPreference();
  assert.equal(state.browserAlertsEnabled, false);
  await toggleBrowserAlerts();
  assert.equal(state.browserAlertsEnabled, true);
  assert.deepEqual(toasts, ['Browser alert preference applies to this tab only because it could not be saved']);

  global.Notification = window.Notification = class extends TestNotification {
    constructor() { throw new TypeError('Page notifications unavailable'); }
  };
  handleEvent({id: 1, type: 'turn.completed', status: 'completed'});
  assert.equal(rendered.length, 1, 'Notification failures must not break the conversation');
  assert.equal(elements.browserAlerts.textContent, 'Browser alerts: Unavailable');
  assert.equal(elements.browserAlerts.disabled, true);
  assert.equal(toasts.at(-1), 'Could not display browser alert: Page notifications unavailable');
}

(async () => {
  await testPreferenceAndPermission();
  await testPermissionFailures();
  testLiveEventsAndClick();
  testHistoryAndIrrelevantEvents();
  testVisibilityAndAvailability();
  testStaleNotificationTargets();
  await testSessionRefreshClosesStaleNotifications();
  await testSessionRefreshKeepsAlertsOnFailedOrStaleResponses();
  await testUnavailableStorageAndNotification();
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
