const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');
function slice(start, end) {
  const first = application.indexOf(start);
  const last = application.indexOf(end, first);
  assert.ok(first >= 0 && last > first);
  return application.slice(first, last);
}

class Node {
  constructor() {
    this.children = [];
    this.dataset = {};
    this.attributes = {};
    this.className = '';
    this.classList = {
      contains: name => this.className.split(' ').includes(name),
      add: name => { this.className += ` ${name}`; },
    };
    this.bounds = {top: 0, bottom: 600, height: 600};
  }
  append(...children) { this.children.push(...children); }
  setAttribute(name, value) { this.attributes[name] = value; }
  addEventListener() {}
  getBoundingClientRect() { return this.bounds; }
  querySelectorAll(selector) {
    return this.children.flatMap(child => [
      ...(child.classList.contains(selector.slice(1)) ? [child] : []),
      ...child.querySelectorAll(selector),
    ]);
  }
}

function harness(receipts = new Map()) {
  const frames = [];
  const requests = [];
  let renders = 0;
  const context = vm.createContext({
    document: {
      visibilityState: 'visible', hasFocus: () => true,
      querySelector: () => null, createElement: () => new Node(),
    },
    window: {requestAnimationFrame: callback => frames.push(callback)},
    elements: {
      messages: new Node(), sidebar: new Node(), sessionsAttentionCount: new Node(), sessionsButton: new Node(),
    },
    state: {
      namespace: 'default', namespaceGeneration: 0, sessionListGeneration: 0, consoleView: 'sessions',
      sessions: [], selected: null, currentView: {historyLoaded: true}, sectionAssignments: new Set(),
      subsessions: new Map(), subsessionSelections: new Map(), deletingSubsessions: new Set(),
    },
    api: async (url, options) => {
      requests.push({url, ...options});
      const payload = JSON.parse(options.body);
      receipts.set(payload.uid, payload.notificationId);
    },
    renderSessions: () => { renders++; },
    sessionDisplayStatus: () => 'Idle', createSessionTimestamp: () => new Node(),
    providerLabel: provider => provider, createPullRequestLink: () => null,
  });
  vm.runInContext(slice('const pendingCompletionReads', 'function moveChildren'), context);
  vm.runInContext(slice('function viewingSession', 'function notifySessions'), context);
  vm.runInContext(slice('function createSessionListItem', 'function sessionActionsTarget'), context);
  return Object.assign(context, {
    receipts, requests, renders: () => renders,
    flush: () => frames.splice(0).forEach(callback => callback()),
    refresh: session => {
      const current = {...session, readNotificationId: receipts.get(session.uid)};
      context.state.sessions = [current];
      context.syncCompletionReads([current]);
      return current;
    },
  });
}

function completion(id = 'result-1', type = 'TurnCompleted', overrides = {}) {
  return {
    name: 'session', namespace: 'default', uid: 'uid-1', provider: 'codex', phase: 'Ready',
    lastNotification: {id, type}, ...overrides,
  };
}

function showResult(h, session, id = session.lastNotification.id) {
  h.state.selected = session;
  h.state.sessions = [session];
  const divider = new Node();
  divider.className = 'turn-divider';
  divider.dataset.notificationId = id;
  divider.bounds = {top: 300, bottom: 310, height: 10};
  h.elements.messages.children = [divider];
  return divider;
}

