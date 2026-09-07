declare const Terminal: typeof import('@xterm/xterm').Terminal;
declare const FitAddon: typeof import('@xterm/addon-fit');

const sessionTerminal = (() => {
  type Session = {namespace: string; name: string; uid?: string; phase?: string; resetting?: boolean; userSuspended?: boolean};
  const container = document.querySelector<HTMLElement>('#terminal-container')!;
  const status = document.querySelector<HTMLElement>('#terminal-status')!;
  const reconnect = document.querySelector<HTMLButtonElement>('#reconnect-terminal')!;
  let active: {session: Session; focus: () => void; dispose: () => void} | null = null;

  function available(session: Session | null) {
    return Boolean(session && session.phase === 'Ready' && !session.resetting && !session.userSuspended);
  }

  function close() {
    active?.dispose();
    active = null;
    container.replaceChildren();
    reconnect.disabled = true;
  }

  function sync(session: Session | null) {
    if (active && (!available(session) || session?.namespace !== active.session.namespace ||
      session?.name !== active.session.name || session?.uid !== active.session.uid)) close();
  }

  function show(session: Session | null) {
    sync(session);
    if (!session || !available(session)) return;
    if (active) {
      active.focus();
      return;
    }
    status.textContent = 'Connecting…';
    reconnect.disabled = true;
    const nonce = document.querySelector<HTMLMetaElement>('meta[name="terminal-style-nonce"]')!.content;
    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: 'monospace',
      scrollback: 5000,
      theme: {background: '#161b22', foreground: '#e6edf3'},
    });
    const fit = new FitAddon.FitAddon();
    terminal.loadAddon(fit);
    // The pinned xterm version creates its styles synchronously during open.
    // Recheck style creation and CSP behavior when upgrading xterm.
    const createElement = document.createElement;
    document.createElement = ((tag: string, options?: ElementCreationOptions) => {
      const element = createElement.call(document, tag, options);
      if (tag === 'style') element.nonce = nonce;
      return element;
    }) as typeof document.createElement;
    try {
      terminal.open(container);
    } finally {
      document.createElement = createElement;
    }
    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const socket = new WebSocket(`${protocol}//${location.host}/api/sessions/${encodeURIComponent(session.namespace)}/${encodeURIComponent(session.name)}/exec`);
    socket.binaryType = 'arraybuffer';
    let disposed = false;
    let finished = false;
    const visible = () => !disposed && container.clientWidth > 0 && container.clientHeight > 0;
    const focus = () => {
      if (!visible()) return;
      fit.fit();
      terminal.focus();
    };
    const send = (data: string | Uint8Array<ArrayBuffer>) => {
      if (disposed || finished || socket.readyState !== WebSocket.OPEN) return;
      if (typeof data === 'string') socket.send(data);
      else {
        // Keep pasted input within the server's WebSocket message limit.
        for (let offset = 0; offset < data.length; offset += 32 * 1024) socket.send(data.subarray(offset, offset + 32 * 1024));
      }
    };
    const sendResize = () => send(JSON.stringify({type: 'resize', cols: terminal.cols, rows: terminal.rows}));
    const input = terminal.onData(data => send(new TextEncoder().encode(data)));
    const binary = terminal.onBinary(data => send(Uint8Array.from(data, character => character.charCodeAt(0))));
    const resize = terminal.onResize(sendResize);
    const observer = new ResizeObserver(() => {
      if (visible()) fit.fit();
    });
    observer.observe(container);
    const finish = (message: string) => {
      finished = true;
      status.textContent = message;
      terminal.options.disableStdin = true;
      reconnect.disabled = false;
    };
    socket.addEventListener('open', () => {
      if (disposed) return;
      status.textContent = 'Connected';
      sendResize();
      focus();
    });
    socket.addEventListener('message', event => {
      if (disposed) return;
      if (event.data instanceof ArrayBuffer) {
        terminal.write(new Uint8Array(event.data));
      } else {
        const message = JSON.parse(event.data);
        if (message.type === 'error') finish(message.text);
        else if (message.type === 'exit') finish('Shell exited');
      }
    });
    socket.addEventListener('error', () => {
      if (!disposed && !finished) finish('Could not connect to the session terminal. Reconnect to try again.');
    });
    socket.addEventListener('close', () => {
      if (!disposed && !finished) finish('Disconnected. Reconnect to start a shell.');
    });
    active = {session, focus, dispose: () => {
      disposed = true;
      observer.disconnect();
      input.dispose();
      binary.dispose();
      resize.dispose();
      socket.close();
      terminal.dispose();
    }};
    focus();
  }

  reconnect.addEventListener('click', () => {
    if (!active || reconnect.disabled) return;
    const session = active.session;
    close();
    show(session);
  });
  return {available, show, sync, close};
})();
