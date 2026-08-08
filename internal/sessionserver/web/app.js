const elements = {
  list: document.querySelector('#session-list'),
  title: document.querySelector('#session-title'),
  meta: document.querySelector('#session-meta'),
  sectionButton: document.querySelector('#session-section'),
  sectionSelect: document.querySelector('#session-section-select'),
  sectionCustom: document.querySelector('#session-section-custom'),
  sectionDialog: document.querySelector('#section-dialog'),
  sectionForm: document.querySelector('#section-form'),
  sectionDialogDescription: document.querySelector('#section-dialog-description'),
  sectionChoice: document.querySelector('#section-choice'),
  sectionChoiceCustom: document.querySelector('#section-choice-custom'),
  sectionDialogError: document.querySelector('#section-dialog-error'),
  saveSectionButton: document.querySelector('#save-session-section'),
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
  runtimeStatus: document.querySelector('#session-runtime-status'),
  progress: document.querySelector('#session-progress'),
  progressLabel: document.querySelector('#session-progress-label'),
  progressElapsed: document.querySelector('#session-progress-elapsed'),
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
  resetButton: document.querySelector('#reset-session'),
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
  activeTurnID: '',
  activeTurnStartedAt: 0,
  waitingForInput: false,
  interrupting: false,
  runtimeStatus: null,
  progressTimer: null,
  replayingHistory: false,
  historyCursor: '',
  historyLastEventID: 0,
  historyPageLoading: false,
  historyPageReading: false,
  historyPageCursor: '',
  historyPageEvents: [],
  historyRequestID: '',
  runtimeRecoveryActive: false,
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
  sectionSaving: false,
  sectionAssignments: new Set(),
  sectionOrders: new Map(),
  sidebarDrag: null,
};

const customOption = '__custom__';
const maxCachedSessionViews = 5;
const sessionHistoryItemLimit = 20;
const sessionHistoryByteLimit = 128 * 1024;
const sectionOrderStoragePrefix = 'kelos-session-section-order:';

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
    activeTurnID: '',
    activeTurnStartedAt: 0,
    waitingForInput: false,
    interrupting: false,
    runtimeStatus: null,
    replayingHistory: false,
    historyCursor: '',
    runtimeRecoveryActive: false,
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
  view.activeTurnID = state.activeTurnID;
  view.activeTurnStartedAt = state.activeTurnStartedAt;
  view.waitingForInput = state.waitingForInput;
  view.interrupting = state.interrupting;
  view.runtimeStatus = state.runtimeStatus;
  view.replayingHistory = state.replayingHistory;
  view.historyCursor = state.historyCursor;
  view.runtimeRecoveryActive = state.runtimeRecoveryActive;
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
  state.activeTurnID = view.activeTurnID;
  state.activeTurnStartedAt = view.activeTurnStartedAt;
  state.waitingForInput = view.waitingForInput;
  state.interrupting = view.interrupting;
  state.runtimeStatus = view.runtimeStatus;
  state.replayingHistory = view.replayingHistory;
  state.historyCursor = view.historyCursor;
  state.historyLastEventID = 0;
  state.historyPageLoading = false;
  state.historyPageReading = false;
  state.historyPageCursor = '';
  state.historyPageEvents = [];
  state.historyRequestID = '';
  state.runtimeRecoveryActive = view.runtimeRecoveryActive;
  state.pinHistoryToBottom = view.pinHistoryToBottom;
  state.fileChangesDirty = view.fileChangesDirty;
  const hasChanges = view.changes.hasChildNodes();
  elements.messages.replaceChildren(view.messages);
  elements.queue.replaceChildren(view.queue);
  elements.changesList.replaceChildren(view.changes);
  elements.queue.hidden = state.queuedMessages.size === 0;
  updateFileChangesHeader();
  if (!hasChanges) renderFileChanges();
  refreshSessionProgress();
  renderRuntimeStatus();
  renderHistoryControl();
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
  state.activeTurnID = '';
  state.activeTurnStartedAt = 0;
  state.waitingForInput = false;
  state.interrupting = false;
  state.runtimeStatus = null;
  state.replayingHistory = true;
  state.historyCursor = '';
  state.historyLastEventID = 0;
  state.historyPageLoading = false;
  state.historyPageReading = false;
  state.historyPageCursor = '';
  state.historyPageEvents = [];
  state.historyRequestID = '';
  state.runtimeRecoveryActive = false;
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
    view.journalID = '';
    view.historyCursor = '';
    view.assistantSegmentByTurn = state.assistantSegmentByTurn;
    view.assistantTextByTurn = state.assistantTextByTurn;
    view.tools = state.tools;
    view.inputs = state.inputs;
    view.diffs = state.diffs;
    view.fileChanges = state.fileChanges;
    view.queuedMessages = state.queuedMessages;
    view.activeTurn = false;
    view.activeTurnID = '';
    view.activeTurnStartedAt = 0;
    view.waitingForInput = false;
    view.interrupting = false;
    view.runtimeStatus = null;
    view.runtimeRecoveryActive = false;
    view.pinHistoryToBottom = true;
  }
  refreshSessionProgress();
  renderRuntimeStatus();
}

function formatSessionProgressElapsed(elapsedMilliseconds) {
  const totalSeconds = Math.max(0, Math.floor(elapsedMilliseconds / 1000));
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const seconds = totalSeconds % 60;
  const totalMinutes = Math.floor(totalSeconds / 60);
  if (totalMinutes < 60) return `${totalMinutes}m ${String(seconds).padStart(2, '0')}s`;
  const minutes = totalMinutes % 60;
  const hours = Math.floor(totalMinutes / 60);
  return `${hours}h ${String(minutes).padStart(2, '0')}m ${String(seconds).padStart(2, '0')}s`;
}

function renderSessionProgress(now = Date.now()) {
  if (!state.activeTurn) {
    elements.progress.hidden = true;
    elements.progress.dataset.state = 'idle';
    elements.progressLabel.textContent = '';
    elements.progressElapsed.textContent = '';
    return;
  }
  let label = 'Working';
  let status = 'working';
  if (state.interrupting) {
    label = 'Interrupting';
    status = 'interrupting';
  } else if (state.waitingForInput) {
    label = 'Waiting for input';
    status = 'waiting';
  }
  const startedAt = state.activeTurnStartedAt || now;
  elements.progress.hidden = false;
  elements.progress.dataset.state = status;
  if (elements.progressLabel.textContent !== label) elements.progressLabel.textContent = label;
  elements.progressElapsed.textContent = `(${formatSessionProgressElapsed(now - startedAt)})`;
}

