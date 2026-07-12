const elements = {
  list: document.querySelector('#session-list'),
  title: document.querySelector('#session-title'),
  meta: document.querySelector('#session-meta'),
  messages: document.querySelector('#messages'),
  welcome: document.querySelector('#welcome'),
  composer: document.querySelector('#composer'),
  input: document.querySelector('#message-input'),
  send: document.querySelector('#send-message'),
  connection: document.querySelector('#connection-pill'),
  dialog: document.querySelector('#session-dialog'),
  form: document.querySelector('#session-form'),
  dialogError: document.querySelector('#dialog-error'),
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
  stopButton: document.querySelector('#stop-session'),
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
  activeTurn: false,
  interrupting: false,
  defaultNamespace: 'default',
  options: {credentials: [], workspaces: [], agentConfigs: []},
  selectedAgentConfigs: [],
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
    empty.textContent = 'No Sessions are visible yet.';
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
  try {
    const sessions = await api('/api/sessions');
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
    if (!quiet) showToast(error.message);
  }
}

async function loadConfig() {
  const config = await api('/api/config');
  state.defaultNamespace = config.defaultNamespace;
  elements.form.elements.namespace.value = state.defaultNamespace;
}

async function loadOptions() {
  state.options = await api('/api/options');
  renderCredentialOptions();
  renderWorkspaceOptions();
  renderAgentConfigOptions();
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
  state.activeTurn = false;
  state.interrupting = false;
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
  updateStopButton();
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
  elements.send.disabled = !enabled;
  elements.input.placeholder = enabled ? 'Message the agent…' : 'Choose a ready session to start chatting';
}

function updateStopButton() {
  const connected = state.socket && state.socket.readyState === WebSocket.OPEN;
  elements.stopButton.disabled = !connected || !state.activeTurn || state.interrupting;
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
  updateStopButton();
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
    updateStopButton();
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
    updateStopButton();
    state.reconnectTimer = window.setTimeout(connectSocket, state.reconnectDelay);
    state.reconnectDelay = Math.min(state.reconnectDelay * 1.8, 10000);
  });
  socket.addEventListener('error', () => socket.close());
}

function ensureConversation() {
  if (elements.messages.querySelector('.welcome')) elements.messages.replaceChildren();
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
      updateStopButton();
      break;
    case 'turn.interrupting':
      state.interrupting = true;
      updateStopButton();
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
  ensureConversation();
  const row = document.createElement('div');
  row.className = 'event-row user';
  const bubble = document.createElement('div');
  bubble.className = 'message-bubble';
  bubble.textContent = event.text;
  row.append(bubble);
  elements.messages.append(row);
  scrollToBottom();
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
  state.assistantSegmentByTurn.delete(turnID || 'current');
}

function renderAssistantDelta(event) {
  const bubble = assistantBubble(event.turnId);
  bubble.append(document.createTextNode(event.text || ''));
  scrollToBottom();
}

function renderAssistantMessage(event) {
  const bubble = assistantBubble(event.turnId);
  if (!bubble.textContent && event.text) bubble.textContent = event.text;
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
  const key = event.turnId || `diff-${event.id || 'current'}`;
  const existing = state.diffs.get(key);
  if (existing) {
    existing.textContent = event.diff;
    return;
  }
  ensureConversation();
  const details = document.createElement('details');
  details.className = 'diff-card';
  const summary = document.createElement('summary');
  summary.textContent = 'File changes';
  const pre = document.createElement('pre');
  pre.textContent = event.diff;
  details.append(summary, pre);
  elements.messages.append(details);
  state.diffs.set(key, pre);
  scrollToBottom();
}

function renderError(event) {
  if (event.status === 'rejected') {
    state.interrupting = false;
    updateStopButton();
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
  updateStopButton();
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
  if (!text || !state.socket || state.socket.readyState !== WebSocket.OPEN) return;
  state.socket.send(JSON.stringify({type: 'message', text}));
  elements.input.value = '';
  resizeComposer();
});

elements.input.addEventListener('keydown', event => {
  if (event.key === 'Enter' && !event.shiftKey) {
    event.preventDefault();
    elements.composer.requestSubmit();
  }
});
elements.input.addEventListener('input', resizeComposer);

function resizeComposer() {
  elements.input.style.height = 'auto';
  elements.input.style.height = `${Math.min(elements.input.scrollHeight, 160)}px`;
}

function openDialog() {
  elements.dialogError.textContent = '';
  elements.dialog.showModal();
  loadOptions().catch(error => { elements.dialogError.textContent = error.message; });
  window.setTimeout(() => elements.form.elements.name.focus(), 0);
}

document.querySelector('#new-session').addEventListener('click', openDialog);
document.querySelector('#welcome-new').addEventListener('click', openDialog);
document.querySelectorAll('.close-dialog').forEach(button => button.addEventListener('click', () => elements.dialog.close()));
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
renderCredentialOptions();
renderWorkspaceOptions();
renderAgentConfigOptions();

elements.form.addEventListener('submit', async event => {
  event.preventDefault();
  if (!elements.form.reportValidity()) return;
  elements.dialogError.textContent = '';
  const submit = document.querySelector('#create-session');
  submit.disabled = true;
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
  try {
    const created = await api('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({
        name: values.get('name').trim(),
        namespace: values.get('namespace').trim(),
        worker,
      }),
    });
    elements.dialog.close();
    elements.form.reset();
    state.selectedAgentConfigs = [];
    elements.form.elements.namespace.value = state.defaultNamespace;
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

elements.stopButton.addEventListener('click', () => {
  if (!state.socket || state.socket.readyState !== WebSocket.OPEN || !state.activeTurn) return;
  state.interrupting = true;
  updateStopButton();
  state.socket.send(JSON.stringify({type: 'interrupt'}));
});

document.querySelector('#refresh-sessions').addEventListener('click', () => loadSessions());
document.querySelector('#logout').addEventListener('click', async () => {
  await api('/api/logout', {method: 'POST'}).catch(() => {});
  window.location.replace('/login');
});
document.querySelector('#open-sidebar').addEventListener('click', () => elements.sidebar.classList.add('open'));
document.querySelector('#close-sidebar').addEventListener('click', () => elements.sidebar.classList.remove('open'));

Promise.all([loadConfig(), loadOptions()]).then(() => loadSessions()).then(() => {
  if (state.sessions.length) selectSession(state.sessions[0]);
}).catch(error => showToast(error.message));
window.setInterval(() => loadSessions({quiet: true}), 5000);
