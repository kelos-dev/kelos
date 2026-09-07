const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

class Events {
  listeners = {};
  addEventListener(name, listener) { (this.listeners[name] ||= []).push(listener); }
  emit(name, event = {}) { for (const listener of this.listeners[name] || []) listener(event); }
}

class Element extends Events {
  hidden = false;
  disabled = false;
  attributes = {};
  dataset = {};
  get clientWidth() { return elements['terminal-view'].hidden ? 0 : 1000; }
  get clientHeight() { return elements['terminal-view'].hidden ? 0 : 600; }
  setAttribute(name, value) { this.attributes[name] = value; }
  focus() { this.focused = true; }
  click() { this.emit('click'); }
  textContent = '';
  replaceChildren() {}
}

const elements = Object.fromEntries(['terminal-view', 'terminal-container', 'terminal-status', 'reconnect-terminal']
  .map(id => [id, new Element()]));
global.document = {
  querySelector: selector => selector.startsWith('meta') ? {content: 'test-style-nonce'} : elements[selector.slice(1)],
  createElement: () => ({}),
};
global.window = new Events();
global.location = {protocol: 'https:', host: 'console.example'};
const terminals = [];
const sockets = [];
const observers = [];

global.Terminal = class {
  cols = 100;
  rows = 30;
  output = [];
  subscriptions = [];
  constructor(options) { this.options = options; terminals.push(this); }
  loadAddon(addon) { this.addon = addon; }
  open(container) {
    this.container = container;
    this.style = document.createElement('style');
  }
  subscribe(name, callback) {
    this[name] = callback;
    const subscription = {disposed: false, dispose() { this.disposed = true; }};
    this.subscriptions.push(subscription);
    return subscription;
  }
  onData(callback) { return this.subscribe('input', callback); }
  onBinary(callback) { return this.subscribe('binary', callback); }
  onResize(callback) { return this.subscribe('resize', callback); }
  focus() { this.focused = true; }
  write(data) { this.output.push(data); }
  dispose() { this.disposed = true; }
};
global.FitAddon = {FitAddon: class {
  fits = 0;
  fit() { this.fits++; }
}};
global.WebSocket = class extends Events {
  static OPEN = 1;
  readyState = 0;
  sent = [];
  constructor(url) { super(); this.url = url; sockets.push(this); }
  send(data) { this.sent.push(data); }
  close() { this.closed = true; this.readyState = 3; this.emit('close'); }
  connect() { this.readyState = 1; this.emit('open'); }
};
global.ResizeObserver = class {
  constructor(callback) { this.callback = callback; observers.push(this); }
  observe(container) { this.container = container; }
  disconnect() { this.disconnected = true; }
};

const source = fs.readFileSync(path.join(__dirname, '../web/terminal.js'), 'utf8');
const controller = vm.runInThisContext(`${source}\nsessionTerminal;`, {filename: 'terminal.js'});
const session = {namespace: 'team-a', name: 'chat', uid: 'uid-1', phase: 'Ready'};
for (const unavailable of [null, {...session, phase: 'Pending'}, {...session, phase: 'Suspended'},
  {...session, phase: 'Failed'}, {...session, resetting: true}, {...session, userSuspended: true}]) {
  assert.equal(controller.available(unavailable), false);
  controller.show(unavailable);
}
assert.equal(sockets.length, 0);

controller.show(session);
const socket = sockets[0];
const terminal = terminals[0];
assert.equal(terminal.style.nonce, 'test-style-nonce');
assert.equal(document.createElement('style').nonce, undefined);
assert.equal(elements['reconnect-terminal'].disabled, true);
assert.equal(socket.url, 'wss://console.example/api/sessions/team-a/chat/exec');
assert.equal(socket.binaryType, 'arraybuffer');
socket.connect();
assert.equal(terminal.focused, true);
assert.equal(elements['terminal-status'].textContent, 'Connected');
assert.deepEqual(JSON.parse(socket.sent[0]), {type: 'resize', cols: 100, rows: 30});
terminal.input('héllo\t\r\x03\x1b');
assert.equal(new TextDecoder().decode(socket.sent[1]), 'héllo\t\r\x03\x1b');
terminal.binary('\x80\xff');
assert.deepEqual([...socket.sent[2]], [128, 255]);
terminal.cols = 120;
terminal.rows = 40;
terminal.resize();
assert.deepEqual(JSON.parse(socket.sent[3]), {type: 'resize', cols: 120, rows: 40});
observers[0].callback();
assert.equal(terminal.addon.fits, 3);
const output = new TextEncoder().encode('\x1b[32mhello\x1b[0m\r\n');
socket.emit('message', {data: output.buffer});
assert.deepEqual(terminal.output[0], output);
const fits = terminal.addon.fits;
terminal.focused = false;
elements['terminal-view'].hidden = true;
observers[0].callback();
assert.equal(terminal.addon.fits, fits);
assert.equal(terminal.focused, false);
socket.emit('message', {data: output.buffer});
assert.deepEqual(terminal.output[1], output);
elements['terminal-view'].hidden = false;
controller.show(session);
assert.equal(sockets.length, 1);
assert.equal(terminal.addon.fits, fits + 1);
assert.equal(terminal.focused, true);
assert.equal(socket.closed, undefined);