function refreshSessionProgress() {
  if (state.progressTimer !== null) {
    window.clearInterval(state.progressTimer);
    state.progressTimer = null;
  }
  renderSessionProgress();
  if (state.activeTurn) {
    state.progressTimer = window.setInterval(() => renderSessionProgress(), 1000);
  }
}

function sessionRuntimePath(workingDir = '', homeDir = '') {
  const home = homeDir.replace(/\/$/, '');
  if (!home) return workingDir;
  if (workingDir === home) return '~';
  if (workingDir.startsWith(`${home}/`)) return `~/${workingDir.slice(home.length + 1)}`;
  return workingDir;
}

function sessionRuntimeContextUsedPercent(usage) {
  const baselineTokens = 12000;
  if (usage.contextWindow <= baselineTokens) return 100;
  const effectiveWindow = usage.contextWindow - baselineTokens;
  const used = Math.max(0, (Number(usage.contextTokens) || 0) - baselineTokens);
  return Math.min(100, Math.round(used * 100 / effectiveWindow));
}

function formatSessionRuntimeTokens(value) {
  const tokens = Math.max(0, Number(value) || 0);
  if (tokens < 1000) return String(tokens);
  let scaled = tokens / 1000;
  let suffix = 'K';
  if (tokens >= 1e12) {
    scaled = tokens / 1e12;
    suffix = 'T';
  } else if (tokens >= 1e9) {
    scaled = tokens / 1e9;
    suffix = 'B';
  } else if (tokens >= 1e6) {
    scaled = tokens / 1e6;
    suffix = 'M';
  }
  const decimals = scaled < 10 ? 2 : scaled < 100 ? 1 : 0;
  return `${Number(scaled.toFixed(decimals))}${suffix}`;
}

function sessionRuntimeStatusText(status) {
  if (!status) return '';
  const parts = [];
  const add = value => { if (value) parts.push(value); };
  add(status.sessionName);
  add(status.agentType);
  add(`${status.model || ''} ${status.effort || ''}`.trim());
  add(sessionRuntimePath(status.workingDir, status.homeDir));
  add(status.branch);
  if (status.pullRequestNumber > 0) add(`PR #${status.pullRequestNumber}`);
  if (status.usage?.contextWindow > 0) {
    add(`Context ${sessionRuntimeContextUsedPercent(status.usage)}% used`);
  }
  if (status.weeklyLimit) {
    const remaining = 100 - Math.min(100, Math.max(0, status.weeklyLimit.usedPercent));
    add(`weekly ${remaining}% left`);
  }
  if (status.usage) {
    add(`${formatSessionRuntimeTokens(status.usage.inputTokens)} in`);
    add(`${formatSessionRuntimeTokens(status.usage.outputTokens)} out`);
  }
  return parts.join(' · ');
}

function renderRuntimeStatus() {
  const text = sessionRuntimeStatusText(state.runtimeStatus);
  elements.runtimeStatus.textContent = text;
  elements.runtimeStatus.title = text;
  elements.runtimeStatus.hidden = !text;
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
  if (session.resetting) return 'Resetting';
  if (session.phase !== 'Ready') return session.phase || 'Pending';
  if (session.waitingForInput) return 'Waiting for input';
  if (session.active === true) return 'Active';
  if (session.active === false) return 'Idle';
  return session.phase;
}

function parseSessionTimestamp(value) {
  const milliseconds = Date.parse(value || '');
  return Number.isNaN(milliseconds) ? null : new Date(milliseconds);
}

function sessionTimestamp(session) {
  const lastActivity = parseSessionTimestamp(session.lastActivityAt);
  if (lastActivity) return {date: lastActivity, activity: true};
  const created = parseSessionTimestamp(session.createdAt);
  return created ? {date: created, activity: false} : null;
}

function formatSessionRecency(session, compact = false, now = Date.now()) {
  const timestamp = sessionTimestamp(session);
  if (!timestamp) return '';
  const elapsed = Math.max(0, now - timestamp.date.getTime());
  const minutes = Math.floor(elapsed / 60000);
  let value;
  if (minutes < 1) {
    value = compact ? 'Now' : 'just now';
  } else if (minutes < 60) {
    value = compact ? `${minutes}m` : `${minutes} minute${minutes === 1 ? '' : 's'} ago`;
  } else {
    const hours = Math.floor(minutes / 60);
    if (hours < 24) {
      value = compact ? `${hours}h` : `${hours} hour${hours === 1 ? '' : 's'} ago`;
    } else {
      const days = Math.floor(hours / 24);
      if (days < 7) {
        value = compact ? `${days}d` : `${days} day${days === 1 ? '' : 's'} ago`;
      } else {
        const currentYear = new Date(now).getFullYear();
        value = new Intl.DateTimeFormat(undefined, {
          month: 'short',
          day: 'numeric',
          ...(timestamp.date.getFullYear() === currentYear ? {} : {year: 'numeric'}),
        }).format(timestamp.date);
      }
    }
  }
  if (compact) return value;
  return `${timestamp.activity ? 'Last active' : 'Created'} ${value}`;
}

function createSessionTimestamp(session, compact, className) {
  const timestamp = sessionTimestamp(session);
  const label = formatSessionRecency(session, compact);
  if (!timestamp || !label) return null;
  const element = document.createElement('time');
  element.className = className;
  element.dateTime = timestamp.date.toISOString();
  element.textContent = label;
  const exact = new Intl.DateTimeFormat(undefined, {dateStyle: 'medium', timeStyle: 'long'}).format(timestamp.date);
  const exactLabel = `${timestamp.activity ? 'Last active' : 'Created'} ${exact}`;
  element.title = exactLabel;
  element.setAttribute('aria-label', exactLabel);
  return element;
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

  const sections = orderedSessionSections();
  if (!sections.length) {
    for (const session of state.sessions) elements.list.append(createSessionListItem(session));
    return;
  }

  const sessionsBySection = new Map();
  for (const session of state.sessions) {
    const section = session.section || '';
    if (!sessionsBySection.has(section)) sessionsBySection.set(section, []);
    sessionsBySection.get(section).push(session);
  }
  for (const [sectionIndex, section] of sections.entries()) {
    const sectionSessions = sessionsBySection.get(section) || [];
    const group = document.createElement('section');
    group.className = 'session-section-group';
    const heading = document.createElement('h2');
    heading.className = 'session-section-heading';
    const title = document.createElement('span');
    title.className = 'session-section-title';
    title.draggable = true;
    title.title = 'Drag to reorder section';
    const dragHandle = document.createElement('span');
    dragHandle.className = 'session-section-drag-handle';
    dragHandle.setAttribute('aria-hidden', 'true');
    dragHandle.textContent = '⠿';
    const name = document.createElement('span');
    name.textContent = section || 'Unsectioned';
    title.append(dragHandle, name);
    const tools = document.createElement('span');
    tools.className = 'session-section-tools';
    const count = document.createElement('span');
    count.className = 'session-section-count';
    count.textContent = String(sectionSessions.length);
    tools.append(count);
    for (const [direction, symbol, action] of [[-1, '↑', 'up'], [1, '↓', 'down']]) {
      const button = document.createElement('button');
      button.className = 'session-section-order-button';
      button.type = 'button';
      button.textContent = symbol;
      button.title = `Move ${name.textContent} section ${action}`;
      button.setAttribute('aria-label', button.title);
      button.dataset.section = section;
      button.dataset.direction = String(direction);
      button.disabled = direction < 0 ? sectionIndex === 0 : sectionIndex === sections.length - 1;
      button.addEventListener('click', () => moveSectionByOffset(section, direction));
      tools.append(button);
    }
    heading.append(title, tools);
    group.append(heading);
    for (const session of sectionSessions) {
      group.append(createSessionListItem(session, true));
    }
    configureSectionDrag(group, heading, title, section);
    elements.list.append(group);
  }
}

