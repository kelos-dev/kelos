const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

global.syncPushSubscription = () => Promise.resolve();

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');

function applicationSlice(start, end) {
  const startIndex = application.indexOf(start);
  const endIndex = application.indexOf(end, startIndex);
  assert.notEqual(startIndex, -1, `${start} not found`);
  assert.notEqual(endIndex, -1, `${end} not found`);
  return application.slice(startIndex, endIndex);
}

vm.runInThisContext(applicationSlice('function errorMessage', 'function requireElements'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function sessionLifecycleAction', 'function updateSessionActionsMenu'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('async function requestSessionLifecycleAction', 'function createWelcome'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('async function loadSessions', 'async function loadConfig'), {filename: 'app.js'});

async function testUserSuspendedSessionResume() {
  const session = {namespace: 'team-a', name: 'chat', phase: 'Suspended', userSuspended: true};
  global.state = {
    namespaceGeneration: 4,
    sessionListGeneration: 0,
    sessions: [session],
    selected: session,
    resumingSession: false,
  };
  global.sessionKey = value => `${value.namespace}/${value.name}`;
  let request;
  let sessionsRendered = 0;
  const headerStates = [];
  const toasts = [];
  global.api = async (requestPath, options) => {
    request = {path: requestPath, options};
    return {...session, userSuspended: false};
  };
  global.renderSessions = () => { sessionsRendered++; };
  global.renderHeader = () => {
    headerStates.push({
      resumingSession: state.resumingSession,
      userSuspended: state.selected.userSuspended,
    });
  };
  global.showToast = message => { toasts.push(message); };

  await resumeSelectedSession();

  assert.equal(request.path, '/api/sessions/team-a/chat/resume');
  assert.deepEqual(request.options, {method: 'POST'});
  assert.equal(state.sessions[0].userSuspended, false);
  assert.equal(state.selected.userSuspended, false);
  assert.equal(state.resumingSession, false);
  assert.equal(sessionsRendered, 1);
  assert.deepEqual(headerStates[headerStates.length - 1], {
    resumingSession: false,
    userSuspended: false,
  });
  assert.deepEqual(toasts, ['Session resume requested']);
}

async function testSessionSuspend() {
  const session = {namespace: 'team-a', name: 'chat', phase: 'Ready', userSuspended: false};
  global.sessionKey = value => `${value.namespace}/${value.name}`;
  global.state = {
    namespaceGeneration: 4,
    sessionListGeneration: 0,
    sessions: [session],
    selected: session,
    suspendingSession: false,
  };
  let request;
  let sessionsRendered = 0;
  let socketClosures = 0;
  const headerStates = [];
  const connectionStates = [];
  const toasts = [];
  global.api = async (requestPath, options) => {
    request = {path: requestPath, options};
    return {...session, userSuspended: true};
  };
  global.renderSessions = () => { sessionsRendered++; };
  global.renderHeader = () => {
    headerStates.push({
      suspendingSession: state.suspendingSession,
      userSuspended: state.selected.userSuspended,
    });
  };
  global.closeSocket = () => { socketClosures++; };
  global.setConnection = (status, label) => { connectionStates.push({status, label}); };
  global.showToast = message => { toasts.push(message); };

  await suspendSelectedSession();

  assert.equal(request.path, '/api/sessions/team-a/chat/suspend');
  assert.deepEqual(request.options, {method: 'POST'});
  assert.equal(state.sessions[0].userSuspended, true);
  assert.equal(state.selected.userSuspended, true);
  assert.equal(state.suspendingSession, false);
  assert.equal(sessionsRendered, 1);
  assert.equal(socketClosures, 1);
  assert.deepEqual(connectionStates, [{status: 'connecting', label: 'Suspending'}]);
  assert.deepEqual(headerStates[headerStates.length - 1], {
    suspendingSession: false,
    userSuspended: true,
  });
  assert.deepEqual(toasts, ['Session suspend requested']);
}

async function testSuspendFailureReconnectsSelectedSession() {
  const session = {namespace: 'team-a', name: 'chat', phase: 'Ready', userSuspended: false};
  global.sessionKey = value => `${value.namespace}/${value.name}`;
  global.state = {
    namespaceGeneration: 4,
    sessionListGeneration: 0,
    sessions: [session],
    selected: session,
    suspendingSession: false,
  };
  let socketClosures = 0;
  let socketConnections = 0;
  const toasts = [];
  global.api = async () => {
    assert.equal(socketClosures, 1);
    throw new Error('suspend failed');
  };
  global.renderHeader = () => {};
  global.closeSocket = () => { socketClosures++; };
  global.connectSocket = () => { socketConnections++; };
  global.setConnection = () => {};
  global.showToast = message => { toasts.push(message); };

  await suspendSelectedSession();

  assert.equal(socketClosures, 1);
  assert.equal(socketConnections, 1);
  assert.equal(state.suspendingSession, false);
  assert.deepEqual(toasts, ['suspend failed']);
}

async function testResumeIgnoresOlderSessionList() {
  const suspended = {namespace: 'team-a', name: 'chat', phase: 'Suspended', userSuspended: true};
  const resumed = {...suspended, userSuspended: false};
  global.state = {
    namespace: 'team-a',
    namespaceGeneration: 4,
    sessionListGeneration: 0,
    sessions: [suspended],
    selected: suspended,
  };
  let resolveList;
  global.api = async (requestPath) => {
    if (requestPath.startsWith('/api/sessions?')) {
      return new Promise(resolve => { resolveList = resolve; });
    }
    return resumed;
  };
  global.renderSessions = () => {};
  global.renderHeader = () => {};

  const loading = loadSessions();
  await requestSessionLifecycleAction(suspended, 'resume');
  resolveList([suspended]);
  await loading;

  assert.equal(state.sessions[0].userSuspended, false);
  assert.equal(state.selected.userSuspended, false);
}

testUserSuspendedSessionResume()
  .then(testSessionSuspend)
  .then(testSuspendFailureReconnectsSelectedSession)
  .then(testResumeIgnoresOlderSessionList)
  .then(() => {
    process.stdout.write('Session suspension tests passed\n');
  });