socket.emit('message', {data: JSON.stringify({type: 'exit'})});
socket.close();
assert.equal(elements['terminal-status'].textContent, 'Shell exited');
assert.equal(terminal.options.disableStdin, true);
terminal.input('ignored');
assert.equal(socket.sent.length, 4);
assert.equal(sockets.length, 1);
controller.show(session);
assert.equal(sockets.length, 1, 'Revisiting the tab must preserve exited shell output');
assert.equal(elements['reconnect-terminal'].disabled, false);
elements['reconnect-terminal'].emit('click');
assert.equal(elements['reconnect-terminal'].disabled, true);
assert.equal(terminal.disposed, true);
assert.ok(terminal.subscriptions.every(subscription => subscription.disposed));
assert.equal(observers[0].disconnected, true);

controller.show(session);
const current = sockets[1];
current.connect();
socket.emit('message', {data: JSON.stringify({type: 'error', text: 'stale error'})});
assert.equal(elements['terminal-status'].textContent, 'Connected');
controller.sync({...session, displayName: 'Renamed'});
assert.equal(current.closed, undefined);
current.emit('message', {data: JSON.stringify({type: 'error', text: 'Session chat terminal: pod is unavailable'})});
current.close();
assert.equal(elements['terminal-status'].textContent, 'Session chat terminal: pod is unavailable');
controller.close();

for (const changed of [null, {...session, namespace: 'team-b'}, {...session, name: 'other'},
  {...session, uid: 'replacement'}, {...session, phase: 'Suspended'}, {...session, resetting: true}, {...session, userSuspended: true}]) {
  controller.show(session);
  const connection = sockets.at(-1);
  controller.sync(changed);
  assert.equal(connection.closed, true);
  assert.equal(terminals.at(-1).disposed, true);
  assert.equal(elements['reconnect-terminal'].disabled, true);
}

controller.show(session);
sockets.at(-1).connect();
const pasted = 'héllo'.repeat(20000);
terminals.at(-1).input(pasted);
const chunks = sockets.at(-1).sent.slice(1);
assert.ok(chunks.length > 1 && chunks.every(chunk => chunk.length <= 32 * 1024));
assert.equal(Buffer.concat(chunks).toString('utf8'), pasted);
controller.close();

controller.show(session);
sockets.at(-1).close();
assert.match(elements['terminal-status'].textContent, /^Disconnected/);
controller.close();
controller.show(session);
sockets.at(-1).emit('error');
assert.match(elements['terminal-status'].textContent, /^Could not connect/);
controller.close();
assert.equal(sockets.at(-1).closed, true);

const index = fs.readFileSync(path.join(__dirname, '../web/index.html'), 'utf8');
const app = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
assert.match(index, /id="terminal-tab"[^>]*role="tab"[^>]*aria-controls="terminal-view"[^>]*disabled/);
assert.match(index, /id="terminal-view"[^>]*role="tabpanel"[^>]*aria-labelledby="terminal-tab"[^>]*hidden/);
assert.match(app, /terminalTab.addEventListener\('click', \(\) => setActiveView\('terminal'\)\)/);