function createSessionListItem(session, draggable = false) {
  const item = document.createElement('div');
  const key = sessionKey(session);
  const assigningSection = state.sectionAssignments.has(key);
  item.className = `session-item${state.selected && sessionKey(state.selected) === key ? ' active' : ''}${assigningSection ? ' section-saving' : ''}`;
  item.draggable = draggable && !assigningSection;
  const button = document.createElement('button');
  button.className = 'session-item-select';
  button.type = 'button';
  const dot = document.createElement('span');
  const displayStatus = sessionDisplayStatus(session);
  dot.className = `phase-dot ${String(displayStatus).toLowerCase().replaceAll(' ', '-')}`;
  const text = document.createElement('span');
  const titleRow = document.createElement('div');
  titleRow.className = 'session-item-title-row';
  const name = document.createElement('div');
  name.className = 'session-item-name';
  name.textContent = session.name;
  titleRow.append(name);
  const timestamp = createSessionTimestamp(session, true, 'session-item-time');
  if (timestamp) titleRow.append(timestamp);
  const meta = document.createElement('div');
  meta.className = 'session-item-meta';
  const provider = document.createElement('span');
  provider.className = 'provider-badge';
  provider.textContent = providerLabel(session.provider);
  const namespace = document.createElement('span');
  namespace.textContent = `· ${session.namespace}`;
  const activity = document.createElement('span');
  activity.textContent = `· ${displayStatus}`;
  meta.append(provider);
  if (session.model) {
    const model = document.createElement('span');
    model.className = 'session-model';
    model.textContent = `· ${session.model}`;
    model.title = session.model;
    meta.append(model);
  }
  meta.append(namespace, activity);
  text.append(titleRow, meta);
  if (session.branch) {
    const branch = document.createElement('div');
    branch.className = 'session-item-branch';
    branch.textContent = session.branch;
    branch.title = session.branch;
    text.append(branch);
  }
  button.append(dot, text);
  button.addEventListener('click', () => selectSession(session, true));
  item.append(button);
  const link = createPullRequestLink(session.pullRequest, 'session-item-pull-request');
  if (link) {
    item.classList.add('has-pull-request');
    link.draggable = false;
    item.append(link);
  }
  if (item.draggable) configureSessionDrag(item, session);
  return item;
}

function sectionLabel(section) {
  return section || 'Unsectioned';
}

function sectionOrderStorageKey(namespace) {
  return `${sectionOrderStoragePrefix}${namespace}`;
}

function uniqueSectionOrder(value) {
  if (!Array.isArray(value)) return [];
  const seen = new Set();
  return value.filter(section => {
    if (typeof section !== 'string' || seen.has(section)) return false;
    seen.add(section);
    return true;
  });
}

function savedSectionOrder(namespace = state.namespace) {
  if (state.sectionOrders.has(namespace)) return state.sectionOrders.get(namespace);
  let order = [];
  try {
    order = uniqueSectionOrder(JSON.parse(window.localStorage.getItem(sectionOrderStorageKey(namespace)) || '[]'));
  } catch (_) {
    order = [];
  }
  state.sectionOrders.set(namespace, order);
  return order;
}

function storeSectionOrder(order, namespace = state.namespace) {
  const normalized = uniqueSectionOrder(order);
  state.sectionOrders.set(namespace, normalized);
  try {
    window.localStorage.setItem(sectionOrderStorageKey(namespace), JSON.stringify(normalized));
    return true;
  } catch (_) {
    return false;
  }
}

function orderedSessionSections() {
  const available = Array.from(new Set(state.sessions.map(session => session.section || '')));
  if (!available.some(Boolean)) return [];
  if (!available.includes('')) available.push('');
  const availableSet = new Set(available);
  const ordered = savedSectionOrder().filter(section => availableSet.delete(section));
  const remaining = Array.from(availableSet).sort((left, right) => {
    if (!left) return 1;
    if (!right) return -1;
    return left.localeCompare(right, undefined, {sensitivity: 'base'});
  });
  return ordered.concat(remaining);
}

function reorderSections(sections, section, target, after) {
  if (section === target || !sections.includes(section) || !sections.includes(target)) return sections;
  const reordered = sections.filter(item => item !== section);
  let targetIndex = reordered.indexOf(target);
  if (after) targetIndex += 1;
  reordered.splice(targetIndex, 0, section);
  return reordered;
}

function focusSectionOrderControl(section, direction) {
  const controls = Array.from(elements.list.querySelectorAll('.session-section-order-button'))
    .filter(button => button.dataset.section === section);
  const preferred = controls.find(button => Number(button.dataset.direction) === direction);
  const target = preferred && !preferred.disabled ? preferred : controls.find(button => !button.disabled);
  if (target) target.focus();
}

function applySectionOrder(order, section, focusDirection = 0) {
  const persisted = storeSectionOrder(order);
  renderSessions();
  if (focusDirection) focusSectionOrderControl(section, focusDirection);
  showToast(persisted
    ? `Moved ${sectionLabel(section)} section`
    : `Moved ${sectionLabel(section)} section, but browser storage is unavailable`);
}

function moveSectionByOffset(section, offset) {
  const sections = orderedSessionSections();
  const index = sections.indexOf(section);
  const targetIndex = index + offset;
  if (index < 0 || targetIndex < 0 || targetIndex >= sections.length) return;
  const reordered = [...sections];
  [reordered[index], reordered[targetIndex]] = [reordered[targetIndex], reordered[index]];
  applySectionOrder(reordered, section, offset);
}

