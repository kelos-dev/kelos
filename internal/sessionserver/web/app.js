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
  toast: document.querySelector('#toast'),
};

const state = {
  sessions: [],
  selected: null,
  socket: null,
  socketGeneration: 0,
  reconnectTimer: null,
  reconnectDelay: 800,
  lastEventID: 0,
  assistantSegmentByTurn: new Map(),
  tools: new Map(),
  inputs: new Map(),
  diffs: new Map(),
  fileChanges: new Map(),
  queuedMessages: new Map(),
  activeTurn: false,
  interrupting: false,
  defaultNamespace: 'default',
  namespace: 'default',
  namespaceGeneration: 0,
  options: {credentials: [], workspaces: [], agentConfigs: []},
  selectedAgentConfigs: [],
  creationMode: 'form',
};

const customOption = '__custom__';

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

function providerLabel(provider) {
  return provider === 'claude-code' ? 'Claude Code' : provider === 'codex' ? 'Codex' : provider === 'opencode' ? 'OpenCode' : provider;
}

function providerInitials(provider) {
  return provider === 'claude-code' ? 'CC' : provider === 'codex' ? 'CX' : provider === 'opencode' ? 'OC' : 'AI';
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
    const button = document.createElement('button');
    button.className = `session-item${state.selected && sessionKey(state.selected) === sessionKey(session) ? ' active' : ''}`;
    button.type = 'button';
    const dot = document.createElement('span');
    dot.className = `phase-dot ${String(session.phase || '').toLowerCase()}`;
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
    meta.append(provider, namespace);
    text.append(name, meta);
    button.append(dot, text);
    button.addEventListener('click', () => selectSession(session));
    elements.list.append(button);
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
        const becameReady = state.selected.phase !== 'Ready' && current.phase === 'Ready';
        state.selected = current;
        renderHeader();
        if (becameReady) connectSocket();
      } else {
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
  renderCredentialOptions();
  renderWorkspaceOptions();
  renderAgentConfigOptions();
}

function resetNamespaceReferences() {
  state.selectedAgentConfigs = [];
  elements.credentialSecret.value = '';
  elements.credentialSecretCustom.value = '';
  elements.workspace.value = '';
  elements.workspaceCustom.value = '';
}

