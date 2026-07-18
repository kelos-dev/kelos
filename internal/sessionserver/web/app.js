const elements = {
  list: document.querySelector('#session-list'),
  title: document.querySelector('#session-title'),
  meta: document.querySelector('#session-meta'),
  messages: document.querySelector('#messages'),
  changes: document.querySelector('#changes-view'),
  changesList: document.querySelector('#changes-list'),
  changesSummary: document.querySelector('#changes-summary'),
  changesCount: document.querySelector('#changes-count'),
  viewTabs: document.querySelector('.view-tabs'),
  conversationTab: document.querySelector('#conversation-tab'),
  changesTab: document.querySelector('#changes-tab'),
  welcome: document.querySelector('#welcome'),
  composerWrap: document.querySelector('.composer-wrap'),
  composer: document.querySelector('#composer'),
  input: document.querySelector('#message-input'),
  send: document.querySelector('#send-message'),
  composerHint: document.querySelector('#composer-hint'),
  queue: document.querySelector('#queued-prompts'),
  connection: document.querySelector('#connection-pill'),
  dialog: document.querySelector('#session-dialog'),
  form: document.querySelector('#session-form'),
  dialogError: document.querySelector('#dialog-error'),
  namespaceForm: document.querySelector('#namespace-form'),
  activeNamespace: document.querySelector('#active-namespace'),
  namespace: document.querySelector('[name="namespace"]'),
  sessionSource: document.querySelector('#session-source'),
  sessionSourceStatus: document.querySelector('#session-source-status'),
  provider: document.querySelector('[name="provider"]'),
  credentialType: document.querySelector('#credential-type'),
  secretField: document.querySelector('#secret-field'),
  credentialSecret: document.querySelector('#credential-secret'),
  credentialSecretCustom: document.querySelector('#credential-secret-custom'),
  workspace: document.querySelector('#workspace-select'),
  workspaceCustom: document.querySelector('#workspace-custom'),
  agentConfig: document.querySelector('#agent-config-select'),
  addAgentConfig: document.querySelector('#add-agent-config'),
  selectedAgentConfigs: document.querySelector('#selected-agent-configs'),
  formFields: document.querySelector('#session-form-fields'),
  formMode: document.querySelector('#session-mode-form'),
  yamlMode: document.querySelector('#session-mode-yaml'),
  yamlPanel: document.querySelector('#session-yaml-panel'),
  yaml: document.querySelector('#session-yaml'),
  persistentVolume: document.querySelector('#volume-claim-enabled'),
  volumeClaimFields: document.querySelector('#volume-claim-fields'),
  createButton: document.querySelector('#create-session'),
  deleteButton: document.querySelector('#delete-session'),
  sidebar: document.querySelector('#sidebar'),
  openSidebar: document.querySelector('#open-sidebar'),
  closeSidebar: document.querySelector('#close-sidebar'),
  sidebarScrim: document.querySelector('#sidebar-scrim'),
  toast: document.querySelector('#toast'),
};

const state = {
  sessions: [],
  selected: null,
  socket: null,
  socketGeneration: 0,
  reconnectTimer: null,
  reconnectDelay: 800,
  bottomScrollFrame: null,
  sessionViews: new Map(),
  currentView: null,
  lastEventID: 0,
  assistantSegmentByTurn: new Map(),
  assistantTextByTurn: new Map(),
  tools: new Map(),
  inputs: new Map(),
  diffs: new Map(),
  fileChanges: new Map(),
  queuedMessages: new Map(),
  promptDrafts: new Map(),
  activeTurn: false,
  interrupting: false,
  replayingHistory: false,
  pinHistoryToBottom: false,
  fileChangesDirty: false,
  defaultNamespace: 'default',
  namespace: 'default',
  namespaceGeneration: 0,
  options: {credentials: [], workspaces: [], agentConfigs: [], sessions: []},
  selectedAgentConfigs: [],
  creationMode: 'form',
  sourceGeneration: 0,
  sourceLoading: false,
  creatingSession: false,
  sourceStorageClassNamePresent: false,
  loadedSource: null,
};

const customOption = '__custom__';
const maxCachedSessionViews = 5;

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: {'Content-Type': 'application/json', ...(options.headers || {})},
  });
  if (response.status === 401) {
    window.location.replace('/login');
    throw new Error('Authentication required');
  }
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    throw new Error(body.error || `${response.status} ${response.statusText}`);
  }
  if (response.status === 204) return null;
  return response.json();
}

function showToast(message) {
  elements.toast.textContent = message;
  elements.toast.classList.add('show');
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => elements.toast.classList.remove('show'), 3200);
}

function sessionKey(session) {
  return `${session.namespace}/${session.name}`;
}

function sessionViewKey(session) {
  return `${sessionKey(session)}/${session.uid || 'unknown'}`;
}

function moveChildren(element) {
  const fragment = document.createDocumentFragment();
  while (element.firstChild) fragment.append(element.firstChild);
  return fragment;
}

function createSessionView() {
  return {
    messages: document.createDocumentFragment(),
    queue: document.createDocumentFragment(),
    changes: document.createDocumentFragment(),
    lastEventID: 0,
    journalID: '',
    assistantSegmentByTurn: new Map(),
    assistantTextByTurn: new Map(),
    tools: new Map(),
    inputs: new Map(),
    diffs: new Map(),
    fileChanges: new Map(),
    queuedMessages: new Map(),
    activeTurn: false,
    interrupting: false,
    replayingHistory: false,
    pinHistoryToBottom: false,
    fileChangesDirty: false,
    historyLoaded: false,
    statusPlaceholder: false,
  };
}

function saveCurrentSessionView() {
  const view = state.currentView;
  if (!view) return;
  view.messages = moveChildren(elements.messages);
  view.queue = moveChildren(elements.queue);
  view.changes = moveChildren(elements.changesList);
  view.lastEventID = state.lastEventID;
  view.assistantSegmentByTurn = state.assistantSegmentByTurn;
  view.assistantTextByTurn = state.assistantTextByTurn;
  view.tools = state.tools;
  view.inputs = state.inputs;
  view.diffs = state.diffs;
  view.fileChanges = state.fileChanges;
  view.queuedMessages = state.queuedMessages;
  view.activeTurn = state.activeTurn;
  view.interrupting = state.interrupting;
  view.replayingHistory = state.replayingHistory;
  view.pinHistoryToBottom = state.pinHistoryToBottom;
  view.fileChangesDirty = state.fileChangesDirty;
}

function updateFileChangesHeader() {
  const count = state.fileChanges.size;
  elements.changesCount.textContent = String(count);
  elements.changesSummary.textContent = count === 1 ? '1 changed file' : `${count} changed files`;
}

function activateSessionView(view) {
  state.currentView = view;
  state.lastEventID = view.lastEventID;
  state.assistantSegmentByTurn = view.assistantSegmentByTurn;
  state.assistantTextByTurn = view.assistantTextByTurn;
  state.tools = view.tools;
  state.inputs = view.inputs;
  state.diffs = view.diffs;
  state.fileChanges = view.fileChanges;
  state.queuedMessages = view.queuedMessages;
  state.activeTurn = view.activeTurn;
  state.interrupting = view.interrupting;
  state.replayingHistory = view.replayingHistory;
  state.pinHistoryToBottom = view.pinHistoryToBottom;
  state.fileChangesDirty = view.fileChangesDirty;
  const hasChanges = view.changes.hasChildNodes();
  elements.messages.replaceChildren(view.messages);
  elements.queue.replaceChildren(view.queue);
  elements.changesList.replaceChildren(view.changes);
  elements.queue.hidden = state.queuedMessages.size === 0;
  updateFileChangesHeader();
  if (!hasChanges) renderFileChanges();
}

function cachedSessionView(session) {
  const key = sessionViewKey(session);
  let view = state.sessionViews.get(key);
  if (view) state.sessionViews.delete(key);
  else view = createSessionView();
  state.sessionViews.set(key, view);
  while (state.sessionViews.size > maxCachedSessionViews) {
    state.sessionViews.delete(state.sessionViews.keys().next().value);
  }
  return view;
}

function discardSessionView(session) {
  if (session) state.sessionViews.delete(sessionViewKey(session));
}

function resetCurrentSessionView() {
  const view = state.currentView;
  state.lastEventID = 0;
  state.assistantSegmentByTurn = new Map();
  state.assistantTextByTurn = new Map();
  state.tools = new Map();
  state.inputs = new Map();
  state.diffs = new Map();
  state.fileChanges = new Map();
  state.queuedMessages = new Map();
  state.activeTurn = false;
  state.interrupting = false;
  state.replayingHistory = true;
  state.pinHistoryToBottom = true;
  state.fileChangesDirty = false;
  elements.messages.replaceChildren();
  elements.queue.replaceChildren();
  elements.queue.hidden = true;
  renderFileChanges();
  if (view) {
    view.historyLoaded = false;
    view.statusPlaceholder = false;
    view.lastEventID = 0;
    view.assistantSegmentByTurn = state.assistantSegmentByTurn;
    view.assistantTextByTurn = state.assistantTextByTurn;
    view.tools = state.tools;
    view.inputs = state.inputs;
    view.diffs = state.diffs;
    view.fileChanges = state.fileChanges;
    view.queuedMessages = state.queuedMessages;
    view.pinHistoryToBottom = true;
  }
}

function savePromptDraft(session) {
  if (!session) return;
  if (elements.input.value) {
    state.promptDrafts.set(sessionKey(session), elements.input.value);
  } else {
    state.promptDrafts.delete(sessionKey(session));
  }
}

function restorePromptDraft(session) {
  elements.input.value = session ? state.promptDrafts.get(sessionKey(session)) || '' : '';
  resizeComposer();
}

function clearPromptDraft(session) {
  if (!session) return;
  state.promptDrafts.delete(sessionKey(session));
  if (state.selected && sessionKey(state.selected) === sessionKey(session)) {
    elements.input.value = '';
    resizeComposer();
    updateComposerAction();
  }
}

function providerLabel(provider) {
  return provider === 'claude-code' ? 'Claude Code' : provider === 'codex' ? 'Codex' : provider === 'opencode' ? 'OpenCode' : provider;
}

function providerInitials(provider) {
  return provider === 'claude-code' ? 'CC' : provider === 'codex' ? 'CX' : provider === 'opencode' ? 'OC' : 'AI';
}

