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
    this.bounds = {top: 0, bottom: 0};
    this.scrollIntoViewOptions = null;
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
      const selectorClasses = selector.startsWith('.') ? selector.slice(1).split('.') : [];
      const classMatch = selector === '.file-change[open]'
        ? child.classes.has('file-change') && child.open
        : selectorClasses.length > 0 && selectorClasses.every(name => child.classes.has(name));
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

  showModal() { this.open = true; }
  close() { this.open = false; }
  focus(options) { this.focused = true; this.focusOptions = options; }
  select() { this.selected = true; }

  getBoundingClientRect() {
    return this.bounds;
  }

  scrollIntoView(options) {
    this.scrollIntoViewOptions = options;
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
  body: new TestNode('body'),
};

let bottomAnchors;
let interruptRequests;
let socketConnections;
let progressTimers;
let animationFrames;
let toasts;
let closeSocketRequests;

global.window = {
  cancelAnimationFrame: frame => { animationFrames[frame - 1] = () => {}; },
  clearInterval: (timer) => progressTimers.delete(timer),
  confirm: () => true,
  matchMedia: () => ({matches: false}),
  requestAnimationFrame: (callback) => {
    animationFrames.push(callback);
    return animationFrames.length;
  },
  setInterval: (callback) => {
    const timer = progressTimers.size + 1;
    progressTimers.set(timer, callback);
    return timer;
  },
};
global.WebSocket = {OPEN: 1};
global.sessionTerminal = {available: session => session?.phase === 'Ready' && !session.resetting && !session.userSuspended, sync() {}};

function resetHarness() {
  global.elements = {
    messages: new TestNode('div'),
    pending: new TestNode('div'),
    changesList: new TestNode('div'),
    changesCount: new TestNode('span'),
    changesSummary: new TestNode('span'),
    currentRequest: new TestNode('div'),
    currentRequestButton: new TestNode('button'),
    currentRequestText: new TestNode('span'),
    sessionsView: new TestNode('main'),
    composerHint: new TestNode('span'),
    input: new TestNode('textarea'),
    attachFiles: new TestNode('button'),
    promptsButton: new TestNode('button'),
    promptsDialog: new TestNode('dialog'),
    promptsTitle: new TestNode('h2'),
    promptsList: new TestNode('div'),
    promptsStatus: new TestNode('p'),
    promptsMore: new TestNode('button'),
    pendingAttachments: new TestNode('div'),
    send: new TestNode('button'),
    progress: new TestNode('div'),
    progressLabel: new TestNode('span'),
    progressElapsed: new TestNode('span'),
    runtimeStatus: new TestNode('div'),
    sidebar: new TestNode('aside'),
    displayNameButton: new TestNode('button'),
    terminalTab: new TestNode('button'),
    terminalView: new TestNode('section'),
    viewChoice: new TestNode('select'),
    terminalChoice: new TestNode('option'),
    suspendButton: new TestNode('button'),
    resumeButton: new TestNode('button'),
    resetButton: new TestNode('button'),
    deleteButton: new TestNode('button'),
    conversationTab: new TestNode('button'),
    changesTab: new TestNode('button'),
    title: new TestNode('h1'),
    meta: new TestNode('div'),
    connection: new TestNode('div'),
    welcome: null,
  };
  elements.currentRequest.hidden = true;
  elements.terminalView.hidden = true;
  elements.connection.append(new TestNode('span'), new TestNode('span'));
  global.state = {
    sessions: [],
    selected: null,
    currentView: null,
    sessionViews: new Map(),
    socket: null,
    bottomScrollFrame: null,
    promptDrafts: new Map(),
    attachmentDrafts: new Map(),
    sendingMessage: false,
    lastEventID: 0,
    assistantSegmentByTurn: new Map(),
    assistantTextByTurn: new Map(),
    tools: new Map(),
    inputs: new Map(),
    diffs: new Map(),
    fileChanges: new Map(),
    pendingMessage: null,
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
    promptsRequestID: '',
    promptsCursor: '',
    promptJumpTarget: null,
    historyPageReading: false,
    historyPageCursor: '',
    historyPageEvents: [],
    historyRequestID: '',
    runtimeRecoveryActive: false,
    pinHistoryToBottom: false,
    fileChangesDirty: false,
    namespace: 'default',
    namespaceGeneration: 0,
    sessionListGeneration: 0,
  };
  bottomAnchors = 0;
  interruptRequests = 0;
  socketConnections = 0;
  closeSocketRequests = 0;
  progressTimers = new Map();
  animationFrames = [];
  toasts = [];
}