async function switchNamespace(namespace) {
  namespace = namespace.trim();
  if (!namespace || namespace === state.namespace) return;
  state.namespace = namespace;
  state.namespaceGeneration += 1;
  state.sessions = [];
  state.options = {credentials: [], workspaces: [], agentConfigs: []};
  window.localStorage.setItem('kelos-session-namespace', namespace);
  elements.activeNamespace.value = namespace;
  elements.namespace.value = namespace;
  resetNamespaceReferences();
  elements.yaml.value = '';
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

function selectSession(session) {
  closeSocket();
  state.selected = session;
  state.lastEventID = 0;
  state.assistantSegmentByTurn.clear();
  state.tools.clear();
  state.inputs.clear();
  state.diffs.clear();
  state.fileChanges.clear();
  state.queuedMessages.clear();
  elements.queue.replaceChildren();
  elements.queue.hidden = true;
  state.activeTurn = false;
  state.interrupting = false;
  setActiveView('conversation');
  renderFileChanges();
  elements.messages.replaceChildren();
  renderSessions();
  renderHeader();
  elements.sidebar.classList.remove('open');
  if (!session) {
    elements.messages.append(elements.welcome || createWelcome());
    return;
  }
  const loading = document.createElement('div');
  loading.className = 'welcome';
  const title = document.createElement('h1');
  title.textContent = session.phase === 'Ready' ? 'Opening conversation…' : 'Preparing the Session Pod…';
  const detail = document.createElement('p');
  detail.textContent = session.message || 'The controller is preparing the workspace and agent runtime.';
  loading.append(title, detail);
  elements.messages.append(loading);
  if (session.phase === 'Ready') connectSocket();
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
  elements.meta.textContent = `${session.namespace} · ${providerLabel(session.provider)} · ${session.phase || 'Pending'}`;
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

function updateComposerAction() {
  const connected = state.socket && state.socket.readyState === WebSocket.OPEN;
  const interrupt = state.activeTurn && !elements.input.value.trim();
  elements.send.dataset.action = interrupt ? 'interrupt' : 'send';
  elements.send.textContent = interrupt ? '■' : '↑';
  elements.send.setAttribute('aria-label', interrupt ? 'Interrupt active work' : 'Send message');
  elements.send.title = interrupt ? 'Interrupt active work' : 'Send message';
  elements.send.disabled = !connected || elements.input.disabled || (interrupt && state.interrupting);
  elements.composerHint.textContent = state.activeTurn
    ? 'Enter to queue · Shift+Enter for a new line'
    : 'Enter to send · Shift+Enter for a new line';
}

function closeSocket() {
  state.socketGeneration += 1;
  window.clearTimeout(state.reconnectTimer);
  state.reconnectTimer = null;
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
  closeSocket();
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
    socket.send(JSON.stringify({type: 'subscribe', since: state.lastEventID}));
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
  if (elements.messages.querySelector('.welcome')) elements.messages.replaceChildren();
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

function renderMessageText(element, text) {
  const value = text || '';
  const pattern = /https?:\/\/[^\s<>"']+/gi;
  const fragment = document.createDocumentFragment();
  let textEnd = 0;

  for (const match of value.matchAll(pattern)) {
    const linkText = trimURLSuffix(match[0]);
    try {
      new URL(linkText);
    } catch {
      continue;
    }

    fragment.append(document.createTextNode(value.slice(textEnd, match.index)));
    const link = document.createElement('a');
    link.href = linkText;
    link.target = '_blank';
    link.rel = 'noopener noreferrer';
    link.textContent = linkText;
    fragment.append(link);
    textEnd = match.index + linkText.length;
  }

  fragment.append(document.createTextNode(value.slice(textEnd)));
  element.replaceChildren(fragment);
}

function handleEvent(event) {
  if (event.id) state.lastEventID = Math.max(state.lastEventID, event.id);
  switch (event.type) {
    case 'history.end':
      scrollToBottom(false);
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
  renderMessageText(bubble, event.text);
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
  if (bubble) renderMessageText(bubble, bubble.textContent);
  state.assistantSegmentByTurn.delete(key);
}

function renderAssistantDelta(event) {
  const bubble = assistantBubble(event.turnId);
  bubble.append(document.createTextNode(event.text || ''));
  scrollToBottom();
}

function renderAssistantMessage(event) {
  const bubble = assistantBubble(event.turnId);
  if (!bubble.textContent && event.text) bubble.textContent = event.text;
  renderMessageText(bubble, bubble.textContent);
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
  renderDiffBlock(block, created);
  if (created) scrollToBottom();
}

function updateFileChanges(files) {
  for (const file of files) state.fileChanges.set(file.name, file.diff);
  renderFileChanges();
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
  elements.changesCount.textContent = String(count);
  elements.changesSummary.textContent = count === 1 ? '1 changed file' : `${count} changed files`;

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
  const distance = elements.messages.scrollHeight - elements.messages.scrollTop - elements.messages.clientHeight;
  if (distance < 240 || !smooth) {
    elements.messages.scrollTo({top: elements.messages.scrollHeight, behavior: smooth ? 'smooth' : 'auto'});
  }
}

elements.composer.addEventListener('submit', event => {
  event.preventDefault();
  const text = elements.input.value.trim();
  if (!state.socket || state.socket.readyState !== WebSocket.OPEN) return;
  if (text) {
    state.socket.send(JSON.stringify({type: 'message', text}));
    elements.input.value = '';
    resizeComposer();
    updateComposerAction();
  } else if (state.activeTurn) {
    interruptActiveTurn();
  }
});

elements.input.addEventListener('keydown', event => {
  if (event.key === 'Enter' && !event.shiftKey) {
    event.preventDefault();
    elements.composer.requestSubmit();
  }
});
elements.input.addEventListener('input', () => {
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
renderCredentialOptions();
renderWorkspaceOptions();
renderAgentConfigOptions();
updateVolumeClaimFields();
setCreationMode('form');

elements.form.addEventListener('submit', async event => {
  event.preventDefault();
  if (!elements.form.reportValidity()) return;
  elements.dialogError.textContent = '';
  const submit = elements.createButton;
  submit.disabled = true;
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
        if (storageClassName) payload.volumeClaimTemplate.storageClassName = storageClassName;
      }
      created = await api('/api/sessions', {
        method: 'POST',
        body: JSON.stringify(payload),
      });
    }
    elements.dialog.close();
    elements.form.reset();
    state.selectedAgentConfigs = [];
    elements.namespace.value = state.namespace;
    elements.yaml.value = '';
    updateVolumeClaimFields();
    setCreationMode('form');
    renderCredentialOptions();
    renderWorkspaceOptions();
    renderAgentConfigOptions();
    await Promise.all([loadSessions(), loadOptions()]);
    const selected = state.sessions.find(item => sessionKey(item) === sessionKey(created));
    selectSession(selected || created);
  } catch (error) {
    elements.dialogError.textContent = error.message;
  } finally {
    submit.disabled = false;
  }
});

elements.deleteButton.addEventListener('click', async () => {
  const session = state.selected;
  if (!session || !window.confirm(`Delete Session ${session.namespace}/${session.name}? The live conversation will end.`)) return;
  try {
    await api(`/api/sessions/${encodeURIComponent(session.namespace)}/${encodeURIComponent(session.name)}`, {method: 'DELETE'});
    selectSession(null);
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
document.querySelector('#open-sidebar').addEventListener('click', () => elements.sidebar.classList.add('open'));
document.querySelector('#close-sidebar').addEventListener('click', () => elements.sidebar.classList.remove('open'));

const configReady = loadConfig();
configReady.then(() => Promise.all([loadOptions(), loadSessions()])).then(() => {
  if (state.sessions.length) selectSession(state.sessions[0]);
}).catch(error => showToast(error.message));
window.setInterval(() => loadSessions({quiet: true}), 5000);