function sessionDisplayStatus(session) {
  if (session.phase !== 'Ready') return session.phase || 'Pending';
  if (session.active === true) return 'Active';
  if (session.active === false) return 'Idle';
  return session.phase;
}

function safeHTTPURL(value) {
  if (!value) return null;
  try {
    const url = new URL(value, window.location.origin);
    return url.protocol === 'http:' || url.protocol === 'https:' ? url : null;
  } catch (_) {
    return null;
  }
}

function pullRequestLabel(url) {
  const match = url.pathname.match(/\/pull\/(\d+)(?:\/|$)/);
  return match ? `PR #${match[1]}` : 'Pull request';
}

function sessionPRState(value) {
  return ['Draft', 'Open', 'Merged', 'Closed'].includes(value) ? value : '';
}

function createPullRequestLink(pullRequest, className) {
  const url = safeHTTPURL(pullRequest?.url);
  if (!url) return null;
  const link = document.createElement('a');
  link.className = `pull-request-link ${className}`;
  link.href = url.href;
  link.target = '_blank';
  link.rel = 'noopener noreferrer';
  const state = sessionPRState(pullRequest.state);
  link.textContent = state ? `${pullRequestLabel(url)} · ${state}` : pullRequestLabel(url);
  if (state) link.dataset.state = state.toLowerCase();
  link.title = pullRequest.url;
  return link;
}

function renderSessions() {
  elements.list.replaceChildren();
  if (!state.sessions.length) {
    const empty = document.createElement('div');
    empty.className = 'sidebar-empty';
    empty.textContent = `No Sessions in ${state.namespace}.`;
    elements.list.append(empty);
    return;
  }
  for (const session of state.sessions) {
    const item = document.createElement('div');
    item.className = `session-item${state.selected && sessionKey(state.selected) === sessionKey(session) ? ' active' : ''}`;
    const button = document.createElement('button');
    button.className = 'session-item-select';
    button.type = 'button';
    const dot = document.createElement('span');
    const displayStatus = sessionDisplayStatus(session);
    dot.className = `phase-dot ${String(displayStatus).toLowerCase()}`;
    const text = document.createElement('span');
    const name = document.createElement('div');
    name.className = 'session-item-name';
    name.textContent = session.name;
    const meta = document.createElement('div');
    meta.className = 'session-item-meta';
    const provider = document.createElement('span');
    provider.className = 'provider-badge';
    provider.textContent = providerLabel(session.provider);
    const namespace = document.createElement('span');
    namespace.textContent = `· ${session.namespace}`;
    const activity = document.createElement('span');
    activity.textContent = `· ${displayStatus}`;
    meta.append(provider, namespace, activity);
    text.append(name, meta);
    if (session.branch) {
      const branch = document.createElement('div');
      branch.className = 'session-item-branch';
      branch.textContent = session.branch;
      branch.title = session.branch;
      text.append(branch);
    }
    button.append(dot, text);
    button.addEventListener('click', () => selectSession(session));
    item.append(button);
    const link = createPullRequestLink(session.pullRequest, 'session-item-pull-request');
    if (link) {
      item.classList.add('has-pull-request');
      item.append(link);
    }
    elements.list.append(item);
  }
}

async function loadSessions({quiet = false} = {}) {
  const namespace = state.namespace;
  const generation = state.namespaceGeneration;
  try {
    const sessions = await api(`/api/sessions?namespace=${encodeURIComponent(namespace)}`);
    if (generation !== state.namespaceGeneration) return;
    state.sessions = sessions;
    if (state.selected) {
      const current = sessions.find(item => sessionKey(item) === sessionKey(state.selected));
      if (current) {
        if (state.selected.uid && current.uid && state.selected.uid !== current.uid) {
          discardSessionView(state.selected);
          selectSession(current);
        } else {
          const becameReady = state.selected.phase !== 'Ready' && current.phase === 'Ready';
          state.selected = current;
          renderHeader();
          if (becameReady) connectSocket();
        }
      } else {
        discardSessionView(state.selected);
        selectSession(null);
      }
    }
    renderSessions();
  } catch (error) {
    if (!quiet && generation === state.namespaceGeneration) showToast(error.message);
  }
}

async function loadConfig() {
  const config = await api('/api/config');
  state.defaultNamespace = config.defaultNamespace;
  state.namespace = window.localStorage.getItem('kelos-session-namespace') || state.defaultNamespace;
  elements.activeNamespace.value = state.namespace;
  elements.namespace.value = state.namespace;
}

function defaultSessionYAML() {
  return `apiVersion: kelos.dev/v1alpha2
kind: Session
metadata:
  name: my-session
  namespace: ${state.namespace}
spec:
  worker:
    type: claude-code
    credentials:
      type: api-key
      secretRef:
        name: claude-credentials
`;
}

function setCreationMode(mode) {
  const yaml = mode === 'yaml';
  state.creationMode = yaml ? 'yaml' : 'form';
  elements.formFields.hidden = yaml;
  elements.formFields.disabled = yaml;
  elements.yamlPanel.hidden = !yaml;
  elements.yaml.disabled = !yaml;
  elements.yaml.required = yaml;
  elements.formMode.setAttribute('aria-selected', String(!yaml));
  elements.yamlMode.setAttribute('aria-selected', String(yaml));
  elements.createButton.textContent = yaml ? 'Apply YAML' : 'Create session';
  if (yaml && !elements.yaml.value.trim()) elements.yaml.value = defaultSessionYAML();
}

function updateVolumeClaimFields() {
  const enabled = elements.persistentVolume.checked;
  elements.volumeClaimFields.hidden = !enabled;
  elements.form.elements.storageRequest.required = enabled;
  elements.form.elements.accessMode.required = enabled;
}

async function loadOptions() {
  const namespace = state.namespace;
  const generation = state.namespaceGeneration;
  let options;
  try {
    options = await api(`/api/options?namespace=${encodeURIComponent(namespace)}`);
  } catch (error) {
    if (generation !== state.namespaceGeneration) return;
    throw error;
  }
  if (generation !== state.namespaceGeneration) return;
  state.options = options;
  renderSessionSourceOptions();
  renderCredentialOptions();
  renderWorkspaceOptions();
  renderAgentConfigOptions();
}

function resetNamespaceReferences() {
  state.selectedAgentConfigs = [];
  state.sourceGeneration += 1;
  setSourceLoading(false);
  state.sourceStorageClassNamePresent = false;
  state.loadedSource = null;
  elements.sessionSource.value = '';
  elements.sessionSourceStatus.hidden = true;
  elements.sessionSourceStatus.textContent = '';
  elements.credentialSecret.value = '';
  elements.credentialSecretCustom.value = '';
  elements.workspace.value = '';
  elements.workspaceCustom.value = '';
}

async function switchNamespace(namespace) {
  namespace = namespace.trim();
  if (!namespace || namespace === state.namespace) return;
  const hadLoadedSource = Boolean(state.loadedSource);
  state.namespace = namespace;
  state.namespaceGeneration += 1;
  state.sessions = [];
  state.options = {credentials: [], workspaces: [], agentConfigs: [], sessions: []};
  window.localStorage.setItem('kelos-session-namespace', namespace);
  elements.activeNamespace.value = namespace;
  elements.namespace.value = namespace;
  resetNamespaceReferences();
  elements.yaml.value = '';
  if (hadLoadedSource) resetSourceValues();
  renderSessionSourceOptions();
  renderCredentialOptions();
  renderWorkspaceOptions();
  renderAgentConfigOptions();
  selectSession(null);
  await Promise.all([loadSessions(), loadOptions()]);
}

function addOption(select, value, label) {
  const option = document.createElement('option');
  option.value = value;
  option.textContent = label;
  select.append(option);
  return option;
}

function credentialTypeLabel(type) {
  return type === 'api-key' ? 'API key' : type === 'oauth' ? 'OAuth' : type;
}

function renderSessionSourceOptions() {
  const previous = elements.sessionSource.value;
  elements.sessionSource.replaceChildren();
  addOption(elements.sessionSource, '', 'Start from scratch');
  for (const name of state.options.sessions) addOption(elements.sessionSource, name, name);
  if (state.options.sessions.includes(previous)) elements.sessionSource.value = previous;
}

function renderCredentialOptions() {
  const selected = elements.credentialSecret.selectedOptions[0];
  const previous = {
    value: elements.credentialSecret.value,
    name: selected?.dataset.name,
    type: selected?.dataset.type,
  };
  elements.credentialSecret.replaceChildren();
  addOption(elements.credentialSecret, '', 'Choose a credential…');
  const credentials = state.options.credentials.filter(option => option.provider === elements.provider.value);
  credentials.forEach((credential, index) => {
    const option = addOption(
      elements.credentialSecret,
      `credential-${index}`,
      `${credential.name} · ${credentialTypeLabel(credential.type)}`,
    );
    option.dataset.name = credential.name;
    option.dataset.type = credential.type;
  });
  addOption(elements.credentialSecret, customOption, 'Enter another Secret name…');

  if (previous.value === customOption) {
    elements.credentialSecret.value = customOption;
  } else if (previous.name) {
    const match = Array.from(elements.credentialSecret.options).find(option =>
      option.dataset.name === previous.name && option.dataset.type === previous.type,
    );
    if (match) elements.credentialSecret.value = match.value;
  } else if (!credentials.length) {
    elements.credentialSecret.value = customOption;
  }
  updateCredentialField();
}

function updateCredentialField() {
  const none = elements.credentialType.value === 'none';
  elements.secretField.hidden = none;
  elements.credentialSecret.required = !none;
  const custom = !none && elements.credentialSecret.value === customOption;
  elements.credentialSecretCustom.hidden = !custom;
  elements.credentialSecretCustom.required = custom;
}

function selectedCredentialName() {
  const option = elements.credentialSecret.selectedOptions[0];
  if (option?.dataset.name) return option.dataset.name;
  if (elements.credentialSecret.value === customOption) return elements.credentialSecretCustom.value.trim();
  return '';
}

function renderWorkspaceOptions() {
  const previous = elements.workspace.value;
  elements.workspace.replaceChildren();
  addOption(elements.workspace, '', 'No workspace');
  for (const name of state.options.workspaces) addOption(elements.workspace, name, name);
  addOption(elements.workspace, customOption, 'Enter another Workspace name…');
  if (Array.from(elements.workspace.options).some(option => option.value === previous)) {
    elements.workspace.value = previous;
  }
  updateWorkspaceField();
}

