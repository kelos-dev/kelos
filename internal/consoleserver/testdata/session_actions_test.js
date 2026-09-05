const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

class TestNode {
  constructor(tag, bounds = {top: 20, right: 260, bottom: 52, width: 32, height: 32}) {
    this.tag = tag;
    this.bounds = bounds;
    this.children = [];
    this.listeners = new Map();
    this.attributes = new Map();
    this.classes = new Set();
    this.dataset = {};
    this.style = {};
    this.hidden = false;
    this.disabled = false;
    this.draggable = false;
    this._text = '';
  }

  append(...nodes) {
    this.children.push(...nodes);
  }

  replaceChildren(...nodes) {
    this.children = [...nodes];
  }

  querySelectorAll(selector) {
    assert.ok(selector.startsWith('.'), `Unsupported selector: ${selector}`);
    const className = selector.slice(1);
    const matches = [];
    for (const child of this.children) {
      if (child.classes?.has(className)) matches.push(child);
      matches.push(...(child.querySelectorAll?.(selector) || []));
    }
    return matches;
  }

  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) || [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  async dispatch(type, event = {}) {
    for (const listener of this.listeners.get(type) || []) await listener(event);
  }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
  }

  removeAttribute(name) {
    this.attributes.delete(name);
  }

  getBoundingClientRect() {
    return this.bounds;
  }

  focus() {
    global.document.activeElement = this;
  }

  contains(target) {
    return this === target || this.children.some(child => child.contains?.(target));
  }

  set className(value) {
    this.classes = new Set(String(value).split(/\s+/).filter(Boolean));
  }

  get className() {
    return [...this.classes].join(' ');
  }

  get classList() {
    return {
      add: (...names) => names.forEach(name => this.classes.add(name)),
      remove: (...names) => names.forEach(name => this.classes.delete(name)),
      contains: name => this.classes.has(name),
    };
  }

  set textContent(value) {
    this._text = String(value);
    this.children = [];
  }

  get textContent() {
    return this.children.length ? this.children.map(child => child.textContent).join('') : this._text;
  }
}

global.document = {
  activeElement: null,
  createElement: tag => new TestNode(tag),
};
global.window = {
  confirm: () => true,
  innerHeight: 800,
  innerWidth: 400,
  setTimeout,
};

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');
const index = fs.readFileSync(path.join(__dirname, '..', 'web', 'index.html'), 'utf8');
const styles = fs.readFileSync(path.join(__dirname, '..', 'web', 'styles.css'), 'utf8');

function applicationSlice(start, end) {
  const startIndex = application.indexOf(start);
  const endIndex = application.indexOf(end, startIndex);
  assert.notEqual(startIndex, -1, `${start} not found`);
  assert.notEqual(endIndex, -1, `${end} not found`);
  return application.slice(startIndex, endIndex);
}

