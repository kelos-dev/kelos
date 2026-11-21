const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

class TestNode {
  constructor(tag, value = '') {
    this.tag = tag;
    this.value = value;
    this.children = [];
    this.dataset = {};
    this.attributes = new Map();
    this.listeners = new Map();
    this.style = {};
    this.classes = new Set();
    this.classList = {
      add: (...names) => names.forEach((name) => this.classes.add(name)),
      remove: (...names) => names.forEach((name) => this.classes.delete(name)),
      contains: (name) => this.classes.has(name),
    };
  }

  append(...nodes) {
    for (const node of nodes) {
      if (node.tag === '#fragment') this.append(...node.children);
      else {
        node.parent = this;
        this.children.push(node);
      }
    }
  }

  replaceChildren(...nodes) {
    this.children = [];
    this.append(...nodes);
  }

  get childNodes() { return this.children; }
  get nodeName() { return this.tag; }
  get data() { return this.value; }
  set data(value) { this.value = value; }
  appendData(value) { this.value += value; }
  appendChild(node) { this.append(node); }

  replaceChild(next, current) {
    this.children[this.children.indexOf(current)] = next;
    next.parent = this;
    current.parent = null;
  }

  removeChild(node) { node.remove(); }
  contains(node) { return this === node || this.children.some((child) => child.contains(node)); }
  isEqualNode(node) { return serialize(this) === serialize(node); }

  querySelector(selector) {
    for (const child of this.children) {
      if (selector.startsWith('.') ? child.classList.contains(selector.slice(1)) : child.tag === selector) return child;
      const found = child.querySelector(selector);
      if (found) return found;
    }
    return null;
  }

  querySelectorAll(selector) {
    const matches = [];
    for (const child of this.children) {
      if (selector.startsWith('.') ? child.classList.contains(selector.slice(1)) : child.tag === selector) matches.push(child);
      matches.push(...child.querySelectorAll(selector));
    }
    return matches;
  }