function clearSidebarDropIndicators() {
  for (const group of elements.list.querySelectorAll('.session-section-group')) {
    group.classList.remove('session-drop-target', 'section-drop-before', 'section-drop-after');
  }
}

function finishSidebarDrag() {
  state.sidebarDrag = null;
  clearSidebarDropIndicators();
}

function configureSessionDrag(item, session) {
  item.addEventListener('dragstart', event => {
    state.sidebarDrag = {kind: 'session', session};
    item.classList.add('dragging');
    event.dataTransfer.effectAllowed = 'move';
    event.dataTransfer.setData('text/plain', sessionKey(session));
  });
  item.addEventListener('dragend', () => {
    item.classList.remove('dragging');
    finishSidebarDrag();
  });
}

function configureSectionDrag(group, heading, title, section) {
  title.addEventListener('dragstart', event => {
    state.sidebarDrag = {kind: 'section', section};
    group.classList.add('dragging');
    event.dataTransfer.effectAllowed = 'move';
    event.dataTransfer.setData('text/plain', sectionLabel(section));
  });
  title.addEventListener('dragend', () => {
    group.classList.remove('dragging');
    finishSidebarDrag();
  });
  group.addEventListener('dragover', event => {
    const drag = state.sidebarDrag;
    if (!drag) return;
    if (drag.kind === 'session') {
      if ((drag.session.section || '') === section) return;
      event.preventDefault();
      clearSidebarDropIndicators();
      group.classList.add('session-drop-target');
    } else {
      if (drag.section === section) return;
      event.preventDefault();
      clearSidebarDropIndicators();
      const bounds = heading.getBoundingClientRect();
      group.classList.add(event.clientY >= bounds.top + bounds.height / 2 ? 'section-drop-after' : 'section-drop-before');
    }
    event.dataTransfer.dropEffect = 'move';
  });
  group.addEventListener('drop', event => {
    const drag = state.sidebarDrag;
    if (!drag) return;
    if (drag.kind === 'session') {
      if ((drag.session.section || '') === section) return;
      event.preventDefault();
      finishSidebarDrag();
      void moveSessionToSection(drag.session, section);
      return;
    }
    if (drag.section === section) return;
    event.preventDefault();
    const bounds = heading.getBoundingClientRect();
    const reordered = reorderSections(
      orderedSessionSections(),
      drag.section,
      section,
      event.clientY >= bounds.top + bounds.height / 2,
    );
    finishSidebarDrag();
    applySectionOrder(reordered, drag.section);
  });
}

function sessionSectionNames() {
  return Array.from(new Set(state.sessions.map(session => session.section).filter(Boolean)))
    .sort((left, right) => left.localeCompare(right, undefined, {sensitivity: 'base'}));
}

function createsNewSection(select) {
  return select.selectedOptions[0]?.dataset.createSection === 'true';
}

function populateSectionSelect(select, selected, emptyLabel, createNew = false) {
  const sections = sessionSectionNames();
  select.replaceChildren();
  const actions = document.createElement('optgroup');
  actions.label = 'Actions';
  addOption(actions, '', emptyLabel);
  const createOption = addOption(actions, '', '＋ Create new section…');
  createOption.dataset.createSection = 'true';
  select.append(actions);
  if (sections.length) {
    const existing = document.createElement('optgroup');
    existing.label = 'Existing sections';
    for (const section of sections) addOption(existing, section, section);
    select.append(existing);
  }
  select.selectedIndex = 0;
  if (createNew) createOption.selected = true;
  else if (sections.includes(selected)) select.value = selected;
}

function updateCustomSectionField(select, input) {
  const custom = createsNewSection(select);
  input.hidden = !custom;
  input.required = custom;
  if (!custom) input.value = '';
  validateCustomSectionField(select, input);
}

function validateCustomSectionField(select, input) {
  const empty = createsNewSection(select) && !input.value.trim();
  input.setCustomValidity(empty ? 'Enter a section name' : '');
}

function selectedSection(select, input) {
  return createsNewSection(select) ? input.value.trim() : select.value;
}

function selectedSectionPayload(select, input, includeEmpty = false) {
  const section = selectedSection(select, input);
  return section || includeEmpty ? {section} : {};
}

async function saveSessionSectionAssignment(session, section) {
  const generation = state.namespaceGeneration;
  const updated = await api(
    `/api/sessions/${encodeURIComponent(session.namespace)}/${encodeURIComponent(session.name)}/section`,
    {method: 'PATCH', body: JSON.stringify({section})},
  );
  if (generation !== state.namespaceGeneration) return updated;
  state.sessions = state.sessions.map(item => sessionKey(item) === sessionKey(updated) ? updated : item);
  if (state.selected && sessionKey(state.selected) === sessionKey(updated)) state.selected = updated;
  renderSectionOptions();
  renderSessions();
  renderHeader();
  return updated;
}

async function moveSessionToSection(session, section) {
  const key = sessionKey(session);
  if ((session.section || '') === section || state.sectionAssignments.has(key)) return;
  const generation = state.namespaceGeneration;
  state.sectionAssignments.add(key);
  renderSessions();
  try {
    await saveSessionSectionAssignment(session, section);
    if (generation === state.namespaceGeneration) {
      showToast(section ? `Moved Session to ${section}` : 'Moved Session to Unsectioned');
    }
  } catch (error) {
    if (generation === state.namespaceGeneration) showToast(error.message);
  } finally {
    state.sectionAssignments.delete(key);
    if (generation === state.namespaceGeneration) renderSessions();
  }
}

function renderSectionOptions() {
  const selected = elements.sectionSelect.value;
  const createNew = createsNewSection(elements.sectionSelect);
  populateSectionSelect(elements.sectionSelect, selected, 'No section (leave unsectioned)', createNew);
  updateCustomSectionField(elements.sectionSelect, elements.sectionCustom);
}

function resetSectionSelection() {
  elements.sectionSelect.selectedIndex = 0;
  elements.sectionCustom.value = '';
}

