const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
function slice(start, end) {
  const from = source.indexOf(start);
  const to = source.indexOf(end, from);
  assert.notEqual(from, -1, start);
  assert.notEqual(to, -1, end);
  return source.slice(from, to);
}

function eventTarget(values = {}) {
  const listeners = new Map();
  return {
    ...values,
    addEventListener(name, listener) { listeners.set(name, listener); },
    removeEventListener(name) { listeners.delete(name); },
    dispatch(name, event = {}) { listeners.get(name)?.(event); },
  };
}

function harness(shared = {}) {
  const notifications = shared.notifications || [];
  const toasts = [];
  const storage = shared.storage || new Map();
  const selected = [];
  const session = {namespace: 'team', name: 'worker', displayName: 'Fix tests', uid: 'session-1', phase: 'Ready'};
  const registration = shared.registration || {
    active: {},
    async showNotification(title, options) {
      for (const notification of notifications) {
        if (notification.options.tag === options.tag) notification.close();
      }
      notifications.push({title, options, data: options.data, closed: false, close() { this.closed = true; }});
    },
    async getNotifications({tag} = {}) { return notifications.filter(item => !item.closed && (!tag || item.options.tag === tag)); },
  };
  let pendingLock = Promise.resolve();
  const locks = shared.locks || {
    request(name, action) {
      const result = pendingLock.then(action);
      pendingLock = result.catch(() => {});
      return result;
    },
  };
  const serviceWorker = eventTarget({
    async register(url) { assert.equal(url, '/notifications.js'); return registration; },
    async getRegistration() { return registration; },
  });
  class Notification {
    static permission = 'granted';
    static requests = 0;
    static async requestPermission() { this.requests++; return this.permission = 'granted'; }
    constructor() { throw new TypeError('Page notifications unavailable on mobile'); }
  }
  const context = {
    console, Map, Set, Date, Promise, URLSearchParams, Notification,
    navigator: {serviceWorker, locks},
    window: {
      Notification, isSecureContext: true,
      atob: value => { assert.match(value, /^[A-Za-z0-9+/=]*$/); return Buffer.from(value, 'base64').toString('binary'); },
      localStorage: {
        getItem: key => storage.get(key) ?? null,
        setItem: (key, value) => storage.set(key, value),
        removeItem: key => storage.delete(key),
      },
    },
    document: eventTarget({visibilityState: 'hidden', hasFocus: () => false}),
    elements: {
      consoleSettings: eventTarget({open: false, contains: target => target === context.elements.browserAlerts || target === context.elements.settingsButton}),
      settingsButton: {focus() { this.focused = true; }}, sessionActionsMenu: {hidden: true},
      browserAlerts: {}, browserAlertsTest: {}, browserAlertsStatus: {}, browserAlertsDescription: {}, messages: {hidden: false},
      memberDialog: {close() {}}, memberList: {replaceChildren() {}}, memberGroups: {replaceChildren() {}}, memberError: {},
      adminEditor: {close() {}}, adminCollections: {replaceChildren() {}},
      activeNamespace: {}, namespace: {}, namespaceLabels: [], yaml: {},
    },
    state: {
      selected: session, sessions: [session], namespace: 'team', namespaceGeneration: 0, sessionListGeneration: 0,
      consoleView: 'sessions', browserAlertsEnabled: true, browserAlertsPending: false, browserAlertsMessage: '',
      namespaces: ['team', 'another'], adminGeneration: 0, memberGeneration: 0, adminEditorGeneration: 0,
    },
    showToast: text => toasts.push(text),
    selectSession: item => { selected.push(item); context.state.selected = item; },
    setConsoleView: view => { context.state.consoleView = view; },
    renderSectionOptions() {}, renderHeader() {}, renderSessions() {}, discardSessionView() {},
    syncCompletionReads() {},
    resetNamespaceReferences() {}, renderSessionSourceOptions() {}, renderCredentialOptions() {},
    renderWorkspaceOptions() {}, renderAgentConfigOptions() {}, renderOverview() {}, renderResourceKindOptions() {}, renderResources() {},
    async loadOptions() {}, async loadResources() {},
    consoleReady: Promise.resolve(),
    async refreshNamespaces() {},
  };
  vm.createContext(context);
  for (const [start, end] of [
    ['function errorMessage', 'function requireElements'],
    ['const browserAlertsStorageKey', 'const allResourceKind'],
    ['function sessionKey', 'function moveChildren'],
    ['async function loadSessions', 'async function loadIdentity'],
    ['async function switchNamespace', 'function addOption'],
  ]) vm.runInContext(slice(start, end), context);
  const flush = () => vm.runInContext('browserNotificationQueue', context);
  const refresh = async sessions => {
    context.api = async () => sessions;
    await context.loadSessions();
    await flush();
  };
  return {context, session, notifications, registration, locks, serviceWorker, toasts, storage, selected, flush, refresh, Notification};
}

