#!/usr/bin/env node

const readline = require('node:readline');
const childProcess = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const sessionID = 'kelos-e2e-session';
const stateDirectory = process.env.CLAUDE_CONFIG_DIR || process.cwd();
const turnPath = path.join(stateDirectory, 'kelos-e2e-turn');
const suggestionsFlag = process.argv.indexOf('--prompt-suggestions');
const suggestionsEnabled = suggestionsFlag !== -1 && process.argv[suggestionsFlag + 1] === 'true';
let turn = Number.parseInt(readFile(turnPath, '0'), 10) || 0;
let pending = null;

function readFile(file, fallback) {
  try {
    return fs.readFileSync(file, 'utf8');
  } catch {
    return fallback;
  }
}

function send(value) {
  process.stdout.write(`${JSON.stringify(value)}\n`);
}

function complete(text) {
  send({
    type: 'stream_event',
    session_id: sessionID,
    event: {
      type: 'content_block_delta',
      delta: {type: 'text_delta', text},
    },
  });
  send({
    type: 'result',
    subtype: 'success',
    is_error: false,
    result: text,
    session_id: sessionID,
  });
}

function promptText(message) {
  return (message.message?.content || [])
    .filter(block => block.type === 'text')
    .map(block => block.text)
    .join('');
}

function readPromptAttachment(prompt) {
  const match = prompt.match(/^- .+ \([^)]+\): (.+)$/m);
  return match ? readFile(match[1], 'missing').trim() : 'missing';
}

function handleUser(message) {
  turn += 1;
  fs.mkdirSync(stateDirectory, {recursive: true});
  fs.writeFileSync(turnPath, String(turn));
  const prompt = promptText(message);
  if (prompt === 'background') {
    const taskID = `background-${turn}`;
    send({type: 'system', subtype: 'task_started', task_id: taskID, task_type: 'local_bash'});
    complete('Background task started');
    setImmediate(() => {
      send({type: 'system', subtype: 'task_notification', task_id: taskID, status: 'completed'});
      complete('Background task finished without another prompt');
    });
    return;
  }
  if (prompt.startsWith('attachment-check\n\n')) {
    complete(`attachment: ${readPromptAttachment(prompt)}`);
    return;
  }
  if (prompt === 'question') {
    pending = {kind: 'question'};
    send({
      type: 'control_request',
      request_id: `question-${turn}`,
      request: {
        subtype: 'can_use_tool',
        tool_name: 'AskUserQuestion',
        tool_use_id: `tool-${turn}`,
        input: {
          questions: [{
            question: 'Which database?',
            header: 'Database',
            multiSelect: false,
            options: [
              {label: 'PostgreSQL', description: 'Relational database'},
              {label: 'SQLite', description: 'Embedded database'},
            ],
          }],
        },
      },
    });
    return;
  }
  if (prompt === 'block') {
    pending = {kind: 'block'};
    return;
  }
  if (prompt === 'write-state') {
    fs.writeFileSync(path.join(process.cwd(), 'kelos-recovery-state'), 'preserved');
    complete(`turn ${turn}: state written`);
    return;
  }
  if (prompt === 'read-state') {
    const state = readFile(path.join(process.cwd(), 'kelos-recovery-state'), 'missing');
    complete(`turn ${turn}: state ${state}`);
    if (suggestionsEnabled) {
      send({type: 'prompt_suggestion', session_id: sessionID, suggestion: 'Check the preserved workspace'});
    }
    return;
  }
  if (prompt === 'create-git-workspace') {
    fs.rmSync(path.join(process.cwd(), '.git'), {recursive: true, force: true});
    childProcess.execFileSync('git', ['init'], {cwd: process.cwd(), stdio: 'ignore'});
    childProcess.execFileSync('git', ['checkout', '-b', 'agent/session-status'], {cwd: process.cwd(), stdio: 'ignore'});
    childProcess.execFileSync('git', ['remote', 'add', 'origin', 'https://github.com/kelos-dev/kelos.git'], {cwd: process.cwd(), stdio: 'ignore'});
    complete(`turn ${turn}: git workspace created`);
    return;
  }
  if (prompt === 'remove-git-workspace') {
    fs.rmSync(path.join(process.cwd(), '.git'), {recursive: true, force: true});
    complete(`turn ${turn}: git workspace removed`);
    return;
  }
  complete(`turn ${turn}: ${prompt}`);
}

function handleControlResponse(message) {
  if (pending?.kind === 'question') {
    const answer = message.response?.response?.updatedInput?.answers?.['Which database?'] || 'missing';
    pending = null;
    complete(`answer: ${answer}`);
    return;
  }
}

function handleControlRequest(message) {
  if (message.request?.subtype !== 'interrupt') return;
  send({
    type: 'control_response',
    response: {
      subtype: 'success',
      request_id: message.request_id,
      response: {},
    },
  });
  if (pending?.kind === 'block') {
    pending = null;
    complete('interrupted');
  }
}

readline.createInterface({input: process.stdin}).on('line', line => {
  let message;
  try {
    message = JSON.parse(line);
  } catch (error) {
    process.stderr.write(`Invalid JSON: ${error.message}\n`);
    process.exitCode = 1;
    return;
  }

  switch (message.type) {
  case 'user':
    handleUser(message);
    break;
  case 'control_response':
    handleControlResponse(message);
    break;
  case 'control_request':
    handleControlRequest(message);
    break;
  }
});