global.maxCachedSessionViews = 5;
global.renderFileChanges = () => {};
global.renderDiffBlock = () => {};
global.renderMessageMarkdown = (element, text) => { element.textContent = text || ''; };
global.providerInitials = () => 'A';
global.savePromptDraft = () => {};
global.restorePromptDraft = () => {};
global.closeSocket = () => {
  closeSocketRequests++;
  state.socket = null;
  closePromptHistory();
};
global.setActiveView = () => {};
global.renderSessions = () => {};
global.renderHeader = () => {};
global.renderSectionOptions = () => {};
global.renderSelectedSessionSection = () => {};
global.createPullRequestLink = () => null;
global.resizeComposer = () => {};
global.scheduleBottomAnchor = () => { bottomAnchors++; };
global.connectSocket = () => { socketConnections++; };
global.updateComposerAction = () => {};
global.updateFileChangesHeader = () => {};
global.endAssistantSegment = () => {};
global.acceptPendingMessage = () => {};
global.renderInputRequest = () => {};
global.resolveInputCard = () => {};
global.scrollToBottom = () => {};
global.interruptActiveTurn = () => { interruptRequests++; };
global.showToast = (message) => { toasts.push(message); };
global.notifySessionEvent = () => {};
global.pruneBrowserNotifications = () => {};
global.closeSessionSectionEditor = () => {};

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');

function applicationSlice(start, end) {
  const startIndex = application.indexOf(start);
  const endIndex = application.indexOf(end, startIndex);
  assert.notEqual(startIndex, -1, `${start} not found`);
  assert.notEqual(endIndex, -1, `${end} not found`);
  return application.slice(startIndex, endIndex);
}