function updateWorkspaceField() {
  const custom = elements.workspace.value === customOption;
  elements.workspaceCustom.hidden = !custom;
  elements.workspaceCustom.required = custom;
}

function selectedWorkspaceName() {
  if (elements.workspace.value === customOption) return elements.workspaceCustom.value.trim();
  return elements.workspace.value;
}

function renderAgentConfigOptions() {
  const previous = elements.agentConfig.value;
  elements.agentConfig.replaceChildren();
  const available = state.options.agentConfigs.filter(name => !state.selectedAgentConfigs.includes(name));
  const placeholder = !state.options.agentConfigs.length
    ? 'No AgentConfigs available'
    : available.length ? 'Add an AgentConfig…' : 'All AgentConfigs selected';
  addOption(elements.agentConfig, '', placeholder);
  for (const name of available) addOption(elements.agentConfig, name, name);
  if (Array.from(elements.agentConfig.options).some(option => option.value === previous)) {
    elements.agentConfig.value = previous;
  }
  elements.agentConfig.disabled = !available.length;
  elements.addAgentConfig.disabled = !elements.agentConfig.value;
  renderSelectedAgentConfigs();
}

function renderSelectedAgentConfigs() {
  elements.selectedAgentConfigs.replaceChildren();
  if (!state.selectedAgentConfigs.length) {
    const empty = document.createElement('span');
    empty.className = 'selected-options-empty';
    empty.textContent = 'None selected';
    elements.selectedAgentConfigs.append(empty);
    return;
  }
  state.selectedAgentConfigs.forEach((name, index) => {
    const chip = document.createElement('span');
    chip.className = 'selected-option';
    const order = document.createElement('span');
    order.className = 'selected-option-order';
    order.textContent = String(index + 1);
    const label = document.createElement('span');
    label.textContent = name;
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.setAttribute('aria-label', `Remove AgentConfig ${name}`);
    remove.textContent = '×';
    remove.addEventListener('click', () => {
      state.selectedAgentConfigs.splice(index, 1);
      renderAgentConfigOptions();
    });
    chip.append(order, label, remove);
    elements.selectedAgentConfigs.append(chip);
  });
}

function setSourceCredential(credentials) {
  const type = credentials?.type || 'none';
  elements.credentialType.value = type;
  renderCredentialOptions();
  const name = credentials?.secretRef?.name || '';
  const match = Array.from(elements.credentialSecret.options).find(option =>
    option.dataset.name === name && option.dataset.type === type,
  );
  if (match) {
    elements.credentialSecret.value = match.value;
    elements.credentialSecretCustom.value = '';
  } else if (name) {
    elements.credentialSecret.value = customOption;
    elements.credentialSecretCustom.value = name;
  }
  updateCredentialField();
}

function setSourceWorkspace(workspaceRef) {
  const name = workspaceRef?.name || '';
  renderWorkspaceOptions();
  if (!name || state.options.workspaces.includes(name)) {
    elements.workspace.value = name;
    elements.workspaceCustom.value = '';
  } else {
    elements.workspace.value = customOption;
    elements.workspaceCustom.value = name;
  }
  updateWorkspaceField();
}

function sourceFitsForm(manifest) {
  const worker = manifest.spec.worker;
  const allowedWorkerFields = new Set(['type', 'credentials', 'model', 'workspaceRef', 'agentConfigRefs']);
  if (Object.keys(worker).some(key => !allowedWorkerFields.has(key))) return false;
  const claim = manifest.spec.volumeClaimTemplate;
  if (!claim) return true;
  const allowedClaimFields = new Set(['accessModes', 'resources', 'storageClassName']);
  if (Object.keys(claim).some(key => !allowedClaimFields.has(key))) return false;
  if (!Array.isArray(claim.accessModes) || claim.accessModes.length !== 1) return false;
  const resources = claim.resources || {};
  if (Object.keys(resources).some(key => key !== 'requests')) return false;
  const requests = resources.requests || {};
  return Object.keys(requests).length === 1 && typeof requests.storage === 'string';
}

function describeSourceReferences(manifest) {
  const worker = manifest.spec.worker;
  const references = [];
  if (worker.credentials?.secretRef?.name) references.push(`Secret ${worker.credentials.secretRef.name}`);
  if (worker.workspaceRef?.name) references.push(`Workspace ${worker.workspaceRef.name}`);
  if (worker.agentConfigRefs?.length) {
    references.push(`AgentConfigs ${worker.agentConfigRefs.map(reference => reference.name).join(', ')}`);
  }
  let description = references.length
    ? ` Namespace references: ${references.join('; ')}.`
    : ' No direct credential, Workspace, or AgentConfig references.';
  const advanced = [];
  if (worker.podOverrides) advanced.push('Pod overrides');
  if (manifest.spec.volumeClaimTemplate) advanced.push('persistent-volume settings');
  if (advanced.length) {
    description += ` Review ${advanced.join(' and ')} in YAML for additional namespace-scoped references.`;
  }
  return description;
}

function populateSessionSource(detail) {
  const manifest = detail.manifest;
  const worker = manifest.spec.worker;
  elements.form.elements.name.value = '';
  elements.namespace.value = detail.namespace;
  elements.provider.value = worker.type;
  elements.form.elements.model.value = worker.model || '';
  setSourceCredential(worker.credentials);
  setSourceWorkspace(worker.workspaceRef);
  state.selectedAgentConfigs = (worker.agentConfigRefs || []).map(reference => reference.name);
  renderAgentConfigOptions();

  const claim = manifest.spec.volumeClaimTemplate;
  state.sourceStorageClassNamePresent = Boolean(claim && 'storageClassName' in claim);
  elements.persistentVolume.checked = Boolean(claim);
  elements.form.elements.storageRequest.value = claim?.resources?.requests?.storage || '10Gi';
  elements.form.elements.accessMode.value = claim?.accessModes?.[0] || 'ReadWriteOnce';
  elements.form.elements.storageClassName.value = claim?.storageClassName ?? '';
  updateVolumeClaimFields();
  elements.yaml.value = detail.yaml;
  const formCompatible = sourceFitsForm(manifest);
  elements.sessionSourceStatus.textContent =
    `Loaded reusable settings from Session ${detail.namespace}/${detail.name}. Enter a name for the new Session.` +
    describeSourceReferences(manifest) +
    (formCompatible ? '' : ' YAML mode is required to preserve settings that the form cannot represent.');
  elements.sessionSourceStatus.hidden = false;
  state.loadedSource = {name: detail.name, namespace: detail.namespace, formCompatible};
  elements.formMode.disabled = !formCompatible;
  if (!formCompatible) setCreationMode('yaml');
}

function resetSourceValues() {
  const mode = state.creationMode;
  elements.form.reset();
  state.selectedAgentConfigs = [];
  state.sourceStorageClassNamePresent = false;
  state.loadedSource = null;
  elements.formMode.disabled = false;
  elements.namespace.value = state.namespace;
  elements.yaml.value = '';
  elements.sessionSourceStatus.hidden = true;
  elements.sessionSourceStatus.textContent = '';
  renderSessionSourceOptions();
  renderCredentialOptions();
  renderWorkspaceOptions();
  renderAgentConfigOptions();
  updateVolumeClaimFields();
  setCreationMode(mode);
}

function updateCreationBusyState() {
  const busy = state.sourceLoading || state.creatingSession;
  elements.sessionSource.disabled = busy;
  elements.createButton.disabled = busy;
}

function setSourceLoading(loading) {
  state.sourceLoading = loading;
  updateCreationBusyState();
}

function setCreatingSession(creating) {
  state.creatingSession = creating;
  updateCreationBusyState();
}

async function loadSessionSource(name) {
  const generation = ++state.sourceGeneration;
  if (!name) {
    setSourceLoading(false);
    resetSourceValues();
    return;
  }
  const namespace = state.namespace;
  setSourceLoading(true);
  elements.dialogError.textContent = '';
  try {
    const detail = await api(`/api/sessions/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`);
    if (generation !== state.sourceGeneration || namespace !== state.namespace) return;
    populateSessionSource(detail);
  } catch (error) {
    if (generation === state.sourceGeneration) {
      elements.sessionSource.value = state.loadedSource?.name || '';
      elements.dialogError.textContent = error.message;
    }
  } finally {
    if (generation === state.sourceGeneration) setSourceLoading(false);
  }
}

function selectSession(session) {
  savePromptDraft(state.selected);
  closeSocket();
  saveCurrentSessionView();
  state.selected = session;
  state.currentView = null;
  setActiveView('conversation');
  restorePromptDraft(session);
  elements.messages.replaceChildren();
  elements.queue.replaceChildren();
  elements.changesList.replaceChildren();
  renderSessions();
  renderHeader();
  elements.sidebar.classList.remove('open');
  if (elements.openSidebar) elements.openSidebar.setAttribute('aria-expanded', 'false');
  if (!session) {
    resetCurrentSessionView();
    state.replayingHistory = false;
    state.pinHistoryToBottom = false;
    elements.messages.append(elements.welcome || createWelcome());
    return;
  }
  const view = cachedSessionView(session);
  const hasCachedMessages = view.messages.hasChildNodes();
  const hasCachedHistory = hasCachedMessages && !view.statusPlaceholder;
  activateSessionView(view);
  state.pinHistoryToBottom = true;
  if (view.historyLoaded) {
    connectSocket();
    scheduleBottomAnchor();
    return;
  }
  if (hasCachedHistory) {
    if (session.phase === 'Ready') connectSocket();
    scheduleBottomAnchor();
    return;
  }
  elements.messages.replaceChildren();
  const loading = document.createElement('div');
  loading.className = 'welcome';
  const title = document.createElement('h1');
  title.textContent = session.phase === 'Ready' ? 'Opening conversation…' : 'Preparing the Session Pod…';
  const detail = document.createElement('p');
  detail.textContent = session.message || 'The controller is preparing the workspace and agent runtime.';
  loading.append(title, detail);
  elements.messages.append(loading);
  view.statusPlaceholder = true;
  if (session.phase === 'Ready') connectSocket();
  scheduleBottomAnchor();
}

function createWelcome() {
  const welcome = document.createElement('div');
  welcome.className = 'welcome';
  const title = document.createElement('h1');
  title.textContent = 'Choose a Session';
  const text = document.createElement('p');
  text.textContent = 'Select a conversation from the sidebar or create a new one.';
  welcome.append(title, text);
  return welcome;
}

