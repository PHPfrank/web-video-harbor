'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const extensionDir = path.resolve(__dirname, '..');

function source(name) {
  return fs.readFileSync(path.join(extensionDir, name), 'utf8');
}

test('manifest wires a popup and options page with existing local resources', () => {
  const manifest = JSON.parse(source('manifest.json'));

  assert.deepEqual(manifest.action, { default_popup: 'popup.html', default_title: '网页视频下载器' });
  assert.deepEqual(manifest.options_ui, { page: 'options.html', open_in_tab: true });
  assert.deepEqual(manifest.permissions, ['storage', 'webRequest']);
  for (const name of ['popup.html', 'popup.css', 'popup.js', 'options.html', 'options.js', 'lib/popup-state.js', 'lib/helper-client.js']) {
    assert.equal(fs.existsSync(path.join(extensionDir, name)), true, name);
  }
});

test('popup is semantic, keyboard accessible, and loads scripts without inline handlers', () => {
  const html = source('popup.html');

  assert.match(html, /<main\b/);
  assert.match(html, /<section\b[^>]*aria-labelledby=/);
  assert.match(html, /aria-live=["']polite["']/);
  assert.match(html, /<button\b[^>]*id=["']rescan-button["']/);
  assert.match(html, /<script src=["']lib\/popup-state\.js["']><\/script>/);
  assert.match(html, /<script src=["']lib\/helper-client\.js["']><\/script>/);
  assert.match(html, /<script src=["']popup\.js["']><\/script>/);
  assert.doesNotMatch(html, /\son\w+=/i);
  assert.doesNotMatch(html, /https?:\/\//i);
});

test('popup controller covers scan, inspect, download, polling, and task actions safely', () => {
  const javascript = source('popup.js');

  for (const marker of ['GET_TAB_MEDIA', 'RESCAN', '.inspect(', '.createTask(', '.cancelTask(', '.retryTask(', '.revealTask(']) {
    assert.match(javascript, new RegExp(marker.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), marker);
  }
  assert.match(javascript, /setInterval\s*\(/);
  assert.match(javascript, /clearInterval\s*\(/);
  assert.match(javascript, /replaceChildren\s*\(/);
  assert.match(javascript, /textContent/);
  assert.doesNotMatch(javascript, /innerHTML|insertAdjacentHTML|document\.write/);
});

test('options page stores only a token locally and offers connection testing and privacy guidance', () => {
  const html = source('options.html');
  const javascript = source('options.js');
  const combined = `${html}\n${javascript}\n${source('lib/helper-client.js')}`;

  assert.match(html, /type=["']password["']/);
  assert.match(html, /测试连接/);
  assert.match(html, /隐私/);
  assert.match(javascript, /chrome\.storage\.local/);
  assert.match(javascript, /\.saveToken\s*\(/);
  assert.match(javascript, /\.listTasks\s*\(/);
  assert.doesNotMatch(combined, /storage\.sync|Cookie\s*导入|自定义\s*(host|地址)/i);
  assert.doesNotMatch(javascript, /innerHTML|insertAdjacentHTML|document\.write/);
});

test('popup visual system stays compact, distinct, and accessible', () => {
  const css = source('popup.css');

  assert.match(css, /width:\s*400px/);
  assert.match(css, /--color-[\w-]+:/);
  assert.match(css, /\.candidate-card\b/);
  assert.match(css, /\.task-card\b/);
  assert.match(css, /\.visually-hidden\b/);
  assert.match(css, /:focus-visible/);
  assert.match(css, /prefers-reduced-motion/);
  assert.doesNotMatch(css, /backdrop-filter|linear-gradient|radial-gradient/);
});