(async () => {

{
  const h = harness();
  assert.equal(h.hasUnreadCompletion(completion()), true);
  assert.equal(h.hasUnreadCompletion(completion('failed', 'TurnFailed')), true);
  assert.equal(h.hasUnreadCompletion(completion('input', 'InputRequested')), false);
  assert.equal(h.hasUnreadCompletion(completion('reset', 'TurnCompleted', {resetting: true})), false);
  assert.equal(h.hasUnreadCompletion(completion('none', 'TurnCompleted', {lastNotification: undefined})), false);
  for (const [type, label] of [
    ['TurnCompleted', 'Completed · Unread'], ['TurnFailed', 'Failed · Unread'], ['InputRequested', 'Input requested'],
  ]) {
    const row = h.createSessionListItem(completion(type, type));
    assert.equal(row.classList.contains('needs-attention'), true);
    assert.equal(row.querySelectorAll('.session-attention-label')[0].textContent, label);
    assert.equal(row.querySelectorAll('.session-attention-dot')[0].attributes['aria-hidden'], 'true');
    assert.equal(row.querySelectorAll('.phase-dot')[0].className, 'phase-dot idle');
  }
  h.state.sessions = [
    completion(), completion('failed', 'TurnFailed'), completion('input', 'InputRequested'),
    completion('read', 'TurnCompleted', {readNotificationId: 'read'}),
    completion('reset', 'InputRequested', {resetting: true}),
  ];
  h.renderSessionAttentionCount();
  assert.equal(h.elements.sessionsAttentionCount.textContent, '3');
  assert.equal(h.elements.sessionsAttentionCount.hidden, false);
  assert.equal(h.elements.sessionsButton.attributes['aria-label'], 'Sessions, 3 needing attention in default');
  h.state.sessions = [completion()];
  h.renderSessionAttentionCount();
  assert.equal(h.elements.sessionsButton.attributes['aria-label'], 'Sessions, 1 needing attention in default');
  h.state.sessions = [];
  h.renderSessionAttentionCount();
  assert.equal(h.elements.sessionsAttentionCount.hidden, true);
  assert.equal(h.elements.sessionsButton.attributes['aria-label'], 'Sessions');
}

{
  const h = harness();
  showResult(h, completion());
  const input = h.refresh(completion('input', 'InputRequested'));
  h.state.selected = input;
  h.markVisibleCompletionRead();
  await new Promise(setImmediate);
  assert.equal(h.requests.length, 0, 'viewing an input request does not acknowledge it as a completion');
  assert.equal(h.createSessionListItem(input).querySelectorAll('.session-attention-label')[0].textContent, 'Input requested');
  h.renderSessionAttentionCount();
  assert.equal(h.elements.sessionsAttentionCount.textContent, '1');
  assert.equal(h.elements.sessionsAttentionCount.hidden, false);
  for (const session of [
    {...input, lastNotification: undefined},
    {...input, resetting: true},
    {...input, userSuspended: true},
    {...input, phase: 'Suspended'},
    {...input, phase: 'Failed'},
    {...input, phase: 'Pending'},
    {...input, phase: undefined},
  ]) {
    const current = h.refresh(session);
    const row = h.createSessionListItem(current);
    assert.equal(row.classList.contains('needs-attention'), false);
    assert.equal(row.querySelectorAll('.session-attention-label').length, 0);
    assert.equal(row.querySelectorAll('.session-attention-dot').length, 0);
    h.renderSessionAttentionCount();
    assert.equal(h.elements.sessionsAttentionCount.hidden, true, 'unavailable input requests do not need attention');
  }
}

for (const [name, hide] of [
  ['background tab', h => { h.document.visibilityState = 'hidden'; }],
  ['unfocused window', h => { h.document.hasFocus = () => false; }],
  ['overview', h => { h.state.consoleView = 'overview'; }],
  ['another conversation view', h => { h.elements.messages.hidden = true; }],
  ['history replay', h => { h.state.replayingHistory = true; }],
  ['history not loaded', h => { h.state.currentView.historyLoaded = false; }],
  ['dialog open', h => { h.document.querySelector = () => ({}); }],
  ['mobile sidebar open', h => { h.elements.sidebar.className = 'open'; }],
  ['result below viewport', (h, divider) => { divider.bounds = {top: 601, bottom: 611, height: 10}; }],
  ['result above viewport', (h, divider) => { divider.bounds = {top: -20, bottom: -10, height: 10}; }],
  ['hidden result', (h, divider) => { divider.bounds = {top: 0, bottom: 0, height: 0}; }],
  ['older result', (h, divider) => { divider.dataset.notificationId = 'older'; }],
]) {
  const h = harness();
  const session = completion();
  const divider = showResult(h, session);
  hide(h, divider);
  h.markVisibleCompletionRead();
  await new Promise(setImmediate);
  assert.equal(h.hasUnreadCompletion(session), true, name);
  assert.equal(h.requests.length, 0, name);
}

{
  const h = harness();
  const session = completion();
  showResult(h, session);
  h.scheduleCompletionRead();
  h.scheduleCompletionRead();
  h.flush();
  assert.equal(h.hasUnreadCompletion(session), true, 'marker waits for the server');
  h.markVisibleCompletionRead();
  assert.equal(h.requests.length, 1, 'concurrent reads are coalesced');
  assert.equal(h.requests[0].url, '/api/sessions/default/session/read');
  assert.equal(h.requests[0].method, 'POST');
  assert.deepEqual(JSON.parse(h.requests[0].body), {uid: 'uid-1', notificationId: 'result-1'});
  await new Promise(setImmediate);
  assert.equal(h.hasUnreadCompletion(session), false);
  assert.equal(h.renders(), 1);
  assert.equal(h.state.sessionListGeneration, 1, 'responses to earlier list requests are invalidated');
  assert.equal(h.createSessionListItem(session).querySelectorAll('.session-attention-label').length, 0);
  const otherBrowser = harness(h.receipts);
  assert.equal(otherBrowser.hasUnreadCompletion(otherBrowser.refresh(completion())), false, 'independent browser receives shared receipt');
  assert.equal(otherBrowser.hasUnreadCompletion(otherBrowser.refresh(completion('result-2'))), true, 'next result is unread');
  assert.equal(otherBrowser.hasUnreadCompletion(otherBrowser.refresh(completion('result-1', 'TurnCompleted', {uid: 'uid-2'}))), true, 'recreated Session is unread');
}

for (const status of [undefined, 409, 429, 500, 503]) {
  const h = harness();
  const session = completion();
  showResult(h, session);
  const send = h.api;
  h.api = async () => { throw Object.assign(new Error('Temporary failure'), {status}); };
  h.markVisibleCompletionRead();
  await new Promise(setImmediate);
  assert.equal(h.hasUnreadCompletion(session), true, 'failed save keeps the marker');
  h.api = send;
  h.syncCompletionReads([session]);
  await new Promise(setImmediate);
  assert.equal(h.hasUnreadCompletion(session), false, 'refresh retries the pending acknowledgment');
}

for (const status of [400, 403, 404]) {
  const h = harness();
  const session = completion();
  showResult(h, session);
  let attempts = 0;
  h.api = async () => {
    attempts++;
    throw Object.assign(new Error('Request rejected'), {status});
  };
  h.markVisibleCompletionRead();
  await new Promise(setImmediate);
  assert.equal(attempts, 1);
  assert.equal(h.hasUnreadCompletion(session), true, 'rejection must not create a local-only receipt');
  for (let i = 0; i < 3; i++) {
    h.syncCompletionReads([session]);
    h.scheduleCompletionRead();
    h.flush();
    await new Promise(setImmediate);
  }
  assert.equal(attempts, 1, `${status} is not retried by polling or visibility events`);
  const later = completion('result-2');
  showResult(h, later);
  h.markVisibleCompletionRead();
  await new Promise(setImmediate);
  assert.equal(attempts, 2, 'a different result can be acknowledged');
  h.receipts.set(session.uid, later.lastNotification.id);
  assert.equal(h.hasUnreadCompletion(h.refresh(later)), false, 'another browser can still acknowledge a rejected result');
  const reloaded = harness();
  showResult(reloaded, session);
  reloaded.markVisibleCompletionRead();
  await new Promise(setImmediate);
  assert.equal(reloaded.hasUnreadCompletion(session), false, 'page reload allows retrying after access is restored');
}

{
  const h = harness();
  const session = completion();
  const older = showResult(h, session, 'older-result');
  const latest = showResult(h, session);
  h.elements.messages.children = [older, latest];
  latest.bounds = {top: 700, bottom: 710, height: 10};
  h.markVisibleCompletionRead();
  assert.equal(h.requests.length, 0, 'scrolling older results does not acknowledge them');
  latest.bounds = {top: 400, bottom: 410, height: 10};
  h.markVisibleCompletionRead();
  await new Promise(setImmediate);
  assert.equal(h.hasUnreadCompletion(session), false);
}

{
  const h = harness();
  const session = completion();
  showResult(h, session, 'live-result');
  h.markVisibleCompletionRead();
  assert.equal(h.requests.length, 0, 'wait for the live result to be published');
  h.state.selected = null;
  const current = h.refresh(completion('live-result'));
  await new Promise(setImmediate);
  assert.equal(h.hasUnreadCompletion(current), false, 'a result seen before polling is remembered even after leaving the conversation');
  assert.equal(h.hasUnreadCompletion(session), true, 'another result remains unread');
}

for (const change of [
  () => completion('result-2'),
  () => completion('result-1', 'TurnCompleted', {uid: 'replacement'}),
]) {
  const h = harness();
  const session = completion();
  showResult(h, session);
  let finish;
  h.api = () => new Promise(resolve => { finish = resolve; });
  h.markVisibleCompletionRead();
  const current = change();
  showResult(h, current);
  finish();
  await new Promise(setImmediate);
  assert.equal(h.hasUnreadCompletion(current), true, 'a delayed acknowledgment cannot clear a different result or Session');
}

{
  const h = harness();
  showResult(h, completion(), 'live-result');
  h.markVisibleCompletionRead();
  h.syncCompletionReads([]);
  h.refresh(completion('live-result'));
  assert.equal(h.requests.length, 0, 'removed Sessions lose pending read intents');
}

console.log('Unread completion tests passed');
})().catch(error => { console.error(error); process.exitCode = 1; });
