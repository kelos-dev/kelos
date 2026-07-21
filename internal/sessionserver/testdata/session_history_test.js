const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

class TestNode {
  constructor(tag, value = '') {
    this.tag = tag;
    this.value = value;
    this.children = [];
    this.parent = null;
    this.hidden = false;
    this.attributes = new Map();
    this.classes = new Set();
    this.classList = {
      add: (...names) => names.forEach((name) => this.classes.add(name)),
      remove: (...names) => names.forEach((name) => this.classes.delete(name)),
    };
  }

  get firstChild() {
    return this.children[0] || null;
  }

  hasChildNodes() {
    return this.children.length > 0;
  }

  removeChild(node) {
    const index = this.children.indexOf(node);
    if (index >= 0) this.children.splice(index, 1);
    node.parent = null;
  }

  append(...nodes) {
    for (const node of nodes) {
      if (node.tag === '#fragment') {
        while (node.firstChild) this.append(node.firstChild);
        continue;
      }
      if (node.parent) node.parent.removeChild(node);
      node.parent = this;
      this.children.push(node);
    }
  }

  replaceChildren(...nodes) {
    for (const child of this.children) child.parent = null;
    this.children = [];
    this.append(...nodes);
  }

  querySelector(selector) {
    if (selector.startsWith('.') && this.classes.has(selector.slice(1))) return this;
    for (const child of this.children) {
      const match = child.querySelector(selector);
      if (match) return match;
    }
    return null;
  }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
  }

  set textContent(value) {
    this.replaceChildren(new TestNode('#text', String(value)));
  }

  get textContent() {
    if (this.tag === '#text') return this.value;
    return this.children.map((child) => child.textContent).join('');
  }

  set className(value) {
    this.classes = new Set(String(value).split(/\s+/).filter(Boolean));
  }

  get className() {
    return [...this.classes].join(' ');
  }
}

global.document = {
  createElement: (tag) => new TestNode(tag),
  createTextNode: (value) => new TestNode('#text', value),
  createDocumentFragment: () => new TestNode('#fragment'),
};

let bottomAnchors;
let socketConnections;

function resetHarness() {
  global.elements = {
    messages: new TestNode('div'),
    queue: new TestNode('div'),
    changesList: new TestNode('div'),
    changesCount: new TestNode('span'),
    changesSummary: new TestNode('span'),
    sidebar: new TestNode('aside'),
    welcome: null,
  };
  global.state = {
    selected: null,
    currentView: null,
    sessionViews: new Map(),
    promptDrafts: new Map(),
    lastEventID: 0,
    assistantSegmentByTurn: new Map(),
    assistantTextByTurn: new Map(),
    tools: new Map(),
    inputs: new Map(),
    diffs: new Map(),
    fileChanges: new Map(),
    queuedMessages: new Map(),
    activeTurn: false,
    interrupting: false,
    replayingHistory: false,
    pinHistoryToBottom: false,
    fileChangesDirty: false,
  };
  bottomAnchors = 0;
  socketConnections = 0;
}

global.maxCachedSessionViews = 5;
global.renderFileChanges = () => {};
global.renderDiffBlock = () => {};
global.savePromptDraft = () => {};
global.restorePromptDraft = () => {};
global.closeSocket = () => {};
global.setActiveView = () => {};
global.renderSessions = () => {};
global.renderHeader = () => {};
global.scheduleBottomAnchor = () => { bottomAnchors++; };
global.connectSocket = () => { socketConnections++; };

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');

function applicationSlice(start, end) {
  const startIndex = application.indexOf(start);
  const endIndex = application.indexOf(end, startIndex);
  assert.notEqual(startIndex, -1, `${start} not found`);
  assert.notEqual(endIndex, -1, `${end} not found`);
  return application.slice(startIndex, endIndex);
}

vm.runInThisContext(applicationSlice('function sessionKey', 'function savePromptDraft'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function parseSessionTimestamp', 'function safeHTTPURL'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function selectSession', 'function renderHeader'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function ensureConversation', 'function trimURLSuffix'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function finishHistoryReplay', 'function handleEvent'), {filename: 'app.js'});

function testSessionViewSaveAndRestore() {
  resetHarness();
  const session = {namespace: 'default', name: 'one', uid: 'uid-one'};
  const view = cachedSessionView(session);
  activateSessionView(view);
  const message = document.createElement('article');
  message.textContent = 'first conversation';
  elements.messages.append(message);
  state.lastEventID = 7;
  state.tools.set('tool-1', {status: 'completed'});

  saveCurrentSessionView();
  assert.equal(elements.messages.hasChildNodes(), false);
  assert.equal(view.messages.textContent, 'first conversation');
  assert.equal(view.lastEventID, 7);

  activateSessionView(createSessionView());
  activateSessionView(view);
  assert.equal(elements.messages.textContent, 'first conversation');
  assert.equal(state.lastEventID, 7);
  assert.equal(state.tools.get('tool-1').status, 'completed');
}