function event(id, type = 'TurnCompleted') {
  return {id: `journal/${id}`, type, eventTime: '2026-10-13T12:00:00Z'};
}

async function testPermissionsAndDelivery() {
  const h = harness();
  const {context: c} = h;
  c.loadBrowserAlertPreference();
  await h.flush();
  assert.equal(c.state.browserAlertsEnabled, false, 'Browser permission alone must not opt in');
  assert.equal(h.Notification.requests, 0);
  h.Notification.permission = 'default';
  await c.toggleBrowserAlerts();
  assert.equal(h.Notification.requests, 1);
  assert.equal(h.storage.get('kelos-console-browser-alerts'), 'true');
  assert.equal(c.elements.browserAlertsTest.disabled, false);
  await c.testBrowserNotification();
  assert.equal(h.notifications[0].title, 'Kelos notifications are enabled');
  assert.match(c.elements.browserAlertsStatus.textContent, /system settings/);
  await c.toggleBrowserAlerts();
  await h.flush();
  assert.equal(c.state.browserAlertsEnabled, false);
  assert.equal(h.notifications[0].closed, true);
  assert.equal(c.elements.browserAlertsTest.disabled, true);
}

function testSettingsPopoverInteractions() {
  const h = harness();
  const c = h.context;
  c.bindConsoleSettings();
  vm.runInContext(slice("document.addEventListener('keydown', event => {", 'bindConsoleSettings();'), c);
  const settings = c.elements.consoleSettings;
  settings.open = true;
  settings.dispatch('toggle');
  assert.equal(c.elements.browserAlerts.checked, true);
  assert.equal(h.Notification.requests, 0, 'Opening Settings must not request permission');
  h.Notification.permission = 'denied';
  settings.dispatch('toggle');
  assert.equal(c.elements.browserAlerts.checked, false);
  assert.equal(c.elements.browserAlerts.disabled, true);
  assert.equal(c.elements.browserAlertsStatus.textContent, 'Allow notifications in your browser’s site settings to enable alerts.');
  c.document.dispatch('pointerdown', {target: c.elements.browserAlerts});
  assert.equal(settings.open, true);
  c.document.dispatch('pointerdown', {target: c.elements.settingsButton});
  assert.equal(settings.open, true);
  c.document.dispatch('pointerdown', {target: {}});
  assert.equal(settings.open, false);
  assert.equal(c.elements.settingsButton.focused, undefined);
  settings.open = true;
  c.document.dispatch('keydown', {key: 'Tab'});
  assert.equal(settings.open, true);
  let prevented = false;
  c.document.dispatch('keydown', {key: 'Escape', preventDefault() { prevented = true; }});
  assert.equal(settings.open, false);
  assert.equal(c.elements.settingsButton.focused, true);
  assert.equal(prevented, true);
}

