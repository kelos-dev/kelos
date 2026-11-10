const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');

function slice(start, end) {
  const first = application.indexOf(start);
  const last = application.indexOf(end, first);
  assert.notEqual(first, -1, `${start} not found`);
  assert.notEqual(last, -1, `${end} not found`);
  return application.slice(first, last);
}

function harness() {
  const element = () => ({
    value: '', textContent: '', disabled: false, hidden: false, options: [], focused: false,
    setAttribute(name, value) { this[name] = value; },
    replaceChildren() { this.options = []; this.value = ''; },
    focus() { this.focused = true; },
  });
  const elements = Object.fromEntries([
    'formFields', 'sessionSourcePicker', 'sessionSource', 'spawnerFields',
    'yamlPanel', 'yaml', 'formMode', 'yamlMode', 'spawnerMode', 'createButton',
    'sessionSpawner', 'sessionSpawnerValues', 'dialogError', 'namespace',
    'sessionSourceStatus', 'sectionSelect', 'sectionCustom',
  ].map(key => [key, element()]));
  const state = {
    namespace: 'team-a', sourceGeneration: 0, sourceLoading: false,
    creatingSession: false, creationMode: 'form', loadedSource: null,
    sessionSpawners: [], sessions: [],
  };
  const calls = {closed: 0, reset: 0, selected: null, optionsLoaded: 0};
  elements.form = {
    elements: {name: element()},
    addEventListener(type, callback) { assert.equal(type, 'submit'); calls.submit = callback; },
    reportValidity() { return elements.spawnerFields.disabled || Boolean(elements.sessionSpawner.value); },
    reset() { calls.reset++; elements.sessionSpawner.value = ''; elements.sessionSpawnerValues.value = ''; },
  };
  elements.dialog = {
    open: false,
    showModal() { this.open = true; },
    close() { this.open = false; calls.closed++; },
  };
  const noop = () => {};
  const context = vm.createContext({
    elements, state,
    addOption(select, value, textContent) { select.options.push({value, textContent}); },
    defaultSessionYAML: () => 'kind: Session',
    errorMessage: error => error.message,
    validateCustomSectionField: noop, updateVolumeClaimFields: noop,
    renderSessionSourceOptions: noop, renderSectionOptions: noop,
    renderCredentialOptions: noop, renderWorkspaceOptions: noop, renderAgentConfigOptions: noop,
    loadSessions: async () => {}, loadOptions: async () => { calls.optionsLoaded++; },
    setConsoleView: noop, showToast: noop,
    window: {setTimeout(callback) { callback(); }},
    sessionKey: session => `${session.namespace}/${session.name}`,
    selectSession: session => { calls.selected = session; },
  });
  vm.runInContext('const configReady = Promise.resolve();', context);
  vm.runInContext(slice('function setCreationMode(', 'function updateVolumeClaimFields('), context);
  vm.runInContext(slice('function updateCreationBusyState(', 'async function loadSessionSource('), context);
  vm.runInContext(slice('async function openDialog()', 'elements.newSessionButton.addEventListener('), context);
  vm.runInContext(slice("elements.form.addEventListener('submit'", 'function openDisplayNameDialog('), context);
  return {context, elements, state, calls};
}

function testCreationModes() {
  const {context: c, elements: e, state} = harness();
  c.setCreationMode('spawner');
  assert.equal(state.creationMode, 'spawner');
  assert.equal(e.spawnerFields.hidden, false);
  assert.equal(e.spawnerFields.disabled, false);
  assert.equal(e.formFields.hidden, true);
  assert.equal(e.formFields.disabled, true);
  assert.equal(e.yaml.disabled, true);
  assert.equal(e.sessionSourcePicker.hidden, true);
  assert.equal(e.sessionSource.disabled, true);
  assert.equal(e.spawnerMode['aria-selected'], 'true');
  assert.equal(e.createButton.textContent, 'Create session');

  c.setCreationMode('yaml');
  assert.equal(e.spawnerFields.hidden, true);
  assert.equal(e.spawnerFields.disabled, true);
  assert.equal(e.yaml.required, true);
  assert.equal(e.yaml.disabled, false);
  assert.equal(e.createButton.textContent, 'Apply YAML');

  c.setCreationMode('form');
  assert.equal(e.formFields.disabled, false);
  assert.equal(e.formFields.hidden, false);
  assert.equal(e.sessionSourcePicker.hidden, false);
  assert.equal(e.sessionSource.disabled, false);
  assert.equal(e.yaml.disabled, true);
  assert.equal(e.spawnerMode['aria-selected'], 'false');
}