function testSessionViewReset() {
  resetHarness();
  const view = createSessionView();
  view.historyLoaded = true;
  view.statusPlaceholder = true;
  activateSessionView(view);
  elements.messages.append(document.createTextNode('stale history'));
  state.lastEventID = 12;
  state.tools.set('tool-1', {});

  resetCurrentSessionView();
  assert.equal(elements.messages.hasChildNodes(), false);
  assert.equal(state.lastEventID, 0);
  assert.equal(state.tools.size, 0);
  assert.equal(state.replayingHistory, true);
  assert.equal(state.pinHistoryToBottom, true);
  assert.equal(view.historyLoaded, false);
  assert.equal(view.statusPlaceholder, false);
}

function testHistoryReplayCompletion() {
  resetHarness();
  const view = createSessionView();
  activateSessionView(view);
  const loading = document.createElement('div');
  loading.className = 'welcome';
  elements.messages.append(loading);
  view.statusPlaceholder = true;
  state.lastEventID = 9;
  state.replayingHistory = true;
  state.pinHistoryToBottom = true;

  finishHistoryReplay();
  assert.equal(state.replayingHistory, false);
  assert.equal(state.pinHistoryToBottom, false);
  assert.equal(view.historyLoaded, true);
  assert.equal(view.lastEventID, 9);
  assert.equal(view.statusPlaceholder, false);
  assert.equal(elements.messages.hasChildNodes(), false);
  assert.equal(bottomAnchors, 1);
}

function testReselectRefreshesStatusPlaceholder() {
  resetHarness();
  const first = {
    namespace: 'default',
    name: 'first',
    uid: 'uid-first',
    phase: 'Pending',
    message: 'Waiting for the Pod',
  };
  const second = {
    namespace: 'default',
    name: 'second',
    uid: 'uid-second',
    phase: 'Pending',
    message: 'Waiting for another Pod',
  };

  selectSession(first);
  assert.match(elements.messages.textContent, /Waiting for the Pod/);
  assert.equal(state.currentView.statusPlaceholder, true);
  selectSession(second);
  selectSession({...first, phase: 'Failed', message: 'Pod startup failed'});

  assert.match(elements.messages.textContent, /Pod startup failed/);
  assert.doesNotMatch(elements.messages.textContent, /Waiting for the Pod/);
  assert.equal(state.currentView.statusPlaceholder, true);
  assert.equal(socketConnections, 0);
}

function testSessionTimestampFormatting() {
  const now = Date.parse('2026-07-21T12:00:00Z');
  const active = {active: true, lastActivityAt: '2026-07-21T11:52:00Z'};
  assert.equal(formatSessionRecency(active, true, now), 'Now');
  assert.equal(formatSessionRecency(active, false, now), 'Active now');

  const idle = {active: false, lastActivityAt: '2026-07-21T11:52:00Z', createdAt: '2026-07-20T12:00:00Z'};
  assert.equal(formatSessionRecency(idle, true, now), '8m');
  assert.equal(formatSessionRecency(idle, false, now), 'Last active 8 minutes ago');

  const pending = {createdAt: '2026-07-21T09:00:00Z'};
  assert.equal(formatSessionRecency(pending, true, now), '3h');
  assert.equal(formatSessionRecency(pending, false, now), 'Created 3 hours ago');
  assert.equal(formatSessionRecency({}, false, now), '');
}

function testSessionTimestampElement() {
  const session = {active: false, lastActivityAt: new Date(Date.now() - 60000).toISOString()};
  const timestamp = createSessionTimestamp(session, true, 'session-item-time');
  assert.equal(timestamp.tag, 'time');
  assert.equal(timestamp.className, 'session-item-time');
  assert.equal(timestamp.dateTime, session.lastActivityAt);
  assert.match(timestamp.title, /^Last active /);
  assert.equal(timestamp.attributes.get('aria-label'), timestamp.title);
}

testSessionViewSaveAndRestore();
testSessionViewReset();
testHistoryReplayCompletion();
testReselectRefreshesStatusPlaceholder();
testSessionTimestampFormatting();
testSessionTimestampElement();

process.stdout.write('Session history tests passed\n');