async function testPermissionAndStorageFailures() {
  for (const permission of ['default', 'denied']) {
    const h = harness();
    h.context.state.browserAlertsEnabled = false;
    h.Notification.permission = 'default';
    h.Notification.requestPermission = async () => h.Notification.permission = permission;
    await h.context.toggleBrowserAlerts();
    assert.equal(h.context.state.browserAlertsEnabled, false);
    assert.equal(h.context.elements.browserAlerts.disabled, permission === 'denied');
    assert.equal(h.notifications.length, 0);
  }
  const h = harness();
  const c = h.context;
  c.state.browserAlertsEnabled = false;
  h.Notification.permission = 'default';
  let resolve;
  h.Notification.requestPermission = () => {
    h.Notification.requests++;
    return new Promise(done => { resolve = done; });
  };
  const pending = c.toggleBrowserAlerts();
  assert.equal(c.elements.browserAlerts.disabled, true);
  await c.toggleBrowserAlerts();
  assert.equal(h.Notification.requests, 1);
  h.Notification.permission = 'granted';
  resolve('granted');
  await pending;
  c.window.localStorage.getItem = () => { throw new Error('Storage unavailable'); };
  c.window.localStorage.setItem = () => { throw new Error('Storage unavailable'); };
  c.loadBrowserAlertPreference();
  assert.equal(c.state.browserAlertsEnabled, false);
  await c.toggleBrowserAlerts();
  assert.equal(c.state.browserAlertsEnabled, true);
  assert.equal(h.toasts.at(-1), 'Browser alert preference applies to this tab only because it could not be saved');
  await h.flush();
}

async function testNamespaceEventsAndReconnects() {
  const h = harness();
  const {context: c, session} = h;
  await h.refresh([{...session, lastNotification: event(1)}]);
  assert.equal(h.notifications.length, 0, 'Loading existing history must not notify');
  const other = {...session, name: 'other', uid: 'session-2', displayName: 'Other work'};
  c.document.visibilityState = 'visible';
  c.document.hasFocus = () => true;
  await h.refresh([session, other]);
  await h.refresh([session, {...other, lastNotification: event(2, 'InputRequested')}]);
  assert.equal(h.notifications.length, 1, 'Unselected Sessions must notify even when the console has focus');
  assert.equal(h.notifications[0].title, 'Input needed · Other work');
  assert.equal(h.notifications[0].options.body, 'team · Your Session is waiting for an answer.');
  assert.deepEqual({...h.notifications[0].data.session}, {namespace: 'team', name: 'other', uid: 'session-2'});
  await h.refresh([session, {...other, lastNotification: event(2, 'InputRequested')}]);
  assert.equal(h.notifications.length, 1, 'Repeated polls must not notify twice');
  c.api = async () => { throw new Error('Disconnected'); };
  await c.loadSessions({quiet: true});
  c.state.replayingHistory = true;
  await h.refresh([session, {...other, lastNotification: event(3, 'TurnFailed')}]);
  assert.equal(h.notifications.length, 2, 'A missed event must notify after reconnecting');
  assert.equal(h.notifications[1].title, 'Work failed · Other work');
  assert.equal(h.notifications[0].closed, true);
  await h.refresh([session, other]);
  await h.refresh([session, {...other, lastNotification: event(2, 'InputRequested')}]);
  assert.equal(h.notifications.length, 2, 'A previously observed event must not notify again');
  c.document.visibilityState = 'hidden';
  await h.refresh([{...session, lastNotification: event(4)}, other]);
  assert.equal(h.notifications[2].title, 'Work completed · Fix tests');
}

async function testDeliveryAcrossTabs() {
  for (const storageAvailable of [true, false]) {
    const first = harness();
    const second = harness(first);
    if (!storageAvailable) {
      for (const h of [first, second]) {
        h.context.window.localStorage.getItem = () => { throw new Error('Storage unavailable'); };
        h.context.window.localStorage.setItem = () => { throw new Error('Storage unavailable'); };
      }
    }
    await Promise.all([first.refresh([first.session]), second.refresh([second.session])]);
    const sessions = [{...first.session, lastNotification: event(1)}];
    await Promise.all([first.refresh(sessions), second.refresh(sessions)]);
    assert.equal(first.notifications.length, 1, 'Concurrent polls in two tabs must show an event once');
    if (storageAvailable) {
      const delayed = harness(first);
      await delayed.refresh([delayed.session]);
      first.notifications[0].close();
      await delayed.refresh(sessions);
      assert.equal(first.notifications.length, 1, 'A delayed tab must not redisplay a dismissed event');
    }
    const updated = [{...first.session, lastNotification: event(2)}];
    await Promise.all([first.refresh(updated), second.refresh(updated)]);
    assert.equal(first.notifications.length, 2, 'A later event must still notify once');
  }
}

