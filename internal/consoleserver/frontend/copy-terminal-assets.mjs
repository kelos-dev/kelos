import {readFile, writeFile} from 'node:fs/promises';

for (const [source, destination] of [
  ['@xterm/xterm/lib/xterm.js', 'xterm.js'],
  ['@xterm/xterm/css/xterm.css', 'xterm.css'],
  ['@xterm/xterm/LICENSE', 'xterm-LICENSE.txt'],
  ['@xterm/addon-fit/lib/addon-fit.js', 'xterm-addon-fit.js'],
  ['@xterm/addon-fit/LICENSE', 'xterm-addon-fit-LICENSE.txt'],
]) {
  let content = await readFile(new URL(`node_modules/${source}`, import.meta.url), 'utf8');
  if (destination.endsWith('.js') || destination.endsWith('.css')) {
    content = `/* Generated from ${source} by copy-terminal-assets.mjs. Do not edit directly. */\n${content}`;
  }
  if (destination.endsWith('.js')) content = content.replace(/^\/\/# sourceMappingURL=.*$/gm, '');
  await writeFile(new URL(`../web/${destination}`, import.meta.url), content);
}
