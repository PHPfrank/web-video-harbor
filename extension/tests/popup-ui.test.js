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

  assert.equal(manifest.name, '网页视频港');
  assert.deepEqual(manifest.action, { default_popup: 'popup.html', default_title: '网页视频港' });
  assert.deepEqual(manifest.options_ui, { page: 'options.html', open_in_tab: true });
  assert.deepEqual(manifest.permissions, ['storage', 'webRequest']);
  for (const name of ['popup.html', 'popup.css', 'popup.js', 'options.html', 'options.js', 'lib/popup-state.js', 'lib/helper-client.js', 'lib/platform-settings.js']) {
    assert.equal(fs.existsSync(path.join(extensionDir, name)), true, name);
  }
});

test('popup is semantic, keyboard accessible, and loads scripts without inline handlers', () => {
  const html = source('popup.html');

  assert.match(html, /<main\b/);
  assert.match(html, /<section\b[^>]*aria-labelledby=/);
  assert.match(html, /aria-live=["']polite["']/);
  assert.match(html, /class=["']connection-panel["'][^>]*role=["']status["']/);
  assert.match(html, /<button\b[^>]*id=["']rescan-button["']/);
  assert.match(html, /<script src=["']lib\/popup-state\.js["']><\/script>/);
  assert.match(html, /<script src=["']lib\/helper-client\.js["']><\/script>/);
  assert.match(html, /<script src=["']lib\/popup-controller\.js["']><\/script>/);
  assert.match(html, /<script src=["']popup\.js["']><\/script>/);
  assert.doesNotMatch(html, /\son\w+=/i);
  assert.doesNotMatch(html, /https?:\/\//i);
});

test('popup controller covers scan, inspect, download, polling, and task actions safely', () => {
  const javascript = source('popup.js');
  const controller = source('lib/popup-controller.js');

  for (const marker of ['GET_TAB_MEDIA', 'RESCAN', '.inspect(', '.createTask(', '.cancelTask(', '.retryTask(', '.revealTask(']) {
    assert.match(`${javascript}\n${controller}`, new RegExp(marker.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), marker);
  }
  assert.match(javascript, /createPopupController\s*\(/);
  assert.doesNotMatch(javascript, /setInterval\s*\(/);
  assert.match(javascript, /replaceChildren\s*\(/);
  assert.match(javascript, /textContent/);
  assert.doesNotMatch(javascript, /innerHTML|insertAdjacentHTML|document\.write/);
});

test('popup gates candidates against the background-coordinated current page', () => {
  const javascript = source('popup.js');

  assert.match(javascript, /VideoGrabberPlatform/);
  assert.match(javascript, /\.getSettings\s*\(/);
  assert.match(javascript, /experimentalPlatformCompatibilityEnabled/);
  assert.match(javascript, /candidatesForCoordinatedPage\s*\(/);
  assert.doesNotMatch(javascript, /url:\s*tab\.url/);
});

test('popup renders separate fixed platform-quality values and safe text-only titles', () => {
  const javascript = source('popup.js');

  assert.match(javascript, /candidate\.kind\s*===\s*['"]platform['"]/);
  assert.match(javascript, /candidate\.qualityOptions/);
  assert.match(javascript, /controller\.selectQuality\s*\(/);
  assert.match(javascript, /makeElement\(['"]h3['"],\s*['"]card-title['"],\s*candidate\.title\)/);
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
  assert.match(javascript, /\.health\s*\(/);
  assert.match(javascript, /\.listTasks\s*\(/);
  assert.match(javascript, /\.describeHealth\s*\(/);
  assert.doesNotMatch(combined, /storage\.sync|Cookie\s*导入|自定义\s*(host|地址)/i);
  assert.doesNotMatch(javascript, /innerHTML|insertAdjacentHTML|document\.write/);
});

test('options page requires explicit accessible consent for experimental platform compatibility', () => {
  const html = source('options.html');
  const javascript = source('options.js');
  const controller = source('lib/platform-settings.js');
  const manifest = JSON.parse(source('manifest.json'));

  assert.match(html, /实验性平台兼容/);
  assert.match(html, /type=["']checkbox["'][^>]*id=["']platform-compatibility-toggle["']/);
  assert.match(html, /<dialog\b[^>]*id=["']platform-notice-dialog["']/);
  assert.match(html, /仅用于技术研究/);
  assert.match(html, /请勿用于会员、付费、私有、加密、DRM/);
  assert.match(html, /我已了解并继续/);
  assert.match(html, /aria-live=["']polite["']/);
  assert.match(html, /<script src=["']lib\/platform-settings\.js["']><\/script>\s*<script src=["']options\.js["']><\/script>/);
  assert.match(javascript, /createPlatformSettingsController\s*\(/);
  assert.match(controller, /\.getSettings\s*\(/);
  assert.doesNotMatch(javascript, /currentPlatformNoticeVersion\s*:\s*["']/);
  assert.deepEqual(manifest.permissions, ['storage', 'webRequest']);
  assert.doesNotMatch(javascript, /innerHTML|insertAdjacentHTML|document\.write/);
});

test('options page offers a disclosed recommendation link without embedding affiliate destinations', () => {
  const html = source('options.html');
  const css = source('popup.css');
  const extensionSources = fs.readdirSync(extensionDir, { recursive: true })
    .filter((name) => !name.startsWith(`tests${path.sep}`))
    .filter((name) => fs.statSync(path.join(extensionDir, name)).isFile())
    .map((name) => source(name))
    .join('\n');

  assert.match(html, /id=["']recommendations-link["']/);
  assert.match(html, /href=["']https:\/\/phpfrank\.github\.io\/web-video-harbor\/recommendations\.html["']/);
  assert.match(html, /页面可能包含推广链接/);
  assert.match(html, /target=["']_blank["']/);
  assert.match(html, /rel=["']noopener noreferrer["']/);
  assert.match(html, /在新窗口打开/);
  assert.match(css, /\.recommendations-link\s*\{[^}]*display:\s*inline-flex/s);
  assert.match(css, /@media\s*\(max-width:\s*560px\)/);
  assert.doesNotMatch(extensionSources, /s\.click\.taobao\.com/);
  assert.doesNotMatch(extensionSources, /userCode=c5z9bjlt/);
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
  assert.match(css, /\.settings-card\s+form\s*>\s*label/);
  assert.doesNotMatch(css, /\.settings-card\s+label\s*\{/);
  assert.doesNotMatch(css, /backdrop-filter|linear-gradient|radial-gradient/);
});