async function testSuppressionAndNamespaceChanges() {
  for (const setup of [
    c => { c.state.browserAlertsEnabled = false; },
    c => { c.Notification.permission = 'denied'; },
    c => { c.window.isSecureContext = false; },
    c => { delete c.navigator.serviceWorker; },
    c => { delete c.navigator.locks; },
    c => { c.document.visibilityState = 'visible'; c.document.hasFocus = () => true; },
  ]) {
    const h = harness();
    await h.refresh([h.session]);
    setup(h.context);
    await h.refresh([{...h.session, lastNotification: event(1)}]);
    assert.equal(h.notifications.length, 0, setup.toString());
  }
  const h = harness();
  await h.refresh([h.session]);
  h.context.state.selected = null;
  await h.refresh([{...h.session, lastNotification: event(1)}]);
  assert.equal(h.notifications.length, 1, 'The overview must not require a selected Session');
  h.context.state.namespace = 'another';
  h.context.state.namespaceGeneration++;
  await h.refresh([{...h.session, namespace: 'another', lastNotification: event(2)}]);
  assert.equal(h.notifications.length, 1, 'Switching namespace must establish a history baseline');
}

async function testStaleResponsesAndRemovedSessions() {
  const h = harness();
  await h.refresh([h.session]);
  await h.refresh([{...h.session, lastNotification: event(1)}]);
  for (const stale of [
    () => { h.context.state.sessionListGeneration++; },
    () => { h.context.state.namespaceGeneration++; },
  ]) {
    h.context.api = async () => { stale(); return []; };
    await h.context.loadSessions();
    await h.flush();
    assert.equal(h.notifications[0].closed, false, 'A stale list must not dismiss notifications');
  }
  await h.refresh([{...h.session, lastNotification: event(1)}]);
  assert.equal(h.notifications[0].closed, false, 'An unchanged Session must keep its notification');
  const storageKey = `kelos-console-notification-${h.context.sessionViewKey(h.session)}`;
  assert.equal(h.storage.get(storageKey), event(1).id);
  await h.refresh([]);
  assert.equal(h.notifications[0].closed, true, 'Removing a Session must dismiss its notification');
  assert.equal(h.storage.has(storageKey), false, 'Removing a Session must remove its delivered ID');
  await h.refresh([{...h.session, lastNotification: event(2)}]);
  await h.refresh([{...h.session, uid: 'replacement'}]);
  assert.equal(h.notifications[1].closed, true, 'Replacing a Session must dismiss its notification');
  assert.equal(h.storage.has(storageKey), false, 'Replacing a Session must remove the previous UID’s delivered ID');
}

async function testFailuresCanBeRetried() {
  const h = harness();
  h.context.state.browserAlertsEnabled = false;
  h.serviceWorker.register = async () => { throw new Error('Worker unavailable'); };
  await h.context.toggleBrowserAlerts();
  assert.equal(h.context.state.browserAlertsEnabled, false);
  assert.equal(h.context.elements.browserAlerts.disabled, false, 'Registration failure must permit retry');
  assert.match(h.context.elements.browserAlertsStatus.textContent, /Worker unavailable/);
  h.serviceWorker.register = async () => h.registration;
  await h.context.toggleBrowserAlerts();
  assert.equal(h.context.state.browserAlertsEnabled, true);
  const show = h.registration.showNotification;
  h.registration.showNotification = async () => { throw new Error('Delivery unavailable'); };
  await h.context.testBrowserNotification();
  assert.match(h.context.elements.browserAlertsStatus.textContent, /Delivery unavailable/);
  assert.equal(h.context.elements.browserAlertsTest.disabled, false);
  h.registration.showNotification = show;
  await h.context.testBrowserNotification();
  assert.equal(h.notifications.length, 1);
}