function testCreationBusyControls() {
  for (const mode of ['form', 'yaml', 'spawner']) {
    for (const busyReason of ['sourceLoading', 'creatingSession']) {
      const {context: c, elements: e} = harness();
      c.setCreationMode(mode);
      const setBusy = busyReason === 'sourceLoading' ? c.setSourceLoading : c.setCreatingSession;
      setBusy(true);
      for (const control of ['sessionSource', 'formMode', 'yamlMode', 'spawnerMode', 'spawnerFields', 'createButton']) {
        assert.equal(e[control].disabled, true, `${mode}: ${control} disabled during ${busyReason}`);
      }
      setBusy(false);
      for (const control of ['formMode', 'yamlMode', 'spawnerMode', 'createButton']) {
        assert.equal(e[control].disabled, false, `${mode}: ${control} enabled after ${busyReason}`);
      }
      assert.equal(e.sessionSource.disabled, mode === 'spawner');
      assert.equal(e.spawnerFields.disabled, mode !== 'spawner');
    }
  }
}

async function testOpeningDialogRefreshesOptionsAndFocuses() {
  for (const mode of ['form', 'yaml', 'spawner']) {
    const {context: c, elements: e, calls} = harness();
    c.setCreationMode(mode);
    let resolve;
    c.api = () => new Promise(done => { resolve = done; });
    const opening = c.openDialog();
    await Promise.resolve();
    assert.equal(calls.optionsLoaded, 1, `${mode}: Form options refreshed`);
    if (mode === 'spawner') {
      assert.equal(e.spawnerFields.disabled, true);
      assert.equal(e.sessionSpawner.focused, false);
      resolve(['review']);
    }
    await opening;
    const target = mode === 'spawner' ? e.sessionSpawner : mode === 'yaml' ? e.yaml : e.form.elements.name;
    assert.equal(target.focused, true, `${mode}: first field focused`);
    assert.equal(e.spawnerFields.disabled, mode !== 'spawner');
  }
}

async function testOpeningDialogDoesNotFocusAfterClosing() {
  const {context: c, elements: e} = harness();
  c.setCreationMode('spawner');
  let resolve;
  c.api = () => new Promise(done => { resolve = done; });
  const opening = c.openDialog();
  await Promise.resolve();
  e.dialog.close();
  resolve(['review']);
  await opening;
  assert.equal(e.sessionSpawner.focused, false);
}

async function testSpawnerOptions() {
  const {context: c, elements: e} = harness();
  c.setCreationMode('spawner');
  let resolve;
  c.api = requestPath => {
    assert.equal(requestPath, '/api/sessionspawners?namespace=team-a');
    return new Promise(done => { resolve = done; });
  };
  const loading = c.loadSessionSpawnerOptions();
  assert.equal(e.createButton.disabled, true);
  assert.equal(e.spawnerMode.disabled, true);
  assert.equal(e.formMode.disabled, true);
  resolve(['review', 'triage']);
  await loading;
  assert.equal(e.createButton.disabled, false);
  assert.equal(e.spawnerFields.disabled, false);
  assert.deepEqual(e.sessionSpawner.options.map(option => option.value), ['', 'review', 'triage']);
  e.sessionSpawner.value = 'review';
  c.api = async () => ['review', 'triage'];
  await c.loadSessionSpawnerOptions();
  assert.equal(e.sessionSpawner.value, 'review');
  c.api = async () => [];
  await c.loadSessionSpawnerOptions();
  assert.equal(e.sessionSpawner.value, '');
  assert.equal(e.sessionSpawner.options.length, 1);
}

