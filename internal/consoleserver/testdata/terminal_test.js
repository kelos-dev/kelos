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
  open = false;
  textContent = '';
  showModal() { this.open = true; }
  close() { this.open = false; this.emit('close'); }
  replaceChildren() {}
}

const elements = Object.fromEntries(['terminal-dialog', 'terminal-container', 'terminal-title', 'terminal-status', 'close-terminal']
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
  controller.open(unavailable);
}
assert.equal(sockets.length, 0);

controller.open(session);
const socket = sockets[0];
const terminal = terminals[0];
assert.equal(terminal.style.nonce, 'test-style-nonce');
assert.equal(document.createElement('style').nonce, undefined);
assert.equal(elements['terminal-dialog'].open, true);
assert.equal(elements['terminal-title'].textContent, 'Terminal · team-a/chat');
assert.equal(socket.url, 'wss://console.example/api/sessions/team-a/chat/exec');
assert.equal(socket.binaryType, 'arraybuffer');
socket.connect();
assert.equal(terminal.focused, true);
assert.equal(elements['terminal-status'].textContent, 'Connected');
assert.deepEqual(JSON.parse(socket.sent[0]), {type: 'resize', cols: 100, rows: 30});
terminal.input('héllo\r\x03');
assert.equal(new TextDecoder().decode(socket.sent[1]), 'héllo\r\x03');
terminal.binary('\x80\xff');
assert.deepEqual([...socket.sent[2]], [128, 255]);
terminal.cols = 120;
terminal.rows = 40;
terminal.resize();
assert.deepEqual(JSON.parse(socket.sent[3]), {type: 'resize', cols: 120, rows: 40});
observers[0].callback();
assert.equal(terminal.addon.fits, 2);
const output = new TextEncoder().encode('\x1b[32mhello\x1b[0m\r\n');
socket.emit('message', {data: output.buffer});
assert.deepEqual(terminal.output[0], output);
let prevented = false;
elements['terminal-dialog'].emit('cancel', {preventDefault() { prevented = true; }});
assert.equal(prevented, true);
assert.equal(socket.closed, undefined);

socket.emit('message', {data: JSON.stringify({type: 'exit'})});
socket.close();
assert.equal(elements['terminal-status'].textContent, 'Shell exited');
assert.equal(terminal.options.disableStdin, true);
terminal.input('ignored');
assert.equal(socket.sent.length, 4);
assert.equal(sockets.length, 1);
elements['close-terminal'].emit('click');
assert.equal(elements['terminal-dialog'].open, false);
assert.equal(terminal.disposed, true);
assert.ok(terminal.subscriptions.every(subscription => subscription.disposed));
assert.equal(observers[0].disconnected, true);

controller.open(session);
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
  controller.open(session);
  const connection = sockets.at(-1);
  controller.sync(changed);
  assert.equal(connection.closed, true);
  assert.equal(terminals.at(-1).disposed, true);
  assert.equal(elements['terminal-dialog'].open, false);
}

controller.open(session);
sockets.at(-1).connect();
const pasted = 'héllo'.repeat(20000);
terminals.at(-1).input(pasted);
const chunks = sockets.at(-1).sent.slice(1);
assert.ok(chunks.length > 1 && chunks.every(chunk => chunk.length <= 32 * 1024));
assert.equal(Buffer.concat(chunks).toString('utf8'), pasted);
controller.close();

controller.open(session);
sockets.at(-1).close();
assert.match(elements['terminal-status'].textContent, /^Disconnected/);
controller.close();
controller.open(session);
sockets.at(-1).emit('error');
assert.match(elements['terminal-status'].textContent, /^Could not connect/);
window.emit('pagehide');
assert.equal(sockets.at(-1).closed, true);

const index = fs.readFileSync(path.join(__dirname, '../web/index.html'), 'utf8');
const app = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
assert.match(index, /id="open-terminal"[^>]*disabled/);
assert.match(index, /id="terminal-dialog"[^>]*aria-labelledby="terminal-title"/);
assert.match(app, /terminalButton.addEventListener\('click', \(\) => sessionTerminal.open\(state.selected\)\)/);
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