  get hidden() { return this.attributes.has('hidden'); }
  set hidden(value) {
    if (value) this.attributes.set('hidden', '');
    else this.attributes.delete('hidden');
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

  setAttribute(name, value) {
    if (name === 'class') this.className = value;
    else if (name.startsWith('data-')) this.dataset[name.slice(5)] = value;
    else if (['href', 'target', 'rel', 'type', 'start'].includes(name)) this[name] = value;
    else if (name === 'checked' || name === 'disabled') this[name] = true;
    else this.attributes.set(name, String(value));
  }

  getAttribute(name) {
    if (name === 'class') return this.className || null;
    if (name.startsWith('data-')) return this.dataset[name.slice(5)] ?? null;
    if (['href', 'target', 'rel', 'type', 'start'].includes(name)) return this[name] || null;
    if (name === 'checked' || name === 'disabled') return this[name] ? '' : null;
    return this.attributes.get(name) ?? null;
  }

  getAttributeNames() {
    return [...new Set([...this.attributes.keys(), ...Object.keys(this.dataset).map((name) => `data-${name}`), 'class', 'href', 'target', 'rel', 'type', 'start', 'checked', 'disabled'])]
      .filter((name) => this.hasAttribute(name));
  }

  hasAttribute(name) { return this.getAttribute(name) !== null; }

  removeAttribute(name) {
    if (name === 'class') this.className = '';
    else if (name.startsWith('data-')) delete this.dataset[name.slice(5)];
    else if (['href', 'target', 'rel', 'type', 'start', 'checked', 'disabled'].includes(name)) delete this[name];
    else this.attributes.delete(name);
  }

  set defaultChecked(value) { this.checked = value; }

  addEventListener(name, listener) {
    this.listeners.set(name, listener);
  }

  click() {
    return this.listeners.get('click')?.();
  }

  select() {
    this.selected = true;
  }

  remove() {
    if (!this.parent) return;
    this.parent.children = this.parent.children.filter((child) => child !== this);
    this.parent = null;
  }
}

global.Element = class { static [Symbol.hasInstance](node) { return !node.tag.startsWith('#'); } };
global.Text = class { static [Symbol.hasInstance](node) { return node.tag === '#text'; } };
global.HTMLInputElement = class { static [Symbol.hasInstance](node) { return node.tag === 'input'; } };

const animationFrames = new Map();
let nextFrameID = 0;
function flushAnimationFrames() {
  const callbacks = [...animationFrames.values()];
  animationFrames.clear();
  callbacks.forEach((callback) => callback());
}

global.document = {
  createElement: (tag) => new TestNode(tag),
  createTextNode: (value) => new TestNode('#text', value),
  createDocumentFragment: () => new TestNode('#fragment'),
  body: new TestNode('body'),
  head: new TestNode('head'),
  execCommand: () => false,
};
global.window = {
  clearTimeout: () => {},
  setTimeout: () => 1,
  requestAnimationFrame: (callback) => {
    animationFrames.set(++nextFrameID, callback);
    return nextFrameID;
  },
  cancelAnimationFrame: (id) => animationFrames.delete(id),
};
Object.defineProperty(global, 'navigator', {
  configurable: true,
  value: {clipboard: {writeText: async () => {}}},
});
global.state = {
  assistantSegmentByTurn: new Map(),
  assistantTextByTurn: new Map(),
  selected: {provider: 'claude'},
};
global.elements = {messages: new TestNode('div')};
global.ensureConversation = () => {};
global.providerInitials = () => 'C';
global.scrollToBottom = () => {};
let nearBottom = true;
let bottomAnchors = 0;
global.messagesNearBottom = () => nearBottom;
global.scheduleBottomAnchor = () => { bottomAnchors++; };

function escapeHTML(value) {
  return value.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;');
}

function serialize(node) {
  if (node.tag === '#text') return escapeHTML(node.value);
  if (node.tag === '#fragment') return node.children.map(serialize).join('');

  const attributes = [];
  if (node.className) attributes.push(`class="${escapeHTML(node.className)}"`);
  if (node.href) attributes.push(`href="${escapeHTML(node.href)}"`);
  if (node.target) attributes.push(`target="${escapeHTML(node.target)}"`);
  if (node.rel) attributes.push(`rel="${escapeHTML(node.rel)}"`);
  for (const [name, value] of Object.entries(node.dataset)) attributes.push(`data-${name}="${escapeHTML(value)}"`);
  if (node.start) attributes.push(`start="${node.start}"`);
  if (node.type) attributes.push(`type="${escapeHTML(node.type)}"`);
  if (node.checked) attributes.push('checked');
  if (node.disabled) attributes.push('disabled');
  for (const [name, value] of node.attributes) attributes.push(`${name}="${escapeHTML(value)}"`);
  const suffix = attributes.length ? ` ${attributes.join(' ')}` : '';
  if (node.tag === 'input') return `<input${suffix}>`;
  return `<${node.tag}${suffix}>${node.children.map(serialize).join('')}</${node.tag}>`;
}

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');
const rendererStart = application.indexOf('function trimURLSuffix');
const rendererEnd = application.indexOf('function renderTool');
assert.notEqual(rendererStart, -1, 'renderer start not found');
assert.notEqual(rendererEnd, -1, 'renderer end not found');
vm.runInThisContext(application.slice(rendererStart, rendererEnd), {filename: 'app.js'});

function render(markdown) {
  const root = new TestNode('div');
  renderMessageMarkdown(root, markdown);
  return serialize(root);
}

const formatting = render([
  '# Heading',
  '',
  'Text with **bold**, *emphasis*, ***both***, **bold with *nested emphasis* text**, ~~removed~~, and `inline`.',
  '',
  '- [x] parent',
  '  - child',
  '- item',
  '',
  '> quoted',
].join('\n'));
assert.match(formatting, /<h1>Heading<\/h1>/);
assert.match(formatting, /<strong>bold<\/strong>/);
assert.match(formatting, /<em>emphasis<\/em>/);
assert.match(formatting, /<strong><em>both<\/em><\/strong>/);
assert.match(formatting, /<strong>bold with <em>nested emphasis<\/em> text<\/strong>/);
assert.match(formatting, /<del>removed<\/del>/);
assert.match(formatting, /<code class="inline-code">inline<\/code>/);
assert.match(formatting, /<ul class="task-list"><li class="task-list-item"><input type="checkbox" checked disabled><p>parent<\/p><ul>/);
assert.match(formatting, /<blockquote><p>quoted<\/p><\/blockquote>/);

assert.equal(render('## About C#'), '<div><h2>About C#</h2></div>');
assert.equal(render('## About ###'), '<div><h2>About</h2></div>');

const mixedTasks = render('- [x] done\n- ordinary');
assert.match(mixedTasks, /<ul class="task-list"><li class="task-list-item">/);
assert.match(mixedTasks, /<li><p>ordinary<\/p><\/li>/);

const table = render([
  '| Name | Result | Note |',
  '| :--- | :----: | ---: |',
  '| **alpha** | [safe](https://example.com/path) | `a\\|b` |',
  '| escaped \\| pipe | <img src=x onerror=alert(1)> | short |',
  '| missing | cell |',
  '| extra | cells | are | ignored |',
].join('\n'));
assert.match(table, /<div class="markdown-table-container"><table><thead><tr>/);
assert.match(table, /<th class="table-align-left">Name<\/th>/);
assert.match(table, /<th class="table-align-center">Result<\/th>/);
assert.match(table, /<th class="table-align-right">Note<\/th>/);
assert.match(table, /<td class="table-align-left"><strong>alpha<\/strong><\/td>/);
assert.match(table, /<td class="table-align-center"><a href="https:\/\/example.com\/path" target="_blank" rel="noopener noreferrer">safe<\/a><\/td>/);
assert.match(table, /<td class="table-align-right"><code class="inline-code">a\|b<\/code><\/td>/);
assert.match(table, /<td class="table-align-left">escaped \| pipe<\/td>/);
assert.match(table, /<td class="table-align-center">&lt;img src=x onerror=alert\(1\)&gt;<\/td>/);
assert.match(table, /<td class="table-align-right"><\/td>/);
assert.doesNotMatch(table, /<img|ignored/);

const tableWithoutOuterPipes = render('Name | Result\n--- | ---:\nonly-first\nfirst | second');
assert.match(tableWithoutOuterPipes, /<table><thead><tr><th>Name<\/th><th class="table-align-right">Result<\/th><\/tr><\/thead>/);
assert.match(tableWithoutOuterPipes, /<tbody><tr><td>only-first<\/td><td class="table-align-right"><\/td><\/tr>/);
assert.match(tableWithoutOuterPipes, /<tr><td>first<\/td><td class="table-align-right">second<\/td><\/tr><\/tbody>/);

const escapedBacktick = render('| first | second |\n| --- | --- |\n| `code\\` | value |');
assert.match(escapedBacktick, /<tbody><tr><td><code class="inline-code">code\\<\/code><\/td><td>value<\/td><\/tr><\/tbody>/);

assert.equal(render('ordinary | prose\nwithout a delimiter'), '<div><p>ordinary | prose\nwithout a delimiter<\/p><\/div>');
assert.doesNotMatch(render('| one | two |\n| --- |'), /<table>/);

const oversizedColumnCount = Math.floor(maxMarkdownTableCells / 2) + 1;
const oversizedTable = [
  Array(oversizedColumnCount).fill('heading').join(' | '),
  Array(oversizedColumnCount).fill('---').join(' | '),
  Array(oversizedColumnCount).fill('value').join(' | '),
].join('\n');
const oversizedRoot = new TestNode('div');
renderMessageMarkdown(oversizedRoot, oversizedTable);
assert.equal(oversizedRoot.textContent, oversizedTable);
assert.doesNotMatch(serialize(oversizedRoot), /<table>/);

const identifiers = render('assistant_segment_by_turn and foo__bar__baz');
assert.equal(identifiers, '<div><p>assistant_segment_by_turn and foo__bar__baz</p></div>');

const malformed = '*a '.repeat(8000);
const malformedRoot = new TestNode('div');
const scanBudget = createInlineScanBudget(malformed);
appendInlineMarkdown(malformedRoot, malformed, 0, true, scanBudget);
assert.equal(scanBudget.exhausted, true);
assert.equal(malformedRoot.textContent, malformed);

const unmatchedBackticks = '`'.repeat(20000);
const backtickRoot = new TestNode('div');
const backtickBudget = createInlineScanBudget(unmatchedBackticks);
const initialBacktickBudget = backtickBudget.remaining;
appendInlineMarkdown(backtickRoot, unmatchedBackticks, 0, true, backtickBudget);
assert.equal(backtickRoot.textContent, unmatchedBackticks);
assert.equal(backtickBudget.remaining, initialBacktickBudget - unmatchedBackticks.length);

const links = render('[safe](https://example.com/path) HTTPS://example.com/UPPER');
assert.match(links, /<a href="https:\/\/example.com\/path" target="_blank" rel="noopener noreferrer">safe<\/a>/);
assert.match(links, /<a href="https:\/\/example.com\/UPPER" target="_blank" rel="noopener noreferrer">HTTPS:\/\/example.com\/UPPER<\/a>/);
assert.equal(render('http://'), '<div><p>http://</p></div>');
assert.equal(render('https://'), '<div><p>https://</p></div>');

const untrusted = render([
  '<img src=x onerror=alert(1)>',
  '',
  '[unsafe](javascript:alert(1))',
  '',
  '```html',
  '<script>alert(1)</script>',
  '```',
].join('\n'));
assert.match(untrusted, /&lt;img src=x onerror=alert\(1\)&gt;/);
assert.match(untrusted, /\[unsafe\]\(javascript:alert\(1\)\)/);
assert.match(untrusted, /<div class="code-block">.*<pre data-language="html"><code class="language-html">&lt;script&gt;alert\(1\)&lt;\/script&gt;<\/code><\/pre><\/div>/);
assert.doesNotMatch(untrusted, /<script|<img|href="javascript:/);

assert.equal(completedAssistantText('complete response', 'retained suffix'), 'complete response');
assert.equal(completedAssistantText('', 'streamed response'), 'streamed response');

renderAssistantMessage({turnId: 'completed', text: 'first block'});
renderAssistantMessage({turnId: 'completed', text: 'second block'});
assert.equal(elements.messages.children.length, 2);
assert.equal(elements.messages.children[0].textContent, 'Cfirst block');
assert.equal(elements.messages.children[1].textContent, 'Csecond block');

elements.messages.replaceChildren();
renderAssistantDelta({turnId: 'streamed', text: 'partial response'});
renderAssistantMessage({turnId: 'streamed', text: 'complete response'});
assert.equal(elements.messages.children.length, 1);
assert.equal(elements.messages.children[0].textContent, 'Ccomplete response');
assert.equal(state.assistantSegmentByTurn.size, 0);
assert.equal(state.assistantTextByTurn.size, 0);
assert.equal(animationFrames.size, 0, 'completion cancels the queued partial render');

function resetStreaming() {
  assert.equal(animationFrames.size, 0);
  elements.messages.replaceChildren();
  state.replayingHistory = false;
  state.pinHistoryToBottom = false;
  nearBottom = true;
  bottomAnchors = 0;
}

function stream(text, turnId = 'live') {
  renderAssistantDelta({turnId, text});
  flushAnimationFrames();
  return state.assistantSegmentByTurn.get(turnId);
}

resetStreaming();
renderAssistantDelta({turnId: 'live', text: '# Heading\n\n'});
renderAssistantDelta({turnId: 'live', text: '**Visible before completion**'});
const liveBubble = state.assistantSegmentByTurn.get('live');
assert.equal(animationFrames.size, 1, 'multiple deltas share a frame');
assert.equal(liveBubble.textContent, '');
flushAnimationFrames();
assert.match(serialize(liveBubble), /<h1>Heading<\/h1><p><strong>Visible before completion<\/strong><\/p>/);
assert.equal(liveBubble.classList.contains('is-streaming'), true);
assert.equal(bottomAnchors, 1);
const headingNode = liveBubble.children[0];
const paragraphNode = liveBubble.children[1];
stream('\n\n```js\nconst a = 1;');
const codeBlock = liveBubble.children[2];
const preNode = codeBlock.querySelector('pre');
const copyNode = codeBlock.querySelector('button');
const codeTextNode = codeBlock.querySelector('code').childNodes[0];
preNode.scrollLeft = 60;
stream('\nconst b = 2;\n```\n\nMore text');
assert.equal(liveBubble.children[0], headingNode);
assert.equal(liveBubble.children[1], paragraphNode);
assert.equal(liveBubble.children[2], codeBlock);
assert.equal(codeBlock.querySelector('pre'), preNode);
assert.equal(codeBlock.querySelector('button'), copyNode);
assert.equal(codeBlock.querySelector('code').childNodes[0], codeTextNode);
assert.equal(preNode.scrollLeft, 60);
assert.equal(codeBlock.querySelector('code').textContent, 'const a = 1;\nconst b = 2;');
nearBottom = false;
const anchorsBeforeScrollingUp = bottomAnchors;
stream(' while reading earlier messages');
assert.equal(bottomAnchors, anchorsBeforeScrollingUp, 'streaming does not pull a reader to the bottom');
renderAssistantMessage({turnId: 'live', text: state.assistantTextByTurn.get('live')});
assert.equal(liveBubble.children[2], codeBlock, 'completion preserves mounted blocks');
assert.equal(liveBubble.classList.contains('is-streaming'), false);
assert.equal(bottomAnchors, anchorsBeforeScrollingUp);

resetStreaming();
const tableBubble = stream('| Name | Status |\n');
assert.equal(tableBubble.querySelector('table'), null);
stream('| --- | --- |\n| Build | Running |\n');
const tableContainer = tableBubble.children[0];
const tableNode = tableBubble.querySelector('table');
const headerNode = tableBubble.querySelector('thead');
const firstRow = tableBubble.querySelector('tbody').children[0];
tableContainer.scrollLeft = 40;
stream('| Tests | Pending |\n');
assert.equal(tableBubble.children[0], tableContainer);
assert.equal(tableBubble.querySelector('table'), tableNode);
assert.equal(tableBubble.querySelector('thead'), headerNode);
assert.equal(tableBubble.querySelector('tbody').children[0], firstRow);
assert.equal(tableContainer.scrollLeft, 40);
assert.match(serialize(tableBubble), /<tr><td>Tests<\/td><td>Pending<\/td><\/tr>/);
endAssistantSegment('live');

resetStreaming();
const partialBubble = stream('**Bold');
assert.equal(partialBubble.textContent, '**Bold');
stream('** and [docs](https://example.com');
assert.equal(partialBubble.querySelector('strong').textContent, 'Bold');
stream(')');
assert.equal(partialBubble.querySelector('a').textContent, 'docs');
assert.equal(partialBubble.querySelector('a').href, 'https://example.com/');
endAssistantSegment('live');

const chunkedMarkdown = [
  '# Heading\n\nParagraph **bold** and *italic* with [link](https://example.com).\n\nNext paragraph.',
  '```js\n\nconst a = 1;\n\nconst b = 2;\n```\n\nAfter code.',
  '~~~python\nprint("hello")\n~~~\n\n    indented code\n\nAfter code.',
  '> A quote\n> with `code`\n\n- [x] Done\n  - Nested\n- [ ] Pending\n\n1. First\n2. Second',
  '| Name | Value |\n| --- | ---: |\n| a | 1 |\n| b | 2 |\n\nAfter table.',
  'Paragraph\n```info`invalid\n\n---\n\n<img src=x onerror=alert(1)>\n\n[bad](javascript:alert(1))',
];
for (const markdown of chunkedMarkdown) {
  resetStreaming();
  let bubble;
  for (const character of markdown) bubble = stream(character);
  assert.equal(`<div>${bubble.children.map(serialize).join('')}</div>`, render(markdown));
  endAssistantSegment('live');
}

resetStreaming();
const interrupted = stream('```sh\nprintf hello');
renderAssistantDelta({turnId: 'live', text: '\nprintf world'});
endAssistantSegment('live');
assert.equal(animationFrames.size, 0);
assert.equal(interrupted.querySelector('code').textContent, 'printf hello\nprintf world');
assert.equal(interrupted.classList.contains('is-streaming'), false);
assert.equal(state.assistantSegmentByTurn.size, 0);

resetStreaming();
renderAssistantDelta({turnId: 'live', text: 'queued '});
state.replayingHistory = true;
renderAssistantDelta({turnId: 'live', text: '**history**'});
const historyBubble = state.assistantSegmentByTurn.get('live');
assert.equal(animationFrames.size, 0, 'history renders synchronously and cancels queued work');
assert.equal(historyBubble.querySelector('strong').textContent, 'history');
assert.equal(bottomAnchors, 0, 'older history does not schedule bottom anchoring');
state.pinHistoryToBottom = true;
renderAssistantDelta({turnId: 'live', text: ' tail'});
assert.equal(bottomAnchors, 1);
endAssistantSegment('live');

resetStreaming();
renderAssistantDelta({turnId: 'cached', text: '**Cached session**'});
const cachedBubble = state.assistantSegmentByTurn.get('cached');
const currentMessages = elements.messages;
elements.messages = new TestNode('div');
flushAnimationFrames();
assert.equal(cachedBubble.querySelector('strong').textContent, 'Cached session');
assert.equal(elements.messages.textContent, '');
assert.equal(bottomAnchors, 0, 'a cached session cannot scroll the selected session');
endAssistantSegment('cached');
elements.messages = currentMessages;

async function testCodeBlockCopy() {
  const copied = [];
  global.navigator.clipboard.writeText = async (value) => copied.push(value);
  const root = new TestNode('div');
  renderMessageMarkdown(root, '```js\nconst value = \"<safe>\";\n```');
  const block = root.children[0];
  assert.equal(block.className, 'code-block');
  assert.equal(block.children[0].className, 'code-block-toolbar');
  assert.equal(block.children[0].children[0].textContent, 'js');
  const button = block.children[0].children[1];
  assert.equal(button.textContent, 'Copy');
  assert.equal(button.attributes.get('aria-label'), 'Copy code block');

  await button.click();
  assert.deepEqual(copied, ['const value = "<safe>";']);
  assert.equal(button.textContent, 'Copied');
  assert.equal(button.attributes.get('aria-label'), 'Code copied');
  assert.equal(button.disabled, false);

  global.navigator.clipboard.writeText = async () => {
    throw new Error('denied');
  };
  let fallbackValue = '';
  document.execCommand = (command) => {
    assert.equal(command, 'copy');
    fallbackValue = document.body.children[0].value;
    return true;
  };
  await writeClipboardText('fallback content');
  assert.equal(fallbackValue, 'fallback content');
  assert.equal(document.body.children.length, 0);

  resetStreaming();
  global.navigator.clipboard.writeText = async (value) => copied.push(value);
  const bubble = stream('```js\nfirst');
  const streamingButton = bubble.querySelector('button');
  await streamingButton.click();
  stream('\nsecond');
  assert.equal(bubble.querySelector('button'), streamingButton);
  assert.equal(streamingButton.textContent, 'Copied', 'streaming preserves copy feedback');
  await streamingButton.click();
  assert.equal(copied.at(-1), 'first\nsecond', 'copy reads the current code');
  renderAssistantMessage({turnId: 'live', text: '```js\nreplacement\n```'});
  await streamingButton.click();
  assert.equal(copied.at(-1), 'replacement', 'copy reads the authoritative final text');
}

async function testMermaidRendering() {
  const calls = [];
  const pending = [];
  let configuration;
  global.mermaid = {
    initialize: (options) => { configuration = options; },
    render: (id, source, container) => {
      assert.equal(container.className, 'mermaid-render-target');
      assert.equal(container.attributes.get('aria-hidden'), 'true');
      assert.equal(document.body.contains(container), true);
      calls.push({id, source});
      return new Promise((resolve, reject) => pending.push({resolve, reject}));
    },
  };
  const settle = async () => {
    for (let i = 0; i < 5; i++) await Promise.resolve();
  };
  const svg = '<svg xmlns="http://www.w3.org/2000/svg"><text>A → B</text></svg>';
  const source = 'flowchart LR\nA --> B';
  const markdown = `Before\n\n\`\`\`mermaid\n${source}\n\`\`\`\n\nAfter`;
  assert.equal(document.head.children.length, 0, 'ordinary Markdown does not load Mermaid');
  const unavailable = new TestNode('div');
  renderMessageMarkdown(unavailable, `\`\`\`mermaid\n${source}\n\`\`\``);
  document.head.children[0].onerror();
  await settle();
  assert.equal(unavailable.querySelector('code').textContent, source);
  assert.equal(unavailable.querySelector('pre').hidden, false, 'source remains visible when the library cannot load');
  assert.equal(unavailable.querySelector('.mermaid-preview').textContent, 'Unable to render Mermaid diagram');
  assert.equal(document.head.children.length, 0, 'a failed script is removed');
  assert.equal(calls.length, 0, 'a failed load does not render a diagram');

  const root = new TestNode('div');
  renderMessageMarkdown(root, markdown);
  assert.equal(document.head.children.length, 1, 'the next diagram retries a failed library load');
  const script = document.head.children[0];
  assert.equal(script.src, '/assets/mermaid.min.js');
  script.onload();
  await settle();
  assert.equal(configuration.startOnLoad, false);
  assert.equal(configuration.securityLevel, 'strict');
  assert.equal(configuration.htmlLabels, false);
  assert.equal(configuration.suppressErrorRendering, true);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].source, source);
  const block = root.querySelector('.mermaid-block');
  assert.equal(block.querySelector('pre').hidden, false, 'source remains visible while rendering');
  pending.shift().resolve({svg});
  await settle();
  const preview = block.querySelector('.mermaid-preview');
  const image = preview.querySelector('img');
  assert.equal(image.alt, 'Mermaid diagram');
  assert.equal(decodeURIComponent(image.src.split(',')[1]), svg);
  assert.equal(preview.hidden, false);
  assert.equal(block.querySelector('pre').hidden, true);
  assert.equal(root.children[0].textContent, 'Before');
  assert.equal(root.children.at(-1).textContent, 'After');
  const copied = [];
  navigator.clipboard.writeText = async (text) => copied.push(text);
  await block.querySelector('button').click();
  assert.deepEqual(copied, [source]);