vm.runInThisContext(applicationSlice('function errorMessage', 'function requireElements'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function sessionKey', 'function sessionViewKey'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function createSessionListItem', 'function sectionLabel'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('async function requestSessionLifecycleAction', 'function createWelcome'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function closeSessionSectionEditor', 'function renderSelectedSessionSection'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('async function deleteSession', 'elements.resumeButton.addEventListener'), {filename: 'app.js'});

global.sessionDisplayStatus = () => 'Ready';
global.createSessionTimestamp = () => null;
global.providerLabel = provider => provider;
global.createPullRequestLink = () => null;
global.configureSessionDrag = () => {};
global.selectSession = () => {};

function resetHarness() {
  global.elements = {
    list: new TestNode('div'),
    sessionActionsMenu: new TestNode('div', {top: 0, right: 0, bottom: 0, width: 176, height: 160}),
    sessionActionRename: new TestNode('button'),
    sessionActionSection: new TestNode('button'),
    sessionActionLifecycle: new TestNode('button'),
    sessionActionReset: new TestNode('button'),
    sectionForm: new TestNode('form'),
    sectionChoice: new TestNode('select'),
    newSessionButton: new TestNode('button'),
  };
  elements.sessionActionsMenu.append(elements.sessionActionRename, elements.sessionActionSection, elements.sessionActionLifecycle, elements.sessionActionReset);
  elements.sessionActionsMenu.hidden = true;
  document.activeElement = null;
  global.state = {
    sessions: [],
    selected: null,
    currentView: null,
    namespaceGeneration: 0,
    sessionListGeneration: 0,
    suspendingSession: false,
    resumingSession: false,
    sessionActionKey: '',
    sessionActionTrigger: null,
    sectionAssignments: new Set(),
  };
}

async function testEverySessionRowHasActionsMenu() {
  resetHarness();
  const session = {namespace: 'default', name: 'review', provider: 'codex'};
  state.sessions = [session];

  const item = createSessionListItem(session);
  const actions = item.children.find(child => child.className === 'session-item-actions');

  assert.ok(actions);
  assert.equal(actions.textContent, '…');
  assert.equal(actions.attributes.get('aria-label'), 'Actions for review');
  assert.equal(actions.attributes.get('aria-haspopup'), 'menu');
  assert.equal(actions.attributes.get('aria-expanded'), 'false');

  let stopped = false;
  await actions.dispatch('click', {stopPropagation: () => { stopped = true; }});
  assert.equal(stopped, true);
  assert.equal(state.sessionActionKey, 'default/review');
  assert.equal(elements.sessionActionsMenu.hidden, false);
  assert.equal(elements.sessionActionLifecycle.textContent, 'Suspend');
  assert.equal(actions.attributes.get('aria-expanded'), 'true');
  assert.equal(document.activeElement, elements.sessionActionRename);

  await actions.dispatch('click', {stopPropagation() {}});
  assert.equal(elements.sessionActionsMenu.hidden, true);
  assert.equal(actions.attributes.get('aria-expanded'), 'false');
  assert.equal(document.activeElement, actions);
}

async function testSessionDragDoesNotCaptureActions() {
  resetHarness();
  const session = {namespace: 'default', name: 'review', provider: 'codex'};
  state.sessions = [session];

  let dragHandle;
  const configureDrag = global.configureSessionDrag;
  global.configureSessionDrag = (item, handle) => {
    assert.equal(item.draggable, false);
    dragHandle = handle;
  };
  const item = createSessionListItem(session, true);
  global.configureSessionDrag = configureDrag;
  const select = item.children.find(child => child.className === 'session-item-select');
  const actions = item.children.find(child => child.className === 'session-item-actions');

  assert.equal(select.draggable, true);
  assert.equal(dragHandle, select);
  assert.equal(actions.draggable, false);
  await actions.dispatch('click', {stopPropagation() {}});
  assert.equal(elements.sessionActionsMenu.hidden, false);
}

function testOpenMenuSurvivesSessionRefresh() {
  resetHarness();
  const session = {namespace: 'default', name: 'review', provider: 'codex'};
  state.sessions = [session];
  const firstItem = createSessionListItem(session);
  openSessionActionsMenu(session, firstItem.children[1]);

  const refreshed = {...session, displayName: 'Review CI', resetting: true, idleSuspended: true};
  state.sessions = [refreshed];
  state.sessionActionTrigger = null;
  const refreshedItem = createSessionListItem(refreshed);
  syncSessionActionsMenu();

  const refreshedActions = refreshedItem.children[1];
  assert.equal(elements.sessionActionsMenu.hidden, false);
  assert.equal(elements.sessionActionsMenu.attributes.get('aria-label'), 'Actions for Review CI');
  assert.equal(elements.sessionActionLifecycle.textContent, 'Resume');
  assert.equal(elements.sessionActionReset.disabled, true);
  assert.equal(refreshedActions.attributes.get('aria-expanded'), 'true');
}

async function testActionsCanTargetAnUnselectedSession() {
  resetHarness();
  const selected = {namespace: 'default', name: 'selected'};
  const target = {namespace: 'team-a', name: 'target'};
  state.sessions = [selected, target];
  state.selected = selected;
  state.sessionActionKey = sessionKey(target);
  assert.equal(sessionActionsTarget(), target);

  const requests = [];
  const discarded = [];
  const clearedDrafts = [];
  const clearedAttachments = [];
  let selectionChanges = 0;
  global.api = async (requestPath, options) => {
    requests.push({path: requestPath, options});
    return {...target, resetting: true};
  };
  global.discardSessionView = session => discarded.push(sessionKey(session));
  global.clearPromptDraft = session => clearedDrafts.push(sessionKey(session));
  global.clearAttachmentDraft = session => clearedAttachments.push(sessionKey(session));
  global.selectSession = () => { selectionChanges++; };
  global.loadSessions = async () => {};
  global.showToast = () => {};
  global.closeSocket = () => {};
  global.resetCurrentSessionView = () => {};

  await resetSession(target);
  await deleteSession(target);

  assert.deepEqual(requests.map(request => request.path), [
    '/api/sessions/team-a/target/reset',
    '/api/sessions/team-a/target',
  ]);
  assert.equal(requests[0].options.method, 'POST');
  assert.equal(requests[1].options.method, 'DELETE');
  assert.deepEqual(discarded, ['team-a/target', 'team-a/target']);
  assert.deepEqual(clearedDrafts, ['team-a/target', 'team-a/target']);
  assert.deepEqual(clearedAttachments, ['team-a/target', 'team-a/target']);
  assert.equal(selectionChanges, 0);
  assert.equal(state.selected, selected);
}

async function testLifecycleActionTargetsAnUnselectedSession() {
  resetHarness();
  const selected = {namespace: 'default', name: 'selected'};
  const target = {namespace: 'team-a', name: 'target', userSuspended: false};
  state.sessions = [selected, target];
  state.selected = selected;
  state.namespaceGeneration = 4;
  state.sessionActionKey = sessionKey(target);

  const requests = [];
  const toasts = [];
  let socketCloses = 0;
  global.api = async (requestPath, options) => {
    requests.push({path: requestPath, options});
    return {...target, userSuspended: requestPath.endsWith('/suspend')};
  };
  global.renderSessions = () => {};
  global.renderHeader = () => {};
  global.closeSocket = () => { socketCloses++; };
  global.connectSocket = () => {};
  global.setConnection = () => {};
  global.showToast = message => { toasts.push(message); };

  await runSessionMenuAction({detail: 1}, toggleSessionSuspension);
  state.sessionActionKey = sessionKey(state.sessions[1]);
  await runSessionMenuAction({detail: 1}, toggleSessionSuspension);

  assert.deepEqual(requests, [
    {path: '/api/sessions/team-a/target/suspend', options: {method: 'POST'}},
    {path: '/api/sessions/team-a/target/resume', options: {method: 'POST'}},
  ]);
  assert.deepEqual(toasts, ['Session suspend requested', 'Session resume requested']);
  assert.equal(socketCloses, 0);
  assert.equal(state.selected, selected);
  assert.equal(state.sessions[1].userSuspended, false);
}

async function testActionsUpdateTheSelectedSession() {
  resetHarness();
  const target = {namespace: 'team-a', name: 'target'};
  const resetting = {...target, resetting: true};
  state.sessions = [target];
  state.selected = target;
  state.currentView = {historyLoaded: true};

  const selections = [];
  let socketCloses = 0;
  let viewResets = 0;
  global.api = async requestPath => requestPath.endsWith('/reset') ? resetting : null;
  global.discardSessionView = () => {};
  global.clearPromptDraft = () => {};
  global.clearAttachmentDraft = () => {};
  global.selectSession = session => selections.push(session);
  global.loadSessions = async () => {};
  global.showToast = () => {};
  global.closeSocket = () => { socketCloses++; };
  global.resetCurrentSessionView = () => { viewResets++; };

  await resetSession(target);
  await deleteSession(target);

  assert.equal(socketCloses, 1);
  assert.equal(viewResets, 1);
  assert.equal(state.currentView, null);
  assert.deepEqual(selections, [resetting, null]);
}

async function testKeyboardActionsRestoreFocusAfterRefresh() {
  resetHarness();
  const target = {namespace: 'team-a', name: 'target', provider: 'codex'};
  const neighbor = {namespace: 'team-a', name: 'neighbor', provider: 'codex'};
  const render = sessions => {
    state.sessions = sessions;
    elements.list.replaceChildren(...sessions.map(session => createSessionListItem(session)));
  };

  render([target, neighbor]);
  const originalAction = elements.list.querySelectorAll('.session-item-actions')[0];
  openSessionActionsMenu(target, originalAction);
  await runSessionMenuAction({detail: 0}, async session => render([{...session, resetting: true}, neighbor]));

  const refreshedActions = elements.list.querySelectorAll('.session-item-actions');
  assert.notEqual(refreshedActions[0], originalAction);
  assert.equal(document.activeElement, refreshedActions[0]);

  openSessionActionsMenu(state.sessions[0], refreshedActions[0]);
  await runSessionMenuAction({detail: 0}, async () => render([neighbor]));
  assert.equal(document.activeElement, elements.list.querySelectorAll('.session-item-actions')[0]);
}

async function testKeyboardActionRestoresFocusAfterRequestFailure() {
  resetHarness();
  const target = {namespace: 'team-a', name: 'target', provider: 'codex'};
  state.sessions = [target];
  elements.list.append(createSessionListItem(target));
  const action = elements.list.querySelectorAll('.session-item-actions')[0];
  openSessionActionsMenu(target, action);

  global.api = async () => { throw new Error('request failed'); };
  global.showToast = () => {};
  await runSessionMenuAction({detail: 0}, resetSession);

  assert.equal(document.activeElement, action);
}

function testMenuClosesWhenFocusLeaves() {
  resetHarness();
  const target = {namespace: 'team-a', name: 'target', provider: 'codex'};
  state.sessions = [target];
  const action = createSessionListItem(target).children[1];
  openSessionActionsMenu(target, action);

  handleSessionActionsFocusOut({relatedTarget: elements.sessionActionReset});
  assert.equal(elements.sessionActionsMenu.hidden, false);

  handleSessionActionsFocusOut({relatedTarget: new TestNode('button')});
  assert.equal(elements.sessionActionsMenu.hidden, true);
}

async function testTouchActionRunsBeforeMenuCloses() {
  resetHarness();
  const target = {namespace: 'team-a', name: 'target', provider: 'codex'};
  state.sessions = [target];
  const action = createSessionListItem(target).children[1];
  openSessionActionsMenu(target, action);

  let actionTarget;
  handleSessionActionsFocusOut({relatedTarget: null});
  assert.equal(elements.sessionActionsMenu.hidden, false);
  await runSessionMenuAction({detail: 1}, async session => { actionTarget = session; });
  await new Promise(resolve => setTimeout(resolve, 0));

  assert.equal(actionTarget, target);
  assert.equal(elements.sessionActionsMenu.hidden, true);

  openSessionActionsMenu(target, action);
  document.activeElement = null;
  handleSessionActionsFocusOut({relatedTarget: null});
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(elements.sessionActionsMenu.hidden, true);
}

function testSectionActionOpensTouchEditorForTargetSession() {
  resetHarness();
  const selected = {namespace: 'default', name: 'selected', provider: 'codex'};
  const target = {namespace: 'team-a', name: 'target', provider: 'codex'};
  state.sessions = [selected, target];
  state.selected = selected;
  state.sessionActionKey = sessionKey(target);
  state.sessionActionTrigger = new TestNode('button');
  elements.sessionActionsMenu.hidden = false;
  global.selectSession = session => { state.selected = session; };
  let pickerOpenCount = 0;
  elements.sectionChoice.showPicker = () => { pickerOpenCount++; };

  openSessionSectionEditor(target);

  assert.equal(state.selected, target);
  assert.equal(elements.sessionActionsMenu.hidden, true);
  assert.equal(elements.sectionForm.classList.contains('mobile-open'), true);
  assert.equal(document.activeElement, elements.sectionChoice);
  assert.equal(pickerOpenCount, 1);

  closeSessionSectionEditor();
  assert.equal(elements.sectionForm.classList.contains('mobile-open'), false);
}

assert.match(index, /id="session-actions-menu" role="menu"/);
assert.match(index, /id="session-action-rename"[^>]+role="menuitem"/);
assert.match(index, /id="session-action-section"[^>]+role="menuitem"/);
assert.match(index, /id="session-action-lifecycle"[^>]+role="menuitem"/);
assert.match(index, /id="session-action-reset"[^>]+role="menuitem"/);
assert.match(index, /id="session-action-delete"[^>]+role="menuitem"/);
assert.match(styles, /\.session-actions-menu button:focus-visible \{[^}]*outline: 2px solid var\(--accent-2\)/);
assert.match(application, /sessionActionLifecycle\.addEventListener\('click'/);
assert.match(application, /sessionActionSection\.addEventListener\('click'/);
assert.match(application, /sessionActionsMenu\.addEventListener\('focusout'/);

testEverySessionRowHasActionsMenu()
  .then(() => testSessionDragDoesNotCaptureActions())
  .then(() => {
    testOpenMenuSurvivesSessionRefresh();
    return testActionsCanTargetAnUnselectedSession();
  })
  .then(() => testLifecycleActionTargetsAnUnselectedSession())
  .then(() => testActionsUpdateTheSelectedSession())
  .then(() => testKeyboardActionsRestoreFocusAfterRefresh())
  .then(() => testKeyboardActionRestoresFocusAfterRequestFailure())
  .then(() => testTouchActionRunsBeforeMenuCloses())
  .then(() => {
    testMenuClosesWhenFocusLeaves();
    testSectionActionOpensTouchEditorForTargetSession();
    process.stdout.write('Session actions tests passed\n');
  });