async function testTransientDeliveryFailureRetries() {
  const h = harness();
  await h.refresh([h.session]);
  const show = h.registration.showNotification;
  h.registration.showNotification = async () => { throw new Error('Temporarily unavailable'); };
  const sessions = [{...h.session, lastNotification: event(1)}];
  await h.refresh(sessions);
  assert.equal(h.notifications.length, 0);
  await h.refresh(sessions);
  assert.equal(h.toasts.length, 1, 'Repeated failures must not repeat the same toast');
  h.registration.showNotification = show;
  await h.refresh(sessions);
  assert.equal(h.notifications.length, 1, 'An undelivered event must be retried');
  assert.equal(h.context.state.browserAlertsMessage, '');
}

async function testActivationAndDisableRace() {
  const h = harness();
  const worker = eventTarget({state: 'installing'});
  h.registration.active = null;
  h.registration.installing = worker;
  await h.refresh([h.session]);
  h.context.api = async () => [{...h.session, lastNotification: event(1)}];
  await h.context.loadSessions();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(h.notifications.length, 0, 'Delivery must wait for service-worker activation');
  h.context.state.browserAlertsEnabled = false;
  worker.state = 'activated';
  worker.dispatch('statechange');
  await h.flush();
  assert.equal(h.notifications.length, 0, 'Disabling alerts must cancel queued delivery');
}

async function testNotificationTargets() {
  const h = harness();
  await h.refresh([h.session]);
  await h.context.openNotificationSession(h.session);
  assert.equal(h.selected[0], h.session);
  assert.equal(h.context.state.consoleView, 'sessions');
  await h.context.openNotificationSession({...h.session, uid: 'deleted-session'});
  assert.equal(h.selected.length, 1);
  assert.equal(h.toasts.at(-1), 'Session worker is no longer available');
  const target = {...h.session, namespace: 'another'};
  h.context.api = async () => [target];
  await h.context.openNotificationSession(target);
  assert.equal(h.selected.at(-1), target);
}

async function testNamespaceSwitchPreservesOtherNotifications() {
  const first = harness();
  const second = harness(first);
  second.context.state.namespace = 'another';
  second.context.state.selected = null;
  const target = {...first.session, namespace: 'another', uid: 'session-2'};
  const sibling = {...target, name: 'sibling', uid: 'session-3'};
  await first.refresh([first.session]);
  await second.refresh([target, sibling]);
  const teamSessions = [{...first.session, lastNotification: event(1, 'InputRequested')}];
  const anotherSessions = [
    {...target, lastNotification: event(2, 'InputRequested')},
    {...sibling, lastNotification: event(3, 'InputRequested')},
  ];
  await first.refresh(teamSessions);
  await second.refresh(anotherSessions);
  assert.equal(first.notifications.length, 3);
  first.context.api = async () => anotherSessions;
  first.notifications[1].close();
  await first.context.openNotificationSession(target);
  await first.flush();
  assert.equal(first.context.state.namespace, 'another');
  assert.equal(first.selected.at(-1).uid, target.uid);
  assert.equal(first.notifications[0].closed, false, 'A cross-namespace click must preserve the previous namespace alert');
  assert.equal(first.notifications[2].closed, false, 'A cross-namespace click must preserve a sibling alert');
  first.context.api = async () => teamSessions;
  await first.context.switchNamespace('team');
  await first.flush();
  assert.equal(first.notifications[0].closed, false, 'A manual switch must preserve current namespace alerts');
  assert.equal(first.notifications[2].closed, false, 'A manual switch must preserve alerts from other tabs');
}

