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
    this.dataset = {};
    this.attributes = new Map();
    this.classes = new Set();
    this.listeners = new Map();
    this.style = {};
    this.scrollTop = 0;
    this.classList = {
      add: (...names) => names.forEach((name) => this.classes.add(name)),
      remove: (...names) => names.forEach((name) => this.classes.delete(name)),
    };
  }

  get firstChild() {
    return this.children[0] || null;
  }

  get lastChild() {
    return this.children[this.children.length - 1] || null;
  }

  get scrollHeight() {
    return this.children.length * 20;
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

  prepend(...nodes) {
    const added = [];
    for (const node of nodes) {
      if (node.tag === '#fragment') {
        while (node.firstChild) {
          const child = node.firstChild;
          node.removeChild(child);
          added.push(child);
        }
        continue;
      }
      if (node.parent) node.parent.removeChild(node);
      added.push(node);
    }
    for (const node of added) node.parent = this;
    this.children = [...added, ...this.children];
  }

  remove() {
    if (this.parent) this.parent.removeChild(this);
  }

  replaceChildren(...nodes) {
    for (const child of this.children) child.parent = null;
    this.children = [];
    this.append(...nodes);
  }

  querySelector(selector) {
    if ((selector.startsWith('.') && this.classes.has(selector.slice(1))) || this.tag === selector) return this;
    for (const child of this.children) {
      const match = child.querySelector(selector);
      if (match) return match;
    }
    return null;
  }

  querySelectorAll(selector) {
    const matches = [];
    for (const child of this.children) {
      const classMatch = selector === '.file-change[open]'
        ? child.classes.has('file-change') && child.open
        : selector.startsWith('.') && child.classes.has(selector.slice(1));
      if (classMatch || child.tag === selector) matches.push(child);
      matches.push(...child.querySelectorAll(selector));
    }
    return matches;
  }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
  }

  addEventListener(name, listener) {
    this.listeners.set(name, listener);
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
let interruptRequests;
let socketConnections;
let progressTimers;
let toasts;

global.window = {
  clearInterval: (timer) => progressTimers.delete(timer),
  matchMedia: () => ({matches: false}),
  setInterval: (callback) => {
    const timer = progressTimers.size + 1;
    progressTimers.set(timer, callback);
    return timer;
  },
};
global.WebSocket = {OPEN: 1};

function resetHarness() {
  global.elements = {
    messages: new TestNode('div'),
    queue: new TestNode('div'),
    changesList: new TestNode('div'),
    changesCount: new TestNode('span'),
    changesSummary: new TestNode('span'),
    composerHint: new TestNode('span'),
    input: new TestNode('textarea'),
    send: new TestNode('button'),
    progress: new TestNode('div'),
    progressLabel: new TestNode('span'),
    progressElapsed: new TestNode('span'),
    runtimeStatus: new TestNode('div'),
    sidebar: new TestNode('aside'),
    welcome: null,
  };
  global.state = {
    selected: null,
    currentView: null,
    sessionViews: new Map(),
    socket: null,
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
    activeTurnID: '',
    activeTurnStartedAt: 0,
    waitingForInput: false,
    interrupting: false,
    runtimeStatus: null,
    progressTimer: null,
    replayingHistory: false,
    historyCursor: '',
    historyLastEventID: 0,
    historyPageLoading: false,
    historyPageReading: false,
    historyPageCursor: '',
    historyPageEvents: [],
    historyRequestID: '',
    runtimeRecoveryActive: false,
    pinHistoryToBottom: false,
    fileChangesDirty: false,
  };
  bottomAnchors = 0;
  interruptRequests = 0;
  socketConnections = 0;
  progressTimers = new Map();
  toasts = [];
}

global.maxCachedSessionViews = 5;
global.renderFileChanges = () => {};
global.renderDiffBlock = () => {};
global.renderMessageMarkdown = (element, text) => { element.textContent = text || ''; };
global.providerInitials = () => 'A';
global.savePromptDraft = () => {};
global.restorePromptDraft = () => {};
global.closeSocket = () => {};
global.setActiveView = () => {};
global.renderSessions = () => {};
global.renderHeader = () => {};
global.resizeComposer = () => {};
global.scheduleBottomAnchor = () => { bottomAnchors++; };
global.connectSocket = () => { socketConnections++; };
global.updateComposerAction = () => {};
global.updateFileChangesHeader = () => {};
global.endAssistantSegment = () => {};
global.acceptQueuedMessage = () => {};
global.renderInputRequest = () => {};
global.resolveInputCard = () => {};
global.scrollToBottom = () => {};
global.interruptActiveTurn = () => { interruptRequests++; };
global.showToast = (message) => { toasts.push(message); };

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');

function applicationSlice(start, end) {
  const startIndex = application.indexOf(start);
  const endIndex = application.indexOf(end, startIndex);
  assert.notEqual(startIndex, -1, `${start} not found`);
  assert.notEqual(endIndex, -1, `${end} not found`);
  return application.slice(startIndex, endIndex);
}

vm.runInThisContext(applicationSlice('function sessionKey', 'function savePromptDraft'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function savePromptDraft', 'function providerLabel'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function parseSessionTimestamp', 'function safeHTTPURL'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function selectSession', 'function renderHeader'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function usesTouchComposer', 'function closeSocket'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function ensureConversation', 'function trimURLSuffix'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function completedAssistantText', 'function handleEvent'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function handleEvent', 'function renderUser'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function renderUser', 'function renderTool'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function renderTool', 'function renderInputRequest'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function renderDiff', 'function setActiveView'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function renderError', 'function scrollToBottom'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function submitComposer', "elements.composer.addEventListener('submit'"), {filename: 'app.js'});

function testSessionViewSaveAndRestore() {
  resetHarness();
  const session = {namespace: 'default', name: 'one', uid: 'uid-one'};
  const view = cachedSessionView(session);
  activateSessionView(view);
  const message = document.createElement('article');
  message.textContent = 'first conversation';
  elements.messages.append(message);
  state.lastEventID = 7;
  state.historyCursor = 'cursor-1';
  state.tools.set('tool-1', {status: 'completed'});

  saveCurrentSessionView();
  assert.equal(elements.messages.hasChildNodes(), false);
  assert.match(view.messages.textContent, /first conversation/);
  assert.equal(view.lastEventID, 7);
  assert.equal(view.historyCursor, 'cursor-1');

  activateSessionView(createSessionView());
  activateSessionView(view);
  assert.match(elements.messages.textContent, /first conversation/);
  assert.equal(state.lastEventID, 7);
  assert.equal(state.historyCursor, 'cursor-1');
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
  state.historyCursor = 'cursor-1';
  state.tools.set('tool-1', {});

  resetCurrentSessionView();
  assert.equal(elements.messages.hasChildNodes(), false);
  assert.equal(state.lastEventID, 0);
  assert.equal(state.historyCursor, '');
  assert.equal(state.tools.size, 0);
  assert.equal(state.replayingHistory, true);
  assert.equal(state.pinHistoryToBottom, true);
  assert.equal(view.historyLoaded, false);
  assert.equal(view.statusPlaceholder, false);
  assert.equal(view.historyCursor, '');
}

function testSessionProgressLifecycle() {
  resetHarness();
  const view = createSessionView();
  activateSessionView(view);
  assert.equal(elements.progress.hidden, true);

  const startedAt = Date.parse('2026-07-23T12:00:00Z');
  handleEvent({type: 'turn.started', turnId: 'turn-1', timestamp: '2026-07-23T12:00:00Z'});
  assert.equal(state.activeTurnStartedAt, startedAt);
  renderSessionProgress(startedAt + 65000);
  assert.equal(elements.progress.hidden, false);
  assert.equal(elements.progress.dataset.state, 'working');
  assert.equal(elements.progressLabel.textContent, 'Working');
  assert.equal(elements.progressElapsed.textContent, '(1m 05s)');

  handleEvent({type: 'input.requested', turnId: 'turn-1', inputId: 'input-1'});
  assert.equal(elements.progress.dataset.state, 'waiting');
  assert.equal(elements.progressLabel.textContent, 'Waiting for input');

  handleEvent({type: 'input.resolved', turnId: 'turn-1', inputId: 'input-1'});
  assert.equal(elements.progress.dataset.state, 'working');

  handleEvent({type: 'turn.interrupting', turnId: 'turn-1'});
  assert.equal(elements.progress.dataset.state, 'interrupting');
  assert.equal(elements.progressLabel.textContent, 'Interrupting');

  handleEvent({type: 'error', turnId: 'turn-1', status: 'rejected', text: 'Still working'});
  assert.equal(elements.progress.dataset.state, 'working');

  handleEvent({type: 'turn.completed', turnId: 'turn-1', status: 'completed'});
  assert.equal(elements.progress.hidden, true);
  assert.equal(state.progressTimer, null);
}

function testComposerInterruptsWhileInputIsDisabled() {
  resetHarness();
  const sent = [];
  state.socket = {
    readyState: WebSocket.OPEN,
    send: (message) => sent.push(message),
  };
  state.activeTurn = true;
  elements.input.disabled = true;
  elements.input.value = 'preserved draft';

  updateComposerAction();

  assert.equal(elements.send.dataset.action, 'interrupt');
  assert.equal(elements.send.attributes.get('aria-label'), 'Interrupt active work');
  assert.equal(elements.send.disabled, false);
  assert.equal(elements.composerHint.textContent, 'Click ■ to interrupt');
  assert.equal(elements.input.value, 'preserved draft');
  window.matchMedia = () => ({matches: true});
  updateComposerAction();
  assert.equal(elements.composerHint.textContent, 'Tap ■ to interrupt · Return for a new line');
  window.matchMedia = () => ({matches: false});

  submitComposer();
  assert.equal(interruptRequests, 1);
  assert.deepEqual(sent, []);
  assert.equal(elements.input.value, 'preserved draft');

  state.interrupting = true;
  updateComposerAction();
  assert.equal(elements.send.disabled, true);
}

function testSessionProgressSurvivesCachedViewSwitch() {
  resetHarness();
  const activeView = createSessionView();
  activateSessionView(activeView);
  state.activeTurn = true;
  state.activeTurnID = 'turn-1';
  state.activeTurnStartedAt = Date.parse('2026-07-23T12:00:00Z');
  state.waitingForInput = true;
  refreshSessionProgress();
  saveCurrentSessionView();

  activateSessionView(createSessionView());
  assert.equal(elements.progress.hidden, true);
  activateSessionView(activeView);
  assert.equal(elements.progress.hidden, false);
  assert.equal(elements.progressLabel.textContent, 'Waiting for input');
  assert.equal(state.activeTurnStartedAt, Date.parse('2026-07-23T12:00:00Z'));
}

function testSessionProgressElapsedFormatting() {
  assert.equal(formatSessionProgressElapsed(59000), '59s');
  assert.equal(formatSessionProgressElapsed(60000), '1m 00s');
  assert.equal(formatSessionProgressElapsed(7389000), '2h 03m 09s');
}

function testRuntimeStatusLifecycle() {
  resetHarness();
  const view = createSessionView();
  activateSessionView(view);
  assert.equal(elements.runtimeStatus.hidden, true);

  handleEvent({
    type: 'runtime.status',
    runtime: {
      sessionName: 'fix-cli-connect',
      agentType: 'codex',
      model: 'gpt-5.6-sol',
      effort: 'xhigh',
      workingDir: '/home/agent/workspace/repo',
      homeDir: '/home/agent',
      branch: 'main',
      usage: {
        inputTokens: 15900000,
        outputTokens: 59100,
        contextTokens: 90960,
        contextWindow: 200000,
      },
      weeklyLimit: {usedPercent: 52},
    },
  });

  const expected = 'fix-cli-connect · codex · gpt-5.6-sol xhigh · ~/workspace/repo · main · Context 42% used · weekly 48% left · 15.9M in · 59.1K out';
  assert.equal(elements.runtimeStatus.hidden, false);
  assert.equal(elements.runtimeStatus.textContent, expected);
  assert.equal(elements.runtimeStatus.title, expected);
  assert.equal(view.runtimeStatus, state.runtimeStatus);

  saveCurrentSessionView();
  activateSessionView(createSessionView());
  assert.equal(elements.runtimeStatus.hidden, true);
  activateSessionView(view);
  assert.equal(elements.runtimeStatus.textContent, expected);

  resetCurrentSessionView();
  assert.equal(elements.runtimeStatus.hidden, true);
  assert.equal(state.runtimeStatus, null);
}

function testRuntimeStatusTreatsOmittedContextTokensAsZero() {
  assert.equal(sessionRuntimeContextUsedPercent({contextWindow: 200000}), 0);
}

function testTurnDividerShowsDuration() {
  resetHarness();
  handleEvent({type: 'turn.started', turnId: 'turn-1', timestamp: '2026-08-08T12:00:00Z'});
  handleEvent({type: 'turn.completed', turnId: 'turn-1', status: 'completed', timestamp: '2026-08-08T12:05:19Z'});

  const divider = elements.messages.querySelector('.turn-divider');
  assert.equal(divider.textContent, 'Worked for 5m 19s');
}

function testUntimestampedHistoryDividerOmitsDuration() {
  resetHarness();
  handleEvent({type: 'history.start'});
  handleEvent({type: 'turn.started', turnId: 'turn-1'});
  handleEvent({type: 'turn.completed', turnId: 'turn-1', status: 'completed'});

  const divider = elements.messages.querySelector('.turn-divider');
  assert.equal(divider.textContent, '');
}

function testUntimestampedLiveTurnUsesLocalDuration() {
  resetHarness();
  const originalNow = Date.now;
  let now = Date.parse('2026-08-08T12:00:00Z');
  Date.now = () => now;
  try {
    handleEvent({type: 'turn.started', turnId: 'turn-1'});
    now += 65_000;
    handleEvent({type: 'turn.completed', turnId: 'turn-1', status: 'completed'});
  } finally {
    Date.now = originalNow;
  }

  const divider = elements.messages.querySelector('.turn-divider');
  assert.equal(divider.textContent, 'Worked for 1m 05s');
}

function testRuntimeRecoveryDividerOmitsDuration() {
  resetHarness();
  handleEvent({type: 'history.start'});
  handleEvent({type: 'turn.started', turnId: 'turn-1', timestamp: '2026-08-08T12:00:00Z'});
  handleEvent({type: 'runtime.recovered', text: 'Session runtime restarted'});
  handleEvent({type: 'input.resolved', turnId: 'turn-1', inputId: 'input-1', status: 'cancelled'});
  handleEvent({type: 'turn.completed', turnId: 'turn-1', status: 'interrupted', timestamp: '2026-08-08T13:00:00Z'});

  const divider = elements.messages.querySelector('.turn-divider');
  assert.equal(divider.textContent, '');
}

function testSessionResetClearsPromptDraft() {
  resetHarness();
  const session = {namespace: 'default', name: 'one', uid: 'uid-one', phase: 'Ready'};
  state.selected = session;
  elements.input.value = 'unsent prompt';
  savePromptDraft(session);

  clearPromptDraft(session);
  selectSession({...session, resetting: true});

  assert.equal(elements.input.value, '');
  assert.equal(state.promptDrafts.has(sessionKey(session)), false);
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

function testProjectedHistoryRestoresStateAndReconnectHighWater() {
  resetHarness();
  const view = createSessionView();
  view.historyLoaded = true;
  view.historyCursor = 'stale-cursor';
  activateSessionView(view);

  const fileDiff = 'diff --git a/old.txt b/old.txt\n--- a/old.txt\n+++ b/old.txt\n-old\n+new';
  handleEvent({
    type: 'history.start',
    journalId: 'journal-1',
    lastEventId: 12,
    historyLimited: true,
    historyCursor: 'cursor-1',
  });
  handleEvent({type: 'assistant.delta', id: 9, text: 'working'});
  handleEvent({
    type: 'history.end',
    historyState: {
      activeTurnId: 'turn-1',
      activeTurnStarted: '2026-08-08T12:00:00Z',
      waitingForInput: true,
      turnInterrupting: true,
      queuedTurns: [{turnId: 'turn-2', text: 'queued request'}],
      fileDiff,
    },
  });

  assert.equal(state.lastEventID, 12);
  assert.equal(view.lastEventID, 12);
  assert.equal(state.historyCursor, 'cursor-1');
  assert.equal(view.historyCursor, 'cursor-1');
  assert.equal(state.activeTurn, true);
  assert.equal(state.activeTurnID, 'turn-1');
  assert.equal(state.activeTurnStartedAt, Date.parse('2026-08-08T12:00:00Z'));
  assert.equal(state.waitingForInput, true);
  assert.equal(state.interrupting, true);
  assert.equal(state.assistantTextByTurn.get('turn-1'), 'working');
  assert.equal(state.assistantTextByTurn.has('current'), false);
  assert.equal(state.queuedMessages.get('turn-2').event.text, 'queued request');
  assert.equal(state.fileChanges.get('old.txt'), fileDiff);
  assert.equal(elements.messages.querySelector('.history-page-control').textContent, 'Load earlier messages');

  handleEvent({type: 'assistant.message', id: 13, turnId: 'turn-1', text: 'working done'});
  assert.equal(elements.messages.textContent.match(/working done/g).length, 1);
}

function testOlderHistoryPageIsPrependedWithoutChangingLiveState() {
  resetHarness();
  const view = createSessionView();
  view.historyLoaded = true;
  view.historyCursor = 'cursor-1';
  view.lastEventID = 50;
  activateSessionView(view);
  state.activeTurn = true;
  state.activeTurnID = 'turn-live';
  state.activeTurnStartedAt = Date.parse('2026-08-08T12:00:00Z');
  state.waitingForInput = true;
  const sent = [];
  state.socket = {readyState: 1, send: (payload) => sent.push(JSON.parse(payload))};
  const recent = document.createElement('article');
  recent.textContent = 'recent response';
  elements.messages.append(recent);
  renderHistoryControl();

  requestOlderHistory();
  requestOlderHistory();
  assert.equal(sent.length, 1);
  assert.equal(sent[0].type, 'history');
  assert.equal(sent[0].historyCursor, 'cursor-1');
  assert.ok(sent[0].requestId);

  handleEvent({type: 'history.start', historyPage: true, requestId: sent[0].requestId, historyCursor: 'cursor-2'});
  elements.messages.scrollTop = 15;
  const previousTop = elements.messages.scrollTop;
  const previousHeight = elements.messages.scrollHeight;
  handleEvent({type: 'user.message', id: 1, text: 'earlier request'});
  handleEvent({type: 'assistant.message', id: 2, text: 'earlier response'});
  handleEvent({type: 'turn.completed', id: 3, status: 'interrupted'});
  assert.doesNotMatch(elements.messages.textContent, /earlier response/);
  handleEvent({type: 'history.end', historyPage: true, requestId: sent[0].requestId});

  const text = elements.messages.textContent;
  assert.ok(text.indexOf('earlier request') < text.indexOf('recent response'));
  assert.ok(text.indexOf('earlier response') < text.indexOf('recent response'));
  assert.equal(state.activeTurn, true);
  assert.equal(state.activeTurnID, 'turn-live');
  assert.equal(state.activeTurnStartedAt, Date.parse('2026-08-08T12:00:00Z'));
  assert.equal(state.waitingForInput, true);
  assert.equal(state.interrupting, false);
  assert.equal(state.lastEventID, 50);
  assert.equal(state.historyCursor, 'cursor-2');
  assert.equal(state.historyPageLoading, false);
  assert.deepEqual(toasts, []);
  assert.ok(elements.messages.scrollHeight > previousHeight);
  assert.equal(elements.messages.scrollTop, previousTop + elements.messages.scrollHeight - previousHeight);

  requestOlderHistory();
  assert.equal(sent.length, 2);
  assert.equal(sent[1].historyCursor, 'cursor-2');
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
  assert.equal(formatSessionRecency(active, true, now), '8m');
  assert.equal(formatSessionRecency(active, false, now), 'Last active 8 minutes ago');

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

function testToolOutputRendering() {
  resetHarness();
  state.replayingHistory = true;
  renderTool({type: 'tool.started', toolId: 'tool-1', toolName: 'make test', status: 'running'});
  completeTool({
    type: 'tool.completed',
    toolId: 'tool-1',
    status: 'completed',
    output: 'line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\n',
  });

  const card = state.tools.get('tool-1');
  assert.equal(card.dataset.status, 'completed');
  assert.equal(card.querySelector('.tool-icon').textContent, '✓');
  assert.equal(
    card.querySelector('.tool-output-preview').textContent,
    'line 1\nline 2\n… +4 lines\nline 7\nline 8',
  );
  assert.equal(card.querySelector('.tool-output-summary').textContent, 'Show all 8 lines');
  assert.equal(
    card.querySelector('.tool-output-full').textContent,
    'line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8',
  );
}

function testToolOutputRenderingNormalizesCarriageReturns() {
  resetHarness();
  state.replayingHistory = true;
  renderTool({type: 'tool.started', toolId: 'tool-1', toolName: 'build', status: 'running'});
  completeTool({
    type: 'tool.completed',
    toolId: 'tool-1',
    status: 'completed',
    output: 'step 1\rstep 2\rstep 3\rstep 4\rstep 5\rstep 6\rstep 7\rstep 8\r',
  });

  const card = state.tools.get('tool-1');
  assert.equal(
    card.querySelector('.tool-output-preview').textContent,
    'step 1\nstep 2\n… +4 lines\nstep 7\nstep 8',
  );
  assert.equal(card.querySelector('.tool-output-summary').textContent, 'Show all 8 lines');
  assert.equal(
    card.querySelector('.tool-output-full').textContent,
    'step 1\nstep 2\nstep 3\nstep 4\nstep 5\nstep 6\nstep 7\nstep 8',
  );
}

function testHistoryToolCompletionRendersOutputWithoutStart() {
  resetHarness();
  state.replayingHistory = true;
  completeTool({
    type: 'tool.completed',
    toolId: 'tool-1',
    toolName: 'search',
    status: 'completed',
    output: 'result',
  });

  const card = state.tools.get('tool-1');
  assert.equal(card.querySelector('.tool-icon').textContent, '✓');
  assert.equal(card.querySelector('.tool-output-preview').textContent, 'result');
}

testSessionViewSaveAndRestore();
testSessionViewReset();
testSessionProgressLifecycle();
testComposerInterruptsWhileInputIsDisabled();
testSessionProgressSurvivesCachedViewSwitch();
testSessionProgressElapsedFormatting();
testRuntimeStatusLifecycle();
testRuntimeStatusTreatsOmittedContextTokensAsZero();
testTurnDividerShowsDuration();
testUntimestampedHistoryDividerOmitsDuration();
testUntimestampedLiveTurnUsesLocalDuration();
testRuntimeRecoveryDividerOmitsDuration();
testSessionResetClearsPromptDraft();
testHistoryReplayCompletion();
testProjectedHistoryRestoresStateAndReconnectHighWater();
testOlderHistoryPageIsPrependedWithoutChangingLiveState();
testReselectRefreshesStatusPlaceholder();
testSessionTimestampFormatting();
testSessionTimestampElement();
testToolOutputRendering();
testToolOutputRenderingNormalizesCarriageReturns();
testHistoryToolCompletionRendersOutputWithoutStart();

process.stdout.write('Session history tests passed\n');