function renderHeader() {
  const session = state.selected;
  elements.deleteButton.disabled = !session;
  elements.conversationTab.disabled = !session;
  elements.changesTab.disabled = !session;
  updateComposerAction();
  if (!session) {
    elements.title.textContent = 'Choose a session';
    elements.meta.textContent = 'Select an existing conversation or create one.';
    setConnection('idle', 'Not connected');
    setComposer(false);
    return;
  }
  elements.title.textContent = session.name;
  const details = [session.namespace, providerLabel(session.provider), sessionDisplayStatus(session)];
  if (session.branch) details.push(session.branch);
  const detailText = document.createElement('span');
  detailText.className = 'session-meta-details';
  detailText.textContent = details.join(' · ');
  elements.meta.replaceChildren(detailText);
  const link = createPullRequestLink(session.pullRequest, 'session-meta-pull-request');
  if (link) {
    const separator = document.createElement('span');
    separator.className = 'session-meta-separator';
    separator.textContent = '·';
    elements.meta.append(separator, link);
  }
  if (session.phase !== 'Ready') {
    setConnection(session.phase === 'Failed' ? 'error' : 'connecting', session.phase || 'Pending');
    setComposer(false);
  }
}

function setConnection(status, label) {
  elements.connection.dataset.state = status;
  elements.connection.lastChild.textContent = label;
}

function setComposer(enabled) {
  elements.input.disabled = !enabled;
  elements.input.placeholder = enabled ? 'Message the agent…' : 'Choose a ready session to start chatting';
  updateComposerAction();
}

function usesTouchComposer() {
  return window.matchMedia('(pointer: coarse)').matches;
}

function updateComposerAction() {
  const connected = state.socket && state.socket.readyState === WebSocket.OPEN;
  const interrupt = state.activeTurn && !elements.input.value.trim();
  const action = state.activeTurn ? 'queue' : 'send';
  elements.send.dataset.action = interrupt ? 'interrupt' : 'send';
  elements.send.textContent = interrupt ? '■' : '↑';
  elements.send.setAttribute('aria-label', interrupt ? 'Interrupt active work' : 'Send message');
  elements.send.title = interrupt ? 'Interrupt active work' : 'Send message';
  elements.send.disabled = !connected || elements.input.disabled || (interrupt && state.interrupting);
  elements.composerHint.textContent = usesTouchComposer()
    ? `Tap ↑ to ${action} · Return for a new line`
    : `Enter to ${action} · Shift+Enter for a new line`;
}

function closeSocket() {
  state.socketGeneration += 1;
  window.clearTimeout(state.reconnectTimer);
  state.reconnectTimer = null;
  if (state.bottomScrollFrame !== null) {
    window.cancelAnimationFrame(state.bottomScrollFrame);
    state.bottomScrollFrame = null;
  }
  if (state.socket) {
    state.socket.onclose = null;
    state.socket.close();
    state.socket = null;
  }
  setComposer(false);
  updateComposerAction();
}

function connectSocket() {
  if (!state.selected || state.selected.phase !== 'Ready') return;
  const pinToBottom = state.pinHistoryToBottom || messagesNearBottom();
  closeSocket();
  state.replayingHistory = true;
  state.pinHistoryToBottom = pinToBottom;
  const generation = state.socketGeneration;
  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const namespace = encodeURIComponent(state.selected.namespace);
  const name = encodeURIComponent(state.selected.name);
  const socket = new WebSocket(`${protocol}//${location.host}/api/sessions/${namespace}/${name}/connect`);
  state.socket = socket;
  setConnection('connecting', 'Connecting');
  socket.addEventListener('open', () => {
    if (generation !== state.socketGeneration) return;
    state.reconnectDelay = 800;
    socket.send(JSON.stringify({
      type: 'subscribe',
      since: state.lastEventID,
      journalId: state.currentView?.journalID || '',
      historyBounds: true,
    }));
    setConnection('connected', 'Connected');
    setComposer(true);
    updateComposerAction();
    elements.input.focus();
  });
  socket.addEventListener('message', event => {
    if (generation !== state.socketGeneration) return;
    try {
      handleEvent(JSON.parse(event.data));
    } catch (error) {
      showToast(`Could not read Session event: ${error.message}`);
    }
  });
  socket.addEventListener('close', () => {
    if (generation !== state.socketGeneration || !state.selected) return;
    state.socket = null;
    setConnection('error', 'Reconnecting');
    setComposer(false);
    updateComposerAction();
    state.reconnectTimer = window.setTimeout(connectSocket, state.reconnectDelay);
    state.reconnectDelay = Math.min(state.reconnectDelay * 1.8, 10000);
  });
  socket.addEventListener('error', () => socket.close());
}

function ensureConversation() {
  if (!elements.messages.querySelector('.welcome')) return;
  elements.messages.replaceChildren();
  if (state.currentView) state.currentView.statusPlaceholder = false;
}

function trimURLSuffix(value) {
  const openingBrackets = '([{';
  const closingBrackets = ')]}';
  const bracketBalance = [0, 0, 0];
  for (const character of value) {
    const openingIndex = openingBrackets.indexOf(character);
    if (openingIndex >= 0) bracketBalance[openingIndex]++;
    const closingIndex = closingBrackets.indexOf(character);
    if (closingIndex >= 0) bracketBalance[closingIndex]--;
  }

  let end = value.length;
  while (end > 0) {
    const character = value[end - 1];
    if ('.,;:!?'.includes(character)) {
      end--;
      continue;
    }
    const closingIndex = closingBrackets.indexOf(character);
    if (closingIndex < 0 || bracketBalance[closingIndex] >= 0) break;
    bracketBalance[closingIndex]++;
    end--;
  }
  return value.slice(0, end);
}