async function testPushSubscriptions() {
  const h = harness();
  const c = h.context;
  c.state.webPushPublicKey = Buffer.from([4, 251, 255, 3]).toString('base64url');
  const requests = [];
  let subscription = null;
  let subscribed = 0;
  let unsubscribed = 0;
  h.registration.pushManager = {
    async getSubscription() { return subscription; },
    async subscribe(options) {
      subscribed++;
      assert.equal(options.userVisibleOnly, true);
      assert.deepEqual([...options.applicationServerKey], [...Buffer.from(c.state.webPushPublicKey, 'base64url')]);
      subscription = {
        endpoint: 'https://web.push.apple.com/device', options,
        async unsubscribe() { unsubscribed++; subscription = null; return true; },
        toJSON() { return {endpoint: this.endpoint, expirationTime: null, keys: {auth: 'auth', p256dh: 'key'}}; },
      };
      return subscription;
    },
  };
  c.api = async (url, options) => {
    requests.push({url, method: options.method, payload: JSON.parse(options.body)});
    return {subscribed: true};
  };
  c.notifySessions([h.session]);
  c.state.sessions = [{...h.session, lastNotification: event(20)}];
  c.notifySessions(c.state.sessions);
  await h.flush();
  assert.equal(h.notifications.length, 1, 'Polling works until push registration succeeds');
  await c.syncPushSubscription();
  assert.equal(subscribed, 1);
  assert.equal(requests[0].url, '/api/push-subscription');
  assert.equal(requests[0].method, 'PUT');
  assert.equal(requests[0].payload.namespace, 'team');
  assert.equal(requests[0].payload.subscription.endpoint, 'https://web.push.apple.com/device');
  assert.equal('expirationTime' in requests[0].payload.subscription, false);
  await c.syncPushSubscription();
  assert.equal(requests.length, 1, 'Polling must not continuously rewrite the subscription');
  c.state.sessions = [{...h.session, lastNotification: event(21)}];
  c.notifySessions(c.state.sessions);
  await h.flush();
  assert.equal(h.notifications.length, 1, 'Active push registration suppresses duplicate page notifications');
  c.state.namespace = 'another';
  await c.syncPushSubscription();
  assert.equal(requests[1].payload.namespace, 'another');
  assert.equal(subscribed, 1, 'A namespace switch reuses the device subscription');
  await c.removePushSubscription();
  assert.equal(requests[2].method, 'DELETE');
  assert.equal(unsubscribed, 1);
  c.api = async () => { throw new Error('Storage unavailable'); };
  await assert.rejects(c.syncPushSubscription(), /Storage unavailable/);
  c.api = async () => ({subscribed: true});
  await c.syncPushSubscription();
  assert.equal(subscribed, 2, 'Retry reuses a subscription created before a failed save');
  c.api = async () => { throw new Error('Storage unavailable'); };
  await assert.rejects(c.removePushSubscription(), /Storage unavailable/);
  assert.equal(unsubscribed, 2, 'Unsubscribe on the device even if server deletion fails');

  const rotated = harness();
  rotated.context.state.webPushPublicKey = Buffer.from([4, 1]).toString('base64url');
  let rotationUnsubscribed = false;
  let replacementOptions;
  rotated.registration.pushManager = {
    async getSubscription() { return {options: {applicationServerKey: new Uint8Array([4, 0]).buffer}, async unsubscribe() { rotationUnsubscribed = true; }}; },
    async subscribe(options) {
      replacementOptions = options;
      return {endpoint: 'https://web.push.apple.com/replacement', toJSON() { return {keys: {auth: 'auth', p256dh: 'key'}}; }};
    },
  };
  rotated.context.api = async () => ({subscribed: true});
  await rotated.context.syncPushSubscription();
  assert.equal(rotationUnsubscribed, true);
  assert.deepEqual([...replacementOptions.applicationServerKey], [4, 1]);

  const coordinated = harness();
  const sibling = harness(coordinated);
  for (const item of [coordinated, sibling]) {
    item.context.state.webPushPublicKey = c.state.webPushPublicKey;
    item.registration.pushManager = h.registration.pushManager;
  }
  // Periodic refreshes must respect the shared namespace selection.
  coordinated.storage.set('kelos-console-namespace', 'another');
  sibling.context.state.namespace = 'another';
  let saves = 0;
  coordinated.context.api = sibling.context.api = async () => { saves++; return {subscribed: true}; };
  await sibling.context.syncPushSubscription(true);
  await coordinated.context.syncPushSubscription(true);
  assert.equal(saves, 1, 'A background tab must not overwrite the most recently selected namespace');
}

