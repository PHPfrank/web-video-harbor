'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const state = require('../lib/popup-state.js');

test('disconnected view asks the user to pair before downloading', () => {
  const view = state.buildViewModel({ connection: 'disconnected' });

  assert.deepEqual(view.connection, {
    label: '未连接本地助手',
    tone: 'offline',
    detail: '请先在设置中输入配对密钥。',
  });
  assert.equal(view.canDownload, false);
});

test('empty and scanning views explain what the popup is doing', () => {
  const empty = state.buildViewModel({ connection: 'connected', candidates: [] });
  const scanning = state.buildViewModel({ connection: 'connected', scanning: true, candidates: [] });
  const wechat = state.buildViewModel({
    connection: 'connected',
    candidates: [],
    pageUrl: 'https://channels.weixin.qq.com/watch/abc',
  });

  assert.equal(empty.emptyMessage, '尚未发现可下载的视频');
  assert.equal(scanning.emptyMessage, '正在重新扫描当前页面…');
  assert.equal(wechat.emptyMessage, '请先在浏览器中播放视频几秒，再重新扫描');
});

test('candidate view distinguishes MP4 and HLS and keeps useful metadata', () => {
  const view = state.buildViewModel({
    connection: 'connected',
    candidates: [
      { url: 'https://cdn.example/movie.mp4', kind: 'mp4', title: '一段视频', width: 1920, height: 1080 },
      { url: 'https://cdn.example/master.m3u8', kind: 'hls', title: '直播回放' },
    ],
  });

  assert.deepEqual(view.candidates.map((item) => ({ typeLabel: item.typeLabel, detail: item.detail })), [
    { typeLabel: 'MP4', detail: '1920 × 1080' },
    { typeLabel: 'M3U8', detail: '需要检查可用画质' },
  ]);
  assert.equal(view.canDownload, true);
});

test('HLS variants sort by height then bandwidth without mutating input', () => {
  const variants = [
    { url: 'https://cdn.example/720-low.m3u8', label: '720p', height: 720, bandwidth: 1800000 },
    { url: 'https://cdn.example/audio.m3u8', label: '256 kbps', bandwidth: 256000 },
    { url: 'https://cdn.example/1080.m3u8', label: '1080p', height: 1080, bandwidth: 5200000 },
    { url: 'https://cdn.example/720-high.m3u8', label: '720p 高码率', height: 720, bandwidth: 2800000 },
  ];

  const sorted = state.sortHlsVariants(variants);

  assert.deepEqual(sorted.map((item) => item.label), ['1080p', '720p 高码率', '720p', '256 kbps']);
  assert.equal(variants[0].label, '720p');
});

test('task lifecycle maps to concise Chinese status and valid actions', () => {
  const cases = [
    ['queued', '等待中', true, false, false],
    ['downloading', '下载中', true, false, false],
    ['merging', '正在合并', true, false, false],
    ['completed', '已完成', false, false, true],
    ['failed', '下载失败', false, true, false],
    ['canceled', '已取消', false, true, false],
  ];

  for (const [status, label, canCancel, canRetry, canReveal] of cases) {
    const [task] = state.buildViewModel({
      connection: 'connected',
      tasks: [{ id: `task-${status}`, status, title: '测试', progress: 43 }],
    }).tasks;
    assert.equal(task.statusLabel, label, status);
    assert.equal(task.canCancel, canCancel, status);
    assert.equal(task.canRetry, canRetry, status);
    assert.equal(task.canReveal, canReveal, status);
  }
});

test('task progress is bounded and failure details remain short', () => {
  const view = state.buildViewModel({
    connection: 'connected',
    tasks: [
      { id: 'over', status: 'downloading', title: 'A', progress: 900 },
      { id: 'failed', status: 'failed', title: 'B', progress: -2, error: 'A'.repeat(300) },
    ],
  });

  assert.equal(view.tasks[0].progress, 100);
  assert.equal(view.tasks[1].progress, 0);
  assert.ok(view.tasks[1].detail.length <= 83);
});

test('setText renders untrusted user text through textContent only', () => {
  const element = { textContent: '' };
  const payload = '<img src=x onerror="globalThis.pwned=true">';

  state.setText(element, payload);

  assert.equal(element.textContent, payload);
  assert.equal(Object.hasOwn(element, 'innerHTML'), false);
});
