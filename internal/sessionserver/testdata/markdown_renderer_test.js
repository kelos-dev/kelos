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
    this.classes = new Set();
    this.classList = {add: (...names) => names.forEach((name) => this.classes.add(name))};
  }

  append(...nodes) {
    for (const node of nodes) {
      if (node.tag === '#fragment') this.children.push(...node.children);
      else this.children.push(node);
    }
  }

  replaceChildren(...nodes) {
    this.children = [];
    this.append(...nodes);
  }

  set textContent(value) {
    this.children = [new TestNode('#text', String(value))];
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
global.state = {
  assistantSegmentByTurn: new Map(),
  assistantTextByTurn: new Map(),
  selected: {provider: 'claude'},
};
global.elements = {messages: new TestNode('div')};
global.ensureConversation = () => {};
global.providerInitials = () => 'C';
global.scrollToBottom = () => {};

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
  if (node.dataset.language) attributes.push(`data-language="${escapeHTML(node.dataset.language)}"`);
  if (node.start) attributes.push(`start="${node.start}"`);
  if (node.type) attributes.push(`type="${escapeHTML(node.type)}"`);
  if (node.checked) attributes.push('checked');
  if (node.disabled) attributes.push('disabled');
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
assert.match(untrusted, /<pre data-language="html"><code class="language-html">&lt;script&gt;alert\(1\)&lt;\/script&gt;<\/code><\/pre>/);
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

process.stdout.write('Markdown renderer tests passed\n');