async function testEnablingAlertsKeepsPollingOnPushFailure() {
  for (const failure of ['subscribe', 'save']) {
    const h = harness();
    const c = h.context;
    c.state.browserAlertsEnabled = false;
    c.state.webPushPublicKey = Buffer.from([4, 1, 2, 3]).toString('base64url');
    let subscription = null;
    const pushFailure = vm.runInContext("new Error('Push unavailable')", c);
    h.registration.pushManager = {
      async getSubscription() { return subscription; },
      async subscribe() {
        if (failure === 'subscribe') throw pushFailure;
        subscription = {
          endpoint: 'https://web.push.apple.com/device',
          toJSON() { return {keys: {auth: 'auth', p256dh: 'key'}}; },
          async unsubscribe() { subscription = null; return true; },
        };
        return subscription;
      },
    };
    c.api = async (_url, options) => {
      if (options.method === 'PUT') throw pushFailure;
      return undefined;
    };
    await c.toggleBrowserAlerts();
    assert.equal(c.state.browserAlertsEnabled, true, failure);
    assert.equal(h.storage.get('kelos-console-browser-alerts'), 'true');
    assert.equal(c.state.browserAlertsMessage, 'Could not enable background notifications: Push unavailable');
    c.notifySessions([h.session]);
    c.state.sessions = [{...h.session, lastNotification: event(30)}];
    c.notifySessions(c.state.sessions);
    await h.flush();
    assert.equal(h.notifications.length, 1, 'Polling must notify after enabling alerts when push fails');
    assert.equal(h.notifications[0].title, 'Work completed · Fix tests');
  }
}

async function testWorkerPushWithoutOpenPage() {
  const displayed = [];
  const worker = eventTarget({registration: {
    async showNotification(title, options) { displayed.push({title, options}); },
  }});
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../web/notifications.js'), 'utf8'), {self: worker, URL});
  const payload = {title: 'Work completed · Fix tests', options: {
    tag: 'kelos-session-team/worker/session-1',
    data: {id: 'journal/1', session: {namespace: 'team', name: 'worker', uid: 'session-1'}},
  }};
  let pending;
  worker.dispatch('push', {data: {json: () => payload}, waitUntil(value) { pending = value; }});
  await pending;
  assert.equal(displayed.length, 1);
  assert.equal(displayed[0].title, payload.title);
  assert.deepEqual(displayed[0].options, payload.options);
}

async function testWorkerClicks() {
  const messages = [];
  const opened = [];
  let focused = false;
  let windows = [{url: 'https://console.example/', async focus() { focused = true; }, postMessage: message => messages.push(message)}];
  const worker = eventTarget({
    location: {origin: 'https://console.example'},
    clients: {async matchAll() { return windows; }, async openWindow(url) { opened.push(url); }},
  });
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../web/notifications.js'), 'utf8'), {self: worker, URL});
  const session = {namespace: 'team', name: 'worker', uid: 'session-1'};
  let closed = false;
  let pending;
  const click = () => {
    worker.dispatch('notificationclick', {
      notification: {data: {session}, close() { closed = true; }},
      waitUntil(value) { pending = value; },
    });
    return pending;
  };
  await click();
  assert.equal(closed, true);
  assert.equal(focused, true);
  assert.equal(messages[0].type, 'notification.clicked');
  assert.equal(messages[0].session, session);
  windows = [];
  await click();
  const url = new URL(opened[0]);
  assert.equal(url.origin, 'https://console.example');
  assert.equal(url.searchParams.get('session'), 'worker');
  assert.equal(url.searchParams.get('namespace'), 'team');
  assert.equal(url.searchParams.get('uid'), 'session-1');
}

(async () => {
  await testPermissionsAndDelivery();
  testSettingsPopoverInteractions();
  await testPermissionAndStorageFailures();
  await testNamespaceEventsAndReconnects();
  await testDeliveryAcrossTabs();
  await testSuppressionAndNamespaceChanges();
  await testStaleResponsesAndRemovedSessions();
  await testFailuresCanBeRetried();
  await testTransientDeliveryFailureRetries();
  await testActivationAndDisableRace();
  await testNotificationTargets();
  await testNamespaceSwitchPreservesOtherNotifications();
  await testWorkerClicks();
  await testPushSubscriptions();
  await testEnablingAlertsKeepsPollingOnPushFailure();
  await testWorkerPushWithoutOpenPage();
})().catch(error => { console.error(error); process.exitCode = 1; });