function appendLink(parent, href, label, depth, scanBudget) {
  let url;
  try {
    url = new URL(href);
  } catch {
    return false;
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') return false;

  const link = document.createElement('a');
  link.href = url.href;
  link.target = '_blank';
  link.rel = 'noopener noreferrer';
  appendInlineMarkdown(link, label, depth + 1, false, scanBudget);
  parent.append(link);
  return true;
}

function appendCodeBlock(parent, content, info) {
  const pre = document.createElement('pre');
  const code = document.createElement('code');
  const language = info.trim().split(/\s+/, 1)[0];
  if (/^[a-z0-9_+-]+$/i.test(language)) {
    pre.dataset.language = language;
    code.className = `language-${language.toLowerCase()}`;
  }
  code.textContent = content;
  pre.append(code);
  parent.append(pre);
}

function createInlineScanBudget(value) {
  // Bound repeated delimiter and link searches; exhausted parses render the remainder as plain text.
  return {remaining: Math.max(1024, value.length * 8), exhausted: false};
}

function findWithBudget(value, search, start, scanBudget) {
  if (scanBudget.exhausted || scanBudget.remaining <= 0) {
    scanBudget.exhausted = true;
    return -1;
  }
  const match = value.indexOf(search, start);
  const scanned = Math.max(0, (match < 0 ? value.length : match + search.length) - start);
  if (scanned > scanBudget.remaining) {
    scanBudget.remaining = 0;
    scanBudget.exhausted = true;
    return -1;
  }
  scanBudget.remaining -= scanned;
  return match;
}

function consumeScanBudget(scanBudget, amount = 1) {
  if (scanBudget.exhausted || amount > scanBudget.remaining) {
    scanBudget.remaining = 0;
    scanBudget.exhausted = true;
    return false;
  }
  scanBudget.remaining -= amount;
  return true;
}

function findMarkdownLink(value, start, scanBudget) {
  const labelEnd = findWithBudget(value, '](', start + 1, scanBudget);
  if (labelEnd < 0) return null;

  let parentheses = 0;
  for (let index = labelEnd + 2; index < value.length; index++) {
    if (!consumeScanBudget(scanBudget)) return null;
    if (value[index] === '\\') {
      index++;
      continue;
    }
    if (value[index] === '(') parentheses++;
    if (value[index] !== ')') continue;
    if (parentheses > 0) {
      parentheses--;
      continue;
    }

    const target = value.slice(labelEnd + 2, index).trim();
    const destination = target.match(/^<([^>]+)>|^(\S+)/);
    if (!destination) return null;
    return {
      end: index + 1,
      href: destination[1] || destination[2],
      label: value.slice(start + 1, labelEnd),
    };
  }
  return null;
}

function isAlphanumeric(character) {
  return Boolean(character) && /[\p{L}\p{N}]/u.test(character);
}

function isExactDelimiterRun(value, index, marker) {
  return value[index - 1] !== marker[0] && value[index + marker.length] !== marker[0];
}

function canOpenDelimiter(value, index, marker) {
  const before = value[index - 1] || '';
  const after = value[index + marker.length] || '';
  if (!isExactDelimiterRun(value, index, marker) || !after || /\s/u.test(after)) return false;
  return marker[0] !== '_' || !isAlphanumeric(before) || !isAlphanumeric(after);
}

function findClosingDelimiter(value, index, marker, scanBudget) {
  let closing = findWithBudget(value, marker, index + marker.length, scanBudget);
  while (closing >= 0) {
    const before = value[closing - 1] || '';
    const after = value[closing + marker.length] || '';
    const intrawordUnderscore = marker[0] === '_' && isAlphanumeric(before) && isAlphanumeric(after);
    if (isExactDelimiterRun(value, closing, marker) && before && !/\s/u.test(before) && !intrawordUnderscore) return closing;
    closing = findWithBudget(value, marker, closing + marker.length, scanBudget);
  }
  return -1;
}

function appendInlineMarkdown(parent, value, depth = 0, allowLinks = true, budget = null) {
  if (depth > 20) {
    parent.append(document.createTextNode(value));
    return;
  }
  const scanBudget = budget || createInlineScanBudget(value);

  const delimiters = [
    ['***', ['strong', 'em']],
    ['___', ['strong', 'em']],
    ['**', ['strong']],
    ['__', ['strong']],
    ['~~', ['del']],
    ['*', ['em']],
    ['_', ['em']],
  ];
  const escapedPunctuation = String.raw`\\` + '`*{}[]()#+-.!_>|';
  let textStart = 0;
  let index = 0;

  const appendTextBefore = (end) => {
    if (end > textStart) parent.append(document.createTextNode(value.slice(textStart, end)));
  };

  while (index < value.length) {
    if (value[index] === '\\' && index + 1 < value.length && escapedPunctuation.includes(value[index + 1])) {
      appendTextBefore(index);
      parent.append(document.createTextNode(value[index + 1]));
      index += 2;
      textStart = index;
      continue;
    }

    if (value[index] === '`') {
      let markerEnd = index + 1;
      while (value[markerEnd] === '`') markerEnd++;
      if (!consumeScanBudget(scanBudget, markerEnd - index)) break;
      const marker = value.slice(index, markerEnd);
      const closing = findWithBudget(value, marker, markerEnd, scanBudget);
      if (scanBudget.exhausted) break;
      if (closing >= markerEnd) {
        appendTextBefore(index);
        const code = document.createElement('code');
        code.className = 'inline-code';
        code.textContent = value.slice(markerEnd, closing).replace(/\n/g, ' ');
        parent.append(code);
        index = closing + marker.length;
        textStart = index;
        continue;
      }
      index = markerEnd;
      continue;
    }

    if (allowLinks && value[index] === '[' && value[index - 1] !== '!') {
      const markdownLink = findMarkdownLink(value, index, scanBudget);
      if (scanBudget.exhausted) break;
      if (markdownLink) {
        const holder = document.createDocumentFragment();
        if (appendLink(holder, markdownLink.href, markdownLink.label, depth, scanBudget)) {
          appendTextBefore(index);
          parent.append(holder);
          index = markdownLink.end;
          textStart = index;
          continue;
        }
      }
    }

    const possibleScheme = value.slice(index, index + 8).toLowerCase();
    if (allowLinks && (possibleScheme.startsWith('http://') || possibleScheme.startsWith('https://'))) {
      const match = value.slice(index).match(/^https?:\/\/[^\s<>"']+/i);
      const linkText = trimURLSuffix(match[0]);
      const holder = document.createDocumentFragment();
      if (appendLink(holder, linkText, linkText, depth, scanBudget)) {
        appendTextBefore(index);
        parent.append(holder);
        index += linkText.length;
        textStart = index;
        continue;
      }
    }

    let matchedDelimiter = false;
    for (const [marker, tags] of delimiters) {
      if (!value.startsWith(marker, index) || !canOpenDelimiter(value, index, marker)) continue;
      const closing = findClosingDelimiter(value, index, marker, scanBudget);
      if (scanBudget.exhausted) break;
      if (closing <= index + marker.length) continue;

      appendTextBefore(index);
      const element = document.createElement(tags[0]);
      let contentParent = element;
      for (const tag of tags.slice(1)) {
        const nested = document.createElement(tag);
        contentParent.append(nested);
        contentParent = nested;
      }
      appendInlineMarkdown(contentParent, value.slice(index + marker.length, closing), depth + 1, allowLinks, scanBudget);
      parent.append(element);
      index = closing + marker.length;
      textStart = index;
      matchedDelimiter = true;
      break;
    }
    if (scanBudget.exhausted) break;
    if (matchedDelimiter) continue;

    index++;
  }

  appendTextBefore(value.length);
}

function matchFence(line) {
  const match = line.match(/^ {0,3}(`{3,}|~{3,})(.*)$/);
  if (!match || (match[1][0] === '`' && match[2].includes('`'))) return null;
  return {marker: match[1], info: match[2].trim()};
}

function isClosingFence(line, marker) {
  const value = line.replace(/^ {0,3}/, '');
  let markerEnd = 0;
  while (value[markerEnd] === marker[0]) markerEnd++;
  return markerEnd >= marker.length && value.slice(markerEnd).trim() === '';
}

function matchListItem(line) {
  const match = line.match(/^( {0,3})([-+*]|\d+[.)])[\t ]+(.*)$/);
  if (!match) return null;
  const ordered = /^\d/.test(match[2]);
  return {
    indent: match[1].length,
    ordered,
    start: ordered ? Number.parseInt(match[2], 10) : 1,
    text: match[3],
  };
}

function isHorizontalRule(line) {
  const compact = line.trim().replace(/[\t ]/g, '');
  return compact.length >= 3 && (/^\*+$/.test(compact) || /^-+$/.test(compact) || /^_+$/.test(compact));
}

const maxMarkdownTableCells = 10000;

function unescapeTablePipes(value) {
  return value.replaceAll('\\|', '|');
}

function matchingCodeSpanEnds(value) {
  const runsByLength = new Map();
  for (let index = 0; index < value.length;) {
    if (value[index] !== '`') {
      index++;
      continue;
    }
    let end = index + 1;
    while (value[end] === '`') end++;
    const length = end - index;
    let backslashes = 0;
    for (let previous = index - 1; previous >= 0 && value[previous] === '\\'; previous--) backslashes++;
    if (!runsByLength.has(length)) runsByLength.set(length, []);
    runsByLength.get(length).push({start: index, end, escaped: backslashes % 2 === 1});
    index = end;
  }

  const matches = new Map();
  for (const runs of runsByLength.values()) {
    for (let index = 0; index + 1 < runs.length; index++) {
      if (!runs[index].escaped) matches.set(runs[index].start, runs[index + 1].end);
    }
  }
  return matches;
}

function splitTableRow(line) {
  const value = line.trim();
  const codeSpanEnds = matchingCodeSpanEnds(value);
  const cells = [];
  let cell = '';
  let hasSeparator = false;
  let trailingSeparator = false;

  for (let index = 0; index < value.length;) {
    const codeSpanEnd = codeSpanEnds.get(index);
    if (codeSpanEnd !== undefined) {
      cell += unescapeTablePipes(value.slice(index, codeSpanEnd));
      index = codeSpanEnd;
      trailingSeparator = false;
      continue;
    }
    if (value[index] === '\\' && index + 1 < value.length) {
      cell += value[index + 1] === '|' ? '|' : value.slice(index, index + 2);
      index += 2;
      trailingSeparator = false;
      continue;
    }
    if (value[index] === '|') {
      cells.push(cell.trim());
      cell = '';
      hasSeparator = true;
      trailingSeparator = true;
      index++;
      continue;
    }
    cell += value[index];
    trailingSeparator = false;
    index++;
  }
  cells.push(cell.trim());

  if (!hasSeparator) return null;
  if (value.startsWith('|')) cells.shift();
  if (trailingSeparator) cells.pop();
  return cells;
}

function tableAlignments(line) {
  const cells = splitTableRow(line);
  if (!cells || cells.length === 0) return null;

  const alignments = [];
  for (const cell of cells) {
    if (!/^:?-+:?$/.test(cell)) return null;
    if (cell.startsWith(':') && cell.endsWith(':')) alignments.push('center');
    else if (cell.endsWith(':')) alignments.push('right');
    else if (cell.startsWith(':')) alignments.push('left');
    else alignments.push('');
  }
  return alignments;
}

function matchTable(lines, index) {
  if (index + 1 >= lines.length) return null;
  const headers = splitTableRow(lines[index]);
  const alignments = tableAlignments(lines[index + 1]);
  if (!headers || !alignments || headers.length !== alignments.length) return null;

  const rows = [];
  let renderedCells = headers.length;
  let oversized = renderedCells > maxMarkdownTableCells;
  let next = index + 2;
  while (next < lines.length && !startsMarkdownBlock(lines[next])) {
    if (!oversized) {
      const cells = splitTableRow(lines[next]) || [lines[next].trim()];
      if (renderedCells > maxMarkdownTableCells - headers.length) {
        oversized = true;
        rows.length = 0;
      } else {
        rows.push(headers.map((_, cellIndex) => cells[cellIndex] || ''));
        renderedCells += headers.length;
      }
    }
    next++;
  }
  return {headers, alignments, rows, next, oversized};
}

function appendTable(parent, tableData) {
  const container = document.createElement('div');
  container.className = 'markdown-table-container';
  const table = document.createElement('table');
  const head = document.createElement('thead');
  const headerRow = document.createElement('tr');

  tableData.headers.forEach((value, index) => {
    const header = document.createElement('th');
    if (tableData.alignments[index]) header.className = `table-align-${tableData.alignments[index]}`;
    appendInlineMarkdown(header, value);
    headerRow.append(header);
  });
  head.append(headerRow);
  table.append(head);

  if (tableData.rows.length > 0) {
    const body = document.createElement('tbody');
    for (const row of tableData.rows) {
      const tableRow = document.createElement('tr');
      row.forEach((value, index) => {
        const cell = document.createElement('td');
        if (tableData.alignments[index]) cell.className = `table-align-${tableData.alignments[index]}`;
        appendInlineMarkdown(cell, value);
        tableRow.append(cell);
      });
      body.append(tableRow);
    }
    table.append(body);
  }

  container.append(table);
  parent.append(container);
}

function startsMarkdownBlock(line) {
  return line.trim() === '' || Boolean(matchFence(line)) || /^ {0,3}#{1,6}(?:[\t ]|$)/.test(line) ||
    /^ {0,3}>/.test(line) || Boolean(matchListItem(line)) || /^( {4}|\t)/.test(line) || isHorizontalRule(line);
}

function appendMarkdownBlocks(parent, value, depth = 0) {
  const lines = value.replace(/\r\n?/g, '\n').split('\n');
  let index = 0;

  while (index < lines.length) {
    if (lines[index].trim() === '') {
      index++;
      continue;
    }

    const fence = matchFence(lines[index]);
    if (fence) {
      const codeLines = [];
      index++;
      while (index < lines.length && !isClosingFence(lines[index], fence.marker)) {
        codeLines.push(lines[index]);
        index++;
      }
      if (index < lines.length) index++;
      appendCodeBlock(parent, codeLines.join('\n'), fence.info);
      continue;
    }

    if (/^( {4}|\t)/.test(lines[index])) {
      const codeLines = [];
      while (index < lines.length && (/^( {4}|\t)/.test(lines[index]) || lines[index].trim() === '')) {
        codeLines.push(lines[index].replace(/^( {4}|\t)/, ''));
        index++;
      }
      appendCodeBlock(parent, codeLines.join('\n'), '');
      continue;
    }

    const heading = lines[index].match(/^ {0,3}(#{1,6})([\t ]+.*|[\t ]*)$/);
    if (heading) {
      const element = document.createElement(`h${heading[1].length}`);
      const headingText = (heading[2] || '').replace(/[\t ]+#+[\t ]*$/, '').trim();
      appendInlineMarkdown(element, headingText);
      parent.append(element);
      index++;
      continue;
    }

    if (isHorizontalRule(lines[index])) {
      parent.append(document.createElement('hr'));
      index++;
      continue;
    }

    if (/^ {0,3}>/.test(lines[index])) {
      const quoteLines = [];
      while (index < lines.length) {
        const quote = lines[index].match(/^ {0,3}>[\t ]?(.*)$/);
        if (!quote) break;
        quoteLines.push(quote[1]);
        index++;
      }
      const blockquote = document.createElement('blockquote');
      if (depth < 20) appendMarkdownBlocks(blockquote, quoteLines.join('\n'), depth + 1);
      else appendInlineMarkdown(blockquote, quoteLines.join('\n'), depth + 1);
      parent.append(blockquote);
      continue;
    }

    const firstItem = matchListItem(lines[index]);
    if (firstItem) {
      const list = document.createElement(firstItem.ordered ? 'ol' : 'ul');
      if (firstItem.ordered && firstItem.start !== 1) list.start = firstItem.start;
      while (index < lines.length) {
        const item = matchListItem(lines[index]);
        if (!item || item.ordered !== firstItem.ordered || item.indent !== firstItem.indent) break;

        const itemLines = [item.text];
        index++;
        const continuationIndent = item.indent + 2;
        while (index < lines.length && lines[index].startsWith(' '.repeat(continuationIndent))) {
          itemLines.push(lines[index].slice(continuationIndent));
          index++;
        }

        const listItem = document.createElement('li');
        const task = itemLines[0].match(/^\[([ xX])\][\t ]+(.*)$/);
        if (task) {
          list.classList.add('task-list');
          listItem.className = 'task-list-item';
          const checkbox = document.createElement('input');
          checkbox.type = 'checkbox';
          checkbox.checked = task[1].toLowerCase() === 'x';
          checkbox.disabled = true;
          listItem.append(checkbox);
          itemLines[0] = task[2];
        }
        if (depth < 20) appendMarkdownBlocks(listItem, itemLines.join('\n'), depth + 1);
        else appendInlineMarkdown(listItem, itemLines.join('\n'), depth + 1);
        list.append(listItem);
      }
      parent.append(list);
      continue;
    }

    const table = matchTable(lines, index);
    if (table) {
      if (table.oversized) {
        const paragraph = document.createElement('p');
        paragraph.textContent = lines.slice(index, table.next).join('\n');
        parent.append(paragraph);
      } else {
        appendTable(parent, table);
      }
      index = table.next;
      continue;
    }

    const paragraphLines = [lines[index]];
    index++;
    while (index < lines.length && !startsMarkdownBlock(lines[index])) {
      paragraphLines.push(lines[index]);
      index++;
    }
    const paragraph = document.createElement('p');
    appendInlineMarkdown(paragraph, paragraphLines.join('\n'));
    parent.append(paragraph);
  }
}

function renderMessageMarkdown(element, text) {
  const fragment = document.createDocumentFragment();
  appendMarkdownBlocks(fragment, text || '');

  element.replaceChildren(fragment);
}

function completedAssistantText(eventText, streamedText) {
  return eventText || streamedText || '';
}

function finishHistoryReplay() {
  const pinToBottom = state.pinHistoryToBottom;
  state.replayingHistory = false;
  if (state.fileChangesDirty) {
    state.fileChangesDirty = false;
    renderFileChanges();
  }
  for (const block of state.diffs.values()) {
    if (!block.dirty) continue;
    renderDiffBlock(block, block.openFirst);
    block.dirty = false;
    block.openFirst = false;
  }
  if (state.currentView) {
    state.currentView.historyLoaded = true;
    state.currentView.lastEventID = state.lastEventID;
  }
  ensureConversation();
  if (pinToBottom) scheduleBottomAnchor();
  state.pinHistoryToBottom = false;
}

function handleEvent(event) {
  if (event.id) state.lastEventID = Math.max(state.lastEventID, event.id);
  switch (event.type) {
    case 'history.start':
      if (event.reset || (state.currentView?.journalID && state.currentView.journalID !== event.journalId)) {
        resetCurrentSessionView();
      }
      if (state.currentView && event.journalId) state.currentView.journalID = event.journalId;
      state.replayingHistory = true;
      break;
    case 'history.end':
      finishHistoryReplay();
      break;
    case 'runtime.recovered':
      renderRecovery(event);
      break;
    case 'user.message':
      renderUser(event);
      break;
    case 'turn.started':
      endAssistantSegment(event.turnId);
      state.activeTurn = true;
      state.interrupting = false;
      acceptQueuedMessage(event.turnId);
      updateComposerAction();
      break;
    case 'turn.interrupting':
      state.interrupting = true;
      updateComposerAction();
      break;
    case 'assistant.delta':
      renderAssistantDelta(event);
      break;
    case 'assistant.message':
      renderAssistantMessage(event);
      break;
    case 'tool.started':
      endAssistantSegment(event.turnId);
      renderTool(event);
      break;
    case 'tool.completed':
      endAssistantSegment(event.turnId);
      completeTool(event);
      break;
    case 'input.requested':
      endAssistantSegment(event.turnId);
      renderInputRequest(event);
      break;
    case 'input.resolved':
      resolveInputCard(event);
      break;
    case 'file.diff':
      endAssistantSegment(event.turnId);
      renderDiff(event);
      break;
    case 'turn.completed':
      endAssistantSegment(event.turnId);
      renderTurnEnd(event);
      break;
    case 'error':
      endAssistantSegment(event.turnId);
      renderError(event);
      break;
  }
}

function renderUser(event) {
  if (event.turnId) {
    renderQueuedUser(event);
    return;
  }
  renderAcceptedUser(event);
}

function renderAcceptedUser(event) {
  ensureConversation();
  const row = document.createElement('div');
  row.className = 'event-row user';
  const message = document.createElement('div');
  message.className = 'user-message';
  const bubble = document.createElement('div');
  bubble.className = 'message-bubble';
  renderMessageMarkdown(bubble, event.text);
  message.append(bubble);
  row.append(message);
  elements.messages.append(row);
  scrollToBottom();
}

function renderQueuedUser(event) {
  if (state.queuedMessages.has(event.turnId)) return;
  const item = document.createElement('div');
  item.className = 'queued-prompt';
  const text = document.createElement('div');
  text.className = 'queued-prompt-text';
  text.textContent = event.text;
  const status = document.createElement('span');
  status.className = 'queued-prompt-status';
  status.textContent = 'Queued';
  item.append(text, status);
  elements.queue.append(item);
  elements.queue.hidden = false;
  state.queuedMessages.set(event.turnId, {event, item});
}

function acceptQueuedMessage(turnID) {
  const queued = state.queuedMessages.get(turnID);
  if (!queued) return;
  queued.item.remove();
  state.queuedMessages.delete(turnID);
  elements.queue.hidden = state.queuedMessages.size === 0;
  renderAcceptedUser(queued.event);
}

function assistantBubble(turnID) {
  const key = turnID || 'current';
  let bubble = state.assistantSegmentByTurn.get(key);
  if (bubble) return bubble;
  ensureConversation();
  const row = document.createElement('div');
  row.className = 'event-row assistant';
  const avatar = document.createElement('div');
  avatar.className = 'agent-avatar';
  avatar.textContent = providerInitials(state.selected?.provider);
  bubble = document.createElement('div');
  bubble.className = 'message-bubble';
  row.append(avatar, bubble);
  elements.messages.append(row);
  state.assistantSegmentByTurn.set(key, bubble);
  return bubble;
}

function endAssistantSegment(turnID) {
  const key = turnID || 'current';
  const bubble = state.assistantSegmentByTurn.get(key);
  if (bubble) renderMessageMarkdown(bubble, state.assistantTextByTurn.get(key) || '');
  state.assistantSegmentByTurn.delete(key);
  state.assistantTextByTurn.delete(key);
}

function renderAssistantDelta(event) {
  const key = event.turnId || 'current';
  const bubble = assistantBubble(event.turnId);
  const delta = event.text || '';
  const text = (state.assistantTextByTurn.get(key) || '') + delta;
  state.assistantTextByTurn.set(key, text);
  const tail = bubble.lastChild;
  if (tail?.nodeType === 3) tail.appendData(delta);
  else bubble.append(document.createTextNode(delta));
  scrollToBottom();
}

function renderAssistantMessage(event) {
  const key = event.turnId || 'current';
  const bubble = assistantBubble(event.turnId);
  const text = completedAssistantText(event.text, state.assistantTextByTurn.get(key));
  state.assistantTextByTurn.set(key, text);
  renderMessageMarkdown(bubble, text);
  state.assistantSegmentByTurn.delete(key);
  state.assistantTextByTurn.delete(key);
  scrollToBottom();
}

function renderTool(event) {
  ensureConversation();
  if (event.toolId && state.tools.has(event.toolId)) return;
  const card = document.createElement('div');
  card.className = 'tool-card';
  card.dataset.status = event.status || 'running';
  const icon = document.createElement('span');
  icon.className = 'tool-icon';
  icon.textContent = '◇';
  const name = document.createElement('span');
  name.className = 'tool-name';
  name.textContent = event.toolName || 'Tool';
  const status = document.createElement('span');
  status.className = 'tool-status';
  status.textContent = event.status || 'running';
  card.append(icon, name, status);
  elements.messages.append(card);
  if (event.toolId) state.tools.set(event.toolId, card);
  scrollToBottom();
}

function completeTool(event) {
  const card = state.tools.get(event.toolId);
  if (!card) {
    renderTool({...event, status: event.status || 'completed'});
    return;
  }
  card.dataset.status = event.status || 'completed';
  card.querySelector('.tool-status').textContent = event.status || 'completed';
  card.querySelector('.tool-icon').textContent = event.status === 'failed' ? '!' : '✓';
}

function renderInputRequest(event) {
  ensureConversation();
  if (!event.inputId || state.inputs.has(event.inputId)) return;
  const card = document.createElement('form');
  card.className = 'input-card';
  const eyebrow = document.createElement('div');
  eyebrow.className = 'input-eyebrow';
  eyebrow.textContent = 'Input requested';
  card.append(eyebrow);

  const rows = [];
  for (const question of event.questions || []) {
    const fieldset = document.createElement('fieldset');
    const legend = document.createElement('legend');
    legend.textContent = question.header || 'Question';
    const prompt = document.createElement('div');
    prompt.className = 'input-question';
    prompt.textContent = question.question;
    const choices = document.createElement('div');
    choices.className = 'input-options';
    const controls = [];
    for (const option of question.options || []) {
      const choice = document.createElement('label');
      choice.className = 'input-option';
      const control = document.createElement('input');
      control.type = question.multiSelect ? 'checkbox' : 'radio';
      control.name = `${event.inputId}-${question.id}`;
      control.value = option.label;
      const copy = document.createElement('span');
      const label = document.createElement('strong');
      label.textContent = option.label;
      const description = document.createElement('small');
      description.textContent = option.description || '';
      copy.append(label, description);
      choice.append(control, copy);
      choices.append(choice);
      controls.push(control);
    }
    const other = document.createElement('input');
    other.className = 'input-other';
    other.type = question.secret ? 'password' : 'text';
    other.placeholder = question.options?.length ? 'Or type another answer' : 'Type your answer';
    other.autocomplete = 'off';
    fieldset.append(legend, prompt, choices, other);
    card.append(fieldset);
    rows.push({question, controls, other});
  }

  const actions = document.createElement('div');
  actions.className = 'input-actions';
  const cancel = document.createElement('button');
  cancel.type = 'button';
  cancel.textContent = 'Cancel';
  cancel.addEventListener('click', () => sendInputResponse(event.inputId, null, true));
  const submit = document.createElement('button');
  submit.type = 'submit';
  submit.textContent = 'Send answers';
  actions.append(cancel, submit);
  card.append(actions);
  card.addEventListener('submit', submitEvent => {
    submitEvent.preventDefault();
    const answers = {};
    for (const row of rows) {
      let values = row.controls.filter(control => control.checked).map(control => control.value);
      const other = row.other.value.trim();
      if (other) values = row.question.multiSelect ? [...values, other] : [other];
      if (!values.length) {
        showToast(`Answer “${row.question.question}” before continuing.`);
        row.other.focus();
        return;
      }
      answers[row.question.id] = values;
    }
    sendInputResponse(event.inputId, answers, false);
  });
  elements.messages.append(card);
  state.inputs.set(event.inputId, card);
  scrollToBottom();
}

function sendInputResponse(inputId, answers, cancel) {
  if (!state.socket || state.socket.readyState !== WebSocket.OPEN) return;
  state.socket.send(JSON.stringify({type: 'input', inputId, ...(answers ? {answers} : {}), ...(cancel ? {cancel: true} : {})}));
}

function resolveInputCard(event) {
  const card = state.inputs.get(event.inputId);
  if (!card) return;
  card.querySelector('.input-eyebrow').textContent = `Input ${event.status || 'resolved'}`;
  card.querySelectorAll('button, input').forEach(control => { control.disabled = true; });
}

function renderDiff(event) {
  if (!event.diff) return;
  const files = parseFileDiffs(event.diff);
  updateFileChanges(files);
  const key = event.turnId || `diff-${event.id || 'current'}`;
  let block = state.diffs.get(key);
  const created = !block;
  if (!block) {
    ensureConversation();
    const card = document.createElement('section');
    card.className = 'diff-card';
    card.setAttribute('aria-label', 'File changes');
    const header = document.createElement('div');
    header.className = 'diff-card-header';
    const title = document.createElement('strong');
    title.textContent = 'File changes';
    const count = document.createElement('span');
    const list = document.createElement('div');
    list.className = 'changes-list';
    header.append(title, count);
    card.append(header, list);
    elements.messages.append(card);
    block = {count, list, files: new Map()};
    state.diffs.set(key, block);
  }
  for (const file of files) block.files.set(file.name, file.diff);
  if (state.replayingHistory) {
    block.dirty = true;
    block.openFirst = block.openFirst || created;
  } else {
    renderDiffBlock(block, created);
    if (created) scrollToBottom();
  }
}

function updateFileChanges(files) {
  for (const file of files) state.fileChanges.set(file.name, file.diff);
  if (state.replayingHistory) state.fileChangesDirty = true;
  else renderFileChanges();
}

function parseFileDiffs(diff) {
  const lines = diff.split('\n');
  const starts = [];
  lines.forEach((line, index) => {
    if (line.startsWith('diff --git ') || /^\*\*\* (?:Add|Delete|Update) File: /.test(line)) starts.push(index);
  });
  if (!starts.length) starts.push(0);

  return starts.map((start, index) => {
    const segment = lines.slice(start, starts[index + 1] ?? lines.length);
    return {name: diffFileName(segment) || 'File changes', diff: segment.join('\n')};
  });
}

function diffFileName(lines) {
  const patchHeader = lines.find(line => /^\*\*\* (?:Add|Delete|Update) File: /.test(line));
  if (patchHeader) return patchHeader.replace(/^\*\*\* (?:Add|Delete|Update) File: /, '');

  for (const prefix of ['+++ ', '--- ']) {
    const header = lines.find(line => line.startsWith(prefix));
    if (!header) continue;
    const path = normalizeDiffPath(header.slice(prefix.length));
    if (path !== '/dev/null') return path;
  }

  const header = lines.find(line => line.startsWith('diff --git '));
  if (!header) return '';
  const quotedPath = header.match(/ ("(?:\\.|[^"\\])*")$/)?.[1];
  if (quotedPath) return normalizeDiffPath(quotedPath);
  const separator = header.lastIndexOf(' b/');
  return normalizeDiffPath(separator < 0 ? header.slice('diff --git '.length) : header.slice(separator + 1));
}

function normalizeDiffPath(value) {
  const rawPath = value.split('\t', 1)[0];
  const path = rawPath.startsWith('"') && rawPath.endsWith('"')
    ? decodeGitQuotedPath(rawPath.slice(1, -1))
    : rawPath;
  return path.replace(/^[ab]\//, '');
}

function decodeGitQuotedPath(value) {
  const bytes = [];
  const encoder = new TextEncoder();
  const escapedBytes = {a: 7, b: 8, t: 9, n: 10, v: 11, f: 12, r: 13, '\\': 92, '"': 34};
  const append = text => bytes.push(...encoder.encode(text));

  for (let index = 0; index < value.length;) {
    if (value[index] !== '\\') {
      const character = String.fromCodePoint(value.codePointAt(index));
      append(character);
      index += character.length;
      continue;
    }

    index++;
    if (index === value.length) {
      append('\\');
      break;
    }
    const octal = value.slice(index).match(/^[0-7]{1,3}/)?.[0];
    if (octal) {
      bytes.push(Number.parseInt(octal, 8));
      index += octal.length;
      continue;
    }
    const escaped = value[index];
    if (Object.prototype.hasOwnProperty.call(escapedBytes, escaped)) bytes.push(escapedBytes[escaped]);
    else append(escaped);
    index++;
  }

  return new TextDecoder().decode(new Uint8Array(bytes));
}

function renderFileChanges() {
  const openFiles = new Set(
    [...elements.changesList.querySelectorAll('.file-change[open]')].map(item => item.dataset.path),
  );
  const count = state.fileChanges.size;
  updateFileChangesHeader();

  if (!count) {
    elements.changesList.replaceChildren();
    const empty = document.createElement('div');
    empty.className = 'changes-empty';
    empty.textContent = state.selected ? 'No file changes yet.' : 'Choose a Session to inspect its file changes.';
    elements.changesList.append(empty);
    return;
  }

  renderFileChangeList(elements.changesList, state.fileChanges, openFiles);
}

function renderDiffBlock(block, created) {
  const openFiles = new Set(
    [...block.list.querySelectorAll('.file-change[open]')].map(item => item.dataset.path),
  );
  if (created && block.files.size === 1) openFiles.add(block.files.keys().next().value);
  const count = block.files.size;
  block.count.textContent = count === 1 ? '1 file' : `${count} files`;
  renderFileChangeList(block.list, block.files, openFiles);
}

function renderFileChangeList(list, files, openFiles) {
  list.replaceChildren();
  for (const [path, diff] of files) {
    const details = document.createElement('details');
    details.className = 'file-change';
    details.dataset.path = path;
    details.open = openFiles.has(path);
    const summary = document.createElement('summary');
    const name = document.createElement('span');
    name.className = 'file-change-name';
    name.textContent = path;
    const stats = diffStats(diff);
    const stat = document.createElement('span');
    stat.className = 'file-change-stat';
    stat.setAttribute('aria-label', `${stats.added} additions and ${stats.removed} deletions`);
    const added = document.createElement('span');
    added.className = 'file-change-added';
    added.textContent = `+${stats.added}`;
    const removed = document.createElement('span');
    removed.className = 'file-change-removed';
    removed.textContent = `−${stats.removed}`;
    stat.append(added, removed);
    summary.append(name, stat);
    details.append(summary, renderDiffLines(diff));
    list.append(details);
  }
}

function diffStats(diff) {
  let added = 0;
  let removed = 0;
  for (const line of diff.split('\n')) {
    const kind = diffChangeKind(line);
    if (kind === 'added') added++;
    if (kind === 'removed') removed++;
  }
  return {added, removed};
}

function diffChangeKind(line) {
  if (line.startsWith('+') && !line.startsWith('+++ ')) return 'added';
  if (line.startsWith('-') && !line.startsWith('--- ')) return 'removed';
  return '';
}

function renderDiffLines(diff) {
  const lines = document.createElement('div');
  lines.className = 'diff-lines';
  for (const text of diff.split('\n')) {
    const line = document.createElement('div');
    line.className = 'diff-line';
    const kind = diffChangeKind(text);
    if (kind) line.classList.add(kind);
    if (text.startsWith('@@')) line.classList.add('hunk');
    if (/^(diff --git |index |--- |\+\+\+ )/.test(text)) line.classList.add('metadata');
    line.textContent = text || ' ';
    lines.append(line);
  }
  return lines;
}

function setActiveView(view) {
  const changesActive = view === 'changes';
  elements.messages.hidden = changesActive;
  elements.composerWrap.hidden = changesActive;
  elements.changes.hidden = !changesActive;
  elements.conversationTab.setAttribute('aria-selected', String(!changesActive));
  elements.changesTab.setAttribute('aria-selected', String(changesActive));
  elements.conversationTab.tabIndex = changesActive ? -1 : 0;
  elements.changesTab.tabIndex = changesActive ? 0 : -1;
}

function handleViewTabKeydown(event) {
  const tabs = [elements.conversationTab, elements.changesTab].filter(tab => !tab.disabled);
  const current = tabs.indexOf(event.target);
  if (current < 0) return;

  let target;
  if (event.key === 'ArrowLeft') target = tabs[(current - 1 + tabs.length) % tabs.length];
  if (event.key === 'ArrowRight') target = tabs[(current + 1) % tabs.length];
  if (event.key === 'Home') target = tabs[0];
  if (event.key === 'End') target = tabs[tabs.length - 1];
  if (!target) return;

  event.preventDefault();
  target.click();
  target.focus();
}

function renderError(event) {
  if (event.status === 'rejected') {
    state.interrupting = false;
    updateComposerAction();
  }
  ensureConversation();
  const card = document.createElement('div');
  card.className = 'error-card';
  card.textContent = event.text || 'The Session runtime reported an error.';
  elements.messages.append(card);
  scrollToBottom();
}

function renderRecovery(event) {
  ensureConversation();
  const card = document.createElement('div');
  card.className = 'recovery-card';
  card.textContent = event.text || 'Session runtime recovered';
  elements.messages.append(card);
  scrollToBottom();
}

function renderTurnEnd(event) {
  state.activeTurn = false;
  state.interrupting = false;
  acceptQueuedMessage(event.turnId);
  updateComposerAction();
  if (event.status === 'interrupted') showToast('Active work interrupted');
  const divider = document.createElement('div');
  divider.className = 'turn-divider';
  elements.messages.append(divider);
  scrollToBottom();
}

function scrollToBottom(smooth = true) {
  if (state.replayingHistory) {
    if (state.pinHistoryToBottom) scheduleBottomAnchor();
    return;
  }
  const distance = messagesBottomDistance();
  if (distance < 240 || !smooth) {
    elements.messages.scrollTo({top: elements.messages.scrollHeight, behavior: smooth ? 'smooth' : 'auto'});
  }
}

function messagesBottomDistance() {
  return elements.messages.scrollHeight - elements.messages.scrollTop - elements.messages.clientHeight;
}

function messagesNearBottom() {
  return messagesBottomDistance() < 240;
}

function scheduleBottomAnchor() {
  if (state.bottomScrollFrame !== null) return;
  state.bottomScrollFrame = window.requestAnimationFrame(() => {
    state.bottomScrollFrame = null;
    const scrollBehavior = elements.messages.style.scrollBehavior;
    elements.messages.style.scrollBehavior = 'auto';
    elements.messages.scrollTop = elements.messages.scrollHeight;
    elements.messages.style.scrollBehavior = scrollBehavior;
  });
}

elements.composer.addEventListener('submit', event => {
  event.preventDefault();
  const text = elements.input.value.trim();
  if (!state.socket || state.socket.readyState !== WebSocket.OPEN) return;
  if (text) {
    state.socket.send(JSON.stringify({type: 'message', text}));
    clearPromptDraft(state.selected);
  } else if (state.activeTurn) {
    interruptActiveTurn();
  }
});

elements.input.addEventListener('keydown', event => {
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing && !usesTouchComposer()) {
    event.preventDefault();
    elements.composer.requestSubmit();
  }
});
elements.input.addEventListener('input', () => {
  savePromptDraft(state.selected);
  resizeComposer();
  updateComposerAction();
});

function resizeComposer() {
  elements.input.style.height = 'auto';
  elements.input.style.height = `${Math.min(elements.input.scrollHeight, 160)}px`;
}

async function openDialog() {
  try {
    await configReady;
  } catch (error) {
    showToast(error.message);
    return;
  }
  elements.dialogError.textContent = '';
  setCreationMode(state.creationMode);
  elements.dialog.showModal();
  loadOptions().catch(error => { elements.dialogError.textContent = error.message; });
  window.setTimeout(() => (state.creationMode === 'yaml' ? elements.yaml : elements.form.elements.name).focus(), 0);
}

document.querySelector('#new-session').addEventListener('click', openDialog);
document.querySelector('#welcome-new').addEventListener('click', openDialog);
document.querySelectorAll('.close-dialog').forEach(button => button.addEventListener('click', () => elements.dialog.close()));
elements.namespaceForm.addEventListener('submit', async event => {
  event.preventDefault();
  try {
    await switchNamespace(elements.activeNamespace.value);
  } catch (error) {
    showToast(error.message);
  }
});
elements.sessionSource.addEventListener('change', () => loadSessionSource(elements.sessionSource.value));
elements.credentialType.addEventListener('change', () => {
  const option = elements.credentialSecret.selectedOptions[0];
  if (option?.dataset.type && option.dataset.type !== elements.credentialType.value) {
    elements.credentialSecretCustom.value = option.dataset.name;
    elements.credentialSecret.value = customOption;
  }
  updateCredentialField();
});
elements.provider.addEventListener('change', renderCredentialOptions);
elements.credentialSecret.addEventListener('change', () => {
  const option = elements.credentialSecret.selectedOptions[0];
  if (option?.dataset.type) elements.credentialType.value = option.dataset.type;
  updateCredentialField();
});
elements.workspace.addEventListener('change', updateWorkspaceField);
elements.agentConfig.addEventListener('change', () => {
  elements.addAgentConfig.disabled = !elements.agentConfig.value;
});
elements.addAgentConfig.addEventListener('click', () => {
  const name = elements.agentConfig.value;
  if (!name || state.selectedAgentConfigs.includes(name)) return;
  state.selectedAgentConfigs.push(name);
  renderAgentConfigOptions();
});
elements.formMode.addEventListener('click', () => setCreationMode('form'));
elements.yamlMode.addEventListener('click', () => setCreationMode('yaml'));
elements.persistentVolume.addEventListener('change', updateVolumeClaimFields);
renderSessionSourceOptions();
renderCredentialOptions();
renderWorkspaceOptions();
renderAgentConfigOptions();
updateVolumeClaimFields();
setCreationMode('form');

elements.form.addEventListener('submit', async event => {
  event.preventDefault();
  if (state.sourceLoading || state.creatingSession) return;
  if (!elements.form.reportValidity()) return;
  elements.dialogError.textContent = '';
  setCreatingSession(true);
  try {
    let created;
    if (state.creationMode === 'yaml') {
      created = await api(`/api/sessions/apply?namespace=${encodeURIComponent(state.namespace)}`, {
        method: 'POST',
        headers: {'Content-Type': 'application/yaml'},
        body: elements.yaml.value,
      });
    } else {
      const values = new FormData(elements.form);
      const credentialType = values.get('credentialType');
      const worker = {
        type: values.get('provider'),
        credentials: {type: credentialType},
      };
      if (credentialType !== 'none') worker.credentials.secretRef = {name: selectedCredentialName()};
      const workspace = selectedWorkspaceName();
      if (workspace) worker.workspaceRef = {name: workspace};
      if (values.get('model').trim()) worker.model = values.get('model').trim();
      if (state.selectedAgentConfigs.length) {
        worker.agentConfigRefs = state.selectedAgentConfigs.map(name => ({name}));
      }
      const payload = {
        name: values.get('name').trim(),
        namespace: values.get('namespace').trim(),
        worker,
      };
      if (values.get('persistentVolume')) {
        payload.volumeClaimTemplate = {
          accessModes: [values.get('accessMode')],
          resources: {requests: {storage: values.get('storageRequest').trim()}},
        };
        const storageClassName = values.get('storageClassName').trim();
        if (storageClassName || state.sourceStorageClassNamePresent) {
          payload.volumeClaimTemplate.storageClassName = storageClassName;
        }
      }
      created = await api('/api/sessions', {
        method: 'POST',
        body: JSON.stringify(payload),
      });
    }
    elements.dialog.close();
    elements.form.reset();
    state.sourceGeneration += 1;
    setSourceLoading(false);
    state.selectedAgentConfigs = [];
    state.sourceStorageClassNamePresent = false;
    state.loadedSource = null;
    elements.formMode.disabled = false;
    elements.namespace.value = state.namespace;
    elements.yaml.value = '';
    elements.sessionSourceStatus.hidden = true;
    elements.sessionSourceStatus.textContent = '';
    updateVolumeClaimFields();
    setCreationMode('form');
    renderSessionSourceOptions();
    renderCredentialOptions();
    renderWorkspaceOptions();
    renderAgentConfigOptions();
    await Promise.all([loadSessions(), loadOptions()]);
    const selected = state.sessions.find(item => sessionKey(item) === sessionKey(created));
    selectSession(selected || created);
  } catch (error) {
    elements.dialogError.textContent = error.message;
  } finally {
    setCreatingSession(false);
  }
});

elements.deleteButton.addEventListener('click', async () => {
  const session = state.selected;
  if (!session || !window.confirm(`Delete Session ${session.namespace}/${session.name}? The live conversation will end.`)) return;
  try {
    await api(`/api/sessions/${encodeURIComponent(session.namespace)}/${encodeURIComponent(session.name)}`, {method: 'DELETE'});
    discardSessionView(session);
    selectSession(null);
    clearPromptDraft(session);
    await loadSessions();
    showToast('Session deleted');
  } catch (error) {
    showToast(error.message);
  }
});
elements.conversationTab.addEventListener('click', () => setActiveView('conversation'));
elements.changesTab.addEventListener('click', () => setActiveView('changes'));
elements.viewTabs.addEventListener('keydown', handleViewTabKeydown);

function interruptActiveTurn() {
  if (!state.socket || state.socket.readyState !== WebSocket.OPEN || !state.activeTurn || state.interrupting) return;
  state.interrupting = true;
  updateComposerAction();
  state.socket.send(JSON.stringify({type: 'interrupt'}));
}

document.querySelector('#refresh-sessions').addEventListener('click', () => loadSessions());
document.querySelector('#logout').addEventListener('click', async () => {
  await api('/api/logout', {method: 'POST'}).catch(() => {});
  window.location.replace('/login');
});
function setSidebarOpen(open) {
  elements.sidebar.classList.toggle('open', open);
  elements.openSidebar.setAttribute('aria-expanded', String(open));
}

elements.openSidebar.addEventListener('click', () => setSidebarOpen(true));
elements.closeSidebar.addEventListener('click', () => setSidebarOpen(false));
elements.sidebarScrim.addEventListener('click', () => setSidebarOpen(false));
document.addEventListener('keydown', event => {
  if (event.key === 'Escape' && elements.sidebar.classList.contains('open')) setSidebarOpen(false);
});

const configReady = loadConfig();
configReady.then(() => Promise.all([loadOptions(), loadSessions()])).then(() => {
  if (state.sessions.length) selectSession(state.sessions[0]);
}).catch(error => showToast(error.message));
window.setInterval(() => loadSessions({quiet: true}), 5000);