  renderMessageMarkdown(root, `${markdown} more text`);
  assert.equal(root.querySelector('.mermaid-block'), block);
  assert.equal(preview.querySelector('img'), image, 'later text preserves the rendered diagram');
  assert.equal(block.querySelector('pre').hidden, true);
  assert.equal(block.querySelector('button').textContent, 'Copied');
  assert.equal(calls.length, 1, 'unchanged diagrams are rendered once');

  resetStreaming();
  const bubble = stream(`\`\`\`mermaid\n${source}`);
  await settle();
  assert.equal(calls.length, 1, 'an open streaming fence remains source code');
  stream('\n```');
  await settle();
  assert.equal(calls.length, 2, 'closing a streaming fence renders the diagram');
  pending.shift().resolve({svg});
  await settle();
  const streamingImage = bubble.querySelector('img');
  stream('\n\nFollowing paragraph');
  assert.equal(bubble.querySelector('img'), streamingImage);
  endAssistantSegment('live');
  assert.equal(calls.length, 2, 'completion reuses a closed diagram');

  resetStreaming();
  const unfinished = stream(`\`\`\`mermaid\n${source}`);
  endAssistantSegment('live');
  await settle();
  assert.equal(calls.length, 3, 'completion renders an unclosed fence');
  pending.shift().resolve({svg});
  await settle();
  assert.equal(unfinished.querySelector('pre').hidden, true);