const viewElements = {
  messages: new Element(), composerWrap: new Element(), changes: new Element(),
  terminalView: elements['terminal-view'], conversationTab: new Element(), changesTab: new Element(), terminalTab: new Element(),
  viewPicker: new Element(), viewChoice: new Element(),
};
global.elements = viewElements;
global.state = {selected: session};
let currentRequestHidden = false;
global.hideCurrentRequest = () => { currentRequestHidden = true; };
global.updateCurrentRequest = () => { currentRequestHidden = false; };
vm.runInThisContext(app.slice(app.indexOf('function setActiveView('), app.indexOf('function renderError(')), {filename: 'app.js'});
const choiceListener = app.indexOf("elements.viewChoice.addEventListener('change'");
assert.notEqual(choiceListener, -1);
vm.runInThisContext(app.slice(choiceListener, app.indexOf('\n', choiceListener)), {filename: 'app.js'});
const pageHideListener = app.indexOf("window.addEventListener('pagehide'");
assert.notEqual(pageHideListener, -1);
vm.runInThisContext(app.slice(pageHideListener, app.indexOf('function interruptActiveTurn(', pageHideListener)), {filename: 'app.js'});
for (const [name, tab] of [['conversation', viewElements.conversationTab], ['changes', viewElements.changesTab], ['terminal', viewElements.terminalTab]]) {
  tab.addEventListener('click', () => setActiveView(name));
}
const beforeTabs = sockets.length;
setActiveView('conversation');
assert.equal(sockets.length, beforeTabs, 'Conversation must not start a shell');
assert.equal(viewElements.messages.hidden, false);
assert.equal(viewElements.composerWrap.hidden, false);
assert.equal(viewElements.terminalView.hidden, true);
viewElements.viewChoice.value = 'terminal';
viewElements.viewChoice.emit('change');
assert.equal(sockets.length, beforeTabs + 1);
assert.equal(viewElements.messages.hidden, true);
assert.equal(viewElements.composerWrap.hidden, true);
assert.equal(viewElements.changes.hidden, true);
assert.equal(viewElements.terminalView.hidden, false);
assert.equal(viewElements.viewPicker.dataset.view, 'terminal');
assert.equal(currentRequestHidden, true);
assert.equal(viewElements.terminalTab.attributes['aria-selected'], 'true');
assert.equal(viewElements.terminalTab.tabIndex, 0);
assert.equal(viewElements.conversationTab.tabIndex, -1);
assert.equal(viewElements.changesTab.tabIndex, -1);
setActiveView('changes');
assert.equal(viewElements.viewChoice.value, 'changes');
assert.equal(viewElements.viewPicker.dataset.view, 'changes');
assert.equal(viewElements.changes.hidden, false);
assert.equal(viewElements.terminalView.hidden, true);
assert.equal(viewElements.composerWrap.hidden, true);
terminals.at(-1).focused = false;
sockets.at(-1).connect();
assert.equal(terminals.at(-1).focused, false, 'Connecting a hidden terminal must not steal focus');
setActiveView('terminal');
assert.equal(sockets.length, beforeTabs + 1, 'Switching tabs must reuse the shell');
setActiveView('conversation');
assert.equal(viewElements.viewChoice.value, 'conversation');
assert.equal(viewElements.viewPicker.dataset.view, 'conversation');
assert.equal(currentRequestHidden, false);
assert.equal(viewElements.composerWrap.hidden, false);
function tabKey(key, target) {
  let prevented = false;
  handleViewTabKeydown({key, target, preventDefault() { prevented = true; }});
  assert.equal(prevented, true);
}
tabKey('End', viewElements.conversationTab);
assert.equal(viewElements.terminalTab.focused, true);
assert.equal(viewElements.terminalTab.attributes['aria-selected'], 'true');
tabKey('ArrowRight', viewElements.terminalTab);
assert.equal(viewElements.conversationTab.attributes['aria-selected'], 'true');
tabKey('ArrowLeft', viewElements.conversationTab);
assert.equal(viewElements.terminalTab.attributes['aria-selected'], 'true');
tabKey('Home', viewElements.terminalTab);
assert.equal(viewElements.conversationTab.attributes['aria-selected'], 'true');
viewElements.terminalTab.disabled = true;
tabKey('End', viewElements.conversationTab);
assert.equal(viewElements.changesTab.attributes['aria-selected'], 'true');
controller.close();

viewElements.terminalTab.disabled = false;
for (const view of ['terminal', 'conversation', 'changes']) {
  setActiveView('terminal');
  const connection = sockets.at(-1);
  const terminal = terminals.at(-1);
  connection.connect();
  setActiveView(view);
  window.emit('pagehide', {persisted: true});
  window.emit('pageshow', {persisted: true});
  assert.equal(connection.closed, true);
  assert.equal(terminal.disposed, true);
  assert.equal(viewElements.terminalView.hidden, true);
  assert.equal(viewElements.viewChoice.value, view === 'terminal' ? 'conversation' : view);
  assert.equal(viewElements.messages.hidden, view === 'changes');
  assert.equal(viewElements.composerWrap.hidden, view === 'changes');
  assert.equal(viewElements.changes.hidden, view !== 'changes');
  const connections = sockets.length;
  setActiveView('terminal');
  assert.equal(sockets.length, connections + 1);
  sockets.at(-1).connect();
  assert.equal(elements['terminal-status'].textContent, 'Connected');
  assert.equal(terminals.at(-1).focused, true);
  controller.close();
}

for (const asset of ['xterm.css', 'xterm.js', 'xterm-addon-fit.js', 'terminal.js']) {
  assert.ok(index.includes(`/assets/${asset}`));
  assert.ok(fs.statSync(path.join(__dirname, '../web', asset)).size > 0);
}
for (const asset of ['xterm.css', 'xterm.js', 'xterm-addon-fit.js']) {
  const content = fs.readFileSync(path.join(__dirname, '../web', asset), 'utf8');
  assert.match(content, /^\/\* Generated from @xterm\//);
  assert.ok(!content.includes('sourceMappingURL='), `${asset} references an unavailable source map`);
}
process.stdout.write('Terminal tests passed\n');