async function loadSessions({quiet = false} = {}) {
  const namespace = state.namespace;
  const generation = state.namespaceGeneration;
  try {
    const sessions = await api(`/api/sessions?namespace=${encodeURIComponent(namespace)}`);
    if (generation !== state.namespaceGeneration) return;
    state.sessions = sessions;
    renderSectionOptions();
    if (state.selected) {
      const current = sessions.find(item => sessionKey(item) === sessionKey(state.selected));
      if (current) {
        if (state.selected.uid && current.uid && state.selected.uid !== current.uid) {
          discardSessionView(state.selected);
          selectSession(current);
        } else {
          const beganReset = !state.selected.resetting && current.resetting;
          const becameReady = (state.selected.phase !== 'Ready' || state.selected.resetting) && current.phase === 'Ready' && !current.resetting;
          state.selected = current;
          if (beganReset) {
            closeSocket();
            resetCurrentSessionView();
          }
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
  resetSectionSelection();
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
  renderSectionOptions();
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
  const allowedSpecFields = new Set(['worker', 'suspend', 'initialBranch', 'initialPrompt', 'volumeClaimTemplate']);
  if (Object.keys(manifest.spec).some(key => !allowedSpecFields.has(key))) return false;
  if (manifest.spec.suspend === true) return false;
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
  elements.form.elements.initialBranch.value = manifest.spec.initialBranch || '';
  elements.form.elements.initialPrompt.value = manifest.spec.initialPrompt || '';
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
  renderSectionOptions();
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

function selectSession(session, resumeIdle = false) {
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
  if (resumeIdle && session.idleSuspended) resumeIdleSession(session);
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
  title.textContent = session.resetting ? 'Resetting the Session…' : session.phase === 'Ready' ? 'Opening conversation…' : 'Preparing the Session Pod…';
  const detail = document.createElement('p');
  detail.textContent = session.message || 'The controller is preparing the workspace and agent runtime.';
  loading.append(title, detail);
  elements.messages.append(loading);
  view.statusPlaceholder = true;
  if (session.phase === 'Ready' && !session.resetting) connectSocket();
  scheduleBottomAnchor();
}

async function resumeIdleSession(session) {
  try {
    await api(`/api/sessions/${encodeURIComponent(session.namespace)}/${encodeURIComponent(session.name)}/resume`, {method: 'POST'});
    await loadSessions({quiet: true});
  } catch (error) {
    showToast(error.message);
  }
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
  elements.sectionButton.hidden = !session;
  elements.sectionButton.disabled = !session;
  elements.resetButton.disabled = !session || session.resetting;
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
  elements.sectionButton.textContent = session.section ? `Section: ${session.section}` : '＋ Choose section';
  elements.sectionButton.title = session.section ? 'Move Session to another section' : 'Move Session to a section';
  const details = [session.namespace, providerLabel(session.provider)];
  if (session.model) details.push(session.model);
  details.push(sessionDisplayStatus(session));
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
  const timestamp = createSessionTimestamp(session, false, 'session-meta-time');
  if (timestamp) {
    const separator = document.createElement('span');
    separator.className = 'session-meta-separator';
    separator.textContent = '·';
    elements.meta.append(separator, timestamp);
  }
  if (session.resetting) {
    setConnection('connecting', 'Resetting');
    setComposer(false);
  } else if (session.phase !== 'Ready') {
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

function composerInterruptAction() {
  return state.activeTurn && (elements.input.disabled || !elements.input.value.trim());
}

function updateComposerAction() {
  const connected = state.socket && state.socket.readyState === WebSocket.OPEN;
  const interrupt = composerInterruptAction();
  const action = interrupt ? 'interrupt' : (state.activeTurn ? 'queue' : 'send');
  const actionSymbol = interrupt ? '■' : '↑';
  elements.send.dataset.action = interrupt ? 'interrupt' : 'send';
  elements.send.textContent = actionSymbol;
  elements.send.setAttribute('aria-label', interrupt ? 'Interrupt active work' : 'Send message');
  elements.send.title = interrupt ? 'Interrupt active work' : 'Send message';
  elements.send.disabled = !connected || (interrupt ? state.interrupting : elements.input.disabled);
  elements.composerHint.textContent = usesTouchComposer()
    ? `Tap ${actionSymbol} to ${action} · Return for a new line`
    : (interrupt && elements.input.disabled
      ? `Click ${actionSymbol} to interrupt`
      : `Enter to ${action} · Shift+Enter for a new line`);
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
  cancelOlderHistoryPage();
  setComposer(false);
  updateComposerAction();
}

function connectSocket() {
  if (!state.selected || state.selected.phase !== 'Ready' || state.selected.resetting) return;
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
    const historyLoaded = Boolean(state.currentView?.historyLoaded);
    socket.send(JSON.stringify({
      type: 'subscribe',
      since: historyLoaded ? state.lastEventID : 0,
      journalId: historyLoaded ? state.currentView?.journalID || '' : '',
      historyBounds: true,
      historyItems: sessionHistoryItemLimit,
      historyBytes: sessionHistoryByteLimit,
    }));
    setConnection('connected', 'Connected');
    setComposer(true);
    updateComposerAction();
    renderHistoryControl();
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
    cancelOlderHistoryPage();
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

async function writeClipboardText(text) {
  if (globalThis.navigator?.clipboard?.writeText) {
    try {
      await globalThis.navigator.clipboard.writeText(text);
      return;
    } catch {
      // Fall through for browsers that expose the Clipboard API but deny access.
    }
  }

  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.setAttribute('readonly', '');
  textarea.style.position = 'fixed';
  textarea.style.opacity = '0';
  textarea.style.pointerEvents = 'none';
  document.body.append(textarea);
  textarea.select();
  let copied = false;
  try {
    copied = document.execCommand('copy');
  } finally {
    textarea.remove();
  }
  if (!copied) throw new Error('Clipboard access is unavailable');
}

async function copyCodeBlock(button, content) {
  button.disabled = true;
  try {
    await writeClipboardText(content);
    button.textContent = 'Copied';
    button.setAttribute('aria-label', 'Code copied');
  } catch {
    button.textContent = 'Copy failed';
    button.setAttribute('aria-label', 'Copy code failed');
  } finally {
    button.disabled = false;
    window.clearTimeout(button.copyResetTimer);
    button.copyResetTimer = window.setTimeout(() => {
      button.textContent = 'Copy';
      button.setAttribute('aria-label', 'Copy code block');
    }, 1600);
  }
}

function appendCodeBlock(parent, content, info) {
  const block = document.createElement('div');
  block.className = 'code-block';
  const toolbar = document.createElement('div');
  toolbar.className = 'code-block-toolbar';
  const languageLabel = document.createElement('span');
  languageLabel.className = 'code-block-language';
  const copyButton = document.createElement('button');
  copyButton.className = 'code-copy-button';
  copyButton.type = 'button';
  copyButton.textContent = 'Copy';
  copyButton.setAttribute('aria-label', 'Copy code block');
  copyButton.setAttribute('aria-live', 'polite');
  copyButton.addEventListener('click', () => copyCodeBlock(copyButton, content));

  const pre = document.createElement('pre');
  const code = document.createElement('code');
  const language = info.trim().split(/\s+/, 1)[0];
  if (/^[a-z0-9_+-]+$/i.test(language)) {
    pre.dataset.language = language;
    code.className = `language-${language.toLowerCase()}`;
  }
  languageLabel.textContent = language || 'Code';
  code.textContent = content;
  pre.append(code);
  toolbar.append(languageLabel, copyButton);
  block.append(toolbar, pre);
  parent.append(block);
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

function sessionHistoryRequestID() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
  return `history-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function renderHistoryControl() {
  let control = elements.messages.querySelector('.history-page-control');
  if (!state.historyCursor && !state.historyPageLoading) {
    if (control) control.remove();
    return;
  }
  if (!control) {
    control = document.createElement('div');
    control.className = 'history-page-control';
    const button = document.createElement('button');
    button.type = 'button';
    button.addEventListener('click', requestOlderHistory);
    control.append(button);
  }
  const button = control.querySelector('button');
  button.disabled = state.historyPageLoading || !state.socket || state.socket.readyState !== 1;
  button.textContent = state.historyPageLoading ? 'Loading earlier messages…' : 'Load earlier messages';
  elements.messages.prepend(control);
}

function cancelOlderHistoryPage() {
  state.historyPageLoading = false;
  state.historyPageReading = false;
  state.historyPageCursor = '';
  state.historyPageEvents = [];
  state.historyRequestID = '';
  renderHistoryControl();
}

function requestOlderHistory() {
  if (state.historyPageLoading || !state.historyCursor || !state.socket || state.socket.readyState !== 1) return;
  const requestID = sessionHistoryRequestID();
  state.historyPageLoading = true;
  state.historyRequestID = requestID;
  renderHistoryControl();
  try {
    state.socket.send(JSON.stringify({type: 'history', requestId: requestID, historyCursor: state.historyCursor}));
  } catch (error) {
    cancelOlderHistoryPage();
    showToast(`Could not load earlier messages: ${error.message}`);
  }
}

function applyHistoryState(historyState) {
  if (!historyState) return;
  const activeTurnID = historyState.activeTurnId || '';
  if (activeTurnID) {
    const projectedBubble = state.assistantSegmentByTurn.get('current');
    const projectedText = state.assistantTextByTurn.get('current');
    if (projectedBubble) state.assistantSegmentByTurn.set(activeTurnID, projectedBubble);
    if (projectedText !== undefined) state.assistantTextByTurn.set(activeTurnID, projectedText);
    state.assistantSegmentByTurn.delete('current');
    state.assistantTextByTurn.delete('current');
  }
  state.activeTurn = Boolean(activeTurnID);
  state.activeTurnID = activeTurnID;
  const startedAt = Date.parse(historyState.activeTurnStarted || '');
  state.activeTurnStartedAt = Number.isNaN(startedAt) ? 0 : startedAt;
  state.waitingForInput = Boolean(historyState.waitingForInput);
  state.interrupting = Boolean(historyState.turnInterrupting);
  if (historyState.fileDiff) updateFileChanges(parseFileDiffs(historyState.fileDiff));
  elements.queue.replaceChildren();
  state.queuedMessages = new Map();
  for (const turn of historyState.queuedTurns || []) {
    renderQueuedUser({type: 'user.message', turnId: turn.turnId, text: turn.text});
  }
  refreshSessionProgress();
  updateComposerAction();
}

function flushDeferredHistoryRendering() {
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
}

function replayOlderHistoryPage(events) {
  const messages = elements.messages;
  const previousHeight = Number(messages.scrollHeight) || 0;
  const previousTop = Number(messages.scrollTop) || 0;
  const page = document.createElement('div');
  const live = {
    activeTurn: state.activeTurn,
    activeTurnID: state.activeTurnID,
    activeTurnStartedAt: state.activeTurnStartedAt,
    waitingForInput: state.waitingForInput,
    interrupting: state.interrupting,
    runtimeRecoveryActive: state.runtimeRecoveryActive,
    replayingHistory: state.replayingHistory,
    pinHistoryToBottom: state.pinHistoryToBottom,
    lastEventID: state.lastEventID,
    assistantSegmentByTurn: state.assistantSegmentByTurn,
    assistantTextByTurn: state.assistantTextByTurn,
    fileChanges: new Map(state.fileChanges),
  };

  elements.messages = page;
  state.assistantSegmentByTurn = new Map();
  state.assistantTextByTurn = new Map();
  state.replayingHistory = true;
  state.pinHistoryToBottom = false;
  for (const event of events) handleEvent(event);
  for (const [name, diff] of live.fileChanges) state.fileChanges.set(name, diff);
  flushDeferredHistoryRendering();
  const rendered = moveChildren(page);

  elements.messages = messages;
  state.activeTurn = live.activeTurn;
  state.activeTurnID = live.activeTurnID;
  state.activeTurnStartedAt = live.activeTurnStartedAt;
  state.waitingForInput = live.waitingForInput;
  state.interrupting = live.interrupting;
  state.runtimeRecoveryActive = live.runtimeRecoveryActive;
  state.replayingHistory = live.replayingHistory;
  state.pinHistoryToBottom = live.pinHistoryToBottom;
  state.lastEventID = live.lastEventID;
  state.assistantSegmentByTurn = live.assistantSegmentByTurn;
  state.assistantTextByTurn = live.assistantTextByTurn;
  messages.prepend(rendered);
  renderHistoryControl();
  const scrollBehavior = messages.style.scrollBehavior;
  messages.style.scrollBehavior = 'auto';
  messages.scrollTop = Math.max(0, previousTop + (Number(messages.scrollHeight) || 0) - previousHeight);
  messages.style.scrollBehavior = scrollBehavior;
  refreshSessionProgress();
  updateComposerAction();
}

function finishOlderHistoryPage(event) {
  if (event.requestId !== state.historyRequestID) return;
  const events = state.historyPageEvents;
  state.historyCursor = state.historyPageCursor;
  state.historyPageLoading = false;
  state.historyPageReading = false;
  state.historyPageCursor = '';
  state.historyPageEvents = [];
  state.historyRequestID = '';
  if (state.currentView) state.currentView.historyCursor = state.historyCursor;
  replayOlderHistoryPage(events);
}

function finishHistoryReplay(historyState) {
  const pinToBottom = state.pinHistoryToBottom;
  state.lastEventID = Math.max(state.lastEventID, state.historyLastEventID);
  applyHistoryState(historyState);
  state.replayingHistory = false;
  flushDeferredHistoryRendering();
  if (state.currentView) {
    state.currentView.historyLoaded = true;
    state.currentView.lastEventID = state.lastEventID;
    state.currentView.historyCursor = state.historyCursor;
  }
  ensureConversation();
  renderHistoryControl();
  if (pinToBottom) scheduleBottomAnchor();
  state.pinHistoryToBottom = false;
}

function handleEvent(event) {
  if (state.historyPageReading) {
    if (event.type === 'history.end' && event.historyPage) {
      finishOlderHistoryPage(event);
      return;
    }
    if (event.type === 'error' && event.requestId === state.historyRequestID) {
      cancelOlderHistoryPage();
    } else if (event.type === 'history.start' && !event.historyPage) {
      cancelOlderHistoryPage();
    } else {
      state.historyPageEvents.push(event);
      return;
    }
  }
  if (event.id) state.lastEventID = Math.max(state.lastEventID, event.id);
  const recoveredCompletion = state.runtimeRecoveryActive && event.type === 'turn.completed' && event.status === 'interrupted';
  if (state.runtimeRecoveryActive && !isRuntimeRecoveryEvent(event)) state.runtimeRecoveryActive = false;
  switch (event.type) {
    case 'history.start': {
      if (event.historyPage) {
        if (!state.historyPageLoading || event.requestId !== state.historyRequestID) return;
        state.historyPageReading = true;
        state.historyPageCursor = event.historyCursor || '';
        state.historyPageEvents = [];
        renderHistoryControl();
        return;
      }
      const incompleteHistory = Boolean(state.currentView && !state.currentView.historyLoaded);
      const replacedJournal = Boolean(state.currentView?.journalID && state.currentView.journalID !== event.journalId);
      const replaceHistoryCursor = event.reset || replacedJournal || incompleteHistory || !state.currentView || state.lastEventID === 0;
      if (event.reset || replacedJournal || incompleteHistory) {
        resetCurrentSessionView();
      }
      if (state.currentView && event.journalId) state.currentView.journalID = event.journalId;
      if (replaceHistoryCursor) state.historyCursor = event.historyCursor || '';
      state.historyLastEventID = event.lastEventId || 0;
      state.replayingHistory = true;
      break;
    }
    case 'history.end':
      if (!event.historyPage) finishHistoryReplay(event.historyState);
      break;
    case 'runtime.status':
      state.runtimeStatus = event.runtime || null;
      if (state.currentView) state.currentView.runtimeStatus = state.runtimeStatus;
      renderRuntimeStatus();
      break;
    case 'runtime.recovered':
      state.runtimeRecoveryActive = true;
      renderRecovery(event);
      break;
    case 'user.message':
      renderUser(event);
      break;
    case 'turn.started':
      endAssistantSegment(event.turnId);
      if (!state.activeTurn || state.activeTurnID !== event.turnId) {
        const timestamp = Date.parse(event.timestamp || '');
        state.activeTurnStartedAt = Number.isNaN(timestamp) ? (state.replayingHistory ? 0 : Date.now()) : timestamp;
      }
      state.activeTurn = true;
      state.activeTurnID = event.turnId || '';
      state.waitingForInput = false;
      state.interrupting = false;
      acceptQueuedMessage(event.turnId);
      updateComposerAction();
      refreshSessionProgress();
      break;
    case 'turn.interrupting':
      state.interrupting = true;
      updateComposerAction();
      refreshSessionProgress();
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
      state.waitingForInput = true;
      refreshSessionProgress();
      renderInputRequest(event);
      break;
    case 'input.resolved':
      state.waitingForInput = false;
      refreshSessionProgress();
      resolveInputCard(event);
      break;
    case 'file.diff':
      endAssistantSegment(event.turnId);
      renderDiff(event);
      break;
    case 'turn.completed':
      endAssistantSegment(event.turnId);
      renderTurnEnd(event, recoveredCompletion);
      break;
    case 'error':
      endAssistantSegment(event.turnId);
      if (event.requestId && event.requestId === state.historyRequestID) cancelOlderHistoryPage();
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
  const header = document.createElement('div');
  header.className = 'tool-card-header';
  const icon = document.createElement('span');
  icon.className = 'tool-icon';
  icon.textContent = event.status === 'failed' ? '!' : event.status && event.status !== 'running' ? '✓' : '◇';
  const name = document.createElement('span');
  name.className = 'tool-name';
  name.textContent = event.toolName || 'Tool';
  const status = document.createElement('span');
  status.className = 'tool-status';
  status.textContent = event.status || 'running';
  header.append(icon, name, status);
  card.append(header);
  renderToolOutput(card, event.output);
  elements.messages.append(card);
  if (event.toolId) state.tools.set(event.toolId, card);
  scrollToBottom();
}

function toolOutputPreview(output, maxLines = 5) {
  const text = String(output || '').replace(/\r\n?/g, '\n').replace(/\n+$/, '');
  if (!text) return {text: '', fullText: '', totalLines: 0, omittedLines: 0};
  const lines = text.split('\n');
  if (lines.length <= maxLines) {
    return {text, fullText: text, totalLines: lines.length, omittedLines: 0};
  }
  const head = Math.floor((maxLines - 1) / 2);
  const tail = maxLines - 1 - head;
  const omittedLines = lines.length - head - tail;
  const preview = [
    ...lines.slice(0, head),
    `… +${omittedLines} lines`,
    ...lines.slice(lines.length - tail),
  ];
  return {text: preview.join('\n'), fullText: text, totalLines: lines.length, omittedLines};
}

function renderToolOutput(card, output) {
  const preview = toolOutputPreview(output);
  if (!preview.text) return;
  let container = card.querySelector('.tool-output');
  if (!container) {
    container = document.createElement('div');
    container.className = 'tool-output';
    card.append(container);
  }
  container.replaceChildren();
  if (preview.omittedLines) {
    const details = document.createElement('details');
    details.className = 'tool-output-details';
    const summary = document.createElement('summary');
    summary.className = 'tool-output-summary';
    summary.textContent = `Show all ${preview.totalLines} lines`;
    const full = document.createElement('pre');
    full.className = 'tool-output-full';
    full.textContent = preview.fullText;
    details.append(summary, full);
    container.append(details);
  }
  const visible = document.createElement('pre');
  visible.className = 'tool-output-preview';
  visible.textContent = preview.text;
  container.append(visible);
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
  renderToolOutput(card, event.output);
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
    refreshSessionProgress();
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

function renderTurnEnd(event, recoveredCompletion = false) {
  const completedAt = Date.parse(event.timestamp || '');
  const matchingTurn = !event.turnId || !state.activeTurnID || event.turnId === state.activeTurnID;
  const completedTimeKnown = !Number.isNaN(completedAt) || !state.replayingHistory;
  const elapsed = state.activeTurnStartedAt > 0 && matchingTurn && completedTimeKnown && !recoveredCompletion
    ? Math.max(0, (Number.isNaN(completedAt) ? Date.now() : completedAt) - state.activeTurnStartedAt)
    : null;
  state.activeTurn = false;
  state.activeTurnID = '';
  state.activeTurnStartedAt = 0;
  state.waitingForInput = false;
  state.interrupting = false;
  acceptQueuedMessage(event.turnId);
  updateComposerAction();
  refreshSessionProgress();
  if (event.status === 'interrupted' && !state.replayingHistory) showToast('Active work interrupted');
  const divider = document.createElement('div');
  divider.className = 'turn-divider';
  if (elapsed !== null) divider.textContent = `Worked for ${formatSessionProgressElapsed(elapsed)}`;
  elements.messages.append(divider);
  scrollToBottom();
}

function isRuntimeRecoveryEvent(event) {
  return event.type === 'runtime.recovered'
    || (event.type === 'input.resolved' && event.status === 'cancelled')
    || (event.type === 'turn.completed' && event.status === 'interrupted');
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

function submitComposer() {
  const text = elements.input.value.trim();
  if (!state.socket || state.socket.readyState !== WebSocket.OPEN) return;
  if (composerInterruptAction()) {
    interruptActiveTurn();
  } else if (text) {
    state.socket.send(JSON.stringify({type: 'message', text}));
    clearPromptDraft(state.selected);
  }
}

elements.composer.addEventListener('submit', event => {
  event.preventDefault();
  submitComposer();
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
elements.sectionSelect.addEventListener('change', () => {
  updateCustomSectionField(elements.sectionSelect, elements.sectionCustom);
  if (!elements.sectionCustom.hidden) elements.sectionCustom.focus();
});
elements.sectionCustom.addEventListener('input', () => {
  validateCustomSectionField(elements.sectionSelect, elements.sectionCustom);
});
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
renderSectionOptions();
renderCredentialOptions();
renderWorkspaceOptions();
renderAgentConfigOptions();
updateVolumeClaimFields();
setCreationMode('form');

elements.form.addEventListener('submit', async event => {
  event.preventDefault();
  if (state.sourceLoading || state.creatingSession) return;
  validateCustomSectionField(elements.sectionSelect, elements.sectionCustom);
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
      Object.assign(payload, selectedSectionPayload(elements.sectionSelect, elements.sectionCustom));
      const initialBranch = values.get('initialBranch').trim();
      if (initialBranch) payload.initialBranch = initialBranch;
      const initialPrompt = values.get('initialPrompt');
      if (initialPrompt.trim()) payload.initialPrompt = initialPrompt;
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
    renderSectionOptions();
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

function openSectionDialog() {
  const session = state.selected;
  if (!session || state.sectionSaving) return;
  elements.sectionDialogError.textContent = '';
  elements.sectionDialogDescription.textContent = `Choose where Session ${session.name} appears in the sidebar.`;
  elements.sectionChoiceCustom.value = '';
  populateSectionSelect(elements.sectionChoice, session.section || '', 'Unsectioned (remove assignment)');
  updateCustomSectionField(elements.sectionChoice, elements.sectionChoiceCustom);
  elements.sectionDialog.showModal();
  window.setTimeout(() => elements.sectionChoice.focus(), 0);
}

function setSectionSaving(saving) {
  state.sectionSaving = saving;
  elements.sectionChoice.disabled = saving;
  elements.sectionChoiceCustom.disabled = saving;
  elements.saveSectionButton.disabled = saving;
  elements.sectionDialog.setAttribute('aria-busy', String(saving));
  document.querySelectorAll('.close-section-dialog').forEach(button => {
    button.disabled = saving;
  });
}

function closeSectionDialog() {
  if (!state.sectionSaving) elements.sectionDialog.close();
}

function handleSectionDialogCancel(event) {
  if (state.sectionSaving) event.preventDefault();
}

elements.sectionButton.addEventListener('click', openSectionDialog);
elements.sectionChoice.addEventListener('change', () => {
  updateCustomSectionField(elements.sectionChoice, elements.sectionChoiceCustom);
  if (!elements.sectionChoiceCustom.hidden) elements.sectionChoiceCustom.focus();
});
elements.sectionChoiceCustom.addEventListener('input', () => {
  validateCustomSectionField(elements.sectionChoice, elements.sectionChoiceCustom);
});
document.querySelectorAll('.close-section-dialog').forEach(button => {
  button.addEventListener('click', closeSectionDialog);
});
elements.sectionDialog.addEventListener('cancel', handleSectionDialogCancel);

elements.sectionForm.addEventListener('submit', async event => {
  event.preventDefault();
  if (state.sectionSaving) return;
  validateCustomSectionField(elements.sectionChoice, elements.sectionChoiceCustom);
  if (!elements.sectionForm.reportValidity()) return;
  const session = state.selected;
  if (!session) {
    elements.sectionDialog.close();
    return;
  }
  const sectionPayload = selectedSectionPayload(elements.sectionChoice, elements.sectionChoiceCustom, true);
  const section = sectionPayload.section;
  if (section === (session.section || '')) {
    elements.sectionDialog.close();
    return;
  }
  elements.sectionDialogError.textContent = '';
  setSectionSaving(true);
  try {
    await saveSessionSectionAssignment(session, section);
    elements.sectionDialog.close();
    showToast(section ? `Moved Session to ${section}` : 'Moved Session to Unsectioned');
  } catch (error) {
    elements.sectionDialogError.textContent = error.message;
  } finally {
    setSectionSaving(false);
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
elements.resetButton.addEventListener('click', async () => {
  const session = state.selected;
  if (!session || session.resetting || !window.confirm(`Reset Session ${session.namespace}/${session.name}? This permanently deletes its conversation history and all workspace changes.`)) return;
  try {
    const resetting = await api(`/api/sessions/${encodeURIComponent(session.namespace)}/${encodeURIComponent(session.name)}/reset`, {method: 'POST'});
    closeSocket();
    resetCurrentSessionView();
    discardSessionView(session);
    state.currentView = null;
    clearPromptDraft(session);
    selectSession(resetting);
    await loadSessions();
    showToast('Session reset requested');
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
  refreshSessionProgress();
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
