const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');
const start = application.indexOf('function renderHeader');
const end = application.indexOf('function setConnection', start);
assert.ok(start >= 0 && end > start);

function element() {
  return {
    hidden: true,
    textContent: '',
    children: [],
    replaceChildren(...children) { this.children = children; },
    append(...children) { this.children.push(...children); },
  };
}

const context = {
  state: {selected: null},
  elements: Object.fromEntries([
    'terminalTab', 'viewChoice', 'terminalChoice', 'terminalView', 'displayNameButton',
    'sectionForm', 'suspendButton', 'resumeButton', 'resetButton', 'deleteButton',
    'conversationTab', 'changesTab', 'title', 'meta',
  ].map(name => [name, element()])),
  document: {createElement: element},
  sessionTerminal: {available: () => true, sync() {}},
  selectedSubsession: () => null,
  sessionDisplayName: session => session.displayName || session.name,
  sessionDisplayStatus: session => session.phase,
  providerLabel: () => 'Codex',
  createPullRequestLink: () => null,
  createSessionTimestamp: () => null,
};
for (const name of [
  'renderSubsessionForm', 'refreshSessionProgress', 'setActiveView',
  'renderSelectedSessionSection', 'updateComposerAction', 'renderConversationHeading',
  'setConnection', 'setComposer', 'renderRuntimeStatus',
]) context[name] = () => {};
vm.createContext(context);
vm.runInContext(application.slice(start, end), context, {filename: 'app.js'});

const session = {
  name: 'chat', namespace: 'team-a', provider: 'codex', model: 'gpt-5', phase: 'Ready',
  credentialName: 'codex-credentials',
};
context.state.selected = session;
context.renderHeader();
assert.equal(
  context.elements.meta.children[0].textContent,
  'team-a · Codex · gpt-5 · Credential: codex-credentials · Ready',
);

context.selectedSubsession = () => ({id: 'child'});
context.renderHeader();
assert.equal(
  context.elements.meta.children[0].textContent,
  'Codex · gpt-5 · Credential: codex-credentials',
);

context.selectedSubsession = () => null;
for (const credentialName of [undefined, '']) {
  context.state.selected = {...session, credentialName};
  context.renderHeader();
  assert.equal(context.elements.meta.children[0].textContent, 'team-a · Codex · gpt-5 · Ready');
}

context.state.selected = null;
context.renderHeader();
assert.equal(context.elements.meta.textContent, 'Select an existing conversation or create one.');

process.stdout.write('Session header tests passed\n');