vm.runInThisContext(applicationSlice('function errorMessage', 'function requireElements'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function sessionKey', 'function savePromptDraft'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function savePromptDraft', 'function providerLabel'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function providerLabel', 'function parseSessionTimestamp'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function parseSessionTimestamp', 'function safeHTTPURL'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function selectSession', 'function renderHeader'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('async function loadSessions', 'async function loadConfig'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function usesTouchComposer', 'function closeSocket'), {filename: 'app.js'});
const renderSessionHeader = vm.runInThisContext(
  `(() => {${applicationSlice('function renderHeader', 'function usesTouchComposer')} return renderHeader;})()`,
  {filename: 'app.js'},
);
vm.runInThisContext(applicationSlice('function ensureConversation', 'function trimURLSuffix'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('async function writeClipboardText', 'async function copyCodeBlock'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function completedAssistantText', 'function handleEvent'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function handleEvent', 'function renderUser'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function renderUser', 'function renderTool'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function renderTool', 'function renderInputRequest'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function renderDiff', 'function setActiveView'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function renderError', 'function scrollToBottom'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function currentAttachmentFiles', "elements.composer.addEventListener('submit'"), {filename: 'app.js'});

async function testPromptHistoryBrowseAndReuse() {
  resetHarness();
  state.selected = {namespace: 'default', name: 'one', uid: 'uid-one'};
  const sent = [];
  state.socket = {readyState: WebSocket.OPEN, send: payload => sent.push(JSON.parse(payload))};
  state.activeTurn = true;
  state.lastEventID = 50;
  state.historyCursor = 'transcript-cursor';
  elements.input.value = 'unsent draft';
  openPromptHistory();
  requestPromptHistory();
  assert.equal(elements.promptsDialog.open, true);
  assert.equal(elements.promptsTitle.textContent, 'Prompt history · one');
  assert.equal(sent.length, 1);
  assert.equal(sent[0].type, 'prompts');
  assert.equal(sent[0].historyCursor, '');
  assert.ok(sent[0].requestId);

  handleEvent({type: 'prompts', requestId: 'stale', prompts: [{id: 0, text: 'wrong session'}]});
  assert.equal(elements.promptsList.hasChildNodes(), false);
  handleEvent({type: 'prompts', requestId: sent[0].requestId, historyCursor: 'earlier-prompts', prompts: [
    {id: 3, text: 'first\nrequest'},
    {id: 5, text: '<script>plain text</script>', attachments: [{id: 'file-1', name: 'notes.txt'}]},
  ]});
  let items = elements.promptsList.querySelectorAll('.prompt-history-item');
  assert.equal(items.length, 2);
  assert.equal(items[0].querySelector('pre').textContent, '<script>plain text</script>');
  assert.equal(items[0].querySelector('p').textContent, 'Attachments: notes.txt');
  assert.equal(items[1].querySelector('pre').textContent, 'first\nrequest');
  assert.equal(elements.promptsMore.hidden, false);

  requestPromptHistory();
  assert.equal(sent[1].type, 'prompts');
  assert.equal(sent[1].historyCursor, 'earlier-prompts');
  handleEvent({type: 'prompts', requestId: sent[1].requestId, prompts: [{id: 1, text: 'earliest request'}]});
  items = elements.promptsList.querySelectorAll('.prompt-history-item');
  assert.equal(items[2].querySelector('pre').textContent, 'earliest request');
  assert.equal(elements.promptsMore.hidden, true);
  assert.equal(elements.promptsStatus.textContent, 'All retained prompts are loaded.');
  assert.equal(state.lastEventID, 50);
  assert.equal(state.historyCursor, 'transcript-cursor');
  assert.equal(state.activeTurn, true);
  assert.equal(elements.messages.hasChildNodes(), false);

  for (const clipboard of [undefined, {writeText: async () => { throw new Error('denied'); }}]) {
    Object.defineProperty(global, 'navigator', {configurable: true, value: {clipboard}});
    let copied;
    document.execCommand = command => {
      assert.equal(command, 'copy');
      assert.equal(elements.promptsDialog.open, true);
      const textarea = elements.promptsDialog.querySelector('textarea');
      assert.ok(textarea, 'clipboard fallback must be inside the open dialog');
      assert.equal(textarea.selected, true);
      copied = textarea.value;
      return true;
    };
    await items[1].querySelectorAll('button').find(button => button.textContent === 'Copy text').listeners.get('click')();
    assert.equal(copied, 'first\nrequest');
    assert.equal(toasts.at(-1), 'Prompt copied');
    assert.equal(elements.promptsDialog.querySelector('textarea'), null);
    assert.equal(document.body.hasChildNodes(), false);
  }
  items[1].querySelectorAll('button').find(button => button.textContent === 'Use text').listeners.get('click')();
  assert.equal(elements.input.value, 'unsent draft\n\nfirst\nrequest');
  assert.equal(state.promptDrafts.get(sessionKey(state.selected)), elements.input.value);
  assert.equal(elements.input.focused, true);
  assert.equal(elements.promptsDialog.open, false);
  assert.equal(sent.length, 2);
}

function testPromptHistoryErrorEmptyAndSessionSwitch() {
  resetHarness();
  const session = {namespace: 'default', name: 'one', uid: 'uid-one', phase: 'Ready'};
  state.selected = session;
  const sent = [];
  state.socket = {readyState: WebSocket.OPEN, send: payload => sent.push(JSON.parse(payload))};
  openPromptHistory();
  handleEvent({type: 'error', requestId: sent[0].requestId, text: 'Could not load prompts'});
  assert.equal(elements.promptsStatus.textContent, 'Could not load prompts');
  assert.equal(elements.promptsMore.textContent, 'Reload prompts');
  assert.equal(elements.promptsMore.disabled, false);
  assert.equal(elements.messages.hasChildNodes(), false);
  requestPromptHistory();
  handleEvent({type: 'prompts', requestId: sent[1].requestId});
  assert.equal(elements.promptsStatus.textContent, 'No retained prompts.');
  assert.equal(elements.promptsMore.hidden, true);

  openPromptHistory();
  selectSession({namespace: 'default', name: 'two', uid: 'uid-two', phase: 'Pending'});
  handleEvent({type: 'prompts', requestId: sent[2].requestId, prompts: [{id: 1, text: 'from session one'}]});
  assert.equal(elements.promptsDialog.open, false);
  assert.equal(elements.promptsList.hasChildNodes(), false);
  assert.equal(state.promptsRequestID, '');
  const transcript = elements.messages.textContent;
  handleEvent({type: 'error', requestId: sent[2].requestId, status: 'rejected', text: 'expired cursor'});
  assert.equal(elements.messages.textContent, transcript);
}

function testPromptHistoryReloadsAfterPageError() {
  resetHarness();
  state.selected = {namespace: 'default', name: 'one', uid: 'uid-one', phase: 'Ready'};
  const sent = [];
  state.socket = {readyState: WebSocket.OPEN, send: payload => sent.push(JSON.parse(payload))};
  openPromptHistory();
  assert.match(sent[0].requestId, /^prompts-/);
  handleEvent({type: 'prompts', requestId: sent[0].requestId, historyCursor: 'expired', prompts: [{id: 1, text: 'retained prompt'}]});
  requestPromptHistory();
  assert.equal(sent[1].historyCursor, 'expired');
  handleEvent({type: 'error', requestId: sent[1].requestId, text: 'Session prompt history cursor expired; reload prompts'});
  assert.equal(elements.promptsList.hasChildNodes(), false);
  assert.equal(elements.promptsMore.textContent, 'Reload prompts');
  requestPromptHistory();
  assert.equal(sent[2].historyCursor, '');
  handleEvent({type: 'prompts', requestId: sent[2].requestId, prompts: [{id: 2, text: 'current prompt'}]});
  assert.equal(elements.promptsList.querySelectorAll('.prompt-history-item').length, 1);
  assert.equal(elements.promptsList.querySelector('pre').textContent, 'current prompt');
  assert.equal(elements.messages.hasChildNodes(), false);
}

function openPromptNavigation(prompts) {
  state.selected = {namespace: 'default', name: 'one', uid: 'uid-one', phase: 'Ready'};
  const sent = [];
  state.socket = {readyState: WebSocket.OPEN, send: payload => sent.push(JSON.parse(payload))};
  openPromptHistory();
  handleEvent({type: 'prompts', requestId: sent[0].requestId, prompts});
  return sent;
}

function clickPromptJump() {
  elements.promptsList.querySelectorAll('button').find(button => button.textContent === 'Jump to message').listeners.get('click')();
}

function receiveTranscriptPage(request, events, historyCursor = '') {
  handleEvent({type: 'history.start', historyPage: true, requestId: request.requestId, historyCursor});
  for (const event of events) handleEvent(event);
  handleEvent({type: 'history.end', historyPage: true, requestId: request.requestId});
}

function testPromptJumpToLoadedMessage() {
  for (const prompt of [{id: 7, text: 'repeated text'}, {id: 7, text: '', attachments: [{id: 'file-1', name: 'notes.txt'}]}]) {
    resetHarness();
    const sent = openPromptNavigation([prompt]);
    elements.input.value = 'unsent draft';
    renderAcceptedUser({...prompt, id: 2});
    renderAcceptedUser(prompt);
    const rows = elements.messages.querySelectorAll('.event-row.user');
    state.bottomScrollFrame = window.requestAnimationFrame(() => assert.fail('Jump must cancel bottom anchoring'));

    clickPromptJump();

    assert.equal(rows[0].scrollIntoViewOptions, null);
    assert.deepEqual(rows[1].scrollIntoViewOptions, {behavior: 'instant', block: 'start'});
    assert.equal(rows[1].focused, true);
    assert.deepEqual(rows[1].focusOptions, {preventScroll: true});
    assert.equal(rows[1].tabIndex, -1);
    assert.equal(elements.promptsDialog.open, false);
    assert.equal(state.promptJumpTarget, null);
    assert.equal(elements.input.value, 'unsent draft');
    assert.equal(sent.length, 1);
    assert.equal(state.bottomScrollFrame, null);
    animationFrames.forEach(callback => callback());
  }
}

function testPromptJumpLoadsEarlierPages() {
  resetHarness();
  const sent = openPromptNavigation([{id: 5, text: 'earliest prompt'}]);
  state.historyCursor = 'recent-cursor';
  state.activeTurn = true;
  state.activeTurnID = 'live-turn';
  state.lastEventID = 100;
  renderAcceptedUser({id: 90, text: 'recent prompt'});
  requestOlderHistory();

  clickPromptJump();

  assert.equal(sent.length, 2, 'An in-flight page must be reused');
  assert.equal(elements.promptsDialog.open, true);
  assert.equal(elements.promptsStatus.textContent, 'Loading messages for prompt 5…');
  assert.equal(elements.promptsMore.disabled, true);
  requestPromptHistory();
  assert.equal(sent.length, 2);
  receiveTranscriptPage(sent[1], [{type: 'user.message', id: 50, text: 'middle prompt'}], 'older-cursor');
  assert.equal(sent.length, 3);
  assert.equal(sent[2].type, 'history');
  assert.equal(sent[2].historyCursor, 'older-cursor');
  receiveTranscriptPage(sent[2], [{type: 'user.message', id: 5, text: 'earliest prompt'}], 'more-cursor');

  const rows = elements.messages.querySelectorAll('.event-row.user');
  assert.deepEqual(rows.map(row => row.dataset.eventId), ['5', '50', '90']);
  assert.deepEqual(rows[0].scrollIntoViewOptions, {behavior: 'instant', block: 'start'});
  assert.equal(rows[1].scrollIntoViewOptions, null);
  assert.equal(rows[2].scrollIntoViewOptions, null);
  assert.equal(elements.promptsDialog.open, false);
  assert.equal(state.activeTurn, true);
  assert.equal(state.activeTurnID, 'live-turn');
  assert.equal(state.lastEventID, 100);
  assert.equal(state.historyCursor, 'more-cursor');
  assert.equal(sent.length, 3, 'Stop loading as soon as the target is found');
}

function testPromptJumpToEditedPendingMessage() {
  for (const accepted of [false, true]) {
    resetHarness();
    const sent = openPromptNavigation([{id: 5, turnId: 'queued-turn', text: 'draft'}]);
    applyHistoryState({pendingTurn: {turnId: 'queued-turn', text: 'draft', revision: 1}});
    handleEvent({type: 'user.message.updated', id: 8, turnId: 'queued-turn', text: 'edited draft', revision: 2});
    if (accepted) handleEvent({type: 'turn.started', id: 9, turnId: 'queued-turn'});
    const target = accepted ? elements.messages.querySelectorAll('.event-row.user')[0] : state.pendingMessage.item;

    clickPromptJump();

    assert.deepEqual(target.scrollIntoViewOptions, {behavior: 'instant', block: 'start'});
    assert.equal(target.focused, true);
    assert.match(target.textContent, /edited draft/);
    assert.equal(elements.promptsDialog.open, false);
    assert.equal(sent.length, 1);
  }
}

function testPromptJumpWaitsForInitialHistory() {
  resetHarness();
  handleEvent({type: 'history.start', historyCursor: 'older-cursor', lastEventId: 10});
  const sent = openPromptNavigation([{id: 5, text: 'prompt'}]);
  clickPromptJump();
  assert.equal(sent.length, 1);
  handleEvent({type: 'user.message', id: 5, text: 'prompt'});
  handleEvent({type: 'history.end', historyState: {}});
  assert.equal(elements.promptsDialog.open, false);
  assert.equal(elements.messages.querySelectorAll('.event-row.user')[0].focused, true);
  assert.equal(sent.length, 1);
}

function testPromptJumpCancellation() {
  for (const cancel of [
    closePromptHistory,
    () => selectSession({namespace: 'default', name: 'two', uid: 'uid-two', phase: 'Pending'}),
    resetCurrentSessionView,
    closeSocket,
  ]) {
    resetHarness();
    const sent = openPromptNavigation([{id: 5, text: 'prompt'}]);
    state.historyCursor = 'older-cursor';
    clickPromptJump();
    assert.equal(sent.length, 2);
    cancel();
    assert.equal(state.promptJumpTarget, null);
    receiveTranscriptPage(sent[1], [{type: 'user.message', id: 5, text: 'prompt'}], 'more-cursor');
    assert.equal(sent.length, 2);
    assert.equal(elements.promptsDialog.open, false);
    for (const row of elements.messages.querySelectorAll('.event-row.user')) assert.equal(row.scrollIntoViewOptions, null);
  }
}

function testPromptJumpUnavailableAndPageError() {
  for (const failure of ['unavailable', 'page-error', 'send-error']) {
    resetHarness();
    const sent = openPromptNavigation([{id: 5, text: 'prompt'}]);
    state.historyCursor = 'older-cursor';
    if (failure === 'send-error') state.socket.send = () => { throw new Error('connection lost'); };
    clickPromptJump();
    if (failure === 'unavailable') receiveTranscriptPage(sent[1], []);
    if (failure === 'page-error') handleEvent({type: 'error', requestId: sent[1].requestId, text: 'Cursor expired'});

    assert.equal(state.promptJumpTarget, null);
    assert.equal(state.historyPageLoading, false);
    assert.equal(elements.promptsDialog.open, true);
    assert.equal(elements.promptsMore.disabled, false);
    assert.equal(elements.promptsStatus.textContent, failure === 'unavailable'
      ? 'This prompt is no longer available in the conversation'
      : 'Could not load messages for this prompt; try again');
    renderAcceptedUser({id: 5, text: 'prompt'});
    clickPromptJump();
    assert.equal(elements.promptsDialog.open, false);
    assert.equal(elements.messages.querySelectorAll('.event-row.user')[0].focused, true);
  }
}

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
  assert.equal(elements.composerHint.textContent, 'Tap ■ to interrupt · Return for a new line · !COMMAND · /goal');
  window.matchMedia = () => ({matches: false});

  submitComposer();
  assert.equal(interruptRequests, 1);
  assert.deepEqual(sent, []);
  assert.equal(elements.input.value, 'preserved draft');

  state.interrupting = true;
  updateComposerAction();
  assert.equal(elements.send.disabled, true);
}

function testComposerLabelsPendingSubmission() {
  resetHarness();
  state.socket = {readyState: WebSocket.OPEN};
  state.activeTurn = true;
  state.pendingMessage = {event: {turnId: 'turn-2'}};
  elements.input.value = 'another detail';

  updateComposerAction();

  assert.equal(elements.composerHint.textContent, 'Enter to add to pending · Shift+Enter for a new line · !COMMAND · /goal');

  elements.input.value = '/goal pause';
  updateComposerAction();
  assert.equal(elements.composerHint.textContent, 'Enter to run goal command · Shift+Enter for a new line · !COMMAND · /goal');
}

function testPendingSessionComposerAllowsDraft() {
  resetHarness();
  const session = {namespace: 'default', name: 'one', uid: 'uid-one', provider: 'codex', phase: 'Pending'};
  state.selected = session;

  renderSessionHeader();

  assert.equal(elements.input.disabled, false);
  assert.equal(elements.attachFiles.disabled, false);
  assert.equal(elements.send.disabled, true);
  elements.input.value = 'Start by reviewing the failing tests';
  savePromptDraft(session);
  assert.equal(state.promptDrafts.get(sessionKey(session)), 'Start by reviewing the failing tests');
}

function testFailedSessionComposerRejectsDraft() {
  resetHarness();
  state.selected = {namespace: 'default', name: 'one', uid: 'uid-one', provider: 'codex', phase: 'Failed'};

  renderSessionHeader();

  assert.equal(elements.input.disabled, true);
  assert.equal(elements.attachFiles.disabled, true);
  assert.equal(elements.send.disabled, true);
}

function testSessionViewPickerAvailability() {
  for (const phase of ['Ready', 'Pending', 'Suspended', 'Failed']) {
    resetHarness();
    state.selected = {namespace: 'default', name: 'one', uid: 'uid-one', provider: 'codex', phase};
    renderSessionHeader();
    assert.equal(elements.viewChoice.disabled, false);
    assert.equal(elements.terminalChoice.disabled, phase !== 'Ready');
  }
  state.selected = null;
  renderSessionHeader();
  assert.equal(elements.viewChoice.disabled, true);
  assert.equal(elements.terminalChoice.disabled, true);
}

function testUnavailableSessionLeavesTerminalView() {
  const setView = global.setActiveView;
  try {
    for (const change of [{phase: 'Pending'}, {phase: 'Suspended'}, {phase: 'Failed'}, {resetting: true}, {userSuspended: true}]) {
      resetHarness();
      state.selected = {namespace: 'default', name: 'one', uid: 'uid-one', provider: 'codex', phase: 'Ready'};
      elements.terminalView.hidden = false;
      const views = [];
      global.setActiveView = view => views.push(view);
      renderSessionHeader();
      assert.deepEqual(views, []);
      Object.assign(state.selected, change);
      renderSessionHeader();
      assert.deepEqual(views, ['conversation']);
      assert.equal(elements.terminalTab.disabled, true);
      assert.equal(elements.terminalChoice.disabled, true);
    }
  } finally {
    global.setActiveView = setView;
  }
}

async function testReadySessionDisconnectsWhenItBecomesPending() {
  resetHarness();
  const session = {namespace: 'default', name: 'one', uid: 'uid-one', provider: 'codex', phase: 'Ready'};
  state.selected = session;
  state.sessions = [session];
  state.socket = {readyState: WebSocket.OPEN};
  global.api = async () => [{...session, phase: 'Pending'}];
  global.renderHeader = renderSessionHeader;

  await loadSessions({quiet: true});

  assert.equal(closeSocketRequests, 1);
  assert.equal(state.socket, null);
  assert.equal(state.selected.phase, 'Pending');
  assert.equal(elements.input.disabled, false);
  assert.equal(elements.send.disabled, true);
}

async function testComposerIgnoresReentrantSubmission() {
  resetHarness();
  const sent = [];
  state.selected = {namespace: 'default', name: 'one', uid: 'uid-one'};
  state.socket = {
    readyState: WebSocket.OPEN,
    send: (message) => sent.push(message),
  };
  state.sendingMessage = true;
  elements.input.value = 'duplicate';

  await submitComposer();

  assert.deepEqual(sent, []);
  assert.equal(state.sendingMessage, true);
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
      pendingTurn: {turnId: 'turn-2', text: 'pending request'},
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
  assert.equal(state.pendingMessage.event.text, 'pending request');
  assert.equal(state.fileChanges.get('old.txt'), fileDiff);
  assert.equal(elements.messages.querySelector('.history-page-control').textContent, 'Load earlier messages');

  handleEvent({type: 'assistant.message', id: 13, turnId: 'turn-1', text: 'working done'});
  assert.equal(elements.messages.textContent.match(/working done/g).length, 1);
}

function testProjectedHistoryHidesEmptyPendingRegion() {
  resetHarness();
  renderPendingUser({type: 'user.message', turnId: 'turn-2', text: 'pending request'});
  assert.equal(elements.pending.hidden, false);

  applyHistoryState({});

  assert.equal(state.pendingMessage, null);
  assert.equal(elements.pending.hidden, true);
  assert.equal(elements.pending.hasChildNodes(), false);
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

function testToolOutputStreamsBeforeCompletion() {
  resetHarness();
  renderTool({type: 'tool.started', toolId: 'tool-1', toolName: 'make test', status: 'running'});
  appendToolDelta({type: 'tool.delta', toolId: 'tool-1', output: 'first\n'});
  appendToolDelta({type: 'tool.delta', toolId: 'tool-1', output: 'second\n'});

  const card = state.tools.get('tool-1');
  assert.equal(card.querySelector('.tool-output-preview').textContent, 'first\nsecond');

  completeTool({type: 'tool.completed', toolId: 'tool-1', status: 'completed', output: 'first\nsecond\n'});
  assert.equal(card.querySelector('.tool-output-preview').textContent, 'first\nsecond');
}

function testGoalRendering() {
  resetHarness();
  renderGoal({
    type: 'goal.updated',
    goal: {objective: 'Improve coverage', status: 'active', tokenBudget: 1000, tokensUsed: 125},
  });
  assert.equal(elements.messages.querySelector('.goal-card').textContent, 'Goal active · 125/1000 tokensImprove coverage');

  renderGoal({type: 'goal.updated', status: 'cleared'});
  const cards = elements.messages.querySelectorAll('.goal-card');
  assert.equal(cards[1].textContent, 'Goal cleared.');
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

function testUserAttachmentRendering() {
  resetHarness();
  state.selected = {namespace: 'team-a', name: 'chat'};
  renderAcceptedUser({
    type: 'user.message',
    text: 'review this',
    attachments: [{id: 'attachment-1', name: 'screen.png', mediaType: 'image/png', sizeBytes: 7}],
  });

  const link = elements.messages.querySelector('.message-attachment');
  assert.equal(link.href, '/api/sessions/team-a/chat/attachments/attachment-1');
  assert.equal(link.querySelector('img').src, link.href);
  assert.match(link.textContent, /screen\.png/);
}

function testCurrentRequestTracksScrolledTurn() {
  resetHarness();
  elements.messages.bounds = {top: 100, bottom: 600};
  renderAcceptedUser({type: 'user.message', text: 'Review the controller changes'});
  renderAcceptedUser({type: 'user.message', text: 'Then run the focused tests'});
  const requests = elements.messages.querySelectorAll('.event-row.user');
  requests[0].bounds = {top: 20, bottom: 80};
  requests[1].bounds = {top: 300, bottom: 340};

  updateCurrentRequest();

  assert.equal(elements.currentRequest.hidden, false);
  assert.equal(elements.currentRequestText.textContent, 'Review the controller changes');
  assert.equal(elements.currentRequestButton.title, 'Review the controller changes');
  jumpToCurrentRequest();
  assert.deepEqual(requests[0].scrollIntoViewOptions, {behavior: 'smooth', block: 'start'});
  assert.equal(elements.currentRequest.hidden, true);
  updateCurrentRequest();

  elements.sessionsView.hidden = true;
  requests[0].bounds = {top: 0, bottom: 0};
  requests[1].bounds = {top: 0, bottom: 0};
  updateCurrentRequest();
  assert.equal(elements.currentRequest.hidden, false);
  assert.equal(elements.currentRequestText.textContent, 'Review the controller changes');

  elements.sessionsView.hidden = false;
  requests[1].bounds = {top: 40, bottom: 90};
  updateCurrentRequest();
  assert.equal(elements.currentRequestText.textContent, 'Then run the focused tests');

  requests[1].bounds = {top: 100, bottom: 140};
  updateCurrentRequest();
  assert.equal(elements.currentRequest.hidden, true);
}

function testCurrentRequestScrollUpdatesAreThrottled() {
  resetHarness();
  elements.messages.bounds = {top: 100, bottom: 600};
  renderAcceptedUser({type: 'user.message', text: 'Inspect the long response'});
  elements.messages.querySelectorAll('.event-row.user')[0].bounds = {top: 20, bottom: 80};

  scheduleCurrentRequestUpdate();
  scheduleCurrentRequestUpdate();

  assert.equal(animationFrames.length, 1);
  animationFrames.shift()();
  assert.equal(elements.currentRequest.hidden, false);
  assert.equal(elements.currentRequestText.textContent, 'Inspect the long response');

  scheduleCurrentRequestUpdate();
  assert.equal(animationFrames.length, 1);
  animationFrames.shift()();
}

function testPendingMessageEditing() {
  resetHarness();
  const sent = [];
  state.socket = {readyState: WebSocket.OPEN, send: (payload) => sent.push(JSON.parse(payload))};
  renderPendingUser({type: 'user.message', turnId: 'turn-2', text: 'original', revision: 1});

  editPendingUser('turn-2');
  const pending = state.pendingMessage;
  const input = pending.item.querySelector('.pending-message-input');
  input.value = 'revised';
  pending.item.querySelector('.pending-message-save').listeners.get('click')();

  assert.equal(sent.length, 1);
  assert.equal(sent[0].type, 'message.edit');
  assert.equal(sent[0].turnId, 'turn-2');
  assert.equal(sent[0].text, 'revised');
  assert.equal(sent[0].expectedRevision, 1);
  handleEvent({type: 'user.message.updated', turnId: 'turn-2', text: 'revised', revision: 2});
  assert.equal(state.pendingMessage.event.revision, 2);
  assert.match(pending.item.textContent, /revised/);
  assert.doesNotMatch(pending.item.textContent, /original/);
}

function testPendingMessageRemoval() {
  resetHarness();
  const sent = [];
  state.socket = {readyState: WebSocket.OPEN, send: (payload) => sent.push(JSON.parse(payload))};
  renderPendingUser({type: 'user.message', turnId: 'turn-2', text: 'remove this', revision: 3});

  state.pendingMessage.item.querySelector('.pending-message-remove').listeners.get('click')();

  assert.equal(sent.length, 1);
  assert.equal(sent[0].type, 'message.remove');
  assert.equal(sent[0].turnId, 'turn-2');
  assert.equal(sent[0].expectedRevision, 3);
  handleEvent({type: 'user.message.removed', turnId: 'turn-2', revision: 4});
  assert.equal(state.pendingMessage, null);
  assert.equal(elements.pending.hidden, true);
  assert.equal(elements.messages.hasChildNodes(), false);
  assert.deepEqual(toasts, ['Pending message removed']);
}

function testPendingMessageSurvivesCompletedHistoryReplay() {
  resetHarness();
  renderPendingUser({type: 'user.message', turnId: 'turn-2', text: 'pending'});

  renderPendingUser({type: 'user.message', turnId: 'turn-1', text: 'completed'});

  assert.equal(state.pendingMessage.event.turnId, 'turn-2');
  assert.equal(state.pendingMessage.event.text, 'pending');
  assert.match(elements.messages.textContent, /completed/);
}

testSessionViewSaveAndRestore();
testSessionViewReset();
testSessionProgressLifecycle();
testComposerInterruptsWhileInputIsDisabled();
testComposerLabelsPendingSubmission();
testPendingSessionComposerAllowsDraft();
testFailedSessionComposerRejectsDraft();
testSessionViewPickerAvailability();
testUnavailableSessionLeavesTerminalView();
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
testProjectedHistoryHidesEmptyPendingRegion();
testOlderHistoryPageIsPrependedWithoutChangingLiveState();
testReselectRefreshesStatusPlaceholder();
testSessionTimestampFormatting();
testSessionTimestampElement();
testToolOutputRendering();
testToolOutputStreamsBeforeCompletion();
testGoalRendering();
testToolOutputRenderingNormalizesCarriageReturns();
testHistoryToolCompletionRendersOutputWithoutStart();
testUserAttachmentRendering();
testCurrentRequestTracksScrolledTurn();
testCurrentRequestScrollUpdatesAreThrottled();
testPendingMessageEditing();
testPendingMessageRemoval();
testPendingMessageSurvivesCompletedHistoryReplay();
testPromptHistoryErrorEmptyAndSessionSwitch();
testPromptHistoryReloadsAfterPageError();
testPromptJumpToLoadedMessage();
testPromptJumpLoadsEarlierPages();
testPromptJumpToEditedPendingMessage();
testPromptJumpWaitsForInitialHistory();
testPromptJumpCancellation();
testPromptJumpUnavailableAndPageError();
testReadySessionDisconnectsWhenItBecomesPending()
  .then(testComposerIgnoresReentrantSubmission)
  .then(testPromptHistoryBrowseAndReuse)
  .then(() => process.stdout.write('Session history tests passed\n'))
  .catch(error => {
    process.stderr.write(`${error.stack}\n`);
    process.exitCode = 1;
  });