async function testSpawnerOptionsErrorsAndNamespaceChanges() {
  const {context: c, elements: e, state} = harness();
  c.setCreationMode('spawner');
  state.sessionSpawners = ['review'];
  c.renderSessionSpawnerOptions();
  e.sessionSpawner.value = 'review';
  c.api = async () => { throw new Error('Cannot list SessionSpawners'); };
  await c.loadSessionSpawnerOptions();
  assert.equal(e.sessionSpawner.value, '');
  assert.equal(e.dialogError.textContent, 'Cannot list SessionSpawners');
  assert.equal(e.createButton.disabled, false);

  let resolve;
  c.api = () => new Promise(done => { resolve = done; });
  const loading = c.loadSessionSpawnerOptions();
  state.namespace = 'team-b';
  state.sourceGeneration++;
  state.sessionSpawners = [];
  c.renderSessionSpawnerOptions();
  c.setSourceLoading(false);
  resolve(['team-a-spawner']);
  await loading;
  assert.equal(e.sessionSpawner.options.length, 1);
  assert.equal(e.sessionSpawner.value, '');
  assert.equal(e.dialogError.textContent, '');
}

async function testCreateSessionFromSpawner() {
  const {context: c, elements: e, state, calls} = harness();
  c.setCreationMode('spawner');
  e.sessionSpawner.value = 'review';
  e.sessionSpawnerValues.value = 'Number: 42\nTitle: Fix login';
  const created = {namespace: 'team-a', name: 'review-manual-abcde', provider: 'codex'};
  const listed = {...created, phase: 'Ready'};
  c.loadSessions = async () => { state.sessions = [listed]; };
  let requests = 0;
  let resolve;
  c.api = (requestPath, options) => {
    requests++;
    assert.equal(requestPath, '/api/resources/sessionspawners/team-a/review/run');
    assert.equal(options.method, 'POST');
    assert.deepEqual(JSON.parse(options.body), {values: 'Number: 42\nTitle: Fix login'});
    return new Promise(done => { resolve = done; });
  };
  let prevented = false;
  const event = {preventDefault() { prevented = true; }};
  const saving = calls.submit(event);
  await calls.submit(event);
  assert.equal(prevented, true);
  assert.equal(requests, 1);
  assert.equal(e.createButton.disabled, true);
  resolve(created);
  await saving;
  assert.equal(calls.closed, 1);
  assert.equal(calls.reset, 1);
  assert.equal(calls.selected, listed);
  assert.equal(state.creationMode, 'form');
  assert.equal(e.createButton.disabled, false);
}

async function testCreateSessionErrors() {
  const {context: c, elements: e, calls} = harness();
  c.setCreationMode('spawner');
  let requests = 0;
  c.api = async () => { requests++; throw new Error('Missing template value Number'); };
  const event = {preventDefault() {}};
  await calls.submit(event);
  assert.equal(requests, 0);
  e.sessionSpawner.value = 'review';
  e.sessionSpawnerValues.value = 'Title: Fix login';
  await calls.submit(event);
  assert.equal(requests, 1);
  assert.equal(e.dialogError.textContent, 'Missing template value Number');
  assert.equal(e.sessionSpawner.value, 'review');
  assert.equal(e.sessionSpawnerValues.value, 'Title: Fix login');
  assert.equal(e.createButton.disabled, false);
  assert.equal(e.spawnerFields.disabled, false);
  assert.equal(calls.closed, 0);
  assert.equal(calls.reset, 0);
}

testCreationModes();
testCreationBusyControls();
testSpawnerOptions()
  .then(testOpeningDialogRefreshesOptionsAndFocuses)
  .then(testOpeningDialogDoesNotFocusAfterClosing)
  .then(testSpawnerOptionsErrorsAndNamespaceChanges)
  .then(testCreateSessionFromSpawner)
  .then(testCreateSessionErrors)
  .then(() => process.stdout.write('Session creation tests passed\n'))
  .catch(error => { console.error(error); process.exitCode = 1; });
