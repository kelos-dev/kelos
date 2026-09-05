"use strict";
{
    function requiredElement(selector) {
        const element = document.querySelector(selector);
        if (!element)
            throw new Error(`Missing required element: ${selector}`);
        return element;
    }
    function errorMessage(cause) {
        return cause instanceof Error ? cause.message : String(cause);
    }
    function requireElements(values) {
        for (const [name, value] of Object.entries(values)) {
            if (value === null)
                throw new Error(`Missing required element: ${name}`);
        }
        return values;
    }
    const elements = requireElements({
        list: document.querySelector('#session-list'),
        title: document.querySelector('#session-title'),
        meta: document.querySelector('#session-meta'),
        displayNameButton: document.querySelector('#session-display-name'),
        displayNameDialog: document.querySelector('#display-name-dialog'),
        displayNameForm: document.querySelector('#display-name-form'),
        displayNameDialogDescription: document.querySelector('#display-name-dialog-description'),
        displayNameInput: document.querySelector('#session-display-name-input'),
        displayNameDialogError: document.querySelector('#display-name-dialog-error'),
        saveDisplayNameButton: document.querySelector('#save-session-display-name'),
        sessionActionsMenu: document.querySelector('#session-actions-menu'),
        sessionActionRename: document.querySelector('#session-action-rename'),
        sessionActionSection: document.querySelector('#session-action-section'),
        sessionActionLifecycle: document.querySelector('#session-action-lifecycle'),
        sessionActionReset: document.querySelector('#session-action-reset'),
        sessionActionDelete: document.querySelector('#session-action-delete'),
        overviewButton: document.querySelector('#console-overview'),
        sessionsButton: document.querySelector('#console-sessions'),
        resourcesButton: document.querySelector('#console-resources'),
        overviewView: document.querySelector('#overview-view'),
        sessionsView: document.querySelector('#sessions-view'),
        resourcesView: document.querySelector('#resources-view'),
        sessionSidebar: document.querySelector('#session-sidebar'),
        summaryGrid: document.querySelector('#resource-summary-grid'),
        recentResources: document.querySelector('#recent-resources'),
        resourceDiagramTab: document.querySelector('#resource-diagram-tab'),
        resourceInventoryTab: document.querySelector('#resource-inventory-tab'),
        resourceDiagramPanel: document.querySelector('#resource-diagram-panel'),
        resourceInventoryPanel: document.querySelector('#resource-inventory-panel'),
        resourceDiagram: document.querySelector('#resource-diagram'),
        resourceRelationshipFocus: document.querySelector('#resource-relationship-focus'),
        resourceKind: document.querySelector('#resource-kind'),
        resourceTypeList: document.querySelector('#resource-type-list'),
        resourceTotalCount: document.querySelector('#resource-total-count'),
        resourceListTitle: document.querySelector('#resource-list-title'),
        resourceListCount: document.querySelector('#resource-list-count'),
        resourceListSummary: document.querySelector('#resource-list-summary'),
        resourceSearch: document.querySelector('#resource-search'),
        resourceList: document.querySelector('#resource-list'),
        resourceDetailDialog: document.querySelector('#resource-detail-dialog'),
        resourceDetailTitle: document.querySelector('#resource-detail-title'),
        resourceDetailSubtitle: document.querySelector('#resource-detail-subtitle'),
        resourceDetailTabs: document.querySelector('#resource-detail-tabs'),
        resourceDetailLogsTab: document.querySelector('#resource-detail-logs-tab'),
        resourceDetailManifestTab: document.querySelector('#resource-detail-manifest-tab'),
        resourceDetailLogsPanel: document.querySelector('#resource-detail-logs-panel'),
        resourceDetailManifestPanel: document.querySelector('#resource-detail-manifest-panel'),
        refreshResourceLogs: document.querySelector('#refresh-resource-logs'),
        resourceDetailLogs: document.querySelector('#resource-detail-logs'),
        resourceDetailYAML: document.querySelector('#resource-detail-yaml'),
        namespaceLabels: document.querySelectorAll('.console-namespace'),
        newSessionButton: document.querySelector('#new-session'),
        sectionSelect: document.querySelector('#session-section-select'),
        sectionCustom: document.querySelector('#session-section-custom'),
        sectionForm: document.querySelector('#session-section-form'),
        sectionChoice: document.querySelector('#session-section-choice'),
        sectionChoiceCustom: document.querySelector('#session-section-choice-custom'),
        saveSectionButton: document.querySelector('#save-session-section'),
        cancelSectionButton: document.querySelector('#cancel-session-section'),
        currentRequest: document.querySelector('#current-request'),
        currentRequestButton: document.querySelector('#current-request-button'),
        currentRequestText: document.querySelector('#current-request-text'),
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
        attachmentInput: document.querySelector('#attachment-input'),
        attachFiles: document.querySelector('#attach-files'),
        pendingAttachments: document.querySelector('#pending-attachments'),
        send: document.querySelector('#send-message'),
        composerHint: document.querySelector('#composer-hint'),
        runtimeStatus: document.querySelector('#session-runtime-status'),
        progress: document.querySelector('#session-progress'),
        progressLabel: document.querySelector('#session-progress-label'),
        progressElapsed: document.querySelector('#session-progress-elapsed'),
        pending: document.querySelector('#pending-message'),
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
        suspendButton: document.querySelector('#suspend-session'),
        resumeButton: document.querySelector('#resume-session'),
        resetButton: document.querySelector('#reset-session'),
        deleteButton: document.querySelector('#delete-session'),
        sidebar: document.querySelector('#sidebar'),
        sidebarScroll: document.querySelector('.sidebar-scroll'),
        openSidebar: document.querySelector('#open-sidebar'),
        closeSidebar: document.querySelector('#close-sidebar'),
        sidebarScrim: document.querySelector('#sidebar-scrim'),
        toast: document.querySelector('#toast'),
    });
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
        pendingMessage: null,
        promptDrafts: new Map(),
        attachmentDrafts: new Map(),
        sendingMessage: false,
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
        sessionListGeneration: 0,
        options: { credentials: [], workspaces: [], agentConfigs: [], sessions: [] },
        selectedAgentConfigs: [],
        creationMode: 'form',
        sourceGeneration: 0,
        sourceLoading: false,
        creatingSession: false,
        suspendingSession: false,
        resumingSession: false,
        consoleView: 'overview',
        resourceGroups: [],
        resourceRelationships: [],
        resourceListGeneration: 0,
        resourceDetailGeneration: 0,
        resourceDetailLogGeneration: 0,
        resourceDetailTask: null,
        sourceStorageClassNamePresent: false,
        loadedSource: null,
        displayNameSaving: false,
        displayNameSession: null,
        sessionActionKey: '',
        sessionActionTrigger: null,
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
            headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
        });
        if (response.status === 401) {
            window.location.replace('/login');
            throw new Error('Authentication required');
        }
        if (!response.ok) {
            const body = await response.json().catch(() => ({}));
            throw new Error(body.error || `${response.status} ${response.statusText}`);
        }
        if (response.status === 204)
            return undefined;
        return response.json();
    }
    async function apiText(path) {
        const response = await fetch(path);
        if (response.status === 401) {
            window.location.replace('/login');
            throw new Error('Authentication required');
        }
        const body = await response.text();
        if (!response.ok) {
            let message = `${response.status} ${response.statusText}`;
            try {
                message = JSON.parse(body).error || message;
            }
            catch (_) {
                // Keep the HTTP status when the response is not a JSON API error.
            }
            throw new Error(message);
        }
        return body;
    }
    let toastTimer;
    function showToast(message) {
        elements.toast.textContent = message;
        elements.toast.classList.add('show');
        window.clearTimeout(toastTimer);
        toastTimer = window.setTimeout(() => elements.toast.classList.remove('show'), 3200);
    }
    const allResourceKind = '__all__';
    const resourceDescriptions = {
        sessions: 'Long-lived, interactive agent conversations.',
        tasks: 'Finite units of agent work and their current state.',
        taskrecords: 'Task lifecycle and usage records.',
        taskspawners: 'Automation that creates Tasks from schedules and events.',
        sessionspawners: 'Automation that creates Sessions from events.',
        workerpools: 'Reusable execution capacity for Tasks.',
        taskbudgets: 'Limits and usage for Task execution.',
        workspaces: 'Repository sources and workspace setup.',
        agentconfigs: 'Shared agent instructions and integrations.',
    };
    function resourceCollections() {
        return state.resourceGroups.flatMap(group => group.resources.map(resource => ({ ...resource, group: group.name })));
    }
    function setConsoleView(view) {
        state.consoleView = ['overview', 'sessions', 'resources'].includes(view) ? view : 'overview';
        elements.overviewView.hidden = state.consoleView !== 'overview';
        elements.sessionsView.hidden = state.consoleView !== 'sessions';
        elements.resourcesView.hidden = state.consoleView !== 'resources';
        elements.sessionSidebar.hidden = state.consoleView !== 'sessions';
        for (const [button, name] of [
            [elements.overviewButton, 'overview'],
            [elements.sessionsButton, 'sessions'],
            [elements.resourcesButton, 'resources'],
        ]) {
            if (name === state.consoleView)
                button.setAttribute('aria-current', 'page');
            else
                button.removeAttribute('aria-current');
        }
        if (state.consoleView === 'sessions' && !state.selected && state.sessions.length)
            selectSession(state.sessions[0]);
        if (state.consoleView === 'sessions')
            updateCurrentRequest();
        setSidebarOpen(false);
    }
    function resourceAge(createdAt) {
        return createdAt ? formatSessionRecency({ createdAt }, true) : '—';
    }
    function resourceStatus(item) {
        return item.phase || 'Configured';
    }
    function createResourceTable(entries, emptyMessage = `No resources in ${state.namespace}.`) {
        if (!entries.length) {
            const empty = document.createElement('div');
            empty.className = 'resource-empty';
            empty.textContent = emptyMessage;
            return empty;
        }
        const table = document.createElement('table');
        table.className = 'resource-table';
        const head = document.createElement('thead');
        const heading = document.createElement('tr');
        for (const label of ['Name', 'Kind', 'Status', 'Created']) {
            const cell = document.createElement('th');
            cell.scope = 'col';
            cell.textContent = label;
            heading.append(cell);
        }
        head.append(heading);
        const body = document.createElement('tbody');
        for (const { collection, item } of entries) {
            const row = document.createElement('tr');
            if (item.message)
                row.title = item.message;
            const nameCell = document.createElement('td');
            nameCell.dataset.label = 'Name';
            const button = document.createElement('button');
            button.className = 'resource-name-button';
            button.type = 'button';
            button.textContent = item.name;
            button.setAttribute('aria-label', `Inspect ${collection.kind} ${item.namespace}/${item.name}`);
            button.addEventListener('click', () => openResourceDetail(collection, item));
            nameCell.append(button);
            if (item.message) {
                const message = document.createElement('div');
                message.className = 'resource-row-message';
                message.textContent = item.message;
                nameCell.append(message);
            }
            const kindCell = document.createElement('td');
            kindCell.className = 'resource-row-kind';
            kindCell.dataset.label = 'Kind';
            kindCell.textContent = collection.kind;
            const statusCell = document.createElement('td');
            statusCell.dataset.label = 'Status';
            const phase = document.createElement('span');
            phase.className = 'resource-row-phase';
            phase.dataset.state = resourceStatus(item).toLowerCase();
            phase.textContent = resourceStatus(item);
            statusCell.append(phase);
            const ageCell = document.createElement('td');
            ageCell.className = 'resource-row-age';
            ageCell.dataset.label = 'Created';
            ageCell.textContent = resourceAge(item.createdAt);
            row.append(nameCell, kindCell, statusCell, ageCell);
            body.append(row);
        }
        table.append(head, body);
        return table;
    }
    function renderOverview() {
        elements.summaryGrid.replaceChildren();
        const collections = resourceCollections();
        for (const collection of collections) {
            const card = document.createElement('button');
            card.className = 'resource-summary-card';
            card.type = 'button';
            const label = document.createElement('span');
            label.textContent = collection.label;
            const count = document.createElement('strong');
            count.textContent = String(collection.items.length);
            card.append(label, count);
            card.addEventListener('click', () => {
                setConsoleView('resources');
                openResourceInventory(collection.resource);
            });
            elements.summaryGrid.append(card);
        }
        if (!collections.length) {
            const loading = document.createElement('div');
            loading.className = 'resource-empty';
            loading.textContent = 'Loading Kelos resources…';
            elements.summaryGrid.append(loading);
        }
        const recent = collections
            .flatMap(collection => collection.items.map(item => ({ collection, item })))
            .sort((left, right) => String(right.item.createdAt).localeCompare(String(left.item.createdAt)))
            .slice(0, 10);
        elements.recentResources.replaceChildren(createResourceTable(recent));
    }
    function renderResourceKindOptions() {
        const selected = elements.resourceKind.value;
        elements.resourceKind.replaceChildren();
        const collections = resourceCollections();
        const total = collections.reduce((count, collection) => count + collection.items.length, 0);
        const allOption = document.createElement('option');
        allOption.value = allResourceKind;
        allOption.textContent = `All resources (${total})`;
        elements.resourceKind.append(allOption);
        for (const group of state.resourceGroups) {
            const optionGroup = document.createElement('optgroup');
            optionGroup.label = group.name;
            for (const collection of group.resources) {
                const option = document.createElement('option');
                option.value = collection.resource;
                option.textContent = `${collection.label} (${collection.items.length})`;
                optionGroup.append(option);
            }
            elements.resourceKind.append(optionGroup);
        }
        if (selected === allResourceKind || collections.some(collection => collection.resource === selected)) {
            elements.resourceKind.value = selected;
        }
        else {
            elements.resourceKind.value = allResourceKind;
        }
        renderResourceTypeNavigation();
        renderResourceRelationshipFocusOptions();
        renderResourceDiagram();
    }
    function setResourceView(view) {
        const showDiagram = view !== 'inventory';
        elements.resourceDiagramPanel.hidden = !showDiagram;
        elements.resourceInventoryPanel.hidden = showDiagram;
        elements.resourceDiagramTab.setAttribute('aria-selected', String(showDiagram));
        elements.resourceDiagramTab.tabIndex = showDiagram ? 0 : -1;
        elements.resourceInventoryTab.setAttribute('aria-selected', String(!showDiagram));
        elements.resourceInventoryTab.tabIndex = showDiagram ? -1 : 0;
    }
    function handleResourceViewTabKeydown(event) {
        const tabs = [elements.resourceDiagramTab, elements.resourceInventoryTab];
        const current = tabs.indexOf(event.target);
        if (current < 0)
            return;
        let target;
        if (event.key === 'ArrowLeft')
            target = tabs[(current - 1 + tabs.length) % tabs.length];
        if (event.key === 'ArrowRight')
            target = tabs[(current + 1) % tabs.length];
        if (event.key === 'Home')
            target = tabs[0];
        if (event.key === 'End')
            target = tabs[tabs.length - 1];
        if (!target)
            return;
        event.preventDefault();
        target.click();
        target.focus();
    }
    function openResourceInventory(resource) {
        selectResourceKind(resource);
        setResourceView('inventory');
    }
    function resourceReferenceKey(reference) {
        return `${reference.resource}/${reference.name}`;
    }
    function resourceEntryForReference(reference) {
        const collection = resourceCollections().find(item => item.resource === reference.resource);
        const item = collection?.items.find(candidate => candidate.name === reference.name);
        if (collection && item)
            return { collection, item };
        return {
            collection: { resource: reference.resource, kind: reference.kind || reference.resource, label: reference.kind || reference.resource },
            item: { name: reference.name, namespace: state.namespace, phase: 'Missing', missing: true },
        };
    }
    function renderResourceRelationshipFocusOptions() {
        const selected = elements.resourceRelationshipFocus.value;
        elements.resourceRelationshipFocus.replaceChildren();
        for (const group of state.resourceGroups) {
            const optionGroup = document.createElement('optgroup');
            optionGroup.label = group.name;
            for (const collection of group.resources) {
                for (const item of collection.items) {
                    const option = document.createElement('option');
                    option.value = resourceReferenceKey({ resource: collection.resource, name: item.name });
                    option.textContent = `${collection.kind}/${item.name}`;
                    optionGroup.append(option);
                }
            }
            if (optionGroup.children.length)
                elements.resourceRelationshipFocus.append(optionGroup);
        }
        const options = Array.from(elements.resourceRelationshipFocus.options);
        if (options.some(option => option.value === selected)) {
            elements.resourceRelationshipFocus.value = selected;
        }
        else {
            const related = new Set(state.resourceRelationships.flatMap(relationship => [
                resourceReferenceKey(relationship.source),
                resourceReferenceKey(relationship.target),
            ]));
            elements.resourceRelationshipFocus.value = options.find(option => related.has(option.value))?.value || options[0]?.value || '';
        }
        elements.resourceRelationshipFocus.disabled = !options.length;
    }
    function focusResource(reference) {
        const key = resourceReferenceKey(reference);
        if (!Array.from(elements.resourceRelationshipFocus.options).some(option => option.value === key))
            return;
        elements.resourceRelationshipFocus.value = key;
        renderResourceDiagram();
    }
    function createResourceRelationshipNode(reference, selected = false) {
        const { collection, item } = resourceEntryForReference(reference);
        const node = document.createElement('button');
        node.className = 'resource-relationship-node';
        node.type = 'button';
        node.dataset.resource = collection.resource;
        if (selected)
            node.dataset.selected = 'true';
        const heading = document.createElement('span');
        heading.className = 'resource-relationship-node-heading';
        const kind = document.createElement('span');
        kind.textContent = collection.kind;
        const status = document.createElement('span');
        status.dataset.state = resourceStatus(item).toLowerCase();
        status.textContent = resourceStatus(item);
        heading.append(kind, status);
        const name = document.createElement('strong');
        name.textContent = item.name;
        node.append(heading, name);
        if (item.message) {
            const message = document.createElement('span');
            message.className = 'resource-relationship-node-message';
            message.textContent = item.message;
            node.append(message);
        }
        if (item.missing) {
            node.disabled = true;
            node.title = `${collection.kind} ${item.name} was not found in ${state.namespace}`;
        }
        else if (selected) {
            node.title = `Inspect ${collection.kind} ${item.name}`;
            node.addEventListener('click', () => openResourceDetail(collection, item));
        }
        else {
            node.title = `Focus ${collection.kind} ${item.name}`;
            node.addEventListener('click', () => focusResource(reference));
        }
        return node;
    }
    function createResourceRelationshipConnector(relationship) {
        const connector = document.createElement('div');
        connector.className = 'resource-relationship-connector';
        connector.dataset.inferred = String(Boolean(relationship.inferred));
        const label = document.createElement('span');
        label.textContent = relationship.relationship;
        const arrow = document.createElement('span');
        arrow.setAttribute('aria-hidden', 'true');
        arrow.textContent = '→';
        connector.append(label, arrow);
        return connector;
    }
    function createResourceRelationshipEdge(relationship, incoming) {
        const edge = document.createElement('div');
        edge.className = 'resource-relationship-edge';
        edge.dataset.direction = incoming ? 'incoming' : 'outgoing';
        const reference = incoming ? relationship.source : relationship.target;
        const node = createResourceRelationshipNode(reference);
        const connector = createResourceRelationshipConnector(relationship);
        if (incoming)
            edge.append(node, connector);
        else
            edge.append(connector, node);
        return edge;
    }
    function createResourceRelationshipColumn(title, relationships, incoming) {
        const column = document.createElement('section');
        column.className = 'resource-relationship-column';
        const heading = document.createElement('h3');
        heading.textContent = title;
        column.append(heading);
        if (!relationships.length) {
            const empty = document.createElement('div');
            empty.className = 'resource-relationship-empty';
            empty.textContent = incoming ? 'No incoming relationships' : 'No outgoing relationships';
            column.append(empty);
            return column;
        }
        const edges = document.createElement('div');
        edges.className = 'resource-relationship-edges';
        for (const relationship of relationships)
            edges.append(createResourceRelationshipEdge(relationship, incoming));
        column.append(edges);
        return column;
    }
    function resourceRelationshipsForFocus(relationships, focusKey) {
        return {
            incoming: relationships.filter(relationship => resourceReferenceKey(relationship.target) === focusKey),
            outgoing: relationships.filter(relationship => resourceReferenceKey(relationship.source) === focusKey),
        };
    }
    function renderResourceDiagram() {
        const focusKey = elements.resourceRelationshipFocus.value;
        if (!focusKey) {
            const empty = document.createElement('div');
            empty.className = 'resource-empty';
            empty.textContent = `No resources in ${state.namespace}.`;
            elements.resourceDiagram.replaceChildren(empty);
            return;
        }
        const { incoming, outgoing } = resourceRelationshipsForFocus(state.resourceRelationships, focusKey);
        const [resource, name] = focusKey.split('/', 2);
        const focusEntry = resourceEntryForReference({ resource, name });
        const center = document.createElement('section');
        center.className = 'resource-relationship-focus-node';
        const heading = document.createElement('h3');
        heading.textContent = 'Selected resource';
        center.append(heading, createResourceRelationshipNode({ resource, kind: focusEntry.collection.kind, name }, true));
        const graph = document.createElement('div');
        graph.className = 'resource-relationship-graph';
        graph.append(createResourceRelationshipColumn('Related from', incoming, true), center, createResourceRelationshipColumn('Connects to', outgoing, false));
        elements.resourceDiagram.replaceChildren(graph);
    }
    function createResourceTypeButton(resource, label, count) {
        const button = document.createElement('button');
        button.type = 'button';
        button.dataset.resource = resource;
        const name = document.createElement('span');
        name.textContent = label;
        const badge = document.createElement('span');
        badge.textContent = String(count);
        button.append(name, badge);
        button.addEventListener('click', () => selectResourceKind(resource));
        return button;
    }
    function renderResourceTypeNavigation() {
        const collections = resourceCollections();
        const total = collections.reduce((count, collection) => count + collection.items.length, 0);
        elements.resourceTotalCount.textContent = String(total);
        elements.resourceTypeList.replaceChildren(createResourceTypeButton(allResourceKind, 'All resources', total));
        for (const group of state.resourceGroups) {
            const section = document.createElement('section');
            section.className = 'resource-type-group';
            const heading = document.createElement('h3');
            heading.textContent = group.name;
            section.append(heading);
            for (const collection of group.resources) {
                section.append(createResourceTypeButton(collection.resource, collection.label, collection.items.length));
            }
            elements.resourceTypeList.append(section);
        }
    }
    function resourceEntries(collections, resource) {
        const selected = collections.find(collection => collection.resource === resource);
        const sources = selected ? [selected] : collections;
        const entries = sources.flatMap(collection => collection.items.map(item => ({ collection, item })));
        if (!selected) {
            entries.sort((left, right) => {
                const created = String(right.item.createdAt || '').localeCompare(String(left.item.createdAt || ''));
                if (created)
                    return created;
                return `${left.collection.kind}/${left.item.name}`.localeCompare(`${right.collection.kind}/${right.item.name}`);
            });
        }
        return entries;
    }
    function filterResourceEntries(entries, query) {
        const normalized = query.trim().toLowerCase();
        if (!normalized)
            return entries;
        return entries.filter(({ collection, item }) => [
            item.name,
            collection.kind,
            collection.label,
            resourceStatus(item),
            item.message,
        ].some(value => String(value || '').toLowerCase().includes(normalized)));
    }
    function selectResourceKind(resource) {
        elements.resourceKind.value = resource;
        renderResources();
    }
    function renderResources() {
        const collections = resourceCollections();
        let resource = elements.resourceKind.value;
        let collection = collections.find(item => item.resource === resource);
        if (resource !== allResourceKind && !collection) {
            resource = allResourceKind;
            elements.resourceKind.value = resource;
        }
        const entries = resourceEntries(collections, resource);
        const filteredEntries = filterResourceEntries(entries, elements.resourceSearch.value);
        const query = elements.resourceSearch.value.trim();
        elements.resourceListTitle.textContent = collection?.label || 'All resources';
        elements.resourceListCount.textContent = String(entries.length);
        elements.resourceListSummary.textContent = query
            ? `${filteredEntries.length} of ${entries.length} resources match “${query}”.`
            : (collection ? resourceDescriptions[collection.resource] : undefined) || 'Every Kelos object in this namespace.';
        for (const button of elements.resourceTypeList.querySelectorAll('button')) {
            if (button.dataset.resource === resource)
                button.setAttribute('aria-current', 'true');
            else
                button.removeAttribute('aria-current');
        }
        const emptyMessage = query
            ? `No resources match “${query}”.`
            : `No ${collection?.label.toLowerCase() || 'resources'} in ${state.namespace}.`;
        elements.resourceList.replaceChildren(createResourceTable(filteredEntries, emptyMessage));
    }
    async function loadResources({ quiet = false } = {}) {
        const namespace = state.namespace;
        const generation = state.namespaceGeneration;
        const listGeneration = ++state.resourceListGeneration;
        try {
            const inventory = await api(`/api/resources?namespace=${encodeURIComponent(namespace)}`);
            if (generation !== state.namespaceGeneration || listGeneration !== state.resourceListGeneration)
                return;
            state.resourceGroups = inventory.groups || [];
            state.resourceRelationships = inventory.relationships || [];
            renderResourceKindOptions();
            renderOverview();
            renderResources();
        }
        catch (error) {
            if (!quiet && generation === state.namespaceGeneration && listGeneration === state.resourceListGeneration)
                showToast(errorMessage(error));
        }
    }
    async function refreshConsole() {
        try {
            await Promise.all([loadSessions(), loadOptions(), loadResources()]);
        }
        catch (error) {
            showToast(errorMessage(error));
        }
    }
    function setResourceDetailView(view) {
        const showLogs = view === 'logs' && !elements.resourceDetailTabs.hidden;
        elements.resourceDetailLogsPanel.hidden = !showLogs;
        elements.resourceDetailManifestPanel.hidden = showLogs;
        elements.resourceDetailLogsTab.setAttribute('aria-selected', String(showLogs));
        elements.resourceDetailLogsTab.tabIndex = showLogs ? 0 : -1;
        elements.resourceDetailManifestTab.setAttribute('aria-selected', String(!showLogs));
        elements.resourceDetailManifestTab.tabIndex = showLogs ? -1 : 0;
    }
    function handleResourceDetailTabKeydown(event) {
        const tabs = [elements.resourceDetailLogsTab, elements.resourceDetailManifestTab];
        const current = tabs.indexOf(event.target);
        if (current < 0)
            return;
        let target;
        if (event.key === 'ArrowLeft')
            target = tabs[(current - 1 + tabs.length) % tabs.length];
        if (event.key === 'ArrowRight')
            target = tabs[(current + 1) % tabs.length];
        if (event.key === 'Home')
            target = tabs[0];
        if (event.key === 'End')
            target = tabs[tabs.length - 1];
        if (!target)
            return;
        event.preventDefault();
        target.click();
        target.focus();
    }
    async function loadResourceTaskLogs(item, detailGeneration) {
        const logGeneration = ++state.resourceDetailLogGeneration;
        elements.refreshResourceLogs.disabled = true;
        elements.resourceDetailLogs.textContent = 'Loading logs…';
        try {
            const logs = await apiText(`/api/resources/tasks/${encodeURIComponent(item.namespace)}/${encodeURIComponent(item.name)}/logs`);
            if (detailGeneration !== state.resourceDetailGeneration || logGeneration !== state.resourceDetailLogGeneration)
                return;
            elements.resourceDetailLogs.textContent = logs || 'No logs yet.';
        }
        catch (error) {
            if (detailGeneration !== state.resourceDetailGeneration || logGeneration !== state.resourceDetailLogGeneration)
                return;
            elements.resourceDetailLogs.textContent = errorMessage(error);
        }
        finally {
            if (detailGeneration === state.resourceDetailGeneration && logGeneration === state.resourceDetailLogGeneration) {
                elements.refreshResourceLogs.disabled = false;
            }
        }
    }
    async function openResourceDetail(collection, item) {
        const generation = ++state.resourceDetailGeneration;
        const showTaskLogs = collection.resource === 'tasks';
        elements.resourceDetailTitle.textContent = `${collection.kind} ${item.name}`;
        elements.resourceDetailSubtitle.textContent = `${item.namespace}/${item.name}`;
        elements.resourceDetailTabs.hidden = !showTaskLogs;
        state.resourceDetailTask = showTaskLogs ? { namespace: item.namespace, name: item.name } : null;
        if (!showTaskLogs) {
            state.resourceDetailLogGeneration++;
            elements.refreshResourceLogs.disabled = true;
            elements.resourceDetailLogs.textContent = '';
        }
        elements.resourceDetailYAML.textContent = 'Loading manifest…';
        setResourceDetailView(showTaskLogs ? 'logs' : 'manifest');
        elements.resourceDetailDialog.showModal();
        const manifestRequest = (async () => {
            try {
                const detail = await api(`/api/resources/${encodeURIComponent(collection.resource)}/${encodeURIComponent(item.namespace)}/${encodeURIComponent(item.name)}`);
                if (generation !== state.resourceDetailGeneration)
                    return;
                elements.resourceDetailYAML.textContent = detail.yaml;
            }
            catch (error) {
                if (generation !== state.resourceDetailGeneration)
                    return;
                elements.resourceDetailYAML.textContent = errorMessage(error);
            }
        })();
        const logsRequest = showTaskLogs ? loadResourceTaskLogs(state.resourceDetailTask, generation) : Promise.resolve();
        await Promise.all([manifestRequest, logsRequest]);
    }
    function sessionKey(session) {
        return `${session.namespace}/${session.name}`;
    }
    function sessionDisplayName(session) {
        return session?.displayName || session?.name || '';
    }
    function sessionViewKey(session) {
        return `${sessionKey(session)}/${session.uid || 'unknown'}`;
    }
    function moveChildren(element) {
        const fragment = document.createDocumentFragment();
        while (element.firstChild)
            fragment.append(element.firstChild);
        return fragment;
    }
    function createSessionView() {
        return {
            messages: document.createDocumentFragment(),
            pending: document.createDocumentFragment(),
            changes: document.createDocumentFragment(),
            lastEventID: 0,
            journalID: '',
            assistantSegmentByTurn: new Map(),
            assistantTextByTurn: new Map(),
            tools: new Map(),
            inputs: new Map(),
            diffs: new Map(),
            fileChanges: new Map(),
            pendingMessage: null,
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
        if (!view)
            return;
        view.messages = moveChildren(elements.messages);
        view.pending = moveChildren(elements.pending);
        view.changes = moveChildren(elements.changesList);
        view.lastEventID = state.lastEventID;
        view.assistantSegmentByTurn = state.assistantSegmentByTurn;
        view.assistantTextByTurn = state.assistantTextByTurn;
        view.tools = state.tools;
        view.inputs = state.inputs;
        view.diffs = state.diffs;
        view.fileChanges = state.fileChanges;
        view.pendingMessage = state.pendingMessage;
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
        state.pendingMessage = view.pendingMessage;
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
        elements.pending.replaceChildren(view.pending);
        elements.changesList.replaceChildren(view.changes);
        elements.pending.hidden = !state.pendingMessage;
        updateFileChangesHeader();
        if (!hasChanges)
            renderFileChanges();
        refreshSessionProgress();
        renderRuntimeStatus();
        renderHistoryControl();
        updateCurrentRequest();
    }
    function cachedSessionView(session) {
        const key = sessionViewKey(session);
        let view = state.sessionViews.get(key);
        if (view)
            state.sessionViews.delete(key);
        else
            view = createSessionView();
        state.sessionViews.set(key, view);
        while (state.sessionViews.size > maxCachedSessionViews) {
            const oldest = state.sessionViews.keys().next().value;
            if (oldest === undefined)
                break;
            state.sessionViews.delete(oldest);
        }
        return view;
    }
    function discardSessionView(session) {
        if (session)
            state.sessionViews.delete(sessionViewKey(session));
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
        state.pendingMessage = null;
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
        elements.pending.replaceChildren();
        elements.pending.hidden = true;
        hideCurrentRequest();
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
            view.pendingMessage = state.pendingMessage;
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
        if (totalSeconds < 60)
            return `${totalSeconds}s`;
        const seconds = totalSeconds % 60;
        const totalMinutes = Math.floor(totalSeconds / 60);
        if (totalMinutes < 60)
            return `${totalMinutes}m ${String(seconds).padStart(2, '0')}s`;
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
        }
        else if (state.waitingForInput) {
            label = 'Waiting for input';
            status = 'waiting';
        }
        const startedAt = state.activeTurnStartedAt || now;
        elements.progress.hidden = false;
        elements.progress.dataset.state = status;
        if (elements.progressLabel.textContent !== label)
            elements.progressLabel.textContent = label;
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
        if (!home)
            return workingDir;
        if (workingDir === home)
            return '~';
        if (workingDir.startsWith(`${home}/`))
            return `~/${workingDir.slice(home.length + 1)}`;
        return workingDir;
    }
    function sessionRuntimeContextUsedPercent(usage) {
        const baselineTokens = 12000;
        if (usage.contextWindow <= baselineTokens)
            return 100;
        const effectiveWindow = usage.contextWindow - baselineTokens;
        const used = Math.max(0, (Number(usage.contextTokens) || 0) - baselineTokens);
        return Math.min(100, Math.round(used * 100 / effectiveWindow));
    }
    function formatSessionRuntimeTokens(value) {
        const tokens = Math.max(0, Number(value) || 0);
        if (tokens < 1000)
            return String(tokens);
        let scaled = tokens / 1000;
        let suffix = 'K';
        if (tokens >= 1e12) {
            scaled = tokens / 1e12;
            suffix = 'T';
        }
        else if (tokens >= 1e9) {
            scaled = tokens / 1e9;
            suffix = 'B';
        }
        else if (tokens >= 1e6) {
            scaled = tokens / 1e6;
            suffix = 'M';
        }
        const decimals = scaled < 10 ? 2 : scaled < 100 ? 1 : 0;
        return `${Number(scaled.toFixed(decimals))}${suffix}`;
    }
    function sessionRuntimeStatusText(status, displayName = '') {
        if (!status)
            return '';
        const parts = [];
        const add = (value) => { if (value)
            parts.push(value); };
        add(displayName || status.sessionName);
        add(status.agentType);
        add(`${status.model || ''} ${status.effort || ''}`.trim());
        add(sessionRuntimePath(status.workingDir, status.homeDir));
        add(status.branch);
        if (status.pullRequestNumber && status.pullRequestNumber > 0)
            add(`PR #${status.pullRequestNumber}`);
        if (status.usage && status.usage.contextWindow > 0) {
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
        const text = sessionRuntimeStatusText(state.runtimeStatus, sessionDisplayName(state.selected));
        elements.runtimeStatus.textContent = text;
        elements.runtimeStatus.title = text;
        elements.runtimeStatus.hidden = !text;
    }
    function savePromptDraft(session) {
        if (!session)
            return;
        if (elements.input.value) {
            state.promptDrafts.set(sessionKey(session), elements.input.value);
        }
        else {
            state.promptDrafts.delete(sessionKey(session));
        }
    }
    function restorePromptDraft(session) {
        elements.input.value = session ? state.promptDrafts.get(sessionKey(session)) || '' : '';
        resizeComposer();
    }
    function clearPromptDraft(session) {
        if (!session)
            return;
        state.promptDrafts.delete(sessionKey(session));
        if (state.selected && sessionKey(state.selected) === sessionKey(session)) {
            elements.input.value = '';
            resizeComposer();
            updateComposerAction();
        }
    }
    function clearAttachmentDraft(session) {
        if (!session)
            return;
        state.attachmentDrafts.delete(sessionKey(session));
        if (state.selected && sessionKey(state.selected) === sessionKey(session))
            renderPendingAttachments();
    }
    function providerLabel(provider) {
        return provider === 'claude-code' ? 'Claude Code' : provider === 'codex' ? 'Codex' : provider === 'opencode' ? 'OpenCode' : provider;
    }
    function providerInitials(provider) {
        return provider === 'claude-code' ? 'CC' : provider === 'codex' ? 'CX' : provider === 'opencode' ? 'OC' : 'AI';
    }
    function sessionDisplayStatus(session) {
        if (session.resetting)
            return 'Resetting';
        if (session.phase !== 'Ready')
            return session.phase || 'Pending';
        if (session.waitingForInput)
            return 'Waiting for input';
        if (session.active === true)
            return 'Active';
        if (session.active === false)
            return 'Idle';
        return session.phase;
    }
    function parseSessionTimestamp(value) {
        const milliseconds = Date.parse(value || '');
        return Number.isNaN(milliseconds) ? null : new Date(milliseconds);
    }
    function sessionTimestamp(session) {
        const lastActivity = parseSessionTimestamp(session.lastActivityAt);
        if (lastActivity)
            return { date: lastActivity, activity: true };
        const created = parseSessionTimestamp(session.createdAt);
        return created ? { date: created, activity: false } : null;
    }
    function formatSessionRecency(session, compact = false, now = Date.now()) {
        const timestamp = sessionTimestamp(session);
        if (!timestamp)
            return '';
        const elapsed = Math.max(0, now - timestamp.date.getTime());
        const minutes = Math.floor(elapsed / 60000);
        let value;
        if (minutes < 1) {
            value = compact ? 'Now' : 'just now';
        }
        else if (minutes < 60) {
            value = compact ? `${minutes}m` : `${minutes} minute${minutes === 1 ? '' : 's'} ago`;
        }
        else {
            const hours = Math.floor(minutes / 60);
            if (hours < 24) {
                value = compact ? `${hours}h` : `${hours} hour${hours === 1 ? '' : 's'} ago`;
            }
            else {
                const days = Math.floor(hours / 24);
                if (days < 7) {
                    value = compact ? `${days}d` : `${days} day${days === 1 ? '' : 's'} ago`;
                }
                else {
                    const currentYear = new Date(now).getFullYear();
                    value = new Intl.DateTimeFormat(undefined, {
                        month: 'short',
                        day: 'numeric',
                        ...(timestamp.date.getFullYear() === currentYear ? {} : { year: 'numeric' }),
                    }).format(timestamp.date);
                }
            }
        }
        if (compact)
            return value;
        return `${timestamp.activity ? 'Last active' : 'Created'} ${value}`;
    }
    function createSessionTimestamp(session, compact, className) {
        const timestamp = sessionTimestamp(session);
        const label = formatSessionRecency(session, compact);
        if (!timestamp || !label)
            return null;
        const element = document.createElement('time');
        element.className = className;
        element.dateTime = timestamp.date.toISOString();
        element.textContent = label;
        const exact = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'long' }).format(timestamp.date);
        const exactLabel = `${timestamp.activity ? 'Last active' : 'Created'} ${exact}`;
        element.title = exactLabel;
        element.setAttribute('aria-label', exactLabel);
        return element;
    }
    function safeHTTPURL(value) {
        if (!value)
            return null;
        try {
            const url = new URL(value, window.location.origin);
            return url.protocol === 'http:' || url.protocol === 'https:' ? url : null;
        }
        catch (_) {
            return null;
        }
    }
    function pullRequestLabel(url) {
        const match = url.pathname.match(/\/pull\/(\d+)(?:\/|$)/);
        return match ? `PR #${match[1]}` : 'Pull request';
    }
    function sessionPRState(value) {
        return value && ['Draft', 'Open', 'Queued', 'Merged', 'Closed'].includes(value) ? value : '';
    }
    function sessionPRChecks(checks) {
        if (!checks || !Number.isInteger(checks.completed) || !Number.isInteger(checks.total) || checks.total < 1)
            return null;
        if (!['Pending', 'Success', 'Failure'].includes(checks.state))
            return null;
        const completed = Math.max(0, Math.min(checks.completed, checks.total));
        const labels = {
            Pending: `Checks ${completed}/${checks.total}`,
            Success: 'Checks passed',
            Failure: 'Checks failed',
        };
        return { state: checks.state.toLowerCase(), label: labels[checks.state] };
    }
    function createPullRequestLink(pullRequest, className) {
        const url = safeHTTPURL(pullRequest?.url);
        if (!pullRequest || !url)
            return null;
        const link = document.createElement('a');
        link.className = `pull-request-link ${className}`;
        link.href = url.href;
        link.target = '_blank';
        link.rel = 'noopener noreferrer';
        const state = sessionPRState(pullRequest.state);
        link.textContent = state ? `${pullRequestLabel(url)} · ${state}` : pullRequestLabel(url);
        if (state)
            link.dataset.state = state.toLowerCase();
        const checks = sessionPRChecks(pullRequest.checks);
        if (checks) {
            const checkStatus = document.createElement('span');
            checkStatus.className = 'pull-request-checks';
            checkStatus.dataset.state = checks.state;
            checkStatus.textContent = `· ${checks.label}`;
            link.append(checkStatus);
        }
        link.title = checks ? `${pullRequest.url} · ${checks.label}` : pullRequest.url;
        return link;
    }
    function renderSessions() {
        if (state.sessionActionKey)
            state.sessionActionTrigger = null;
        elements.list.replaceChildren();
        if (!state.sessions.length) {
            const empty = document.createElement('div');
            empty.className = 'sidebar-empty';
            empty.textContent = `No Sessions in ${state.namespace}.`;
            elements.list.append(empty);
            syncSessionActionsMenu();
            return;
        }
        const sections = orderedSessionSections();
        if (!sections.length) {
            for (const session of state.sessions)
                elements.list.append(createSessionListItem(session));
            syncSessionActionsMenu();
            return;
        }
        const sessionsBySection = new Map();
        for (const session of state.sessions) {
            const section = session.section || '';
            if (!sessionsBySection.has(section))
                sessionsBySection.set(section, []);
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
        syncSessionActionsMenu();
    }
    function createSessionListItem(session, draggable = false) {
        const item = document.createElement('div');
        const key = sessionKey(session);
        const assigningSection = state.sectionAssignments.has(key);
        item.className = `session-item${state.selected && sessionKey(state.selected) === key ? ' active' : ''}${assigningSection ? ' section-saving' : ''}`;
        const button = document.createElement('button');
        button.className = 'session-item-select';
        button.type = 'button';
        button.draggable = draggable && !assigningSection;
        const dot = document.createElement('span');
        const displayStatus = sessionDisplayStatus(session);
        dot.className = `phase-dot ${String(displayStatus).toLowerCase().replaceAll(' ', '-')}`;
        const text = document.createElement('span');
        const titleRow = document.createElement('div');
        titleRow.className = 'session-item-title-row';
        const name = document.createElement('div');
        name.className = 'session-item-name';
        name.textContent = sessionDisplayName(session);
        titleRow.append(name);
        const timestamp = createSessionTimestamp(session, true, 'session-item-time');
        if (timestamp)
            titleRow.append(timestamp);
        const meta = document.createElement('div');
        meta.className = 'session-item-meta';
        const provider = document.createElement('span');
        provider.className = 'provider-badge';
        provider.textContent = providerLabel(session.provider);
        const namespace = document.createElement('span');
        namespace.className = 'session-item-namespace';
        namespace.textContent = sessionDisplayName(session) === session.name ? `· ${session.namespace}` : `· ${session.namespace}/${session.name}`;
        const activity = document.createElement('span');
        activity.className = 'session-item-activity';
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
        const actions = document.createElement('button');
        actions.className = 'session-item-actions';
        actions.type = 'button';
        actions.textContent = '…';
        actions.title = `Actions for ${sessionDisplayName(session)}`;
        actions.setAttribute('aria-label', actions.title);
        actions.setAttribute('aria-haspopup', 'menu');
        actions.setAttribute('aria-controls', 'session-actions-menu');
        actions.setAttribute('aria-expanded', 'false');
        actions.dataset.sessionKey = key;
        actions.draggable = false;
        actions.addEventListener('click', event => {
            event.stopPropagation();
            toggleSessionActionsMenu(session, actions);
        });
        item.append(actions);
        if (state.sessionActionKey === key)
            state.sessionActionTrigger = actions;
        const link = createPullRequestLink(session.pullRequest, 'session-item-pull-request');
        if (link) {
            item.classList.add('has-pull-request');
            link.draggable = false;
            item.append(link);
        }
        if (button.draggable)
            configureSessionDrag(item, button, session);
        return item;
    }
    function sessionActionsTarget() {
        return state.sessions.find(session => sessionKey(session) === state.sessionActionKey) || null;
    }
    function sessionActionButtons() {
        return Array.from(elements.list.querySelectorAll('.session-item-actions'));
    }
    function restoreSessionActionFocus(session, fallbackIndex) {
        const buttons = sessionActionButtons();
        const target = buttons.find(button => button.dataset.sessionKey === sessionKey(session));
        const fallback = buttons[Math.min(Math.max(fallbackIndex, 0), buttons.length - 1)];
        (target || fallback || elements.newSessionButton).focus();
    }
    async function runSessionMenuAction(event, action) {
        const session = sessionActionsTarget();
        if (!session)
            return;
        const restoreFocus = event.detail === 0;
        const fallbackIndex = sessionActionButtons().indexOf(state.sessionActionTrigger);
        closeSessionActionsMenu();
        await action(session);
        if (restoreFocus)
            restoreSessionActionFocus(session, fallbackIndex);
    }
    function positionSessionActionsMenu(trigger) {
        const triggerBounds = trigger.getBoundingClientRect();
        const menuBounds = elements.sessionActionsMenu.getBoundingClientRect();
        const margin = 8;
        const gap = 4;
        const left = Math.max(margin, Math.min(triggerBounds.right - menuBounds.width, window.innerWidth - menuBounds.width - margin));
        let top = triggerBounds.bottom + gap;
        if (top + menuBounds.height > window.innerHeight - margin) {
            top = Math.max(margin, triggerBounds.top - menuBounds.height - gap);
        }
        elements.sessionActionsMenu.style.left = `${left}px`;
        elements.sessionActionsMenu.style.top = `${top}px`;
    }
    function closeSessionActionsMenu(restoreFocus = false) {
        const trigger = state.sessionActionTrigger;
        if (trigger)
            trigger.setAttribute('aria-expanded', 'false');
        elements.sessionActionsMenu.hidden = true;
        elements.sessionActionsMenu.removeAttribute('aria-label');
        state.sessionActionKey = '';
        state.sessionActionTrigger = null;
        if (restoreFocus && trigger)
            trigger.focus();
    }
    function sessionLifecycleAction(session) {
        return session?.userSuspended || session?.idleSuspended ? 'resume' : 'suspend';
    }
    function updateSessionActionsMenu(session) {
        const action = sessionLifecycleAction(session);
        elements.sessionActionLifecycle.textContent = action === 'resume' ? 'Resume' : 'Suspend';
        elements.sessionActionReset.disabled = Boolean(session.resetting);
        elements.sessionActionsMenu.setAttribute('aria-label', `Actions for ${sessionDisplayName(session)}`);
    }
    function openSessionActionsMenu(session, trigger) {
        closeSessionActionsMenu();
        state.sessionActionKey = sessionKey(session);
        state.sessionActionTrigger = trigger;
        trigger.setAttribute('aria-expanded', 'true');
        updateSessionActionsMenu(session);
        elements.sessionActionsMenu.hidden = false;
        positionSessionActionsMenu(trigger);
        elements.sessionActionRename.focus();
    }
    function toggleSessionActionsMenu(session, trigger) {
        if (!elements.sessionActionsMenu.hidden && state.sessionActionKey === sessionKey(session)) {
            closeSessionActionsMenu(true);
            return;
        }
        openSessionActionsMenu(session, trigger);
    }
    function syncSessionActionsMenu() {
        if (!state.sessionActionKey)
            return;
        const session = sessionActionsTarget();
        if (!session || !state.sessionActionTrigger) {
            closeSessionActionsMenu();
            return;
        }
        state.sessionActionTrigger.setAttribute('aria-expanded', 'true');
        updateSessionActionsMenu(session);
        positionSessionActionsMenu(state.sessionActionTrigger);
    }
    function handleSessionActionsFocusOut(event) {
        const relatedTarget = event.relatedTarget;
        if (elements.sessionActionsMenu.contains(relatedTarget) || state.sessionActionTrigger?.contains(relatedTarget))
            return;
        if (event.relatedTarget) {
            closeSessionActionsMenu();
            return;
        }
        const trigger = state.sessionActionTrigger;
        window.setTimeout(() => {
            if (trigger !== state.sessionActionTrigger)
                return;
            if (elements.sessionActionsMenu.contains(document.activeElement) || trigger?.contains(document.activeElement))
                return;
            closeSessionActionsMenu();
        }, 0);
    }
    function sectionLabel(section) {
        return section || 'Unsectioned';
    }
    function sectionOrderStorageKey(namespace) {
        return `${sectionOrderStoragePrefix}${namespace}`;
    }
    function uniqueSectionOrder(value) {
        if (!Array.isArray(value))
            return [];
        const seen = new Set();
        return value.filter((section) => {
            if (typeof section !== 'string' || seen.has(section))
                return false;
            seen.add(section);
            return true;
        });
    }
    function savedSectionOrder(namespace = state.namespace) {
        const cached = state.sectionOrders.get(namespace);
        if (cached)
            return cached;
        let order = [];
        try {
            order = uniqueSectionOrder(JSON.parse(window.localStorage.getItem(sectionOrderStorageKey(namespace)) || '[]'));
        }
        catch (_) {
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
        }
        catch (_) {
            return false;
        }
    }
    function orderedSessionSections() {
        const available = Array.from(new Set(state.sessions.map(session => session.section || '')));
        if (!available.some(Boolean))
            return [];
        if (!available.includes(''))
            available.push('');
        const availableSet = new Set(available);
        const ordered = savedSectionOrder().filter(section => availableSet.delete(section));
        const remaining = Array.from(availableSet).sort((left, right) => {
            if (!left)
                return 1;
            if (!right)
                return -1;
            return left.localeCompare(right, undefined, { sensitivity: 'base' });
        });
        return ordered.concat(remaining);
    }
    function reorderSections(sections, section, target, after) {
        if (section === target || !sections.includes(section) || !sections.includes(target))
            return sections;
        const reordered = sections.filter(item => item !== section);
        let targetIndex = reordered.indexOf(target);
        if (after)
            targetIndex += 1;
        reordered.splice(targetIndex, 0, section);
        return reordered;
    }
    function focusSectionOrderControl(section, direction) {
        const controls = Array.from(elements.list.querySelectorAll('.session-section-order-button'))
            .filter(button => button.dataset.section === section);
        const preferred = controls.find(button => Number(button.dataset.direction) === direction);
        const target = preferred && !preferred.disabled ? preferred : controls.find(button => !button.disabled);
        if (target)
            target.focus();
    }
    function applySectionOrder(order, section, focusDirection = 0) {
        const persisted = storeSectionOrder(order);
        renderSessions();
        if (focusDirection)
            focusSectionOrderControl(section, focusDirection);
        showToast(persisted
            ? `Moved ${sectionLabel(section)} section`
            : `Moved ${sectionLabel(section)} section, but browser storage is unavailable`);
    }
    function moveSectionByOffset(section, offset) {
        const sections = orderedSessionSections();
        const index = sections.indexOf(section);
        const targetIndex = index + offset;
        if (index < 0 || targetIndex < 0 || targetIndex >= sections.length)
            return;
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
    function configureSessionDrag(item, handle, session) {
        handle.addEventListener('dragstart', event => {
            state.sidebarDrag = { kind: 'session', session };
            item.classList.add('dragging');
            event.dataTransfer.effectAllowed = 'move';
            event.dataTransfer.setData('text/plain', sessionKey(session));
        });
        handle.addEventListener('dragend', () => {
            item.classList.remove('dragging');
            finishSidebarDrag();
        });
    }
    function configureSectionDrag(group, heading, title, section) {
        title.addEventListener('dragstart', event => {
            state.sidebarDrag = { kind: 'section', section };
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
            if (!drag)
                return;
            if (drag.kind === 'session') {
                if ((drag.session.section || '') === section)
                    return;
                event.preventDefault();
                clearSidebarDropIndicators();
                group.classList.add('session-drop-target');
            }
            else {
                if (drag.section === section)
                    return;
                event.preventDefault();
                clearSidebarDropIndicators();
                const bounds = heading.getBoundingClientRect();
                group.classList.add(event.clientY >= bounds.top + bounds.height / 2 ? 'section-drop-after' : 'section-drop-before');
            }
            event.dataTransfer.dropEffect = 'move';
        });
        group.addEventListener('drop', event => {
            const drag = state.sidebarDrag;
            if (!drag)
                return;
            if (drag.kind === 'session') {
                if ((drag.session.section || '') === section)
                    return;
                event.preventDefault();
                finishSidebarDrag();
                void moveSessionToSection(drag.session, section);
                return;
            }
            if (drag.section === section)
                return;
            event.preventDefault();
            const bounds = heading.getBoundingClientRect();
            const reordered = reorderSections(orderedSessionSections(), drag.section, section, event.clientY >= bounds.top + bounds.height / 2);
            finishSidebarDrag();
            applySectionOrder(reordered, drag.section);
        });
    }
    function sessionSectionNames() {
        return Array.from(new Set(state.sessions.map(session => session.section).filter((section) => Boolean(section))))
            .sort((left, right) => left.localeCompare(right, undefined, { sensitivity: 'base' }));
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
            for (const section of sections)
                addOption(existing, section, section);
            select.append(existing);
        }
        select.selectedIndex = 0;
        if (createNew)
            createOption.selected = true;
        else if (sections.includes(selected))
            select.value = selected;
    }
    function updateCustomSectionField(select, input) {
        const custom = createsNewSection(select);
        input.hidden = !custom;
        input.required = custom;
        if (!custom)
            input.value = '';
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
        return section || includeEmpty ? { section } : {};
    }
    async function saveSessionSectionAssignment(session, section) {
        const generation = state.namespaceGeneration;
        const updated = await api(`/api/sessions/${encodeURIComponent(session.namespace)}/${encodeURIComponent(session.name)}/section`, { method: 'PATCH', body: JSON.stringify({ section }) });
        if (generation !== state.namespaceGeneration)
            return updated;
        state.sessions = state.sessions.map(item => sessionKey(item) === sessionKey(updated) ? updated : item);
        if (state.selected && sessionKey(state.selected) === sessionKey(updated))
            state.selected = updated;
        renderSectionOptions();
        renderSessions();
        renderHeader();
        return updated;
    }
    async function saveSessionDisplayName(session, displayName) {
        const generation = state.namespaceGeneration;
        const updated = await api(`/api/sessions/${encodeURIComponent(session.namespace)}/${encodeURIComponent(session.name)}/display-name`, { method: 'PATCH', body: JSON.stringify({ displayName }) });
        if (generation !== state.namespaceGeneration)
            return updated;
        state.sessions = state.sessions.map(item => sessionKey(item) === sessionKey(updated) ? updated : item);
        if (state.selected && sessionKey(state.selected) === sessionKey(updated))
            state.selected = updated;
        renderSessions();
        renderHeader();
        return updated;
    }
    async function moveSessionToSection(session, section) {
        const key = sessionKey(session);
        if ((session.section || '') === section || state.sectionAssignments.has(key))
            return;
        const generation = state.namespaceGeneration;
        state.sectionAssignments.add(key);
        renderSessions();
        try {
            await saveSessionSectionAssignment(session, section);
            if (generation === state.namespaceGeneration) {
                showToast(section ? `Moved Session to ${section}` : 'Moved Session to Unsectioned');
            }
        }
        catch (error) {
            if (generation === state.namespaceGeneration)
                showToast(errorMessage(error));
        }
        finally {
            state.sectionAssignments.delete(key);
            if (generation === state.namespaceGeneration)
                renderSessions();
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
    async function loadSessions({ quiet = false } = {}) {
        const namespace = state.namespace;
        const generation = state.namespaceGeneration;
        const listGeneration = ++state.sessionListGeneration;
        try {
            const sessions = await api(`/api/sessions?namespace=${encodeURIComponent(namespace)}`);
            if (generation !== state.namespaceGeneration || listGeneration !== state.sessionListGeneration)
                return;
            state.sessions = sessions;
            renderSectionOptions();
            if (state.selected) {
                const selected = state.selected;
                const current = sessions.find(item => sessionKey(item) === sessionKey(selected));
                if (current) {
                    if (state.selected.uid && current.uid && state.selected.uid !== current.uid) {
                        discardSessionView(state.selected);
                        selectSession(current);
                    }
                    else {
                        const beganReset = !state.selected.resetting && current.resetting;
                        const becameReady = (state.selected.phase !== 'Ready' || state.selected.resetting) && current.phase === 'Ready' && !current.resetting;
                        const becameNotReady = state.selected.phase === 'Ready' && current.phase !== 'Ready';
                        state.selected = current;
                        if (beganReset || becameNotReady)
                            closeSocket();
                        if (beganReset) {
                            resetCurrentSessionView();
                        }
                        renderHeader();
                        if (becameReady)
                            connectSocket();
                    }
                }
                else {
                    discardSessionView(state.selected);
                    selectSession(null);
                }
            }
            renderSessions();
        }
        catch (error) {
            if (!quiet && generation === state.namespaceGeneration && listGeneration === state.sessionListGeneration)
                showToast(errorMessage(error));
        }
    }
    async function loadConfig() {
        const config = await api('/api/config');
        state.defaultNamespace = config.defaultNamespace;
        state.namespace = window.localStorage.getItem('kelos-console-namespace') || state.defaultNamespace;
        elements.activeNamespace.value = state.namespace;
        elements.namespace.value = state.namespace;
        for (const label of elements.namespaceLabels)
            label.textContent = state.namespace;
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
        if (yaml && !elements.yaml.value.trim())
            elements.yaml.value = defaultSessionYAML();
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
        }
        catch (error) {
            if (generation !== state.namespaceGeneration)
                return;
            throw error;
        }
        if (generation !== state.namespaceGeneration)
            return;
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
        if (!namespace || namespace === state.namespace)
            return;
        const hadLoadedSource = Boolean(state.loadedSource);
        state.namespace = namespace;
        state.namespaceGeneration += 1;
        state.sessions = [];
        state.resourceGroups = [];
        state.resourceRelationships = [];
        state.options = { credentials: [], workspaces: [], agentConfigs: [], sessions: [] };
        window.localStorage.setItem('kelos-console-namespace', namespace);
        elements.activeNamespace.value = namespace;
        elements.namespace.value = namespace;
        for (const label of elements.namespaceLabels)
            label.textContent = namespace;
        resetNamespaceReferences();
        elements.yaml.value = '';
        if (hadLoadedSource)
            resetSourceValues();
        renderSessionSourceOptions();
        renderSectionOptions();
        renderCredentialOptions();
        renderWorkspaceOptions();
        renderAgentConfigOptions();
        renderOverview();
        renderResourceKindOptions();
        renderResources();
        selectSession(null);
        await Promise.all([loadSessions(), loadOptions(), loadResources()]);
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
        for (const name of state.options.sessions)
            addOption(elements.sessionSource, name, name);
        if (state.options.sessions.includes(previous))
            elements.sessionSource.value = previous;
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
            const option = addOption(elements.credentialSecret, `credential-${index}`, `${credential.name} · ${credentialTypeLabel(credential.type)}`);
            option.dataset.name = credential.name;
            option.dataset.type = credential.type;
        });
        addOption(elements.credentialSecret, customOption, 'Enter another Secret name…');
        if (previous.value === customOption) {
            elements.credentialSecret.value = customOption;
        }
        else if (previous.name) {
            const match = Array.from(elements.credentialSecret.options).find(option => option.dataset.name === previous.name && option.dataset.type === previous.type);
            if (match)
                elements.credentialSecret.value = match.value;
        }
        else if (!credentials.length) {
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
        if (option?.dataset.name)
            return option.dataset.name;
        if (elements.credentialSecret.value === customOption)
            return elements.credentialSecretCustom.value.trim();
        return '';
    }
    function renderWorkspaceOptions() {
        const previous = elements.workspace.value;
        elements.workspace.replaceChildren();
        addOption(elements.workspace, '', 'No workspace');
        for (const name of state.options.workspaces)
            addOption(elements.workspace, name, name);
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
        if (elements.workspace.value === customOption)
            return elements.workspaceCustom.value.trim();
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
        for (const name of available)
            addOption(elements.agentConfig, name, name);
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
        const match = Array.from(elements.credentialSecret.options).find(option => option.dataset.name === name && option.dataset.type === type);
        if (match) {
            elements.credentialSecret.value = match.value;
            elements.credentialSecretCustom.value = '';
        }
        else if (name) {
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
        }
        else {
            elements.workspace.value = customOption;
            elements.workspaceCustom.value = name;
        }
        updateWorkspaceField();
    }
    function sourceFitsForm(manifest) {
        const allowedSpecFields = new Set(['worker', 'suspend', 'initialBranch', 'initialPrompt', 'volumeClaimTemplate']);
        if (Object.keys(manifest.spec).some(key => !allowedSpecFields.has(key)))
            return false;
        if (manifest.spec.suspend === true)
            return false;
        const worker = manifest.spec.worker;
        const allowedWorkerFields = new Set(['type', 'credentials', 'model', 'workspaceRef', 'agentConfigRefs']);
        if (Object.keys(worker).some(key => !allowedWorkerFields.has(key)))
            return false;
        const claim = manifest.spec.volumeClaimTemplate;
        if (!claim)
            return true;
        const allowedClaimFields = new Set(['accessModes', 'resources', 'storageClassName']);
        if (Object.keys(claim).some(key => !allowedClaimFields.has(key)))
            return false;
        if (!Array.isArray(claim.accessModes) || claim.accessModes.length !== 1)
            return false;
        const resources = claim.resources || {};
        if (Object.keys(resources).some(key => key !== 'requests'))
            return false;
        const requests = resources.requests || {};
        return Object.keys(requests).length === 1 && typeof requests.storage === 'string';
    }
    function describeSourceReferences(manifest) {
        const worker = manifest.spec.worker;
        const references = [];
        if (worker.credentials?.secretRef?.name)
            references.push(`Secret ${worker.credentials.secretRef.name}`);
        if (worker.workspaceRef?.name)
            references.push(`Workspace ${worker.workspaceRef.name}`);
        if (worker.agentConfigRefs?.length) {
            references.push(`AgentConfigs ${worker.agentConfigRefs.map(reference => reference.name).join(', ')}`);
        }
        let description = references.length
            ? ` Namespace references: ${references.join('; ')}.`
            : ' No direct credential, Workspace, or AgentConfig references.';
        const advanced = [];
        if (worker.podOverrides)
            advanced.push('Pod overrides');
        if (manifest.spec.volumeClaimTemplate)
            advanced.push('persistent-volume settings');
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
        state.loadedSource = { name: detail.name, namespace: detail.namespace, formCompatible };
        elements.formMode.disabled = !formCompatible;
        if (!formCompatible)
            setCreationMode('yaml');
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
            if (generation !== state.sourceGeneration || namespace !== state.namespace)
                return;
            populateSessionSource(detail);
        }
        catch (error) {
            if (generation === state.sourceGeneration) {
                elements.sessionSource.value = state.loadedSource?.name || '';
                elements.dialogError.textContent = errorMessage(error);
            }
        }
        finally {
            if (generation === state.sourceGeneration)
                setSourceLoading(false);
        }
    }
    function selectSession(session, resumeIdle = false) {
        savePromptDraft(state.selected);
        closeSessionSectionEditor();
        closeSocket();
        saveCurrentSessionView();
        state.selected = session;
        state.currentView = null;
        setActiveView('conversation');
        restorePromptDraft(session);
        renderPendingAttachments();
        elements.messages.replaceChildren();
        elements.pending.replaceChildren();
        elements.changesList.replaceChildren();
        renderSessions();
        renderHeader();
        elements.sidebar.classList.remove('open');
        if (elements.openSidebar)
            elements.openSidebar.setAttribute('aria-expanded', 'false');
        if (!session) {
            resetCurrentSessionView();
            state.replayingHistory = false;
            state.pinHistoryToBottom = false;
            elements.messages.append(elements.welcome || createWelcome());
            return;
        }
        if (resumeIdle && session.idleSuspended)
            resumeIdleSession(session);
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
            if (session.phase === 'Ready')
                connectSocket();
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
        if (session.phase === 'Ready' && !session.resetting)
            connectSocket();
        scheduleBottomAnchor();
    }
    async function resumeIdleSession(session) {
        try {
            await requestSessionLifecycleAction(session, 'resume');
        }
        catch (error) {
            showToast(errorMessage(error));
        }
    }
    async function requestSessionLifecycleAction(session, action) {
        const generation = state.namespaceGeneration;
        const updated = await api(`/api/sessions/${encodeURIComponent(session.namespace)}/${encodeURIComponent(session.name)}/${action}`, { method: 'POST' });
        if (generation !== state.namespaceGeneration)
            return updated;
        state.sessionListGeneration += 1;
        state.sessions = state.sessions.map(item => sessionKey(item) === sessionKey(updated) ? updated : item);
        if (state.selected && sessionKey(state.selected) === sessionKey(updated))
            state.selected = updated;
        renderSessions();
        renderHeader();
        return updated;
    }
    async function suspendSession(session) {
        if (!session || session.userSuspended || state.suspendingSession)
            return;
        const generation = state.namespaceGeneration;
        const key = sessionKey(session);
        const selected = state.selected && sessionKey(state.selected) === key;
        state.suspendingSession = true;
        renderHeader();
        if (selected) {
            closeSocket();
            setConnection('connecting', 'Suspending');
        }
        try {
            await requestSessionLifecycleAction(session, 'suspend');
            if (generation === state.namespaceGeneration)
                showToast('Session suspend requested');
        }
        catch (error) {
            if (generation === state.namespaceGeneration) {
                showToast(errorMessage(error));
                if (selected && state.selected && sessionKey(state.selected) === key)
                    connectSocket();
            }
        }
        finally {
            state.suspendingSession = false;
            renderHeader();
        }
    }
    async function resumeSession(session) {
        if (!session || sessionLifecycleAction(session) !== 'resume' || state.resumingSession)
            return;
        const generation = state.namespaceGeneration;
        state.resumingSession = true;
        renderHeader();
        try {
            await requestSessionLifecycleAction(session, 'resume');
            if (generation === state.namespaceGeneration)
                showToast('Session resume requested');
        }
        catch (error) {
            if (generation === state.namespaceGeneration)
                showToast(errorMessage(error));
        }
        finally {
            state.resumingSession = false;
            renderHeader();
        }
    }
    function toggleSessionSuspension(session) {
        return sessionLifecycleAction(session) === 'resume' ? resumeSession(session) : suspendSession(session);
    }
    function suspendSelectedSession() {
        return suspendSession(state.selected);
    }
    function resumeSelectedSession() {
        return resumeSession(state.selected);
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
        elements.displayNameButton.hidden = !session;
        elements.displayNameButton.disabled = !session;
        renderSelectedSessionSection(session);
        elements.suspendButton.hidden = !session || Boolean(session.userSuspended);
        elements.suspendButton.disabled = !session || Boolean(session.userSuspended) || state.suspendingSession;
        elements.resumeButton.hidden = !Boolean(session?.userSuspended);
        elements.resumeButton.disabled = !Boolean(session?.userSuspended) || state.resumingSession;
        elements.resetButton.disabled = !session || Boolean(session.resetting);
        elements.deleteButton.disabled = !session;
        elements.conversationTab.disabled = !session;
        elements.changesTab.disabled = !session;
        updateComposerAction();
        if (!session) {
            elements.title.textContent = 'Choose a session';
            elements.meta.textContent = 'Select an existing conversation or create one.';
            setConnection('idle', 'Not connected');
            setComposer(false);
            renderRuntimeStatus();
            return;
        }
        elements.title.textContent = sessionDisplayName(session);
        const resourceName = sessionDisplayName(session) === session.name ? session.namespace : `${session.namespace}/${session.name}`;
        const details = [resourceName, providerLabel(session.provider)];
        if (session.model)
            details.push(session.model);
        details.push(sessionDisplayStatus(session));
        if (session.branch)
            details.push(session.branch);
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
        }
        else if (session.phase !== 'Ready') {
            setConnection(session.phase === 'Failed' ? 'error' : 'connecting', session.phase || 'Pending');
            setComposer(!session.phase || session.phase === 'Pending');
        }
        renderRuntimeStatus();
    }
    function setConnection(status, label) {
        elements.connection.dataset.state = status;
        elements.connection.lastChild.textContent = label;
    }
    function setComposer(enabled) {
        elements.input.disabled = !enabled;
        elements.attachFiles.disabled = !enabled || state.sendingMessage;
        elements.input.placeholder = enabled ? 'Message the agent…' : 'Choose a ready session to start chatting';
        updateComposerAction();
    }
    function usesTouchComposer() {
        return window.matchMedia('(pointer: coarse)').matches;
    }
    function composerInterruptAction() {
        return state.activeTurn && (elements.input.disabled || (!elements.input.value.trim() && currentAttachmentFiles().length === 0));
    }
    function updateComposerAction() {
        const connected = state.socket && state.socket.readyState === WebSocket.OPEN;
        const interrupt = composerInterruptAction();
        const goalControl = state.activeTurn && /^\/goal(?:\s|$)/.test(elements.input.value.trim());
        let action = 'send';
        if (interrupt)
            action = 'interrupt';
        else if (goalControl)
            action = 'run goal command';
        else if (state.activeTurn || state.pendingMessage)
            action = 'add to pending';
        const actionSymbol = interrupt ? '■' : '↑';
        elements.send.dataset.action = interrupt ? 'interrupt' : 'send';
        elements.send.textContent = actionSymbol;
        elements.send.setAttribute('aria-label', interrupt ? 'Interrupt active work' : 'Send message');
        elements.send.title = interrupt ? 'Interrupt active work' : 'Send message';
        elements.send.disabled = !connected || state.sendingMessage || (interrupt ? state.interrupting : elements.input.disabled);
        elements.composerHint.textContent = usesTouchComposer()
            ? `Tap ${actionSymbol} to ${action} · Return for a new line · !COMMAND · /goal`
            : (interrupt && elements.input.disabled
                ? `Click ${actionSymbol} to interrupt`
                : `Enter to ${action} · Shift+Enter for a new line · !COMMAND · /goal`);
    }
    function closeSocket() {
        state.socketGeneration += 1;
        if (state.reconnectTimer !== null)
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
        if (!state.selected || state.selected.phase !== 'Ready' || state.selected.resetting)
            return;
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
            if (generation !== state.socketGeneration)
                return;
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
            if (generation !== state.socketGeneration)
                return;
            try {
                handleEvent(JSON.parse(event.data));
            }
            catch (error) {
                showToast(`Could not read Session event: ${errorMessage(error)}`);
            }
        });
        socket.addEventListener('close', () => {
            if (generation !== state.socketGeneration || !state.selected)
                return;
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
        if (!elements.messages.querySelector('.welcome'))
            return;
        elements.messages.replaceChildren();
        if (state.currentView)
            state.currentView.statusPlaceholder = false;
    }
    function trimURLSuffix(value) {
        const openingBrackets = '([{';
        const closingBrackets = ')]}';
        const bracketBalance = [0, 0, 0];
        for (const character of value) {
            const openingIndex = openingBrackets.indexOf(character);
            if (openingIndex >= 0)
                bracketBalance[openingIndex]++;
            const closingIndex = closingBrackets.indexOf(character);
            if (closingIndex >= 0)
                bracketBalance[closingIndex]--;
        }
        let end = value.length;
        while (end > 0) {
            const character = value[end - 1];
            if ('.,;:!?'.includes(character)) {
                end--;
                continue;
            }
            const closingIndex = closingBrackets.indexOf(character);
            if (closingIndex < 0 || bracketBalance[closingIndex] >= 0)
                break;
            bracketBalance[closingIndex]++;
            end--;
        }
        return value.slice(0, end);
    }
    function appendLink(parent, href, label, depth, scanBudget) {
        let url;
        try {
            url = new URL(href);
        }
        catch {
            return false;
        }
        if (url.protocol !== 'http:' && url.protocol !== 'https:')
            return false;
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
            }
            catch {
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
        }
        finally {
            textarea.remove();
        }
        if (!copied)
            throw new Error('Clipboard access is unavailable');
    }
    async function copyCodeBlock(button, content) {
        button.disabled = true;
        try {
            await writeClipboardText(content);
            button.textContent = 'Copied';
            button.setAttribute('aria-label', 'Code copied');
        }
        catch {
            button.textContent = 'Copy failed';
            button.setAttribute('aria-label', 'Copy code failed');
        }
        finally {
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
        return { remaining: Math.max(1024, value.length * 8), exhausted: false };
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
        if (labelEnd < 0)
            return null;
        let parentheses = 0;
        for (let index = labelEnd + 2; index < value.length; index++) {
            if (!consumeScanBudget(scanBudget))
                return null;
            if (value[index] === '\\') {
                index++;
                continue;
            }
            if (value[index] === '(')
                parentheses++;
            if (value[index] !== ')')
                continue;
            if (parentheses > 0) {
                parentheses--;
                continue;
            }
            const target = value.slice(labelEnd + 2, index).trim();
            const destination = target.match(/^<([^>]+)>|^(\S+)/);
            if (!destination)
                return null;
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
        if (!isExactDelimiterRun(value, index, marker) || !after || /\s/u.test(after))
            return false;
        return marker[0] !== '_' || !isAlphanumeric(before) || !isAlphanumeric(after);
    }
    function findClosingDelimiter(value, index, marker, scanBudget) {
        let closing = findWithBudget(value, marker, index + marker.length, scanBudget);
        while (closing >= 0) {
            const before = value[closing - 1] || '';
            const after = value[closing + marker.length] || '';
            const intrawordUnderscore = marker[0] === '_' && isAlphanumeric(before) && isAlphanumeric(after);
            if (isExactDelimiterRun(value, closing, marker) && before && !/\s/u.test(before) && !intrawordUnderscore)
                return closing;
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
        const escapedPunctuation = String.raw `\\` + '`*{}[]()#+-.!_>|';
        let textStart = 0;
        let index = 0;
        const appendTextBefore = (end) => {
            if (end > textStart)
                parent.append(document.createTextNode(value.slice(textStart, end)));
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
                while (value[markerEnd] === '`')
                    markerEnd++;
                if (!consumeScanBudget(scanBudget, markerEnd - index))
                    break;
                const marker = value.slice(index, markerEnd);
                const closing = findWithBudget(value, marker, markerEnd, scanBudget);
                if (scanBudget.exhausted)
                    break;
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
                if (scanBudget.exhausted)
                    break;
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
                if (!value.startsWith(marker, index) || !canOpenDelimiter(value, index, marker))
                    continue;
                const closing = findClosingDelimiter(value, index, marker, scanBudget);
                if (scanBudget.exhausted)
                    break;
                if (closing <= index + marker.length)
                    continue;
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
            if (scanBudget.exhausted)
                break;
            if (matchedDelimiter)
                continue;
            index++;
        }
        appendTextBefore(value.length);
    }
    function matchFence(line) {
        const match = line.match(/^ {0,3}(`{3,}|~{3,})(.*)$/);
        if (!match || (match[1][0] === '`' && match[2].includes('`')))
            return null;
        return { marker: match[1], info: match[2].trim() };
    }
    function isClosingFence(line, marker) {
        const value = line.replace(/^ {0,3}/, '');
        let markerEnd = 0;
        while (value[markerEnd] === marker[0])
            markerEnd++;
        return markerEnd >= marker.length && value.slice(markerEnd).trim() === '';
    }
    function matchListItem(line) {
        const match = line.match(/^( {0,3})([-+*]|\d+[.)])[\t ]+(.*)$/);
        if (!match)
            return null;
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
            while (value[end] === '`')
                end++;
            const length = end - index;
            let backslashes = 0;
            for (let previous = index - 1; previous >= 0 && value[previous] === '\\'; previous--)
                backslashes++;
            if (!runsByLength.has(length))
                runsByLength.set(length, []);
            runsByLength.get(length).push({ start: index, end, escaped: backslashes % 2 === 1 });
            index = end;
        }
        const matches = new Map();
        for (const runs of runsByLength.values()) {
            for (let index = 0; index + 1 < runs.length; index++) {
                if (!runs[index].escaped)
                    matches.set(runs[index].start, runs[index + 1].end);
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
        if (!hasSeparator)
            return null;
        if (value.startsWith('|'))
            cells.shift();
        if (trailingSeparator)
            cells.pop();
        return cells;
    }
    function tableAlignments(line) {
        const cells = splitTableRow(line);
        if (!cells || cells.length === 0)
            return null;
        const alignments = [];
        for (const cell of cells) {
            if (!/^:?-+:?$/.test(cell))
                return null;
            if (cell.startsWith(':') && cell.endsWith(':'))
                alignments.push('center');
            else if (cell.endsWith(':'))
                alignments.push('right');
            else if (cell.startsWith(':'))
                alignments.push('left');
            else
                alignments.push('');
        }
        return alignments;
    }
    function matchTable(lines, index) {
        if (index + 1 >= lines.length)
            return null;
        const headers = splitTableRow(lines[index]);
        const alignments = tableAlignments(lines[index + 1]);
        if (!headers || !alignments || headers.length !== alignments.length)
            return null;
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
                }
                else {
                    rows.push(headers.map((_, cellIndex) => cells[cellIndex] || ''));
                    renderedCells += headers.length;
                }
            }
            next++;
        }
        return { headers, alignments, rows, next, oversized };
    }
    function appendTable(parent, tableData) {
        const container = document.createElement('div');
        container.className = 'markdown-table-container';
        const table = document.createElement('table');
        const head = document.createElement('thead');
        const headerRow = document.createElement('tr');
        tableData.headers.forEach((value, index) => {
            const header = document.createElement('th');
            if (tableData.alignments[index])
                header.className = `table-align-${tableData.alignments[index]}`;
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
                    if (tableData.alignments[index])
                        cell.className = `table-align-${tableData.alignments[index]}`;
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
                if (index < lines.length)
                    index++;
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
                    if (!quote)
                        break;
                    quoteLines.push(quote[1]);
                    index++;
                }
                const blockquote = document.createElement('blockquote');
                if (depth < 20)
                    appendMarkdownBlocks(blockquote, quoteLines.join('\n'), depth + 1);
                else
                    appendInlineMarkdown(blockquote, quoteLines.join('\n'), depth + 1);
                parent.append(blockquote);
                continue;
            }
            const firstItem = matchListItem(lines[index]);
            if (firstItem) {
                const list = document.createElement(firstItem.ordered ? 'ol' : 'ul');
                if (firstItem.ordered && firstItem.start !== 1)
                    list.start = firstItem.start;
                while (index < lines.length) {
                    const item = matchListItem(lines[index]);
                    if (!item || item.ordered !== firstItem.ordered || item.indent !== firstItem.indent)
                        break;
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
                    if (depth < 20)
                        appendMarkdownBlocks(listItem, itemLines.join('\n'), depth + 1);
                    else
                        appendInlineMarkdown(listItem, itemLines.join('\n'), depth + 1);
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
                }
                else {
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
    function sessionRequestID(prefix) {
        if (globalThis.crypto?.randomUUID)
            return globalThis.crypto.randomUUID();
        return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
    }
    function sessionHistoryRequestID() {
        return sessionRequestID('history');
    }
    function renderHistoryControl() {
        let control = elements.messages.querySelector('.history-page-control');
        if (!state.historyCursor && !state.historyPageLoading) {
            if (control)
                control.remove();
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
        if (!button)
            return;
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
        if (state.historyPageLoading || !state.historyCursor || !state.socket || state.socket.readyState !== 1)
            return;
        const requestID = sessionHistoryRequestID();
        state.historyPageLoading = true;
        state.historyRequestID = requestID;
        renderHistoryControl();
        try {
            state.socket.send(JSON.stringify({ type: 'history', requestId: requestID, historyCursor: state.historyCursor }));
        }
        catch (error) {
            cancelOlderHistoryPage();
            showToast(`Could not load earlier messages: ${errorMessage(error)}`);
        }
    }
    function applyHistoryState(historyState) {
        if (!historyState)
            return;
        const activeTurnID = historyState.activeTurnId || '';
        if (activeTurnID) {
            const projectedBubble = state.assistantSegmentByTurn.get('current');
            const projectedText = state.assistantTextByTurn.get('current');
            if (projectedBubble)
                state.assistantSegmentByTurn.set(activeTurnID, projectedBubble);
            if (projectedText !== undefined)
                state.assistantTextByTurn.set(activeTurnID, projectedText);
            state.assistantSegmentByTurn.delete('current');
            state.assistantTextByTurn.delete('current');
        }
        state.activeTurn = Boolean(activeTurnID);
        state.activeTurnID = activeTurnID;
        const startedAt = Date.parse(historyState.activeTurnStarted || '');
        state.activeTurnStartedAt = Number.isNaN(startedAt) ? 0 : startedAt;
        state.waitingForInput = Boolean(historyState.waitingForInput);
        state.interrupting = Boolean(historyState.turnInterrupting);
        if (historyState.fileDiff)
            updateFileChanges(parseFileDiffs(historyState.fileDiff));
        elements.pending.replaceChildren();
        elements.pending.hidden = true;
        state.pendingMessage = null;
        if (historyState.pendingTurn) {
            const turn = historyState.pendingTurn;
            renderPendingUser({
                type: 'user.message',
                turnId: turn.turnId,
                text: turn.text,
                revision: turn.revision,
                attachments: turn.attachments || [],
            });
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
            if (!block.dirty)
                continue;
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
        for (const event of events)
            handleEvent(event);
        for (const [name, diff] of live.fileChanges)
            state.fileChanges.set(name, diff);
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
        updateCurrentRequest();
        refreshSessionProgress();
        updateComposerAction();
    }
    function finishOlderHistoryPage(event) {
        if (event.requestId !== state.historyRequestID)
            return;
        const events = state.historyPageEvents;
        state.historyCursor = state.historyPageCursor;
        state.historyPageLoading = false;
        state.historyPageReading = false;
        state.historyPageCursor = '';
        state.historyPageEvents = [];
        state.historyRequestID = '';
        if (state.currentView)
            state.currentView.historyCursor = state.historyCursor;
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
        if (pinToBottom)
            scheduleBottomAnchor();
        state.pinHistoryToBottom = false;
        updateCurrentRequest();
    }
    function handleEvent(event) {
        if (state.historyPageReading) {
            if (event.type === 'history.end' && event.historyPage) {
                finishOlderHistoryPage(event);
                return;
            }
            if (event.type === 'error' && event.requestId === state.historyRequestID) {
                cancelOlderHistoryPage();
            }
            else if (event.type === 'history.start' && !event.historyPage) {
                cancelOlderHistoryPage();
            }
            else {
                state.historyPageEvents.push(event);
                return;
            }
        }
        if (event.id)
            state.lastEventID = Math.max(state.lastEventID, event.id);
        const recoveredCompletion = state.runtimeRecoveryActive && event.type === 'turn.completed' && event.status === 'interrupted';
        if (state.runtimeRecoveryActive && !isRuntimeRecoveryEvent(event))
            state.runtimeRecoveryActive = false;
        switch (event.type) {
            case 'history.start': {
                if (event.historyPage) {
                    if (!state.historyPageLoading || event.requestId !== state.historyRequestID)
                        return;
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
                if (state.currentView && event.journalId)
                    state.currentView.journalID = event.journalId;
                if (replaceHistoryCursor)
                    state.historyCursor = event.historyCursor || '';
                state.historyLastEventID = event.lastEventId || 0;
                state.replayingHistory = true;
                break;
            }
            case 'history.end':
                if (!event.historyPage)
                    finishHistoryReplay(event.historyState);
                break;
            case 'runtime.status':
                state.runtimeStatus = event.runtime || null;
                if (state.currentView)
                    state.currentView.runtimeStatus = state.runtimeStatus;
                renderRuntimeStatus();
                break;
            case 'runtime.recovered':
                state.runtimeRecoveryActive = true;
                renderRecovery(event);
                break;
            case 'user.message':
                renderUser(event);
                break;
            case 'user.message.updated':
                renderPendingUser(event);
                break;
            case 'user.message.removed':
                discardPendingMessage(event.turnId);
                if (!state.replayingHistory)
                    showToast('Pending message removed');
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
                acceptPendingMessage(event.turnId);
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
            case 'tool.delta':
                appendToolDelta(event);
                break;
            case 'tool.completed':
                endAssistantSegment(event.turnId);
                completeTool(event);
                break;
            case 'goal.updated':
                endAssistantSegment(event.turnId);
                renderGoal(event);
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
                if (event.requestId && event.requestId === state.historyRequestID)
                    cancelOlderHistoryPage();
                renderError(event);
                break;
        }
    }
    function renderUser(event) {
        if (event.turnId) {
            renderPendingUser(event);
            return;
        }
        renderAcceptedUser(event);
    }
    let currentRequestRow = null;
    let currentRequestUpdateFrame = null;
    function hideCurrentRequest() {
        if (!currentRequestRow && elements.currentRequest.hidden)
            return;
        currentRequestRow = null;
        elements.currentRequest.hidden = true;
    }
    function updateCurrentRequest() {
        if (elements.sessionsView.hidden)
            return;
        if (elements.messages.hidden) {
            hideCurrentRequest();
            return;
        }
        const viewportTop = elements.messages.getBoundingClientRect().top;
        let request = null;
        for (const row of elements.messages.querySelectorAll('.event-row.user')) {
            const bounds = row.getBoundingClientRect();
            if (bounds.top > viewportTop)
                break;
            if (bounds.bottom > viewportTop) {
                request = null;
                break;
            }
            request = row;
        }
        const text = request?.dataset.requestText || '';
        if (!request || !text) {
            hideCurrentRequest();
            return;
        }
        if (currentRequestRow === request && !elements.currentRequest.hidden && elements.currentRequestText.textContent === text)
            return;
        currentRequestRow = request;
        elements.currentRequestText.textContent = text;
        elements.currentRequestButton.title = text;
        elements.currentRequest.hidden = false;
    }
    function scheduleCurrentRequestUpdate() {
        if (currentRequestUpdateFrame !== null)
            return;
        currentRequestUpdateFrame = window.requestAnimationFrame(() => {
            currentRequestUpdateFrame = null;
            updateCurrentRequest();
        });
    }
    function jumpToCurrentRequest() {
        const request = currentRequestRow;
        if (!request)
            return;
        hideCurrentRequest();
        request.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
    function renderAcceptedUser(event) {
        ensureConversation();
        const row = document.createElement('div');
        row.className = 'event-row user';
        row.dataset.requestText = event.text || (event.attachments || []).map(attachment => attachment.name).join(', ');
        const message = document.createElement('div');
        message.className = 'user-message';
        const bubble = document.createElement('div');
        bubble.className = 'message-bubble';
        renderMessageMarkdown(bubble, event.text);
        appendMessageAttachments(bubble, event.attachments || []);
        message.append(bubble);
        row.append(message);
        elements.messages.append(row);
        scrollToBottom();
    }
    function renderPendingUser(event) {
        const existing = state.pendingMessage;
        if (existing) {
            if (existing.event.turnId !== event.turnId) {
                renderAcceptedUser(event);
                return;
            }
            existing.event = { ...event, revision: event.revision || 1 };
            renderPendingMessageContent(existing);
            return;
        }
        const item = document.createElement('div');
        item.className = 'pending-message-card';
        const text = document.createElement('div');
        text.className = 'pending-message-text';
        const actions = document.createElement('div');
        actions.className = 'pending-message-actions';
        item.append(text, actions);
        elements.pending.append(item);
        elements.pending.hidden = false;
        const pending = { event: { ...event, revision: event.revision || 1 }, item, text, actions };
        state.pendingMessage = pending;
        renderPendingMessageContent(pending);
    }
    function pendingMessageText(event) {
        const names = (event.attachments || []).map(attachment => attachment.name);
        return [event.text, names.length ? `Attachments: ${names.join(', ')}` : ''].filter(Boolean).join(' · ');
    }
    function renderPendingMessageContent(pending) {
        pending.item.classList.remove('editing');
        pending.text.textContent = pendingMessageText(pending.event);
        pending.actions.replaceChildren();
        const status = document.createElement('span');
        status.className = 'pending-message-status';
        status.textContent = 'Pending';
        const edit = document.createElement('button');
        edit.type = 'button';
        edit.className = 'pending-message-edit';
        edit.textContent = 'Edit';
        edit.setAttribute('aria-label', 'Edit pending message');
        edit.addEventListener('click', () => editPendingUser(pending.event.turnId));
        const remove = document.createElement('button');
        remove.type = 'button';
        remove.className = 'pending-message-remove';
        remove.textContent = 'Remove';
        remove.setAttribute('aria-label', 'Remove pending message');
        remove.addEventListener('click', () => removePendingUser(pending.event.turnId));
        pending.actions.append(status, edit, remove);
    }
    function removePendingUser(turnID) {
        const pending = state.pendingMessage;
        if (!pending || pending.event.turnId !== turnID)
            return;
        if (!window.confirm('Remove this pending message?'))
            return;
        if (!state.socket || state.socket.readyState !== WebSocket.OPEN) {
            showToast('Session is disconnected');
            return;
        }
        state.socket.send(JSON.stringify({
            type: 'message.remove',
            requestId: sessionRequestID('message-remove'),
            turnId: pending.event.turnId,
            expectedRevision: pending.event.revision,
        }));
    }
    function editPendingUser(turnID) {
        const pending = state.pendingMessage;
        if (!pending || pending.event.turnId !== turnID)
            return;
        pending.item.classList.add('editing');
        const input = document.createElement('textarea');
        input.className = 'pending-message-input';
        input.value = pending.event.text || '';
        input.setAttribute('aria-label', 'Pending message');
        pending.text.replaceChildren(input);
        const cancel = document.createElement('button');
        cancel.type = 'button';
        cancel.className = 'pending-message-edit';
        cancel.textContent = 'Cancel';
        cancel.addEventListener('click', () => renderPendingMessageContent(pending));
        const save = document.createElement('button');
        save.type = 'button';
        save.className = 'pending-message-save';
        save.textContent = 'Save';
        save.addEventListener('click', () => {
            const text = input.value.trim();
            if (!text && !(pending.event.attachments || []).length)
                return;
            if (!state.socket || state.socket.readyState !== WebSocket.OPEN) {
                showToast('Session is disconnected');
                return;
            }
            state.socket.send(JSON.stringify({
                type: 'message.edit',
                requestId: sessionRequestID('message-edit'),
                turnId: pending.event.turnId,
                text,
                expectedRevision: pending.event.revision,
            }));
            renderPendingMessageContent(pending);
        });
        pending.actions.replaceChildren(cancel, save);
        if (typeof input.focus === 'function')
            input.focus();
    }
    function attachmentURL(attachment) {
        if (!state.selected)
            return '#';
        return `/api/sessions/${encodeURIComponent(state.selected.namespace)}/${encodeURIComponent(state.selected.name)}/attachments/${encodeURIComponent(attachment.id)}`;
    }
    function appendMessageAttachments(parent, attachments) {
        if (!attachments.length)
            return;
        const list = document.createElement('div');
        list.className = 'message-attachments';
        for (const attachment of attachments) {
            const link = document.createElement('a');
            link.className = 'message-attachment';
            link.href = attachmentURL(attachment);
            link.target = '_blank';
            link.rel = 'noopener noreferrer';
            if ((attachment.mediaType || '').startsWith('image/')) {
                const preview = document.createElement('img');
                preview.src = link.href;
                preview.alt = attachment.name;
                preview.loading = 'lazy';
                link.append(preview);
            }
            const label = document.createElement('span');
            label.textContent = attachment.name;
            link.append(label);
            list.append(link);
        }
        parent.append(list);
    }
    function acceptPendingMessage(turnID) {
        const pending = state.pendingMessage;
        if (!pending || pending.event.turnId !== turnID)
            return;
        pending.item.remove();
        state.pendingMessage = null;
        elements.pending.hidden = true;
        renderAcceptedUser(pending.event);
    }
    function discardPendingMessage(turnID) {
        const pending = state.pendingMessage;
        if (!pending || pending.event.turnId !== turnID)
            return;
        pending.item.remove();
        state.pendingMessage = null;
        elements.pending.hidden = true;
        updateComposerAction();
    }
    function assistantBubble(turnID) {
        const key = turnID || 'current';
        let bubble = state.assistantSegmentByTurn.get(key);
        if (bubble)
            return bubble;
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
        if (bubble)
            renderMessageMarkdown(bubble, state.assistantTextByTurn.get(key) || '');
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
        if (tail?.nodeType === 3)
            tail.appendData(delta);
        else
            bubble.append(document.createTextNode(delta));
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
        if (event.toolId && state.tools.has(event.toolId))
            return;
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
        if (event.toolId)
            state.tools.set(event.toolId, card);
        scrollToBottom();
    }
    function appendToolDelta(event) {
        let card = state.tools.get(event.toolId);
        if (!card) {
            renderTool({ ...event, status: 'running' });
            card = state.tools.get(event.toolId);
        }
        if (!card)
            return;
        card.toolOutput = (card.toolOutput || '') + (event.output || '');
        renderToolOutput(card, card.toolOutput);
        scrollToBottom();
    }
    function toolOutputPreview(output, maxLines = 5) {
        const text = String(output || '').replace(/\r\n?/g, '\n').replace(/\n+$/, '');
        if (!text)
            return { text: '', fullText: '', totalLines: 0, omittedLines: 0 };
        const lines = text.split('\n');
        if (lines.length <= maxLines) {
            return { text, fullText: text, totalLines: lines.length, omittedLines: 0 };
        }
        const head = Math.floor((maxLines - 1) / 2);
        const tail = maxLines - 1 - head;
        const omittedLines = lines.length - head - tail;
        const preview = [
            ...lines.slice(0, head),
            `… +${omittedLines} lines`,
            ...lines.slice(lines.length - tail),
        ];
        return { text: preview.join('\n'), fullText: text, totalLines: lines.length, omittedLines };
    }
    function renderToolOutput(card, output) {
        const preview = toolOutputPreview(output);
        if (!preview.text)
            return;
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
            renderTool({ ...event, status: event.status || 'completed' });
            return;
        }
        card.dataset.status = event.status || 'completed';
        card.querySelector('.tool-status').textContent = event.status || 'completed';
        card.querySelector('.tool-icon').textContent = event.status === 'failed' ? '!' : '✓';
        card.toolOutput = event.output || card.toolOutput || '';
        renderToolOutput(card, card.toolOutput);
        scrollToBottom();
    }
    function renderGoal(event) {
        ensureConversation();
        const card = document.createElement('div');
        card.className = 'goal-card';
        if (!event.goal) {
            card.textContent = event.status === 'cleared' ? 'Goal cleared.' : 'No goal is currently set.';
        }
        else {
            const goal = event.goal;
            let usage = '';
            if (goal.tokenBudget != null)
                usage = ` · ${goal.tokensUsed || 0}/${goal.tokenBudget} tokens`;
            else if (goal.tokensUsed)
                usage = ` · ${goal.tokensUsed} tokens`;
            const status = document.createElement('strong');
            status.textContent = `Goal ${goal.status}${usage}`;
            const objective = document.createElement('div');
            objective.textContent = goal.objective || '';
            card.append(status, objective);
        }
        elements.messages.append(card);
        scrollToBottom();
    }
    function bindOtherAnswer(controls, other, multiSelect) {
        if (multiSelect)
            return;
        for (const control of controls) {
            control.addEventListener('change', () => {
                if (control.checked)
                    other.value = '';
            });
        }
        other.addEventListener('input', () => {
            if (!other.value.trim())
                return;
            controls.forEach(control => { control.checked = false; });
        });
    }
    function renderInputRequest(event) {
        ensureConversation();
        if (!event.inputId || state.inputs.has(event.inputId))
            return;
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
            bindOtherAnswer(controls, other, question.multiSelect);
            fieldset.append(legend, prompt, choices, other);
            card.append(fieldset);
            rows.push({ question, controls, other });
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
                if (other)
                    values = row.question.multiSelect ? [...values, other] : [other];
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
        if (!state.socket || state.socket.readyState !== WebSocket.OPEN)
            return;
        state.socket.send(JSON.stringify({ type: 'input', inputId, ...(answers ? { answers } : {}), ...(cancel ? { cancel: true } : {}) }));
    }
    function resolveInputCard(event) {
        const card = state.inputs.get(event.inputId);
        if (!card)
            return;
        card.querySelector('.input-eyebrow').textContent = `Input ${event.status || 'resolved'}`;
        card.querySelectorAll('button, input').forEach(control => { control.disabled = true; });
    }
    function renderDiff(event) {
        if (!event.diff)
            return;
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
            block = { count, list, files: new Map() };
            state.diffs.set(key, block);
        }
        for (const file of files)
            block.files.set(file.name, file.diff);
        if (state.replayingHistory) {
            block.dirty = true;
            block.openFirst = block.openFirst || created;
        }
        else {
            renderDiffBlock(block, created);
            if (created)
                scrollToBottom();
        }
    }
    function updateFileChanges(files) {
        for (const file of files)
            state.fileChanges.set(file.name, file.diff);
        if (state.replayingHistory)
            state.fileChangesDirty = true;
        else
            renderFileChanges();
    }
    function parseFileDiffs(diff) {
        const lines = diff.split('\n');
        const starts = [];
        lines.forEach((line, index) => {
            if (line.startsWith('diff --git ') || /^\*\*\* (?:Add|Delete|Update) File: /.test(line))
                starts.push(index);
        });
        if (!starts.length)
            starts.push(0);
        return starts.map((start, index) => {
            const segment = lines.slice(start, starts[index + 1] ?? lines.length);
            return { name: diffFileName(segment) || 'File changes', diff: segment.join('\n') };
        });
    }
    function diffFileName(lines) {
        const patchHeader = lines.find(line => /^\*\*\* (?:Add|Delete|Update) File: /.test(line));
        if (patchHeader)
            return patchHeader.replace(/^\*\*\* (?:Add|Delete|Update) File: /, '');
        for (const prefix of ['+++ ', '--- ']) {
            const header = lines.find(line => line.startsWith(prefix));
            if (!header)
                continue;
            const path = normalizeDiffPath(header.slice(prefix.length));
            if (path !== '/dev/null')
                return path;
        }
        const header = lines.find(line => line.startsWith('diff --git '));
        if (!header)
            return '';
        const quotedPath = header.match(/ ("(?:\\.|[^"\\])*")$/)?.[1];
        if (quotedPath)
            return normalizeDiffPath(quotedPath);
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
        const escapedBytes = { a: 7, b: 8, t: 9, n: 10, v: 11, f: 12, r: 13, '\\': 92, '"': 34 };
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
            if (Object.prototype.hasOwnProperty.call(escapedBytes, escaped))
                bytes.push(escapedBytes[escaped]);
            else
                append(escaped);
            index++;
        }
        return new TextDecoder().decode(new Uint8Array(bytes));
    }
    function renderFileChanges() {
        const openFiles = new Set([...elements.changesList.querySelectorAll('.file-change[open]')].map(item => item.dataset.path));
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
        const openFiles = new Set([...block.list.querySelectorAll('.file-change[open]')].map(item => item.dataset.path));
        if (created && block.files.size === 1)
            openFiles.add(block.files.keys().next().value);
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
            if (kind === 'added')
                added++;
            if (kind === 'removed')
                removed++;
        }
        return { added, removed };
    }
    function diffChangeKind(line) {
        if (line.startsWith('+') && !line.startsWith('+++ '))
            return 'added';
        if (line.startsWith('-') && !line.startsWith('--- '))
            return 'removed';
        return '';
    }
    function renderDiffLines(diff) {
        const lines = document.createElement('div');
        lines.className = 'diff-lines';
        for (const text of diff.split('\n')) {
            const line = document.createElement('div');
            line.className = 'diff-line';
            const kind = diffChangeKind(text);
            if (kind)
                line.classList.add(kind);
            if (text.startsWith('@@'))
                line.classList.add('hunk');
            if (/^(diff --git |index |--- |\+\+\+ )/.test(text))
                line.classList.add('metadata');
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
        if (changesActive)
            hideCurrentRequest();
        else
            updateCurrentRequest();
    }
    function handleViewTabKeydown(event) {
        const tabs = [elements.conversationTab, elements.changesTab].filter(tab => !tab.disabled);
        const current = tabs.indexOf(event.target);
        if (current < 0)
            return;
        let target;
        if (event.key === 'ArrowLeft')
            target = tabs[(current - 1 + tabs.length) % tabs.length];
        if (event.key === 'ArrowRight')
            target = tabs[(current + 1) % tabs.length];
        if (event.key === 'Home')
            target = tabs[0];
        if (event.key === 'End')
            target = tabs[tabs.length - 1];
        if (!target)
            return;
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
        acceptPendingMessage(event.turnId);
        updateComposerAction();
        refreshSessionProgress();
        if (event.status === 'interrupted' && !state.replayingHistory)
            showToast('Active work interrupted');
        const divider = document.createElement('div');
        divider.className = 'turn-divider';
        if (elapsed !== null)
            divider.textContent = `Worked for ${formatSessionProgressElapsed(elapsed)}`;
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
            if (state.pinHistoryToBottom)
                scheduleBottomAnchor();
            return;
        }
        const distance = messagesBottomDistance();
        if (distance < 240 || !smooth) {
            elements.messages.scrollTo({ top: elements.messages.scrollHeight, behavior: smooth ? 'smooth' : 'auto' });
        }
    }
    function messagesBottomDistance() {
        return elements.messages.scrollHeight - elements.messages.scrollTop - elements.messages.clientHeight;
    }
    function messagesNearBottom() {
        return messagesBottomDistance() < 240;
    }
    function scheduleBottomAnchor() {
        if (state.bottomScrollFrame !== null)
            return;
        state.bottomScrollFrame = window.requestAnimationFrame(() => {
            state.bottomScrollFrame = null;
            const scrollBehavior = elements.messages.style.scrollBehavior;
            elements.messages.style.scrollBehavior = 'auto';
            elements.messages.scrollTop = elements.messages.scrollHeight;
            elements.messages.style.scrollBehavior = scrollBehavior;
        });
    }
    function currentAttachmentFiles() {
        if (!state.selected)
            return [];
        return state.attachmentDrafts.get(sessionKey(state.selected)) || [];
    }
    function renderPendingAttachments() {
        const files = currentAttachmentFiles();
        elements.pendingAttachments.replaceChildren();
        elements.pendingAttachments.hidden = files.length === 0;
        files.forEach((file, index) => {
            const item = document.createElement('span');
            item.className = 'pending-attachment';
            item.textContent = file.name;
            const remove = document.createElement('button');
            remove.type = 'button';
            remove.setAttribute('aria-label', `Remove ${file.name}`);
            remove.textContent = '×';
            remove.addEventListener('click', () => {
                const remaining = currentAttachmentFiles().filter((_, fileIndex) => fileIndex !== index);
                state.attachmentDrafts.set(sessionKey(state.selected), remaining);
                renderPendingAttachments();
                updateComposerAction();
            });
            item.append(remove);
            elements.pendingAttachments.append(item);
        });
    }
    function stageAttachments(fileList) {
        if (!state.selected || elements.input.disabled)
            return;
        const incoming = Array.from(fileList || []);
        const existing = currentAttachmentFiles();
        const accepted = [];
        for (const file of incoming) {
            if (file.size > 10 * 1024 * 1024) {
                showToast(`${file.name} exceeds the 10 MiB attachment limit`);
                continue;
            }
            if (existing.length + accepted.length >= 8) {
                showToast('A message supports at most 8 attachments');
                break;
            }
            accepted.push(file);
        }
        if (accepted.length)
            state.attachmentDrafts.set(sessionKey(state.selected), [...existing, ...accepted]);
        renderPendingAttachments();
        updateComposerAction();
    }
    async function uploadAttachment(session, file) {
        const body = new FormData();
        body.append('file', file, file.name);
        const response = await fetch(`/api/sessions/${encodeURIComponent(session.namespace)}/${encodeURIComponent(session.name)}/attachments`, {
            method: 'POST',
            body,
        });
        if (response.status === 401) {
            window.location.replace('/login');
            throw new Error('Authentication required');
        }
        if (!response.ok) {
            const payload = await response.json().catch(() => ({}));
            throw new Error(payload.error || `${response.status} ${response.statusText}`);
        }
        return response.json();
    }
    async function submitComposer() {
        if (state.sendingMessage)
            return;
        const text = elements.input.value.trim();
        if (!state.socket || state.socket.readyState !== WebSocket.OPEN)
            return;
        if (composerInterruptAction()) {
            interruptActiveTurn();
            return;
        }
        const session = state.selected;
        const files = [...currentAttachmentFiles()];
        if (!session)
            return;
        if (!text && files.length === 0)
            return;
        state.sendingMessage = true;
        updateComposerAction();
        elements.attachFiles.disabled = true;
        try {
            const attachments = await Promise.all(files.map(file => uploadAttachment(session, file)));
            if (!state.selected || sessionViewKey(state.selected) !== sessionViewKey(session) || !state.socket || state.socket.readyState !== WebSocket.OPEN) {
                throw new Error('Session disconnected before the message could be sent');
            }
            state.socket.send(JSON.stringify({ type: 'message', text, attachmentIds: attachments.map(attachment => attachment.id) }));
            clearPromptDraft(session);
            state.attachmentDrafts.delete(sessionKey(session));
            renderPendingAttachments();
        }
        catch (error) {
            showToast(errorMessage(error));
        }
        finally {
            state.sendingMessage = false;
            updateComposerAction();
            elements.attachFiles.disabled = elements.input.disabled;
        }
    }
    elements.composer.addEventListener('submit', event => {
        event.preventDefault();
        void submitComposer();
    });
    elements.attachFiles.addEventListener('click', () => elements.attachmentInput.click());
    elements.attachmentInput.addEventListener('change', () => {
        stageAttachments(elements.attachmentInput.files);
        elements.attachmentInput.value = '';
    });
    for (const eventName of ['dragenter', 'dragover']) {
        elements.composerWrap.addEventListener(eventName, event => {
            const dragEvent = event;
            if (!dragEvent.dataTransfer?.types?.includes('Files'))
                return;
            event.preventDefault();
            elements.composerWrap.classList.add('attachment-drop-target');
            dragEvent.dataTransfer.dropEffect = 'copy';
        });
    }
    for (const eventName of ['dragleave', 'drop']) {
        elements.composerWrap.addEventListener(eventName, event => {
            const dragEvent = event;
            elements.composerWrap.classList.remove('attachment-drop-target');
            if (eventName === 'drop' && dragEvent.dataTransfer?.files?.length) {
                event.preventDefault();
                stageAttachments(dragEvent.dataTransfer.files);
            }
        });
    }
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
        }
        catch (error) {
            showToast(errorMessage(error));
            return;
        }
        setConsoleView('sessions');
        elements.dialogError.textContent = '';
        setCreationMode(state.creationMode);
        elements.dialog.showModal();
        loadOptions().catch(error => { elements.dialogError.textContent = errorMessage(error); });
        window.setTimeout(() => (state.creationMode === 'yaml' ? elements.yaml : elements.form.elements.name).focus(), 0);
    }
    elements.newSessionButton.addEventListener('click', openDialog);
    requiredElement('#welcome-new').addEventListener('click', openDialog);
    document.querySelectorAll('.close-dialog').forEach(button => button.addEventListener('click', () => elements.dialog.close()));
    elements.namespaceForm.addEventListener('submit', async (event) => {
        event.preventDefault();
        try {
            await switchNamespace(elements.activeNamespace.value);
        }
        catch (error) {
            showToast(errorMessage(error));
        }
    });
    elements.sessionSource.addEventListener('change', () => loadSessionSource(elements.sessionSource.value));
    elements.credentialType.addEventListener('change', () => {
        const option = elements.credentialSecret.selectedOptions[0];
        if (option?.dataset.type && option.dataset.type !== elements.credentialType.value) {
            elements.credentialSecretCustom.value = option.dataset.name || '';
            elements.credentialSecret.value = customOption;
        }
        updateCredentialField();
    });
    elements.provider.addEventListener('change', renderCredentialOptions);
    elements.credentialSecret.addEventListener('change', () => {
        const option = elements.credentialSecret.selectedOptions[0];
        if (option?.dataset.type)
            elements.credentialType.value = option.dataset.type;
        updateCredentialField();
    });
    elements.workspace.addEventListener('change', updateWorkspaceField);
    elements.sectionSelect.addEventListener('change', () => {
        updateCustomSectionField(elements.sectionSelect, elements.sectionCustom);
        if (!elements.sectionCustom.hidden)
            elements.sectionCustom.focus();
    });
    elements.sectionCustom.addEventListener('input', () => {
        validateCustomSectionField(elements.sectionSelect, elements.sectionCustom);
    });
    elements.agentConfig.addEventListener('change', () => {
        elements.addAgentConfig.disabled = !elements.agentConfig.value;
    });
    elements.addAgentConfig.addEventListener('click', () => {
        const name = elements.agentConfig.value;
        if (!name || state.selectedAgentConfigs.includes(name))
            return;
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
    function formValue(values, name) {
        const value = values.get(name);
        return typeof value === 'string' ? value : '';
    }
    elements.form.addEventListener('submit', async (event) => {
        event.preventDefault();
        if (state.sourceLoading || state.creatingSession)
            return;
        validateCustomSectionField(elements.sectionSelect, elements.sectionCustom);
        if (!elements.form.reportValidity())
            return;
        elements.dialogError.textContent = '';
        setCreatingSession(true);
        try {
            let created;
            if (state.creationMode === 'yaml') {
                created = await api(`/api/sessions/apply?namespace=${encodeURIComponent(state.namespace)}`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/yaml' },
                    body: elements.yaml.value,
                });
            }
            else {
                const values = new FormData(elements.form);
                const credentialType = formValue(values, 'credentialType');
                const worker = {
                    type: formValue(values, 'provider'),
                    credentials: { type: credentialType },
                };
                if (credentialType !== 'none')
                    worker.credentials.secretRef = { name: selectedCredentialName() };
                const workspace = selectedWorkspaceName();
                if (workspace)
                    worker.workspaceRef = { name: workspace };
                const model = formValue(values, 'model').trim();
                if (model)
                    worker.model = model;
                if (state.selectedAgentConfigs.length) {
                    worker.agentConfigRefs = state.selectedAgentConfigs.map(name => ({ name }));
                }
                const payload = {
                    name: formValue(values, 'name').trim(),
                    namespace: formValue(values, 'namespace').trim(),
                    worker,
                };
                Object.assign(payload, selectedSectionPayload(elements.sectionSelect, elements.sectionCustom));
                const initialBranch = formValue(values, 'initialBranch').trim();
                if (initialBranch)
                    payload.initialBranch = initialBranch;
                const initialPrompt = formValue(values, 'initialPrompt');
                if (initialPrompt.trim())
                    payload.initialPrompt = initialPrompt;
                if (formValue(values, 'persistentVolume')) {
                    payload.volumeClaimTemplate = {
                        accessModes: [formValue(values, 'accessMode')],
                        resources: { requests: { storage: formValue(values, 'storageRequest').trim() } },
                    };
                    const storageClassName = formValue(values, 'storageClassName').trim();
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
        }
        catch (error) {
            elements.dialogError.textContent = errorMessage(error);
        }
        finally {
            setCreatingSession(false);
        }
    });
    function openDisplayNameDialog(session = state.selected) {
        if (!session || state.displayNameSaving)
            return;
        state.displayNameSession = session;
        elements.displayNameDialogError.textContent = '';
        elements.displayNameDialogDescription.textContent = `Set a display name for Session ${session.namespace}/${session.name} without changing its Kubernetes resource name.`;
        elements.displayNameInput.value = sessionDisplayName(session) === session.name ? '' : sessionDisplayName(session);
        elements.displayNameInput.placeholder = session.name;
        elements.displayNameDialog.showModal();
        window.setTimeout(() => elements.displayNameInput.focus(), 0);
    }
    function setDisplayNameSaving(saving) {
        state.displayNameSaving = saving;
        elements.displayNameInput.disabled = saving;
        elements.saveDisplayNameButton.disabled = saving;
        elements.displayNameDialog.setAttribute('aria-busy', String(saving));
        document.querySelectorAll('.close-display-name-dialog').forEach(button => {
            button.disabled = saving;
        });
    }
    function closeDisplayNameDialog() {
        if (state.displayNameSaving)
            return;
        state.displayNameSession = null;
        elements.displayNameDialog.close();
    }
    function handleDisplayNameDialogCancel(event) {
        if (state.displayNameSaving) {
            event.preventDefault();
            return;
        }
        state.displayNameSession = null;
    }
    elements.displayNameButton.addEventListener('click', () => openDisplayNameDialog());
    document.querySelectorAll('.close-display-name-dialog').forEach(button => {
        button.addEventListener('click', closeDisplayNameDialog);
    });
    elements.displayNameDialog.addEventListener('cancel', handleDisplayNameDialogCancel);
    elements.displayNameForm.addEventListener('submit', async (event) => {
        event.preventDefault();
        if (state.displayNameSaving)
            return;
        const session = state.displayNameSession;
        if (!session) {
            state.displayNameSession = null;
            elements.displayNameDialog.close();
            return;
        }
        const displayName = elements.displayNameInput.value.trim();
        if ((displayName || session.name) === sessionDisplayName(session)) {
            state.displayNameSession = null;
            elements.displayNameDialog.close();
            return;
        }
        elements.displayNameDialogError.textContent = '';
        setDisplayNameSaving(true);
        try {
            await saveSessionDisplayName(session, displayName);
            elements.displayNameDialog.close();
            state.displayNameSession = null;
            showToast(displayName ? 'Session renamed' : 'Session name reset');
        }
        catch (error) {
            elements.displayNameDialogError.textContent = errorMessage(error);
        }
        finally {
            setDisplayNameSaving(false);
        }
    });
    function updateSelectedSectionField() {
        updateCustomSectionField(elements.sectionChoice, elements.sectionChoiceCustom);
        elements.saveSectionButton.hidden = elements.sectionChoiceCustom.hidden;
    }
    function closeSessionSectionEditor() {
        elements.sectionForm.classList.remove('mobile-open');
    }
    function openSessionSectionEditor(session) {
        closeSessionActionsMenu();
        if (!state.selected || sessionKey(state.selected) !== sessionKey(session))
            selectSession(session);
        elements.sectionForm.classList.add('mobile-open');
        elements.sectionChoice.focus();
        try {
            elements.sectionChoice.showPicker?.();
        }
        catch (_) {
            // Keep the focused select usable when the browser refuses to open its picker.
        }
    }
    function renderSelectedSessionSection(session, preserveEditing = true) {
        elements.sectionForm.hidden = !session;
        if (!session) {
            elements.sectionForm.dataset.session = '';
            elements.sectionChoiceCustom.value = '';
            populateSectionSelect(elements.sectionChoice, '', 'Choose section');
            updateSelectedSectionField();
            elements.sectionChoice.disabled = true;
            return;
        }
        const sessionID = sessionKey(session);
        const editing = preserveEditing
            && elements.sectionForm.dataset.session === sessionID
            && createsNewSection(elements.sectionChoice);
        elements.sectionForm.dataset.session = sessionID;
        if (!editing) {
            elements.sectionChoiceCustom.value = '';
            const emptyLabel = session.section ? 'Unsectioned (remove assignment)' : 'Choose section';
            populateSectionSelect(elements.sectionChoice, session.section || '', emptyLabel);
        }
        updateSelectedSectionField();
        elements.sectionChoice.disabled = state.sectionSaving;
    }
    function setSectionSaving(saving) {
        state.sectionSaving = saving;
        const disabled = saving || !state.selected;
        elements.sectionChoice.disabled = disabled;
        elements.sectionChoiceCustom.disabled = disabled;
        elements.saveSectionButton.disabled = disabled;
        elements.sectionForm.setAttribute('aria-busy', String(saving));
    }
    async function saveSelectedSessionSection(section) {
        const session = state.selected;
        if (!session || state.sectionSaving)
            return;
        if (section === (session.section || '')) {
            renderSelectedSessionSection(session, false);
            closeSessionSectionEditor();
            return;
        }
        const creating = createsNewSection(elements.sectionChoice);
        setSectionSaving(true);
        try {
            await saveSessionSectionAssignment(session, section);
            if (state.selected && sessionKey(state.selected) === sessionKey(session)) {
                renderSelectedSessionSection(state.selected, false);
            }
            closeSessionSectionEditor();
            showToast(section ? `Moved Session to ${section}` : 'Moved Session to Unsectioned');
        }
        catch (error) {
            if (!creating && state.selected && sessionKey(state.selected) === sessionKey(session)) {
                renderSelectedSessionSection(session, false);
            }
            showToast(errorMessage(error));
        }
        finally {
            setSectionSaving(false);
        }
    }
    elements.sectionChoice.addEventListener('change', () => {
        updateSelectedSectionField();
        if (!elements.sectionChoiceCustom.hidden) {
            elements.sectionChoiceCustom.focus();
            return;
        }
        void saveSelectedSessionSection(elements.sectionChoice.value);
    });
    elements.sectionChoiceCustom.addEventListener('input', () => {
        validateCustomSectionField(elements.sectionChoice, elements.sectionChoiceCustom);
    });
    elements.cancelSectionButton.addEventListener('click', closeSessionSectionEditor);
    elements.sectionForm.addEventListener('submit', async (event) => {
        event.preventDefault();
        if (state.sectionSaving || !createsNewSection(elements.sectionChoice))
            return;
        validateCustomSectionField(elements.sectionChoice, elements.sectionChoiceCustom);
        if (!elements.sectionForm.reportValidity())
            return;
        await saveSelectedSessionSection(elements.sectionChoiceCustom.value.trim());
    });
    async function deleteSession(session) {
        if (!session || !window.confirm(`Delete Session ${session.namespace}/${session.name}? The live conversation will end.`))
            return;
        try {
            await api(`/api/sessions/${encodeURIComponent(session.namespace)}/${encodeURIComponent(session.name)}`, { method: 'DELETE' });
            discardSessionView(session);
            clearPromptDraft(session);
            clearAttachmentDraft(session);
            if (state.selected && sessionKey(state.selected) === sessionKey(session))
                selectSession(null);
            await loadSessions();
            showToast('Session deleted');
        }
        catch (error) {
            showToast(errorMessage(error));
        }
    }
    async function resetSession(session) {
        if (!session || session.resetting || !window.confirm(`Reset Session ${session.namespace}/${session.name}? This permanently deletes its conversation history and all workspace changes.`))
            return;
        try {
            const resetting = await api(`/api/sessions/${encodeURIComponent(session.namespace)}/${encodeURIComponent(session.name)}/reset`, { method: 'POST' });
            discardSessionView(session);
            clearPromptDraft(session);
            clearAttachmentDraft(session);
            if (state.selected && sessionKey(state.selected) === sessionKey(session)) {
                closeSocket();
                resetCurrentSessionView();
                state.currentView = null;
                selectSession(resetting);
            }
            await loadSessions();
            showToast('Session reset requested');
        }
        catch (error) {
            showToast(errorMessage(error));
        }
    }
    elements.resumeButton.addEventListener('click', resumeSelectedSession);
    elements.suspendButton.addEventListener('click', suspendSelectedSession);
    elements.deleteButton.addEventListener('click', () => deleteSession(state.selected));
    elements.resetButton.addEventListener('click', () => resetSession(state.selected));
    elements.sessionActionRename.addEventListener('click', () => {
        const session = sessionActionsTarget();
        closeSessionActionsMenu();
        openDisplayNameDialog(session);
    });
    elements.sessionActionSection.addEventListener('click', () => {
        const session = sessionActionsTarget();
        if (session)
            openSessionSectionEditor(session);
    });
    elements.sessionActionLifecycle.addEventListener('click', event => {
        void runSessionMenuAction(event, toggleSessionSuspension);
    });
    elements.sessionActionReset.addEventListener('click', event => {
        void runSessionMenuAction(event, resetSession);
    });
    elements.sessionActionDelete.addEventListener('click', event => {
        void runSessionMenuAction(event, deleteSession);
    });
    elements.sessionActionsMenu.addEventListener('keydown', event => {
        const items = Array.from(elements.sessionActionsMenu.querySelectorAll('[role="menuitem"]'))
            .filter(item => !item.disabled);
        if (!items.length)
            return;
        let index = items.indexOf(document.activeElement);
        if (event.key === 'ArrowDown')
            index = (index + 1) % items.length;
        else if (event.key === 'ArrowUp')
            index = (index - 1 + items.length) % items.length;
        else if (event.key === 'Home')
            index = 0;
        else if (event.key === 'End')
            index = items.length - 1;
        else
            return;
        event.preventDefault();
        items[index].focus();
    });
    elements.sessionActionsMenu.addEventListener('focusout', handleSessionActionsFocusOut);
    elements.conversationTab.addEventListener('click', () => setActiveView('conversation'));
    elements.changesTab.addEventListener('click', () => setActiveView('changes'));
    elements.viewTabs.addEventListener('keydown', handleViewTabKeydown);
    elements.currentRequestButton.addEventListener('click', jumpToCurrentRequest);
    elements.messages.addEventListener('scroll', scheduleCurrentRequestUpdate);
    function interruptActiveTurn() {
        if (!state.socket || state.socket.readyState !== WebSocket.OPEN || !state.activeTurn || state.interrupting)
            return;
        state.interrupting = true;
        updateComposerAction();
        refreshSessionProgress();
        state.socket.send(JSON.stringify({ type: 'interrupt' }));
    }
    elements.overviewButton.addEventListener('click', () => setConsoleView('overview'));
    elements.sessionsButton.addEventListener('click', () => setConsoleView('sessions'));
    elements.resourcesButton.addEventListener('click', () => setConsoleView('resources'));
    elements.resourceDiagramTab.addEventListener('click', () => setResourceView('diagram'));
    elements.resourceInventoryTab.addEventListener('click', () => setResourceView('inventory'));
    requiredElement('.resource-view-tabs').addEventListener('keydown', event => handleResourceViewTabKeydown(event));
    elements.resourceRelationshipFocus.addEventListener('change', renderResourceDiagram);
    elements.resourceKind.addEventListener('change', renderResources);
    elements.resourceSearch.addEventListener('input', renderResources);
    document.querySelectorAll('.close-resource-detail').forEach(button => {
        button.addEventListener('click', () => elements.resourceDetailDialog.close());
    });
    elements.resourceDetailLogsTab.addEventListener('click', () => setResourceDetailView('logs'));
    elements.resourceDetailManifestTab.addEventListener('click', () => setResourceDetailView('manifest'));
    elements.resourceDetailTabs.addEventListener('keydown', handleResourceDetailTabKeydown);
    elements.refreshResourceLogs.addEventListener('click', () => {
        if (state.resourceDetailTask)
            void loadResourceTaskLogs(state.resourceDetailTask, state.resourceDetailGeneration);
    });
    requiredElement('#refresh-console').addEventListener('click', () => {
        void refreshConsole();
    });
    requiredElement('#logout').addEventListener('click', async () => {
        await api('/api/logout', { method: 'POST' }).catch(() => { });
        window.location.replace('/login');
    });
    function setSidebarOpen(open) {
        elements.sidebar.classList.toggle('open', open);
        for (const button of document.querySelectorAll('.open-sidebar-button')) {
            button.setAttribute('aria-expanded', String(open));
        }
    }
    document.querySelectorAll('.open-sidebar-button').forEach(button => {
        button.addEventListener('click', () => setSidebarOpen(true));
    });
    elements.closeSidebar.addEventListener('click', () => setSidebarOpen(false));
    elements.sidebarScrim.addEventListener('click', () => setSidebarOpen(false));
    elements.sidebarScroll.addEventListener('scroll', () => closeSessionActionsMenu());
    window.addEventListener('resize', () => {
        closeSessionActionsMenu();
        scheduleCurrentRequestUpdate();
    });
    document.addEventListener('pointerdown', event => {
        if (elements.sessionActionsMenu.hidden)
            return;
        const target = event.target;
        if (elements.sessionActionsMenu.contains(target) || state.sessionActionTrigger?.contains(target))
            return;
        closeSessionActionsMenu();
    });
    document.addEventListener('keydown', event => {
        if (event.key === 'Escape' && !elements.sessionActionsMenu.hidden) {
            event.preventDefault();
            closeSessionActionsMenu(true);
            return;
        }
        if (event.key === 'Escape' && elements.sidebar.classList.contains('open'))
            setSidebarOpen(false);
    });
    const configReady = loadConfig();
    configReady.then(() => Promise.all([loadOptions(), loadSessions(), loadResources()])).then(() => {
        setConsoleView('overview');
    }).catch(error => showToast(error.message));
    window.setInterval(() => loadSessions({ quiet: true }), 5000);
    window.setInterval(() => loadResources({ quiet: true }), 10000);
}