  const failure = new TestNode('div');
  renderMessageMarkdown(failure, '```mermaid\ninvalid diagram\n```');
  await settle();
  pending.shift().reject(new Error('Syntax error'));
  await settle();
  assert.equal(failure.querySelector('code').textContent, 'invalid diagram');
  assert.equal(failure.querySelector('pre').hidden, false);
  assert.equal(failure.querySelector('.mermaid-preview').textContent, 'Unable to render Mermaid diagram');
  const callsBeforeRetry = calls.length;
  renderMessageMarkdown(failure, '```mermaid\ninvalid diagram\n```\n\nMore text');
  await settle();
  assert.equal(calls.length, callsBeforeRetry, 'unchanged invalid diagrams are not retried');

  renderMessageMarkdown(failure, `~~~MERMAID\n${source}\n~~~`);
  await settle();
  pending.shift().resolve({svg});
  await settle();
  assert.equal(failure.querySelector('pre').hidden, true, 'corrected diagrams replace failures');

  const racing = new TestNode('div');
  renderMessageMarkdown(racing, '```mermaid\nflowchart LR\nA --> B\n```');
  await settle();
  const stale = pending.shift();
  renderMessageMarkdown(racing, '```mermaid\nflowchart LR\nA --> C\n```');
  await settle();
  const current = pending.shift();
  current.resolve({svg});
  await settle();
  const currentImage = racing.querySelector('img');
  stale.resolve({svg: '<svg>stale</svg>'});
  await settle();
  assert.equal(racing.querySelector('img'), currentImage, 'outdated results cannot replace the current diagram');
  assert.equal(racing.querySelector('code').textContent, 'flowchart LR\nA --> C');

  const nested = new TestNode('div');
  renderMessageMarkdown(nested, '> ```mermaid\n> flowchart LR\n> A --> B\n> ```\n\n```mermaid\nflowchart LR\nA --> B\n```');
  await settle();
  assert.equal(nested.querySelectorAll('.mermaid-block').length, 2);
  for (const render of pending.splice(0)) render.resolve({svg});
  await settle();
  assert.equal(nested.querySelectorAll('img').length, 2);
  assert.equal(new Set(calls.map((call) => call.id)).size, calls.length, 'diagram IDs are unique');
  assert.equal(document.head.children.length, 1, 'Mermaid is loaded once for all messages');
  assert.equal(document.body.children.length, 0, 'temporary render containers are removed on success and failure');
}

testCodeBlockCopy().then(testMermaidRendering).then(
  () => process.stdout.write('Markdown renderer tests passed\n'),
  (error) => {
    process.stderr.write(`${error.stack}\n`);
    process.exitCode = 1;
  },
);
